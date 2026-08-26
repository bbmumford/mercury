/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package store

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/bbmumford/minerva/crdt"
	"github.com/bbmumford/whisper"
)

func generateKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	return pub, priv
}

func testStoreManager(t *testing.T) *StoreManager {
	t.Helper()
	pub, priv := generateKeypair(t)
	return NewStoreManager(StoreManagerConfig{
		OwnerPubKey:  pub,
		OwnerPrivKey: priv,
		Name:         "Test Store",
		Description:  "A test store",
		Categories:   []string{"electronics", "gadgets"},
		NodeID:       "test-node",
		HLC:          crdt.NewHLC("test-node"),
	})
}

func testListing(id string) *Listing {
	return &Listing{
		ID:          id,
		Title:       "Widget " + id,
		Description: "A fine widget",
		Price:       1999,
		Currency:    "USD",
		Images:      []string{"cid-img-1", "cid-img-2"},
		Variants: []Variant{
			{Name: "Size", Value: "Large", PriceModifier: 200},
		},
		ShippingOpts: []ShippingOption{
			{Name: "Standard", Price: 500, EstDays: 5},
		},
		Digital:    false,
		DigitalCID: "",
	}
}

// --- M4: Store Protocol Tests ---

func TestSignAndVerifyStoreRoot(t *testing.T) {
	pub, priv := generateKeypair(t)

	root := &StoreRoot{
		OwnerPubKey: pub,
		Name:        "Test Store",
		Description: "desc",
		Categories:  []string{"cat1"},
		MerkleRoot:  []byte{1, 2, 3},
		Version:     1,
		Timestamp:   time.Now(),
	}

	if err := SignStoreRoot(root, priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(root.Signature) == 0 {
		t.Fatal("signature is empty")
	}
	if err := VerifyStoreRoot(root); err != nil {
		t.Fatalf("verify valid: %v", err)
	}
}

func TestVerifyStoreRootTampered(t *testing.T) {
	pub, priv := generateKeypair(t)

	root := &StoreRoot{
		OwnerPubKey: pub,
		Name:        "Test Store",
		MerkleRoot:  []byte{1, 2, 3},
		Version:     1,
		Timestamp:   time.Now(),
	}

	if err := SignStoreRoot(root, priv); err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Tamper with the name.
	root.Name = "Tampered Store"
	if err := VerifyStoreRoot(root); err != ErrInvalidSignature {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestSignStoreRootNilKey(t *testing.T) {
	root := &StoreRoot{OwnerPubKey: make([]byte, 32)}
	if err := SignStoreRoot(root, nil); err != ErrNilPrivateKey {
		t.Fatalf("expected ErrNilPrivateKey, got %v", err)
	}
}

func TestStoreRootProtoRoundTrip(t *testing.T) {
	pub, priv := generateKeypair(t)
	root := &StoreRoot{
		OwnerPubKey: pub,
		Name:        "Proto Test",
		Description: "roundtrip",
		Categories:  []string{"a", "b"},
		MerkleRoot:  []byte{10, 20, 30},
		Version:     42,
		Timestamp:   time.UnixMilli(time.Now().UnixMilli()), // truncate for comparison
	}
	SignStoreRoot(root, priv)

	msg := StoreRootToProto(root)
	back := StoreRootFromProto(msg)

	if back.Name != root.Name || back.Version != root.Version {
		t.Fatal("proto roundtrip mismatch")
	}
	if !bytes.Equal(back.OwnerPubKey, root.OwnerPubKey) {
		t.Fatal("pubkey mismatch")
	}
	if !back.Timestamp.Equal(root.Timestamp) {
		t.Fatalf("timestamp mismatch: %v vs %v", back.Timestamp, root.Timestamp)
	}
}

// --- Merkle Tree Tests ---

func TestBuildTreeAndProof(t *testing.T) {
	listings := []*Listing{
		testListing("a"),
		testListing("b"),
		testListing("c"),
	}

	tree, err := BuildTree(listings)
	if err != nil {
		t.Fatalf("build tree: %v", err)
	}

	root := tree.Root()
	if root == [32]byte{} {
		t.Fatal("root is zero")
	}
	if tree.LeafCount() != 3 {
		t.Fatalf("expected 3 leaves, got %d", tree.LeafCount())
	}

	// Generate and verify proof for each listing.
	for _, l := range listings {
		leafHash, err := HashListing(l)
		if err != nil {
			t.Fatalf("hash listing: %v", err)
		}
		proof, err := tree.GenerateProof(leafHash)
		if err != nil {
			t.Fatalf("generate proof for %s: %v", l.ID, err)
		}
		ok, err := VerifyProof(l, proof, root)
		if err != nil {
			t.Fatalf("verify proof: %v", err)
		}
		if !ok {
			t.Fatalf("proof verification failed for listing %s", l.ID)
		}
	}
}

func TestMerkleRootChangesOnUpdate(t *testing.T) {
	listings := []*Listing{
		testListing("a"),
		testListing("b"),
		testListing("c"),
	}

	tree1, _ := BuildTree(listings)
	root1 := tree1.Root()

	// Modify one listing.
	listings[1].Title = "Updated Widget"
	tree2, _ := BuildTree(listings)
	root2 := tree2.Root()

	if root1 == root2 {
		t.Fatal("Merkle root should change when a listing is modified")
	}
}

func TestBuildTreeEmpty(t *testing.T) {
	_, err := BuildTree(nil)
	if err != ErrEmptyTree {
		t.Fatalf("expected ErrEmptyTree, got %v", err)
	}
}

func TestVerifyProofWrongRoot(t *testing.T) {
	listings := []*Listing{testListing("a"), testListing("b")}
	tree, _ := BuildTree(listings)

	leafHash, _ := HashListing(listings[0])
	proof, _ := tree.GenerateProof(leafHash)

	fakeRoot := [32]byte{0xff}
	ok := VerifyProofHash(proof, fakeRoot)
	if ok {
		t.Fatal("proof should fail against wrong root")
	}
}

// --- StoreManager CRUD Tests ---

func TestAddAndGetListing(t *testing.T) {
	sm := testStoreManager(t)
	l := testListing("item-1")

	if err := sm.AddListing(l); err != nil {
		t.Fatalf("add: %v", err)
	}

	got, err := sm.GetListing("item-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Widget item-1" {
		t.Fatalf("expected title 'Widget item-1', got %q", got.Title)
	}
	if got.Price != 1999 {
		t.Fatalf("expected price 1999, got %d", got.Price)
	}
	if len(got.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(got.Images))
	}
	if len(got.Variants) != 1 {
		t.Fatalf("expected 1 variant, got %d", len(got.Variants))
	}
}

func TestUpdateListing(t *testing.T) {
	sm := testStoreManager(t)
	sm.AddListing(testListing("item-1"))

	err := sm.UpdateListing(&Listing{
		ID:    "item-1",
		Title: "Updated Widget",
		Price: 2499,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := sm.GetListing("item-1")
	if got.Title != "Updated Widget" {
		t.Fatalf("title not updated: %q", got.Title)
	}
	if got.Price != 2499 {
		t.Fatalf("price not updated: %d", got.Price)
	}
	// Description should be unchanged.
	if got.Description != "A fine widget" {
		t.Fatalf("description changed unexpectedly: %q", got.Description)
	}
}

func TestRemoveListing(t *testing.T) {
	sm := testStoreManager(t)
	sm.AddListing(testListing("item-1"))

	if err := sm.RemoveListing("item-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	_, err := sm.GetListing("item-1")
	if err != ErrListingNotFound {
		t.Fatalf("expected ErrListingNotFound, got %v", err)
	}

	// Should not appear in AllListings.
	if len(sm.AllListings()) != 0 {
		t.Fatal("deleted listing still appears in AllListings")
	}
}

func TestAllListings(t *testing.T) {
	sm := testStoreManager(t)
	sm.AddListing(testListing("a"))
	sm.AddListing(testListing("b"))
	sm.AddListing(testListing("c"))

	all := sm.AllListings()
	if len(all) != 3 {
		t.Fatalf("expected 3 listings, got %d", len(all))
	}
}

func TestRemoveNonexistent(t *testing.T) {
	sm := testStoreManager(t)
	err := sm.RemoveListing("nope")
	if err != ErrListingNotFound {
		t.Fatalf("expected ErrListingNotFound, got %v", err)
	}
}

func TestUpdateDeletedListing(t *testing.T) {
	sm := testStoreManager(t)
	sm.AddListing(testListing("item-1"))
	sm.RemoveListing("item-1")

	err := sm.UpdateListing(&Listing{ID: "item-1", Title: "Ghost"})
	if err != ErrListingNotFound {
		t.Fatalf("expected ErrListingNotFound, got %v", err)
	}
}

// --- Publish & Version Tests ---

func TestPublishIncrementsVersion(t *testing.T) {
	sm := testStoreManager(t)
	sm.AddListing(testListing("a"))

	r1, err := sm.Publish()
	if err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	if r1.Version != 1 {
		t.Fatalf("expected version 1, got %d", r1.Version)
	}
	if err := VerifyStoreRoot(r1); err != nil {
		t.Fatalf("verify root 1: %v", err)
	}

	sm.AddListing(testListing("b"))
	r2, err := sm.Publish()
	if err != nil {
		t.Fatalf("publish 2: %v", err)
	}
	if r2.Version != 2 {
		t.Fatalf("expected version 2, got %d", r2.Version)
	}
	if bytes.Equal(r1.MerkleRoot, r2.MerkleRoot) {
		t.Fatal("merkle roots should differ after adding a listing")
	}
}

func TestPublishEmptyStore(t *testing.T) {
	sm := testStoreManager(t)
	root, err := sm.Publish()
	if err != nil {
		t.Fatalf("publish empty: %v", err)
	}
	if root.MerkleRoot != nil {
		t.Fatal("empty store should have nil merkle root")
	}
	if root.Version != 1 {
		t.Fatalf("expected version 1, got %d", root.Version)
	}
}

// --- Listing Serialization Tests ---

func TestListingMarshalRoundTrip(t *testing.T) {
	l := testListing("rt")
	l.CreatedAt = time.UnixMilli(time.Now().UnixMilli())
	l.UpdatedAt = l.CreatedAt

	data, err := MarshalListing(l)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	back, err := UnmarshalListing(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if back.ID != l.ID || back.Title != l.Title || back.Price != l.Price {
		t.Fatal("roundtrip mismatch")
	}
	if len(back.Variants) != len(l.Variants) {
		t.Fatal("variants mismatch")
	}
	if len(back.ShippingOpts) != len(l.ShippingOpts) {
		t.Fatal("shipping opts mismatch")
	}
}

// --- Media CID Tests ---

func TestMediaCID(t *testing.T) {
	data := []byte("hello media content")
	cid := MediaCID(data)
	if len(cid) != 64 { // SHA-256 hex = 64 chars
		t.Fatalf("expected 64-char CID, got %d", len(cid))
	}

	// Same data should produce same CID.
	cid2 := MediaCID(data)
	if cid != cid2 {
		t.Fatal("CID should be deterministic")
	}

	// Different data should produce different CID.
	cid3 := MediaCID([]byte("different"))
	if cid == cid3 {
		t.Fatal("different data should produce different CID")
	}
}

// --- M5: Replication Tests ---

func TestReplicationPinAndServe(t *testing.T) {
	engine := whisper.NewEngine()
	sm := testStoreManager(t)

	sm.AddListing(testListing("item-1"))
	sm.AddListing(testListing("item-2"))

	rm := NewReplicationManager(sm, engine)
	if err := rm.RegisterTopics(); err != nil {
		t.Fatalf("register topics: %v", err)
	}

	// Publish a root.
	root, err := sm.Publish()
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Set up a second replicator to pin this store.
	repPub, repPriv := generateKeypair(t)
	rm.SetReplicatorIdentity(repPub, repPriv)
	rm.SetCapacity(1000)

	if err := rm.Replicate(t.Context(), *root); err != nil {
		t.Fatalf("replicate: %v", err)
	}

	storeID := sm.StoreID()
	if !rm.IsPinned(storeID) {
		t.Fatal("store should be pinned")
	}
	if rm.PinnedVersion(storeID) != root.Version {
		t.Fatalf("pinned version mismatch")
	}
}

func TestReplicationRefuseStaleVersion(t *testing.T) {
	engine := whisper.NewEngine()
	sm := testStoreManager(t)
	sm.AddListing(testListing("a"))

	rm := NewReplicationManager(sm, engine)
	rm.RegisterTopics()

	root1, _ := sm.Publish()
	sm.AddListing(testListing("b"))
	root2, _ := sm.Publish()

	// Pin version 2.
	rm.Replicate(t.Context(), *root2)

	// Try to replicate version 1 (stale).
	err := rm.Replicate(t.Context(), *root1)
	if err != ErrStaleVersion {
		t.Fatalf("expected ErrStaleVersion, got %v", err)
	}
}

func TestReplicationUnpin(t *testing.T) {
	engine := whisper.NewEngine()
	sm := testStoreManager(t)
	sm.AddListing(testListing("a"))

	rm := NewReplicationManager(sm, engine)
	rm.RegisterTopics()

	root, _ := sm.Publish()
	rm.Replicate(t.Context(), *root)

	storeID := sm.StoreID()
	if err := rm.Unpin(storeID); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if rm.IsPinned(storeID) {
		t.Fatal("store should not be pinned after unpin")
	}
}

func TestReplicationServeLocal(t *testing.T) {
	engine := whisper.NewEngine()
	sm := testStoreManager(t)

	sm.AddListing(testListing("item-1"))
	sm.Publish()

	rm := NewReplicationManager(sm, engine)

	resp, err := rm.ServeStore(t.Context(), "buyer-1", StoreRequest{
		StoreID:   sm.StoreID(),
		ListingID: "item-1",
		Nonce:     []byte("nonce-123"),
	})
	if err != nil {
		t.Fatalf("serve store: %v", err)
	}
	if resp == nil || len(resp.Payload) == 0 {
		t.Fatal("response payload is empty")
	}
	if len(resp.MerkleProof) == 0 {
		t.Fatal("response merkle proof is empty")
	}

	// Verify the content hash matches the payload.
	expectedHash := HashMedia(resp.Payload)
	if resp.ContentHash != expectedHash {
		t.Fatal("content hash mismatch")
	}
}

func TestReplicationServePinned(t *testing.T) {
	engine := whisper.NewEngine()
	sm := testStoreManager(t)

	sm.AddListing(testListing("item-1"))

	rm := NewReplicationManager(sm, engine)
	rm.RegisterTopics()

	root, _ := sm.Publish()
	rm.Replicate(t.Context(), *root)

	// Set pinned listings data.
	storeID := sm.StoreID()
	rm.SetPinnedListings(storeID, []*Listing{testListing("item-1")})

	resp, err := rm.ServeStore(t.Context(), "buyer-2", StoreRequest{
		StoreID:   storeID,
		ListingID: "item-1",
	})
	if err != nil {
		t.Fatalf("serve pinned: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("empty payload from pinned store")
	}
}

func TestPinAnnouncementSignVerify(t *testing.T) {
	pub, priv := generateKeypair(t)

	pa := &PinAnnouncement{
		ReplicatorPubKey: pub,
		StoreID:          "store-abc",
		StoreVersion:     5,
		CapacityMB:       1000,
	}

	if err := SignPinAnnouncement(pa, priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !VerifyPinAnnouncement(pa) {
		t.Fatal("valid pin announcement failed verification")
	}

	// Tamper.
	pa.StoreVersion = 999
	if VerifyPinAnnouncement(pa) {
		t.Fatal("tampered announcement should fail verification")
	}
}

func TestStoreAdSignVerify(t *testing.T) {
	pub, priv := generateKeypair(t)

	ad := &StoreAd{
		OwnerPubKey:  pub,
		Name:         "My Store",
		Categories:   []string{"electronics"},
		Version:      3,
		ListingCount: 10,
		MerkleRoot:   []byte{1, 2, 3},
	}

	if err := SignStoreAd(ad, priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !VerifyStoreAd(ad) {
		t.Fatal("valid store ad failed verification")
	}

	ad.Name = "Tampered"
	if VerifyStoreAd(ad) {
		t.Fatal("tampered ad should fail verification")
	}
}

func TestMultipleReplicatorsSameStore(t *testing.T) {
	engine := whisper.NewEngine()
	sm := testStoreManager(t)
	sm.AddListing(testListing("a"))

	root, _ := sm.Publish()

	// Create two replicators.
	rm1 := NewReplicationManager(sm, engine)
	rm1.RegisterTopics()

	// rm2 shares the engine but has its own pinned state.
	rm2 := &ReplicationManager{
		store:   sm,
		whisper: engine,
		peers:   make(map[string]time.Time),
		pinned:  make(map[string]*pinnedStore),
	}

	rm1.Replicate(t.Context(), *root)
	rm2.Replicate(t.Context(), *root)

	storeID := sm.StoreID()
	if !rm1.IsPinned(storeID) {
		t.Fatal("rm1 should have store pinned")
	}
	if !rm2.IsPinned(storeID) {
		t.Fatal("rm2 should have store pinned")
	}
}

func TestPeerCountTracking(t *testing.T) {
	engine := whisper.NewEngine()
	sm := testStoreManager(t)
	sm.AddListing(testListing("a"))
	sm.Publish()

	rm := NewReplicationManager(sm, engine)

	rm.ServeStore(t.Context(), "peer-1", StoreRequest{StoreID: sm.StoreID()})
	rm.ServeStore(t.Context(), "peer-2", StoreRequest{StoreID: sm.StoreID()})
	rm.ServeStore(t.Context(), "peer-1", StoreRequest{StoreID: sm.StoreID()}) // duplicate

	if rm.PeerCount() != 2 {
		t.Fatalf("expected 2 peers, got %d", rm.PeerCount())
	}
}
