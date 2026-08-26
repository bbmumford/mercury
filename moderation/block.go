/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package moderation

import (
	"crypto/ed25519"
	"encoding/hex"
	"sync"
	"time"
)

// BlockList manages a local-only list of blocked peers. Block lists are NOT
// propagated via gossip -- blocking is a personal choice stored locally.
type BlockList struct {
	OwnerPubKey ed25519.PublicKey
	Blocked     map[string]time.Time // hex pubkey -> blocked at
	mu          sync.RWMutex
}

// NewBlockList creates a BlockList for the given owner.
func NewBlockList(owner ed25519.PublicKey) *BlockList {
	return &BlockList{
		OwnerPubKey: owner,
		Blocked:     make(map[string]time.Time),
	}
}

// Block adds a peer to the block list. If the peer is already blocked,
// this is a no-op (the original block time is preserved).
func (bl *BlockList) Block(pubkey ed25519.PublicKey) {
	key := hex.EncodeToString(pubkey)

	bl.mu.Lock()
	defer bl.mu.Unlock()

	if _, exists := bl.Blocked[key]; !exists {
		bl.Blocked[key] = time.Now()
	}
}

// Unblock removes a peer from the block list.
func (bl *BlockList) Unblock(pubkey ed25519.PublicKey) {
	key := hex.EncodeToString(pubkey)

	bl.mu.Lock()
	defer bl.mu.Unlock()
	delete(bl.Blocked, key)
}

// IsBlocked returns true if the given peer is on the block list.
func (bl *BlockList) IsBlocked(pubkey ed25519.PublicKey) bool {
	key := hex.EncodeToString(pubkey)

	bl.mu.RLock()
	defer bl.mu.RUnlock()
	_, exists := bl.Blocked[key]
	return exists
}

// BlockedAt returns the time a peer was blocked, or zero time if not blocked.
func (bl *BlockList) BlockedAt(pubkey ed25519.PublicKey) time.Time {
	key := hex.EncodeToString(pubkey)

	bl.mu.RLock()
	defer bl.mu.RUnlock()
	return bl.Blocked[key]
}

// Count returns the number of blocked peers.
func (bl *BlockList) Count() int {
	bl.mu.RLock()
	defer bl.mu.RUnlock()
	return len(bl.Blocked)
}

// All returns a copy of all blocked peer hex keys with their block timestamps.
func (bl *BlockList) All() map[string]time.Time {
	bl.mu.RLock()
	defer bl.mu.RUnlock()

	result := make(map[string]time.Time, len(bl.Blocked))
	for k, v := range bl.Blocked {
		result[k] = v
	}
	return result
}

// FilterMessages takes a slice of peer hex keys and returns only the non-blocked ones.
func (bl *BlockList) FilterMessages(peerHexKeys []string) []string {
	bl.mu.RLock()
	defer bl.mu.RUnlock()

	var result []string
	for _, key := range peerHexKeys {
		if _, blocked := bl.Blocked[key]; !blocked {
			result = append(result, key)
		}
	}
	return result
}
