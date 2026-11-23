/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package store

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	pb "github.com/bbmumford/mercury/store/proto"
	"github.com/bbmumford/plutus/observability"
	"github.com/bbmumford/whisper"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/proto"
)

const (
	// TopicStoreDirectory is the gossip topic for store advertisements.
	TopicStoreDirectory = "mercury.stores.directory"
	// TopicStoreReplicate is the gossip topic for replicator pin announcements.
	TopicStoreReplicate = "mercury.stores.replicate"

	// StreamIDStoreListing is the Aether stream ID for listing data requests.
	StreamIDStoreListing uint64 = 200
	// StreamIDStoreMedia is the Aether stream ID for media blob requests.
	StreamIDStoreMedia uint64 = 201
)

var (
	ErrAlreadyPinned   = errors.New("replication: store already pinned")
	ErrNotPinned       = errors.New("replication: store not pinned")
	ErrStaleVersion    = errors.New("replication: refusing stale store version")
	ErrCapacityExhaust = errors.New("replication: storage capacity exhausted")
)

// PinAnnouncement is broadcast by replicators to advertise that they are
// serving a store's data.
type PinAnnouncement struct {
	ReplicatorPubKey ed25519.PublicKey
	StoreID          string
	StoreVersion     uint64
	CapacityMB       uint64
	Signature        []byte
}

// SignPinAnnouncement signs a PinAnnouncement with the replicator's private key.
func SignPinAnnouncement(pa *PinAnnouncement, privKey ed25519.PrivateKey) error {
	if privKey == nil {
		return ErrNilPrivateKey
	}
	payload := pinSigningPayload(pa)
	pa.Signature = ed25519.Sign(privKey, payload)
	return nil
}

// VerifyPinAnnouncement verifies the signature on a PinAnnouncement.
func VerifyPinAnnouncement(pa *PinAnnouncement) bool {
	if pa.ReplicatorPubKey == nil || pa.Signature == nil {
		return false
	}
	payload := pinSigningPayload(pa)
	return ed25519.Verify(pa.ReplicatorPubKey, payload, pa.Signature)
}

func pinSigningPayload(pa *PinAnnouncement) []byte {
	msg := &pb.PinAnnouncementProto{
		ReplicatorPubKey: pa.ReplicatorPubKey,
		StoreId:          pa.StoreID,
		StoreVersion:     pa.StoreVersion,
		CapacityMb:       pa.CapacityMB,
	}
	data, _ := proto.Marshal(msg)
	return data
}

// StoreAd is broadcast by sellers to advertise their store on the directory topic.
type StoreAd struct {
	OwnerPubKey  ed25519.PublicKey
	Name         string
	Categories   []string
	Version      uint64
	ListingCount uint64
	MerkleRoot   []byte
	Addresses    []string // custom announce addresses for reaching this node
	Signature    []byte
}

// SignStoreAd signs a StoreAd with the owner's private key.
func SignStoreAd(ad *StoreAd, privKey ed25519.PrivateKey) error {
	if privKey == nil {
		return ErrNilPrivateKey
	}
	payload := adSigningPayload(ad)
	ad.Signature = ed25519.Sign(privKey, payload)
	return nil
}

// VerifyStoreAd verifies the signature on a StoreAd.
func VerifyStoreAd(ad *StoreAd) bool {
	if ad.OwnerPubKey == nil || ad.Signature == nil {
		return false
	}
	payload := adSigningPayload(ad)
	return ed25519.Verify(ad.OwnerPubKey, payload, ad.Signature)
}

func adSigningPayload(ad *StoreAd) []byte {
	msg := &pb.StoreAdProto{
		OwnerPubKey:  ad.OwnerPubKey,
		Name:         ad.Name,
		Categories:   ad.Categories,
		Version:      ad.Version,
		ListingCount: ad.ListingCount,
		MerkleRoot:   ad.MerkleRoot,
	}
	data, _ := proto.Marshal(msg)
	return data
}

// pinnedStore holds local state for a pinned (replicated) store.
type pinnedStore struct {
	root     *StoreRoot
	listings []*Listing
	pinnedAt time.Time
}

