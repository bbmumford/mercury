/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package discovery

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/bbmumford/whisper"
)

const (
	// TopicAnnounce is the gossip topic for node reachability announcements.
	TopicAnnounce = "mercury.announce"

	// announceTTL is the TTL for announcements on the gossip topic.
	// Set to 10 minutes so stale nodes drop out automatically.
	announceTTL = 10 * time.Minute

	// defaultAnnounceInterval is how often announcements are re-published.
	defaultAnnounceInterval = 5 * time.Minute
)

// AnnounceConfig holds custom reachability settings for this node.
type AnnounceConfig struct {
	// Addresses are custom announce addresses (e.g., "wss://my-store.com:443", "relay.mysite.com:41641").
	Addresses []string

	// DNSRecords are DNS TXT records to publish (e.g., "_mercury._tcp.mystore.com").
	DNSRecords []string

	// Interval is the re-announce interval (default 5 minutes).
	Interval time.Duration

	// NodePubKey is the Ed25519 public key for signing.
	NodePubKey []byte
}

// AnnounceRecord is a signed reachability advertisement broadcast via gossip.
type AnnounceRecord struct {
	NodePubKey []byte    `json:"nodePubKey"`
	Addresses  []string  `json:"addresses"`
	DNSRecords []string  `json:"dnsRecords,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
	Signature  []byte    `json:"signature"`
}

// signingPayload produces the deterministic byte sequence for signing.
// Excludes the Signature field itself.
func (r *AnnounceRecord) signingPayload() []byte {
	// Build a canonical JSON of the fields that are signed.
	wire := struct {
		NodePubKey []byte   `json:"nodePubKey"`
		Addresses  []string `json:"addresses"`
		DNSRecords []string `json:"dnsRecords,omitempty"`
		Timestamp  int64    `json:"timestamp"`
	}{
		NodePubKey: r.NodePubKey,
		Addresses:  r.Addresses,
		DNSRecords: r.DNSRecords,
		Timestamp:  r.Timestamp.UnixMilli(),
	}
	data, _ := json.Marshal(wire)
	return data
}

// SignAnnouncement signs an AnnounceRecord with the given Ed25519 private key.
func SignAnnouncement(rec *AnnounceRecord, privKey ed25519.PrivateKey) error {
	if privKey == nil {
		return fmt.Errorf("announce: private key is nil")
	}
	if rec.NodePubKey == nil {
		return fmt.Errorf("announce: node public key is nil")
	}
	payload := rec.signingPayload()
	rec.Signature = ed25519.Sign(privKey, payload)
	return nil
}

// VerifyAnnouncement verifies the Ed25519 signature on an AnnounceRecord.
func VerifyAnnouncement(rec *AnnounceRecord) bool {
	if rec.NodePubKey == nil || rec.Signature == nil {
		return false
	}
	if len(rec.NodePubKey) != ed25519.PublicKeySize {
		return false
	}
	payload := rec.signingPayload()
	return ed25519.Verify(ed25519.PublicKey(rec.NodePubKey), payload, rec.Signature)
}

// Announcer periodically publishes this node's reachability to the gossip network.
type Announcer struct {
	config  AnnounceConfig
	whisper *whisper.Engine
	privKey ed25519.PrivateKey

	mu      sync.Mutex
	cancel  context.CancelFunc
	stopped chan struct{}
}

// NewAnnouncer creates an Announcer that will broadcast custom reachability
// addresses on the mercury.announce gossip topic.
func NewAnnouncer(config AnnounceConfig, engine *whisper.Engine, privKey ed25519.PrivateKey) *Announcer {
	if config.Interval <= 0 {
		config.Interval = defaultAnnounceInterval
	}
	return &Announcer{
		config:  config,
		whisper: engine,
		privKey: privKey,
		stopped: make(chan struct{}),
	}
}

// Start registers the gossip topic and begins the periodic announce loop.
// Blocks until ctx is cancelled or Stop is called.
func (a *Announcer) Start(ctx context.Context) error {
	// Register the announce topic as BroadcastOnly with TTL.
	if err := a.whisper.RegisterTopic(TopicAnnounce, whisper.TopicConfig{
		Mode: whisper.BroadcastOnly,
		TTL:  announceTTL,
	}); err != nil {
		log.Printf("[ANNOUNCE] topic registration: %v (continuing)", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.mu.Unlock()

	// Announce immediately on start.
	if err := a.AnnounceNow(); err != nil {
		// Non-fatal: log but continue the loop.
		_ = err
	}

	// Periodic announce loop.
	ticker := time.NewTicker(a.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			close(a.stopped)
			return ctx.Err()
		case <-ticker.C:
			_ = a.AnnounceNow()
		}
	}
}

// Stop cancels the announce loop.
func (a *Announcer) Stop() {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()

	if cancel != nil {
		cancel()
		<-a.stopped
	}
}

// AnnounceNow performs an immediate announcement broadcast.
func (a *Announcer) AnnounceNow() error {
	rec := &AnnounceRecord{
		NodePubKey: a.config.NodePubKey,
		Addresses:  a.config.Addresses,
		DNSRecords: a.config.DNSRecords,
		Timestamp:  time.Now(),
	}

	if err := SignAnnouncement(rec, a.privKey); err != nil {
		return fmt.Errorf("announce: sign: %w", err)
	}

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("announce: marshal: %w", err)
	}

	if err := a.whisper.Publish(TopicAnnounce, data); err != nil {
		return fmt.Errorf("announce: publish: %w", err)
	}

	return nil
}

// NodeHex returns the hex-encoded public key string for a node.
func NodeHex(pubKey []byte) string {
	return hex.EncodeToString(pubKey)
}
