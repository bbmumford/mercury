/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package offline

import (
	"fmt"
	"sync"
	"time"

	"github.com/bbmumford/mercury/order"
)

const (
	// staleFreshnessThreshold is the max age before a store is considered stale.
	staleFreshnessThreshold = 72 * time.Hour
)

// OfflineManager tracks pre-authorised listing commitments and processes
// orders when sellers are offline.
type OfflineManager struct {
	commitments map[string]*ListingCommitment // listingID -> commitment
	consumed    map[string]int                // listingID -> quantity consumed
	mu          sync.RWMutex
}

// NewOfflineManager creates an OfflineManager with empty commitment state.
func NewOfflineManager() *OfflineManager {
	return &OfflineManager{
		commitments: make(map[string]*ListingCommitment),
		consumed:    make(map[string]int),
	}
}

// RegisterCommitment stores a signed listing commitment. The commitment must
// be valid (signed, not expired, correct fields).
func (om *OfflineManager) RegisterCommitment(c ListingCommitment) error {
	if err := c.IsValid(); err != nil {
		return fmt.Errorf("offline: invalid commitment: %w", err)
	}

	om.mu.Lock()
	defer om.mu.Unlock()

	// Replace any existing commitment for this listing
	om.commitments[c.ListingID] = &c
	// Reset consumed count when a new commitment is registered
	om.consumed[c.ListingID] = 0
	return nil
}

// GetCommitment returns the commitment for a listing, or nil if none exists.
func (om *OfflineManager) GetCommitment(listingID string) *ListingCommitment {
	om.mu.RLock()
	defer om.mu.RUnlock()
	c := om.commitments[listingID]
	if c == nil {
		return nil
	}
	// Return a copy
	cp := *c
	return &cp
}

// CanAutoAccept returns true if the listing has a valid, non-expired commitment
// with auto-accept enabled and remaining quantity.
func (om *OfflineManager) CanAutoAccept(listingID string) bool {
	om.mu.RLock()
	defer om.mu.RUnlock()

	c, ok := om.commitments[listingID]
	if !ok {
		return false
	}
	if !c.AutoAccept {
		return false
	}
	if c.IsExpired() {
		return false
	}
	consumed := om.consumed[listingID]
	return consumed < c.MaxQuantity
}

// RemainingQuantity returns how many units are still available under
// the commitment for a listing.
func (om *OfflineManager) RemainingQuantity(listingID string) int {
	om.mu.RLock()
	defer om.mu.RUnlock()

	c, ok := om.commitments[listingID]
	if !ok {
		return 0
	}
	consumed := om.consumed[listingID]
	remaining := c.MaxQuantity - consumed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// ProcessOfflineOrder validates and processes an order against a listing
// commitment. If the commitment allows auto-accept and has remaining quantity,
// the order is accepted and the consumed count is incremented.
func (om *OfflineManager) ProcessOfflineOrder(intent order.OrderIntent) error {
	if !intent.Verify() {
		return fmt.Errorf("offline: invalid order intent signature")
	}

	om.mu.Lock()
	defer om.mu.Unlock()

	c, ok := om.commitments[intent.ListingID]
	if !ok {
		return fmt.Errorf("offline: no commitment found for listing %q", intent.ListingID)
	}

	if err := c.IsValid(); err != nil {
		return fmt.Errorf("offline: commitment invalid: %w", err)
	}

	if !c.AutoAccept {
		return fmt.Errorf("offline: commitment for listing %q does not allow auto-accept", intent.ListingID)
	}

	consumed := om.consumed[intent.ListingID]
	if consumed+intent.Quantity > c.MaxQuantity {
		return fmt.Errorf("offline: insufficient quantity (requested %d, remaining %d)",
			intent.Quantity, c.MaxQuantity-consumed)
	}

	// Verify price matches (TotalPrice should be PriceSats * Quantity)
	expectedTotal := c.PriceSats * uint64(intent.Quantity)
	if intent.TotalPrice != expectedTotal {
		return fmt.Errorf("offline: price mismatch (intent total %d, expected %d)",
			intent.TotalPrice, expectedTotal)
	}

	if intent.Currency != c.Currency {
		return fmt.Errorf("offline: currency mismatch (intent %q, commitment %q)",
			intent.Currency, c.Currency)
	}

	om.consumed[intent.ListingID] = consumed + intent.Quantity
	return nil
}

// RemoveCommitment removes a commitment for a listing.
func (om *OfflineManager) RemoveCommitment(listingID string) {
	om.mu.Lock()
	defer om.mu.Unlock()
	delete(om.commitments, listingID)
	delete(om.consumed, listingID)
}

// PurgeExpired removes all expired commitments and returns the count removed.
func (om *OfflineManager) PurgeExpired() int {
	om.mu.Lock()
	defer om.mu.Unlock()

	var count int
	for id, c := range om.commitments {
		if c.IsExpired() {
			delete(om.commitments, id)
			delete(om.consumed, id)
			count++
		}
	}
	return count
}

// ActiveCommitments returns a list of all non-expired commitments.
func (om *OfflineManager) ActiveCommitments() []ListingCommitment {
	om.mu.RLock()
	defer om.mu.RUnlock()

	var result []ListingCommitment
	for _, c := range om.commitments {
		if !c.IsExpired() {
			result = append(result, *c)
		}
	}
	return result
}