// ReplicationManager handles gossip-based store advertisement and replication.
// Sellers call Advertise to broadcast their store root. Replicators call
// Replicate to pin a remote store. ServeStore handles incoming data requests.
type ReplicationManager struct {
	mu      sync.RWMutex
	store   *StoreManager
	whisper *whisper.Engine
	peers   map[string]time.Time // replicator peerID -> last sync

	// pinned holds stores that this node is replicating for others.
	pinned map[string]*pinnedStore

	// replicatorPubKey is this node's identity when acting as a replicator.
	replicatorPubKey ed25519.PublicKey
	replicatorPriv   ed25519.PrivateKey

	// capacityMB is the storage budget for pinned stores.
	capacityMB uint64

	// adSubscribers receives StoreAd broadcasts from the directory topic.
	adSubscribers []func(StoreAd)

	// pinSubscribers receives PinAnnouncement broadcasts.
	pinSubscribers []func(PinAnnouncement)

	// announceAddresses are custom reachability addresses to include in StoreAds.
	announceAddresses []string
}

// NewReplicationManager creates a ReplicationManager wired to the given
// StoreManager (for the local store) and Whisper engine (for gossip).
func NewReplicationManager(store *StoreManager, engine *whisper.Engine) *ReplicationManager {
	rm := &ReplicationManager{
		store:   store,
		whisper: engine,
		peers:   make(map[string]time.Time),
		pinned:  make(map[string]*pinnedStore),
	}
	return rm
}

// SetReplicatorIdentity configures the keypair used when this node acts as
// a replicator (pins and serves other stores).
func (r *ReplicationManager) SetReplicatorIdentity(pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replicatorPubKey = pub
	r.replicatorPriv = priv
}

// SetAnnounceAddresses sets the custom reachability addresses that will be
// included in StoreAd broadcasts so peers know WHERE to fetch this store.
func (r *ReplicationManager) SetAnnounceAddresses(addrs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.announceAddresses = addrs
}

// SetCapacity sets the maximum storage budget (in MB) for pinned stores.
func (r *ReplicationManager) SetCapacity(mb uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capacityMB = mb
}

// OnStoreAd registers a callback for incoming StoreAd broadcasts.
func (r *ReplicationManager) OnStoreAd(fn func(StoreAd)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adSubscribers = append(r.adSubscribers, fn)
}

// OnPinAnnouncement registers a callback for incoming pin announcements.
func (r *ReplicationManager) OnPinAnnouncement(fn func(PinAnnouncement)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pinSubscribers = append(r.pinSubscribers, fn)
}

// RegisterTopics registers the gossip topics with the Whisper engine.
// Must be called before Advertise or StartListening.
func (r *ReplicationManager) RegisterTopics() error {
	if err := r.whisper.RegisterTopic(TopicStoreDirectory, whisper.TopicConfig{
		Mode: whisper.BroadcastOnly,
	}); err != nil {
		return fmt.Errorf("replication: register directory topic: %w", err)
	}
	if err := r.whisper.RegisterTopic(TopicStoreReplicate, whisper.TopicConfig{
		Mode: whisper.BroadcastOnly,
	}); err != nil {
		return fmt.Errorf("replication: register replicate topic: %w", err)
	}

	// Subscribe to incoming messages on both topics.
	r.whisper.Subscribe(TopicStoreDirectory, &adSubscriber{rm: r})
	r.whisper.Subscribe(TopicStoreReplicate, &pinSubscriber{rm: r})

	return nil
}

// Advertise publishes the current store root as a StoreAd via the directory
// gossip topic. Calls Publish() on the StoreManager to get a fresh root.
func (r *ReplicationManager) Advertise(ctx context.Context) error {
	root, err := r.store.Publish()
	if err != nil {
		return fmt.Errorf("replication: publish store: %w", err)
	}

	priv := r.store.PrivKey()

	r.mu.RLock()
	addrs := r.announceAddresses
	r.mu.RUnlock()

	ad := &StoreAd{
		OwnerPubKey:  root.OwnerPubKey,
		Name:         root.Name,
		Categories:   root.Categories,
		Version:      root.Version,
		ListingCount: uint64(r.store.ListingCount()),
		MerkleRoot:   root.MerkleRoot,
		Addresses:    addrs,
	}

	if err := SignStoreAd(ad, priv); err != nil {
		return fmt.Errorf("replication: sign ad: %w", err)
	}

	data, err := marshalStoreAd(ad)
	if err != nil {
		return fmt.Errorf("replication: marshal ad: %w", err)
	}

	if err := r.whisper.Publish(TopicStoreDirectory, data); err != nil {
		return fmt.Errorf("replication: broadcast ad: %w", err)
	}

	return nil
}

