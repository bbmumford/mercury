/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package offline

import (
	"fmt"
	"sync"
	"time"
)

// StoreVersion tracks the version and last-seen time for a replicated store.
type StoreVersion struct {
	StoreID    string
	Version    uint64
	SeenAt     time.Time
	SellerHex  string // hex-encoded seller pubkey
}

// FreshnessEnforcer tracks store root versions and enforces freshness rules.
// Replicators use this to decide whether to continue serving a store.
type FreshnessEnforcer struct {
	stores map[string]*StoreVersion // storeID -> latest known version
	mu     sync.RWMutex
}

// NewFreshnessEnforcer creates a FreshnessEnforcer with empty state.
func NewFreshnessEnforcer() *FreshnessEnforcer {
	return &FreshnessEnforcer{
		stores: make(map[string]*StoreVersion),
	}
}

// RecordVersion updates the known version for a store. If the new version is
// higher than the existing one, the seen time is updated.
func (fe *FreshnessEnforcer) RecordVersion(storeID string, version uint64, sellerHex string) {
	fe.mu.Lock()
	defer fe.mu.Unlock()

	existing, ok := fe.stores[storeID]
	if !ok || version > existing.Version {
		fe.stores[storeID] = &StoreVersion{
			StoreID:   storeID,
			Version:   version,
			SeenAt:    time.Now(),
			SellerHex: sellerHex,
		}
	}
}

// IsFresh returns true if the store's latest version was seen within the
// staleness threshold (72 hours). Returns an error if the store is unknown.
func (fe *FreshnessEnforcer) IsFresh(storeID string) (bool, error) {
	fe.mu.RLock()
	defer fe.mu.RUnlock()

	sv, ok := fe.stores[storeID]
	if !ok {
		return false, fmt.Errorf("offline: unknown store %q", storeID)
	}

	return time.Since(sv.SeenAt) <= staleFreshnessThreshold, nil
}

// ShouldServe returns true if a replicator should continue serving the store.
// A store should be served if it was seen within the freshness threshold.
func (fe *FreshnessEnforcer) ShouldServe(storeID string) bool {
	fresh, err := fe.IsFresh(storeID)
	if err != nil {
		return false
	}
	return fresh
}

// LatestVersion returns the latest known version for a store.
func (fe *FreshnessEnforcer) LatestVersion(storeID string) (uint64, bool) {
	fe.mu.RLock()
	defer fe.mu.RUnlock()

	sv, ok := fe.stores[storeID]
	if !ok {
		return 0, false
	}
	return sv.Version, true
}

// NeedsUpdate returns true if a replicator's local version is behind the
// known latest version for a store.
func (fe *FreshnessEnforcer) NeedsUpdate(storeID string, localVersion uint64) bool {
	fe.mu.RLock()
	defer fe.mu.RUnlock()

	sv, ok := fe.stores[storeID]
	if !ok {
		return false
	}
	return localVersion < sv.Version
}

// StaleStores returns all stores that have exceeded the freshness threshold.
func (fe *FreshnessEnforcer) StaleStores() []StoreVersion {
	fe.mu.RLock()
	defer fe.mu.RUnlock()

	var stale []StoreVersion
	for _, sv := range fe.stores {
		if time.Since(sv.SeenAt) > staleFreshnessThreshold {
			stale = append(stale, *sv)
		}
	}
	return stale
}

// RemoveStore removes tracking state for a store.
func (fe *FreshnessEnforcer) RemoveStore(storeID string) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	delete(fe.stores, storeID)
}

// TrackedStores returns all tracked store IDs.
func (fe *FreshnessEnforcer) TrackedStores() []string {
	fe.mu.RLock()
	defer fe.mu.RUnlock()

	ids := make([]string, 0, len(fe.stores))
	for id := range fe.stores {
		ids = append(ids, id)
	}
	return ids
}
