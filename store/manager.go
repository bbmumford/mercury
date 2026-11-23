/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package store

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bbmumford/minerva/crdt"
	"github.com/bbmumford/plutus/observability"
)

// listingField is a single field value stored in the LWW-Map.
// The LWW-Map key is "{listingID}.{fieldName}" and the value is
// JSON-encoded listingField bytes.
type listingField struct {
	Value json.RawMessage `json:"v"`
}

// StoreManager provides CRUD operations for listings backed by a Minerva
// LWW-Map CRDT. Each listing field is an independent key in the map, enabling
// minimal delta sync and per-field conflict resolution via HLC timestamps.
//
// Key format: "{listingID}.{fieldName}" where fieldName is one of:
// title, description, price, currency, images, variants, shippingOpts,
// digital, digitalCID, createdAt, updatedAt, _deleted.
type StoreManager struct {
	mu          sync.RWMutex
	ownerPubKey ed25519.PublicKey
	ownerPriv   ed25519.PrivateKey
	name        string
	description string
	categories  []string
	lwwMap      *crdt.LWWMap
	version     uint64
	lastRoot    *StoreRoot
}

// StoreManagerConfig configures a new StoreManager.
type StoreManagerConfig struct {
	OwnerPubKey  ed25519.PublicKey
	OwnerPrivKey ed25519.PrivateKey
	Name         string
	Description  string
	Categories   []string
	NodeID       string
	HLC          *crdt.HLC
}

// NewStoreManager creates a StoreManager backed by a fresh LWW-Map.
func NewStoreManager(cfg StoreManagerConfig) *StoreManager {
	hlc := cfg.HLC
	if hlc == nil {
		hlc = crdt.NewHLC(cfg.NodeID)
	}
	nodeID := cfg.NodeID
	if nodeID == "" {
		nodeID = StoreID(cfg.OwnerPubKey)
	}
	return &StoreManager{
		ownerPubKey: cfg.OwnerPubKey,
		ownerPriv:   cfg.OwnerPrivKey,
		name:        cfg.Name,
		description: cfg.Description,
		categories:  cfg.Categories,
		lwwMap:      crdt.NewLWWMap("mercury.store."+StoreID(cfg.OwnerPubKey), nodeID, hlc),
	}
}

// AddListing inserts a new listing into the store. All fields are written
// to the LWW-Map as individual keys. Returns an error if the listing ID
// is empty.
func (sm *StoreManager) AddListing(l *Listing) error {
	if l.ID == "" {
		return fmt.Errorf("store: listing ID must not be empty")
	}
	now := time.Now()
	if l.CreatedAt.IsZero() {
		l.CreatedAt = now
	}
	l.UpdatedAt = now

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.setField(l.ID, "title", l.Title)
	sm.setField(l.ID, "description", l.Description)
	sm.setField(l.ID, "price", l.Price)
	sm.setField(l.ID, "currency", l.Currency)
	sm.setField(l.ID, "images", l.Images)
	sm.setField(l.ID, "variants", l.Variants)
	sm.setField(l.ID, "shippingOpts", l.ShippingOpts)
	sm.setField(l.ID, "digital", l.Digital)
	sm.setField(l.ID, "digitalCID", l.DigitalCID)
	sm.setField(l.ID, "createdAt", l.CreatedAt.UnixMilli())
	sm.setField(l.ID, "updatedAt", l.UpdatedAt.UnixMilli())

	// Clear any prior tombstone.
	sm.lwwMap.Delete(fieldKey(l.ID, "_deleted"))
	sm.setField(l.ID, "_deleted", false)

	observability.StorePutCount().Add(context.Background(), 1)
	return nil
}