// Replicate pins a remote store by fetching its data and storing it locally.
// The storeRoot must have a valid signature. If a newer version is already
// pinned, ErrStaleVersion is returned.
func (r *ReplicationManager) Replicate(ctx context.Context, storeRoot StoreRoot) error {
	ctx, span := observability.Tracer("mercury.store").Start(ctx, "ReplicationManager.Replicate")
	defer span.End()
	// Verify the store root signature.
	if err := VerifyStoreRoot(&storeRoot); err != nil {
		wrapped := fmt.Errorf("replication: verify store root: %w", err)
		observability.RecordError(span, wrapped)
		return wrapped
	}

	storeID := StoreID(storeRoot.OwnerPubKey)

	// Prepare the pin announcement payload before acquiring the lock so we
	// can publish outside the lock (Publish notifies local subscribers which
	// acquire RLock, causing a deadlock if we hold the write lock).
	var pinData []byte

	r.mu.Lock()

	// Check version monotonicity.
	if existing, ok := r.pinned[storeID]; ok {
		if storeRoot.Version <= existing.root.Version {
			r.mu.Unlock()
			return ErrStaleVersion
		}
	}

	// Pin the store root (listings will be populated by ServeStore or delta sync).
	r.pinned[storeID] = &pinnedStore{
		root:     &storeRoot,
		listings: nil,
		pinnedAt: time.Now(),
	}

	// Build pin announcement while holding the lock.
	if r.replicatorPubKey != nil && r.replicatorPriv != nil {
		pa := &PinAnnouncement{
			ReplicatorPubKey: r.replicatorPubKey,
			StoreID:          storeID,
			StoreVersion:     storeRoot.Version,
			CapacityMB:       r.capacityMB,
		}
		if err := SignPinAnnouncement(pa, r.replicatorPriv); err == nil {
			pinData, _ = marshalPinAnnouncement(pa)
		}
	}

	r.mu.Unlock()

	// Publish outside the lock to avoid deadlock with subscriber callbacks.
	if pinData != nil {
		_ = r.whisper.Publish(TopicStoreReplicate, pinData)
	}

	span.SetAttributes(attribute.String("mercury.store.id", storeID))
	observability.StoreReplicateCount().Add(ctx, 1)
	return nil
}

// Unpin removes a pinned store from local storage.
func (r *ReplicationManager) Unpin(storeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pinned[storeID]; !ok {
		return ErrNotPinned
	}
	delete(r.pinned, storeID)
	return nil
}

// IsPinned returns true if the given store is currently pinned.
func (r *ReplicationManager) IsPinned(storeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.pinned[storeID]
	return ok
}

// PinnedVersion returns the version of a pinned store, or 0 if not pinned.
func (r *ReplicationManager) PinnedVersion(storeID string) uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if ps, ok := r.pinned[storeID]; ok {
		return ps.root.Version
	}
	return 0
}

// PinnedStoreIDs returns the IDs of all pinned stores.
func (r *ReplicationManager) PinnedStoreIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.pinned))
	for id := range r.pinned {
		ids = append(ids, id)
	}
	return ids
}

// SetPinnedListings sets the listing data for a pinned store (e.g., after
// fetching via Aether BULK).
func (r *ReplicationManager) SetPinnedListings(storeID string, listings []*Listing) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ps, ok := r.pinned[storeID]
	if !ok {
		return ErrNotPinned
	}
	ps.listings = listings
	return nil
}

// ServeStore handles a data request for a store. It looks up the requested
// listing or store root from pinned data (or the local store if the requester
// is asking for our own store) and returns the serialized response.
func (r *ReplicationManager) ServeStore(ctx context.Context, requesterID string, req StoreRequest) (*StoreResponse, error) {
	r.mu.Lock()
	r.peers[requesterID] = time.Now()
	r.mu.Unlock()

	storeID := req.StoreID

	// Check if this is our own store.
	if storeID == r.store.StoreID() {
		return r.serveLocal(ctx, req)
	}

	// Check pinned stores.
	r.mu.RLock()
	ps, ok := r.pinned[storeID]
	r.mu.RUnlock()
	if !ok {
		return nil, ErrStoreNotFound
	}

	return r.servePinned(ctx, ps, req)
}

