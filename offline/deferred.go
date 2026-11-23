/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package offline

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DeferredSettlement tracks a settlement that cannot be claimed immediately
// (e.g., the seller is offline, the escrow has a time-lock, or the chain is
// temporarily unavailable). The DeferredManager holds these and claims them
// when they become ready.
type DeferredSettlement struct {
	OrderID    string
	EscrowAddr string
	ChainID    string
	ClaimAfter time.Time
	Claimed    bool
}

// PlutusSettler is the interface for claiming escrow settlements via Plutus.
// Injected by the consumer to avoid a hard dependency on the plutus package.
type PlutusSettler interface {
	// ClaimSettlement triggers the on-chain escrow release for the given order.
	ClaimSettlement(ctx context.Context, chainID, escrowAddr string) error
}

// DeferredManager tracks deferred settlements and claims them when ready.
type DeferredManager struct {
	mu       sync.Mutex
	deferred map[string]*DeferredSettlement // orderID -> settlement
	settler  PlutusSettler
}

// NewDeferredManager creates a DeferredManager with the given Plutus settler.
// If settler is nil, ClaimReady will return an error.
func NewDeferredManager(settler PlutusSettler) *DeferredManager {
	return &DeferredManager{
		deferred: make(map[string]*DeferredSettlement),
		settler:  settler,
	}
}

// RegisterDeferred adds a deferred settlement to be claimed after the given time.
func (dm *DeferredManager) RegisterDeferred(orderID, escrowAddr, chainID string, claimAfter time.Time) error {
	if orderID == "" {
		return fmt.Errorf("offline: order ID is required")
	}
	if escrowAddr == "" {
		return fmt.Errorf("offline: escrow address is required")
	}
	if chainID == "" {
		return fmt.Errorf("offline: chain ID is required")
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	dm.deferred[orderID] = &DeferredSettlement{
		OrderID:    orderID,
		EscrowAddr: escrowAddr,
		ChainID:    chainID,
		ClaimAfter: claimAfter,
		Claimed:    false,
	}
	return nil
}

// ClaimReady attempts to claim all deferred settlements whose ClaimAfter time
// has passed. Returns an error if the settler is not configured or if any
// claim fails (partial progress is possible — already-claimed items are skipped).
func (dm *DeferredManager) ClaimReady(ctx context.Context) error {
	if dm.settler == nil {
		return fmt.Errorf("offline: no PlutusSettler configured")
	}

	dm.mu.Lock()
	// Collect ready settlements.
	var ready []*DeferredSettlement
	now := time.Now()
	for _, ds := range dm.deferred {
		if !ds.Claimed && now.After(ds.ClaimAfter) {
			ready = append(ready, ds)
		}
	}
	dm.mu.Unlock()

	var lastErr error
	for _, ds := range ready {
		if err := dm.settler.ClaimSettlement(ctx, ds.ChainID, ds.EscrowAddr); err != nil {
			lastErr = fmt.Errorf("offline: claim deferred settlement %s: %w", ds.OrderID, err)
			continue
		}

		dm.mu.Lock()
		ds.Claimed = true
		dm.mu.Unlock()
	}

	return lastErr
}

// PendingClaims returns all deferred settlements that have not yet been claimed.
func (dm *DeferredManager) PendingClaims() []DeferredSettlement {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	var result []DeferredSettlement
	for _, ds := range dm.deferred {
		if !ds.Claimed {
			result = append(result, *ds)
		}
	}
	return result
}