// UpdateListing updates specific fields of an existing listing. Only non-zero
// fields in the provided Listing are written. The updatedAt timestamp is
// always refreshed.
func (sm *StoreManager) UpdateListing(l *Listing) error {
	if l.ID == "" {
		return fmt.Errorf("store: listing ID must not be empty")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Check listing exists and is not deleted.
	if sm.isDeleted(l.ID) {
		return ErrListingNotFound
	}
	if _, ok := sm.lwwMap.Get(fieldKey(l.ID, "title")); !ok {
		return ErrListingNotFound
	}

	// Partial update: only non-zero/non-empty fields overwrite. Zero-value
	// fields leave the existing value intact so callers can update a subset
	// without specifying every field. To clear a field, use RemoveListing
	// then AddListing with the desired state.
	if l.Title != "" {
		sm.setField(l.ID, "title", l.Title)
	}
	if l.Description != "" {
		sm.setField(l.ID, "description", l.Description)
	}
	if l.Price != 0 {
		sm.setField(l.ID, "price", l.Price)
	}
	if l.Currency != "" {
		sm.setField(l.ID, "currency", l.Currency)
	}
	if len(l.Images) > 0 {
		sm.setField(l.ID, "images", l.Images)
	}
	if len(l.Variants) > 0 {
		sm.setField(l.ID, "variants", l.Variants)
	}
	if len(l.ShippingOpts) > 0 {
		sm.setField(l.ID, "shippingOpts", l.ShippingOpts)
	}
	if l.Digital {
		sm.setField(l.ID, "digital", l.Digital)
	}
	if l.DigitalCID != "" {
		sm.setField(l.ID, "digitalCID", l.DigitalCID)
	}

	now := time.Now()
	sm.setField(l.ID, "updatedAt", now.UnixMilli())

	return nil
}

// RemoveListing marks a listing as deleted by setting the _deleted tombstone.
func (sm *StoreManager) RemoveListing(listingID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, ok := sm.lwwMap.Get(fieldKey(listingID, "title")); !ok {
		return ErrListingNotFound
	}
	sm.setField(listingID, "_deleted", true)
	return nil
}

// GetListing reconstructs a Listing from its LWW-Map fields.
// Returns ErrListingNotFound if the listing does not exist or is deleted.
func (sm *StoreManager) GetListing(listingID string) (*Listing, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.getListing(listingID)
}

func (sm *StoreManager) getListing(listingID string) (*Listing, error) {
	if sm.isDeleted(listingID) {
		return nil, ErrListingNotFound
	}

	titleBytes, ok := sm.lwwMap.Get(fieldKey(listingID, "title"))
	if !ok {
		return nil, ErrListingNotFound
	}

	l := &Listing{ID: listingID}

	// Unmarshal each field from the LWW-Map.
	unmarshalField(titleBytes, &l.Title)
	if v, ok := sm.lwwMap.Get(fieldKey(listingID, "description")); ok {
		unmarshalField(v, &l.Description)
	}
	if v, ok := sm.lwwMap.Get(fieldKey(listingID, "price")); ok {
		unmarshalField(v, &l.Price)
	}
	if v, ok := sm.lwwMap.Get(fieldKey(listingID, "currency")); ok {
		unmarshalField(v, &l.Currency)
	}
	if v, ok := sm.lwwMap.Get(fieldKey(listingID, "images")); ok {
		unmarshalField(v, &l.Images)
	}
	if v, ok := sm.lwwMap.Get(fieldKey(listingID, "variants")); ok {
		unmarshalField(v, &l.Variants)
	}
	if v, ok := sm.lwwMap.Get(fieldKey(listingID, "shippingOpts")); ok {
		unmarshalField(v, &l.ShippingOpts)
	}
	if v, ok := sm.lwwMap.Get(fieldKey(listingID, "digital")); ok {
		unmarshalField(v, &l.Digital)
	}
	if v, ok := sm.lwwMap.Get(fieldKey(listingID, "digitalCID")); ok {
		unmarshalField(v, &l.DigitalCID)
	}
	if v, ok := sm.lwwMap.Get(fieldKey(listingID, "createdAt")); ok {
		var ms int64
		unmarshalField(v, &ms)
		l.CreatedAt = time.UnixMilli(ms)
	}
	if v, ok := sm.lwwMap.Get(fieldKey(listingID, "updatedAt")); ok {
		var ms int64
		unmarshalField(v, &ms)
		l.UpdatedAt = time.UnixMilli(ms)
	}

	return l, nil
}

// AllListings returns all non-deleted listings in the store.
func (sm *StoreManager) AllListings() []*Listing {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Collect unique listing IDs from the LWW-Map keys.
	ids := sm.listingIDs()

	listings := make([]*Listing, 0, len(ids))
	for _, id := range ids {
		l, err := sm.getListing(id)
		if err != nil {
			continue // deleted or incomplete
		}
		listings = append(listings, l)
	}
	return listings
}

// ListingCount returns the number of non-deleted listings.
func (sm *StoreManager) ListingCount() int {
	return len(sm.AllListings())
}

// Publish builds the Merkle tree from all listings, increments the version
// counter, signs the StoreRoot, and returns it.
func (sm *StoreManager) Publish() (*StoreRoot, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	listings := make([]*Listing, 0)
	for _, id := range sm.listingIDs() {
		l, err := sm.getListing(id)
		if err != nil {
			continue
		}
		listings = append(listings, l)
	}

	var merkleRoot []byte
	if len(listings) > 0 {
		tree, err := BuildTree(listings)
		if err != nil {
			return nil, fmt.Errorf("store: build merkle tree: %w", err)
		}
		merkleRoot = tree.RootBytes()
	}

	sm.version++
	root := &StoreRoot{
		OwnerPubKey: sm.ownerPubKey,
		Name:        sm.name,
		Description: sm.description,
		Categories:  sm.categories,
		MerkleRoot:  merkleRoot,
		Version:     sm.version,
		Timestamp:   time.Now(),
	}

	if sm.ownerPriv != nil {
		if err := SignStoreRoot(root, sm.ownerPriv); err != nil {
			return nil, fmt.Errorf("store: sign root: %w", err)
		}
	}

	sm.lastRoot = root
	return root, nil
}

// LastRoot returns the most recently published StoreRoot (nil if never published).
func (sm *StoreManager) LastRoot() *StoreRoot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.lastRoot
}