// StoreRequest is a data request for store content.
type StoreRequest struct {
	StoreID   string
	ListingID string
	MediaCID  string
	Nonce     []byte
}

// StoreResponse is the response to a store data request.
type StoreResponse struct {
	Payload     []byte
	MerkleProof []byte
	ContentHash [32]byte
}

func (r *ReplicationManager) serveLocal(ctx context.Context, req StoreRequest) (*StoreResponse, error) {
	if req.ListingID != "" {
		listing, err := r.store.GetListing(req.ListingID)
		if err != nil {
			return nil, err
		}

		data, err := MarshalListing(listing)
		if err != nil {
			return nil, err
		}

		contentHash := HashMedia(data)

		// Build Merkle proof.
		tree, err := r.store.BuildMerkleTree()
		if err != nil {
			return nil, err
		}
		leafHash, err := HashListing(listing)
		if err != nil {
			return nil, err
		}
		proof, err := tree.GenerateProof(leafHash)
		if err != nil {
			return nil, err
		}
		proofBytes, err := marshalProof(proof)
		if err != nil {
			return nil, err
		}

		return &StoreResponse{
			Payload:     data,
			MerkleProof: proofBytes,
			ContentHash: contentHash,
		}, nil
	}

	// Return the StoreRoot.
	root := r.store.LastRoot()
	if root == nil {
		return nil, ErrStoreNotFound
	}
	data, err := proto.Marshal(StoreRootToProto(root))
	if err != nil {
		return nil, err
	}
	return &StoreResponse{
		Payload:     data,
		ContentHash: HashMedia(data),
	}, nil
}

func (r *ReplicationManager) servePinned(ctx context.Context, ps *pinnedStore, req StoreRequest) (*StoreResponse, error) {
	if req.ListingID != "" {
		if ps.listings == nil {
			return nil, ErrListingNotFound
		}
		for _, l := range ps.listings {
			if l.ID == req.ListingID {
				data, err := MarshalListing(l)
				if err != nil {
					return nil, err
				}
				contentHash := HashMedia(data)

				// Build Merkle proof from pinned listings.
				tree, err := BuildTree(ps.listings)
				if err != nil {
					return nil, err
				}
				leafHash, err := HashListing(l)
				if err != nil {
					return nil, err
				}
				proof, err := tree.GenerateProof(leafHash)
				if err != nil {
					return nil, err
				}
				proofBytes, err := marshalProof(proof)
				if err != nil {
					return nil, err
				}

				return &StoreResponse{
					Payload:     data,
					MerkleProof: proofBytes,
					ContentHash: contentHash,
				}, nil
			}
		}
		return nil, ErrListingNotFound
	}

	// Return the pinned StoreRoot.
	data, err := proto.Marshal(StoreRootToProto(ps.root))
	if err != nil {
		return nil, err
	}
	return &StoreResponse{
		Payload:     data,
		ContentHash: HashMedia(data),
	}, nil
}

// PeerCount returns the number of peers that have requested data from us.
func (r *ReplicationManager) PeerCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.peers)
}

// --- gossip subscriber adapters ---

type adSubscriber struct {
	rm *ReplicationManager
}

func (s *adSubscriber) OnBroadcast(topic string, from string, payload []byte) {
	ad, err := unmarshalStoreAd(payload)
	if err != nil {
		return
	}
	if !VerifyStoreAd(ad) {
		return
	}
	s.rm.mu.RLock()
	subs := s.rm.adSubscribers
	s.rm.mu.RUnlock()
	for _, fn := range subs {
		fn(*ad)
	}
}

type pinSubscriber struct {
	rm *ReplicationManager
}

func (s *pinSubscriber) OnBroadcast(topic string, from string, payload []byte) {
	pa, err := unmarshalPinAnnouncement(payload)
	if err != nil {
		return
	}
	if !VerifyPinAnnouncement(pa) {
		return
	}
	s.rm.mu.RLock()
	subs := s.rm.pinSubscribers
	s.rm.mu.RUnlock()
	for _, fn := range subs {
		fn(*pa)
	}
}

