/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package discovery

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/bbmumford/whisper"
)

const (
	// defaultMaxAge is the default duration after which announcements are pruned.
	// Set to 3x the default announce interval (15 minutes).
	defaultMaxAge = 15 * time.Minute

	// pruneInterval is how often the resolver runs automatic pruning.
	pruneInterval = 1 * time.Minute
)

// AnnounceResolver maintains a cache of received announcements from peers.
// It subscribes to the mercury.announce gossip topic and validates + caches
// incoming AnnounceRecords, providing a lookup API for peer addresses.
type AnnounceResolver struct {
	whisper *whisper.Engine
	mu      sync.RWMutex
	records map[string]*AnnounceRecord // hex pubkey -> latest announcement
	maxAge  time.Duration

	cancel  context.CancelFunc
	stopped chan struct{}
}

// NewAnnounceResolver creates a resolver that caches peer announcements.
func NewAnnounceResolver(engine *whisper.Engine) *AnnounceResolver {
	return &AnnounceResolver{
		whisper: engine,
		records: make(map[string]*AnnounceRecord),
		maxAge:  defaultMaxAge,
		stopped: make(chan struct{}),
	}
}

// SetMaxAge overrides the default max age for cached announcements.
func (r *AnnounceResolver) SetMaxAge(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxAge = d
}

// Start registers the gossip topic, subscribes for incoming announcements,
// and starts a background prune loop.
func (r *AnnounceResolver) Start(ctx context.Context) error {
	// Register the announce topic (tolerate if already registered by Announcer).
	_ = r.whisper.RegisterTopic(TopicAnnounce, whisper.TopicConfig{
		Mode: whisper.BroadcastOnly,
		TTL:  announceTTL,
	})

	// Subscribe to incoming announcements.
	r.whisper.Subscribe(TopicAnnounce, &announceSubscriber{resolver: r})

	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	// Background prune loop.
	go func() {
		defer close(r.stopped)
		ticker := time.NewTicker(pruneInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.PruneStale()
			}
		}
	}()

	return nil
}

// Stop cancels the prune loop.
func (r *AnnounceResolver) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		<-r.stopped
	}
}

// Resolve looks up the latest AnnounceRecord for the given public key.
// Returns nil, false if no announcement is cached or if it has expired.
func (r *AnnounceResolver) Resolve(pubkey []byte) (*AnnounceRecord, bool) {
	hexKey := NodeHex(pubkey)

	r.mu.RLock()
	rec, ok := r.records[hexKey]
	maxAge := r.maxAge
	r.mu.RUnlock()

	if !ok {
		return nil, false
	}

	// Check staleness.
	if time.Since(rec.Timestamp) > maxAge {
		return nil, false
	}

	return rec, true
}

// AllNodes returns all known, non-expired AnnounceRecords.
func (r *AnnounceResolver) AllNodes() []*AnnounceRecord {
	r.mu.RLock()
	maxAge := r.maxAge
	all := make([]*AnnounceRecord, 0, len(r.records))
	for _, rec := range r.records {
		if time.Since(rec.Timestamp) <= maxAge {
			all = append(all, rec)
		}
	}
	r.mu.RUnlock()
	return all
}

// PruneStale removes expired announcements from the cache.
func (r *AnnounceResolver) PruneStale() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key, rec := range r.records {
		if time.Since(rec.Timestamp) > r.maxAge {
			delete(r.records, key)
		}
	}
}

// NodeCount returns the number of cached (including possibly stale) records.
func (r *AnnounceResolver) NodeCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.records)
}

// handleAnnouncement processes a single incoming announcement.
func (r *AnnounceResolver) handleAnnouncement(rec *AnnounceRecord) {
	// Verify signature.
	if !VerifyAnnouncement(rec) {
		return
	}

	hexKey := NodeHex(rec.NodePubKey)

	r.mu.Lock()
	defer r.mu.Unlock()

	// Only accept if newer than existing record.
	if existing, ok := r.records[hexKey]; ok {
		if !rec.Timestamp.After(existing.Timestamp) {
			return
		}
	}

	r.records[hexKey] = rec
}

// announceSubscriber implements whisper.Subscriber for the announce topic.
type announceSubscriber struct {
	resolver *AnnounceResolver
}

func (s *announceSubscriber) OnBroadcast(topic string, from string, payload []byte) {
	var rec AnnounceRecord
	if err := json.Unmarshal(payload, &rec); err != nil {
		return
	}
	s.resolver.handleAnnouncement(&rec)
}