// Version returns the current version counter.
func (sm *StoreManager) Version() uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.version
}

// LWWMap returns the underlying CRDT map for delta sync.
func (sm *StoreManager) LWWMap() *crdt.LWWMap {
	return sm.lwwMap
}

// OwnerPubKey returns the store owner's public key.
func (sm *StoreManager) OwnerPubKey() ed25519.PublicKey {
	return sm.ownerPubKey
}

// PrivKey returns the store owner's private key under StoreManager's own lock.
func (sm *StoreManager) PrivKey() ed25519.PrivateKey {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.ownerPriv
}

// StoreID returns this store's unique identifier.
func (sm *StoreManager) StoreID() string {
	return StoreID(sm.ownerPubKey)
}

// BuildMerkleTree builds and returns the current Merkle tree for all listings.
func (sm *StoreManager) BuildMerkleTree() (*MerkleTree, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	listings := make([]*Listing, 0)
	for _, id := range sm.listingIDs() {
		l, err := sm.getListing(id)
		if err != nil {
			continue
		}
		listings = append(listings, l)
	}
	if len(listings) == 0 {
		return nil, ErrEmptyTree
	}
	return BuildTree(listings)
}

// --- internal helpers ---

// fieldKey builds the LWW-Map key for a listing field.
func fieldKey(listingID, field string) string {
	return listingID + "." + field
}

// setField JSON-encodes a value and writes it to the LWW-Map.
func (sm *StoreManager) setField(listingID, field string, value any) {
	data, _ := json.Marshal(&listingField{Value: mustJSON(value)})
	sm.lwwMap.Set(fieldKey(listingID, field), data)
}

// isDeleted checks the _deleted tombstone for a listing.
func (sm *StoreManager) isDeleted(listingID string) bool {
	v, ok := sm.lwwMap.Get(fieldKey(listingID, "_deleted"))
	if !ok {
		return false
	}
	var f listingField
	if err := json.Unmarshal(v, &f); err != nil {
		return false
	}
	var deleted bool
	if err := json.Unmarshal(f.Value, &deleted); err != nil {
		return false
	}
	return deleted
}

// listingIDs extracts unique listing IDs from the LWW-Map keys.
func (sm *StoreManager) listingIDs() []string {
	all := sm.lwwMap.All()
	seen := make(map[string]bool, len(all)/8)
	for key := range all {
		parts := strings.SplitN(key, ".", 2)
		if len(parts) == 2 && parts[0] != "" {
			seen[parts[0]] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

// mustJSON marshals v to JSON, panicking on error (should never fail for
// the types we store).
func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic("store: json.Marshal: " + err.Error())
	}
	return data
}

// unmarshalField extracts a value from a JSON-encoded listingField.
func unmarshalField(data []byte, target any) {
	var f listingField
	if err := json.Unmarshal(data, &f); err != nil {
		return
	}
	_ = json.Unmarshal(f.Value, target)
}