// --- serialization helpers ---

// storeAdWire is the JSON envelope for StoreAd serialization.
// Core fields are protobuf-encoded for signing determinism; addresses
// are carried in the JSON envelope so no proto changes are needed.
type storeAdWire struct {
	Core      []byte   `json:"core"`               // protobuf-encoded StoreAdProto
	Addresses []string `json:"addresses,omitempty"` // custom announce addresses
}

func marshalStoreAd(ad *StoreAd) ([]byte, error) {
	msg := &pb.StoreAdProto{
		OwnerPubKey:  ad.OwnerPubKey,
		Name:         ad.Name,
		Categories:   ad.Categories,
		Version:      ad.Version,
		ListingCount: ad.ListingCount,
		MerkleRoot:   ad.MerkleRoot,
		Signature:    ad.Signature,
	}
	core, err := proto.Marshal(msg)
	if err != nil {
		return nil, err
	}
	wire := storeAdWire{
		Core:      core,
		Addresses: ad.Addresses,
	}
	return json.Marshal(wire)
}

func unmarshalStoreAd(data []byte) (*StoreAd, error) {
	// Try JSON envelope first (new format with addresses).
	var wire storeAdWire
	if err := json.Unmarshal(data, &wire); err == nil && len(wire.Core) > 0 {
		msg := &pb.StoreAdProto{}
		if err := proto.Unmarshal(wire.Core, msg); err != nil {
			return nil, err
		}
		return &StoreAd{
			OwnerPubKey:  msg.OwnerPubKey,
			Name:         msg.Name,
			Categories:   msg.Categories,
			Version:      msg.Version,
			ListingCount: msg.ListingCount,
			MerkleRoot:   msg.MerkleRoot,
			Signature:    msg.Signature,
			Addresses:    wire.Addresses,
		}, nil
	}

	// Fall back to raw protobuf (backward compatibility with old nodes).
	msg := &pb.StoreAdProto{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, err
	}
	return &StoreAd{
		OwnerPubKey:  msg.OwnerPubKey,
		Name:         msg.Name,
		Categories:   msg.Categories,
		Version:      msg.Version,
		ListingCount: msg.ListingCount,
		MerkleRoot:   msg.MerkleRoot,
		Signature:    msg.Signature,
	}, nil
}

func marshalPinAnnouncement(pa *PinAnnouncement) ([]byte, error) {
	msg := &pb.PinAnnouncementProto{
		ReplicatorPubKey: pa.ReplicatorPubKey,
		StoreId:          pa.StoreID,
		StoreVersion:     pa.StoreVersion,
		CapacityMb:       pa.CapacityMB,
		Signature:        pa.Signature,
	}
	return proto.Marshal(msg)
}

func unmarshalPinAnnouncement(data []byte) (*PinAnnouncement, error) {
	msg := &pb.PinAnnouncementProto{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, err
	}
	return &PinAnnouncement{
		ReplicatorPubKey: msg.ReplicatorPubKey,
		StoreID:          msg.StoreId,
		StoreVersion:     msg.StoreVersion,
		CapacityMB:       msg.CapacityMb,
		Signature:        msg.Signature,
	}, nil
}

// proofWire is the JSON wire format for Merkle proofs.
type proofWire struct {
	LeafHash [32]byte       `json:"leaf"`
	Path     []proofNodeWire `json:"path"`
}

type proofNodeWire struct {
	Hash    [32]byte `json:"h"`
	IsRight bool     `json:"r"`
}

func marshalProof(p *MerkleProof) ([]byte, error) {
	w := proofWire{LeafHash: p.LeafHash}
	w.Path = make([]proofNodeWire, len(p.Path))
	for i, n := range p.Path {
		w.Path[i] = proofNodeWire{Hash: n.Hash, IsRight: n.IsRight}
	}
	return json.Marshal(w)
}

func unmarshalProof(data []byte) (*MerkleProof, error) {
	var w proofWire
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	p := &MerkleProof{LeafHash: w.LeafHash}
	p.Path = make([]ProofNode, len(w.Path))
	for i, n := range w.Path {
		p.Path[i] = ProofNode{Hash: n.Hash, IsRight: n.IsRight}
	}
	return p, nil
}
