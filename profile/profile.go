/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package profile

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/bbmumford/whisper"
)

const (
	// topicProfiles is the StatefulMerge topic for user profiles.
	topicProfiles = "mercury.profiles"

	// maxBioLength is the maximum bio length in characters.
	maxBioLength = 500

	// maxNameLength is the maximum display name length.
	maxNameLength = 64
)

// UserProfile represents a user's public identity on the Mercury marketplace.
// Profiles are gossiped via Whisper StatefulMerge and stored locally.
type UserProfile struct {
	PubKey    ed25519.PublicKey `json:"pubKey"`
	Name      string            `json:"name"`
	Bio       string            `json:"bio"`
	AvatarCID string            `json:"avatarCid"` // content-addressed avatar image
	Location  string            `json:"location"`  // general area, not exact (ISO 3166-1 alpha-2)
	JoinedAt  time.Time         `json:"joinedAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
	Signature []byte            `json:"signature"`
}

// profileDigest computes the SHA-256 hash of all UserProfile fields except Signature.
func profileDigest(p *UserProfile) []byte {
	h := sha256.New()
	h.Write(p.PubKey)
	h.Write([]byte(p.Name))
	h.Write([]byte(p.Bio))
	h.Write([]byte(p.AvatarCID))
	h.Write([]byte(p.Location))
	h.Write([]byte(p.JoinedAt.Format(time.RFC3339Nano)))
	h.Write([]byte(p.UpdatedAt.Format(time.RFC3339Nano)))
	return h.Sum(nil)
}

// Sign signs the profile with the owner's Ed25519 private key.
func (p *UserProfile) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("profile: invalid private key size %d", len(privateKey))
	}
	digest := profileDigest(p)
	p.Signature = ed25519.Sign(privateKey, digest)
	return nil
}

// Verify checks that the profile signature is valid.
func (p *UserProfile) Verify() bool {
	if len(p.PubKey) != ed25519.PublicKeySize {
		return false
	}
	if len(p.Signature) != ed25519.SignatureSize {
		return false
	}
	digest := profileDigest(p)
	return ed25519.Verify(p.PubKey, digest, p.Signature)
}

// profileStateStore answers whisper.StateStore for the mercury.profiles
// topic. Fingerprint, Snapshot and Delta read the service's record set,
// which holds the encoding of exactly the profiles in the cache, so the
// summary whisper's G2 digest compares covers every held profile's
// content, and Delta filters on the time a profile was stored here rather
// than its author's UpdatedAt, which whisper's per-peer watermark (the
// local time it last selected records for that peer) cannot be compared
// with. Apply admits through mergeProfile, the rule Set and the gossip
// subscriber use.
type profileStateStore struct {
	service *ProfileService
}

// Fingerprint summarises every held profile's content; 0 when none is held.
func (s *profileStateStore) Fingerprint() uint64 { return s.service.records.Fingerprint() }

// Snapshot returns every held profile.
func (s *profileStateStore) Snapshot() [][]byte { return s.service.records.Snapshot() }

// Delta returns the profiles stored here at or after since.
func (s *profileStateStore) Delta(since time.Time) [][]byte { return s.service.records.Delta(since) }

// BucketDigest lets a fingerprint mismatch narrow to the differing buckets.
func (s *profileStateStore) BucketDigest(buckets uint16) []uint64 {
	return s.service.records.BucketDigest(buckets)
}

// RecordsInBuckets serves the profiles of the buckets a peer differs on.
func (s *profileStateStore) RecordsInBuckets(buckets uint16, want []bool) [][]byte {
	return s.service.records.RecordsInBuckets(buckets, want)
}

// Apply admits a profile a peer sent through state sync, refusing what
// mergeProfile refuses.
func (s *profileStateStore) Apply(data []byte) error {
	var p UserProfile
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("profile: unmarshal: %w", err)
	}
	return s.service.mergeProfile(&p)
}

// ProfileService manages user profiles with gossip-based replication.
// records holds the encoding of each profile in cache under the same key,
// written only by mergeProfile under mu, so the two never disagree.
type ProfileService struct {
	whisper   *whisper.Engine
	cache     map[string]*UserProfile // hex(pubkey) -> profile
	records   *whisper.RecordSet      // hex(pubkey) -> encoded profile
	following map[string]bool         // hex(pubkey) -> true if following
	mu        sync.RWMutex
}

// NewProfileService creates a ProfileService wired to the given Whisper engine.
// It registers the mercury.profiles StatefulMerge topic for profile gossip.
func NewProfileService(engine *whisper.Engine) *ProfileService {
	ps := &ProfileService{
		whisper:   engine,
		cache:     make(map[string]*UserProfile),
		records:   whisper.NewRecordSet(),
		following: make(map[string]bool),
	}

	store := &profileStateStore{service: ps}

	// Register the StatefulMerge topic for profiles
	_ = engine.RegisterTopic(topicProfiles, whisper.TopicConfig{
		Mode:  whisper.StatefulMerge,
		Store: store,
		Rumor: true,
	})

	// Subscribe for broadcast notifications
	engine.Subscribe(topicProfiles, &profileSubscriber{service: ps})

	return ps
}

// Set publishes a user profile. The profile must be signed and valid.
func (ps *ProfileService) Set(profile UserProfile) error {
	// Apply locally, under the rule a peer's copy is admitted by.
	if err := ps.mergeProfile(&profile); err != nil {
		return err
	}

	// Publish to gossip — mergeProfile already applied local state above,
	// so we skip cfg.Store.Apply (that path is for the gossip receive side).
	data, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("profile: marshal: %w", err)
	}

	// Broadcast to peers via Whisper.
	if err := ps.whisper.Publish(topicProfiles, data); err != nil {
		return fmt.Errorf("profile: publish: %w", err)
	}

	return nil
}

// Get retrieves a user profile by public key. Returns nil if not found.
func (ps *ProfileService) Get(pubkey ed25519.PublicKey) (*UserProfile, error) {
	if len(pubkey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("profile: invalid public key size %d", len(pubkey))
	}

	key := hex.EncodeToString(pubkey)

	ps.mu.RLock()
	defer ps.mu.RUnlock()

	p, ok := ps.cache[key]
	if !ok {
		return nil, nil
	}
	cp := *p
	return &cp, nil
}

// Follow adds a pubkey to the local following list.
func (ps *ProfileService) Follow(pubkey ed25519.PublicKey) error {
	if len(pubkey) != ed25519.PublicKeySize {
		return fmt.Errorf("profile: invalid public key size %d", len(pubkey))
	}

	key := hex.EncodeToString(pubkey)

	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.following[key] = true
	return nil
}

// Unfollow removes a pubkey from the local following list.
func (ps *ProfileService) Unfollow(pubkey ed25519.PublicKey) error {
	if len(pubkey) != ed25519.PublicKeySize {
		return fmt.Errorf("profile: invalid public key size %d", len(pubkey))
	}

	key := hex.EncodeToString(pubkey)

	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.following, key)
	return nil
}

// Following returns a list of all pubkeys the user is following.
func (ps *ProfileService) Following() []ed25519.PublicKey {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	result := make([]ed25519.PublicKey, 0, len(ps.following))
	for hexKey := range ps.following {
		pub, err := hex.DecodeString(hexKey)
		if err != nil {
			continue
		}
		result = append(result, ed25519.PublicKey(pub))
	}
	return result
}

// IsFollowing returns true if the given pubkey is in the following list.
func (ps *ProfileService) IsFollowing(pubkey ed25519.PublicKey) bool {
	key := hex.EncodeToString(pubkey)

	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.following[key]
}

// CachedProfiles returns the count of locally cached profiles.
func (ps *ProfileService) CachedProfiles() int {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return len(ps.cache)
}

// mergeProfile admits one profile, from Set, from gossip
// (profileSubscriber.OnBroadcast) or from state sync
// (profileStateStore.Apply), under one rule, so a peer cannot place on one
// path a profile another refuses: validateProfile's checks, then the later
// UpdatedAt of the same author wins. Two versions with the same UpdatedAt
// are settled by the greater signature, so every node keeps the same one
// and the content fingerprints whisper compares can agree. A profile no
// newer than the one held is not an error.
func (ps *ProfileService) mergeProfile(p *UserProfile) error {
	if err := validateProfile(p); err != nil {
		return err
	}
	key := hex.EncodeToString(p.PubKey)

	ps.mu.Lock()
	defer ps.mu.Unlock()

	if existing, ok := ps.cache[key]; ok && !profileSupersedes(p, existing) {
		return nil
	}
	cp := *p
	data, err := json.Marshal(&cp)
	if err != nil {
		return fmt.Errorf("profile: marshal: %w", err)
	}
	ps.cache[key] = &cp
	ps.records.Put(key, data)
	return nil
}

// profileSupersedes reports whether incoming replaces held: a later
// UpdatedAt, or the same UpdatedAt and a greater signature.
func profileSupersedes(incoming, held *UserProfile) bool {
	if !incoming.UpdatedAt.Equal(held.UpdatedAt) {
		return incoming.UpdatedAt.After(held.UpdatedAt)
	}
	return bytes.Compare(incoming.Signature, held.Signature) > 0
}

// validateProfile performs comprehensive validation on a profile.
func validateProfile(p *UserProfile) error {
	if len(p.PubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("profile: invalid public key size %d", len(p.PubKey))
	}
	if p.Name == "" {
		return fmt.Errorf("profile: name is required")
	}
	if len(p.Name) > maxNameLength {
		return fmt.Errorf("profile: name length %d exceeds limit %d", len(p.Name), maxNameLength)
	}
	if len(p.Bio) > maxBioLength {
		return fmt.Errorf("profile: bio length %d exceeds limit %d", len(p.Bio), maxBioLength)
	}
	if p.JoinedAt.IsZero() {
		return fmt.Errorf("profile: joined_at is required")
	}
	if p.UpdatedAt.IsZero() {
		return fmt.Errorf("profile: updated_at is required")
	}
	if !p.Verify() {
		return fmt.Errorf("profile: invalid signature")
	}
	return nil
}

// profileSubscriber implements whisper.Subscriber for profile gossip.
type profileSubscriber struct {
	service *ProfileService
}

// OnBroadcast implements whisper.Subscriber. A refused profile is dropped:
// gossip is best-effort.
func (s *profileSubscriber) OnBroadcast(topic string, from string, payload []byte) {
	var p UserProfile
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}
	_ = s.service.mergeProfile(&p)
}
