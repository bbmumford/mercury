/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package arbitration

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	plutus_arb "github.com/bbmumford/plutus/arbitrator"
	"github.com/bbmumford/whisper"
)

const (
	// maxSpecializations limits the number of specializations per Mercury profile.
	maxSpecializations = 10

	// maxLanguages limits the number of languages per Mercury profile.
	maxLanguages = 20

	// maxResponseTime caps the advertised response time.
	maxResponseTime = 30 * 24 * time.Hour // 30 days

	// defaultMaxConcurrent is the default maximum concurrent cases.
	defaultMaxConcurrent = 10

	// topicMercuryArbitrators is the BroadcastOnly topic for Mercury profile extensions.
	topicMercuryArbitrators = "mercury.arbitrators"

	// livenessInterval is how often an arbitrator re-publishes their Mercury
	// profile extension to signal liveness to the gossip mesh. Per the
	// architecture plan (M16), arbitrators should re-publish every 6 hours.
	livenessInterval = 6 * time.Hour
)

// validSpecializations is the set of recognised Mercury arbitrator specializations.
var validSpecializations = map[string]bool{
	"physical-goods":   true,
	"digital-delivery": true,
	"services":         true,
}

// MercuryArbitratorProfile extends the Plutus ArbitratorProfile with
// marketplace-specific fields for Mercury dispute workflows.
type MercuryArbitratorProfile struct {
	PlutusPubKey    ed25519.PublicKey `json:"plutusPubKey"`
	Specializations []string         `json:"specializations"` // "physical-goods", "digital-delivery", "services"
	Languages       []string         `json:"languages"`
	ResponseTime    time.Duration    `json:"responseTime"`
	ActiveCases     int              `json:"activeCases"`
	MaxConcurrent   int              `json:"maxConcurrent"`
}

// ArbitrationService manages Mercury-specific arbitrator profiles on top of
// the Plutus universal arbitrator registry. Plutus handles identity, fees,
// chain addresses, and signature verification; Mercury adds specializations,
// languages, response times, and capacity tracking.
type ArbitrationService struct {
	plutusRegistry *plutus_arb.ArbitratorRegistry
	whisper        *whisper.Engine

	mu            sync.RWMutex
	localProfiles map[string]*MercuryArbitratorProfile // hex(pubkey) -> profile

	// liveness goroutine lifecycle
	livenessMu     sync.Mutex
	livenessCancel context.CancelFunc
	livenessWg     sync.WaitGroup
}

// NewArbitrationService creates an ArbitrationService wired to Plutus for
// identity/registry and Whisper for gossip propagation of Mercury extensions.
func NewArbitrationService(plutusReg *plutus_arb.ArbitratorRegistry, engine *whisper.Engine) *ArbitrationService {
	s := &ArbitrationService{
		plutusRegistry: plutusReg,
		whisper:        engine,
		localProfiles:  make(map[string]*MercuryArbitratorProfile),
	}

	// Register the BroadcastOnly topic for Mercury arbitrator profile extensions.
	// Errors are swallowed if the topic is already registered (idempotent).
	_ = engine.RegisterTopic(topicMercuryArbitrators, whisper.TopicConfig{
		Mode: whisper.BroadcastOnly,
	})

	return s
}

// RegisterAsArbitrator validates and stores a Mercury arbitrator profile
// extension. The arbitrator must already be registered in the Plutus registry;
// this adds Mercury-specific metadata (specializations, languages, capacity).
func (s *ArbitrationService) RegisterAsArbitrator(profile MercuryArbitratorProfile) error {
	if len(profile.PlutusPubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("arbitration: invalid public key size %d", len(profile.PlutusPubKey))
	}

	// Verify the arbitrator exists in Plutus registry.
	_, found := s.plutusRegistry.Get(profile.PlutusPubKey)
	if !found {
		return fmt.Errorf("arbitration: arbitrator not found in Plutus registry — register there first")
	}

	// Validate specializations.
	if len(profile.Specializations) == 0 {
		return fmt.Errorf("arbitration: at least one specialization required")
	}
	if len(profile.Specializations) > maxSpecializations {
		return fmt.Errorf("arbitration: specialization count %d exceeds limit %d", len(profile.Specializations), maxSpecializations)
	}
	for _, spec := range profile.Specializations {
		if !validSpecializations[spec] {
			return fmt.Errorf("arbitration: unknown specialization %q (valid: physical-goods, digital-delivery, services)", spec)
		}
	}

	// Validate languages.
	if len(profile.Languages) == 0 {
		return fmt.Errorf("arbitration: at least one language required")
	}
	if len(profile.Languages) > maxLanguages {
		return fmt.Errorf("arbitration: language count %d exceeds limit %d", len(profile.Languages), maxLanguages)
	}

	// Validate response time.
	if profile.ResponseTime <= 0 {
		return fmt.Errorf("arbitration: response time must be positive")
	}
	if profile.ResponseTime > maxResponseTime {
		return fmt.Errorf("arbitration: response time %s exceeds maximum %s", profile.ResponseTime, maxResponseTime)
	}

	// Apply defaults.
	if profile.MaxConcurrent <= 0 {
		profile.MaxConcurrent = defaultMaxConcurrent
	}
	if profile.ActiveCases < 0 {
		profile.ActiveCases = 0
	}

	key := hex.EncodeToString(profile.PlutusPubKey)

	s.mu.Lock()
	s.localProfiles[key] = &profile
	s.mu.Unlock()

	// Broadcast the profile extension immediately so peers see the live status.
	s.publishProfile(&profile)

	return nil
}

// publishProfile publishes a Mercury arbitrator profile extension to the
// gossip mesh on the topicMercuryArbitrators broadcast topic. Errors are
// swallowed because re-publish is the canonical liveness signal — a single
// publish failure is corrected by the next tick.
func (s *ArbitrationService) publishProfile(profile *MercuryArbitratorProfile) {
	if s.whisper == nil {
		return
	}
	payload, err := json.Marshal(profile)
	if err != nil {
		return
	}
	_ = s.whisper.Publish(topicMercuryArbitrators, payload)
}

// StartLivenessLoop starts a background goroutine that re-publishes every
// registered Mercury arbitrator profile every livenessInterval (6h). This
// is the liveness signal required by the architecture plan (M16): peers
// that stop seeing re-publishes can prune stale arbitrators.
//
// Calling StartLivenessLoop more than once is a no-op (only one loop runs).
func (s *ArbitrationService) StartLivenessLoop(ctx context.Context) {
	s.livenessMu.Lock()
	defer s.livenessMu.Unlock()

	if s.livenessCancel != nil {
		return // already running
	}

	loopCtx, cancel := context.WithCancel(ctx)
	s.livenessCancel = cancel
	s.livenessWg.Add(1)

	go func() {
		defer s.livenessWg.Done()
		ticker := time.NewTicker(livenessInterval)
		defer ticker.Stop()

		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				s.republishAll()
			}
		}
	}()
}

// StopLivenessLoop stops the liveness re-publish goroutine and waits for it
// to exit. Safe to call if the loop was never started.
func (s *ArbitrationService) StopLivenessLoop() {
	s.livenessMu.Lock()
	cancel := s.livenessCancel
	s.livenessCancel = nil
	s.livenessMu.Unlock()

	if cancel != nil {
		cancel()
		s.livenessWg.Wait()
	}
}

// republishAll snapshots the current profiles and publishes each to the
// gossip mesh. Called on each livenessInterval tick.
func (s *ArbitrationService) republishAll() {
	s.mu.RLock()
	snapshot := make([]MercuryArbitratorProfile, 0, len(s.localProfiles))
	for _, p := range s.localProfiles {
		snapshot = append(snapshot, *p)
	}
	s.mu.RUnlock()

	for i := range snapshot {
		s.publishProfile(&snapshot[i])
	}
}

// FindArbitrators returns Mercury arbitrator profiles that match the given
// specialization category and chain ID. It queries the Plutus registry for
// chain/category matching, then enriches results with Mercury-specific data.
// Both category and chainID may be empty to match all.
func (s *ArbitrationService) FindArbitrators(category string, chainID string) []MercuryArbitratorProfile {
	// Map Mercury specializations to Plutus categories for the Plutus query.
	// Plutus uses "digital-goods", "services", "physical" — Mercury maps
	// its specializations through. If category is a Mercury specialization,
	// we pass it to Plutus as-is since the categories align.
	plutusCandidates := s.plutusRegistry.Find(category, chainID)

	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []MercuryArbitratorProfile
	for _, pc := range plutusCandidates {
		key := hex.EncodeToString(pc.PubKey)
		mp, ok := s.localProfiles[key]
		if !ok {
			continue
		}

		// If a specialization filter was requested, verify the Mercury profile
		// actually declares that specialization (Plutus categories may not
		// align 1:1 with Mercury specializations).
		if category != "" {
			found := false
			for _, spec := range mp.Specializations {
				if spec == category {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Skip arbitrators at capacity.
		if mp.ActiveCases >= mp.MaxConcurrent {
			continue
		}

		results = append(results, *mp)
	}

	return results
}

// SelectArbitrator picks the best available arbitrator for an order.
// Selection criteria:
//  1. Must match the category specialization
//  2. Must have capacity (activeCases < maxConcurrent)
//  3. Prefer lowest response time among candidates
//  4. Tie-break by fewest active cases
//
// The chainID is derived from the order via the Plutus registry.
func (s *ArbitrationService) SelectArbitrator(orderID string, category string) (*MercuryArbitratorProfile, error) {
	if category == "" {
		return nil, fmt.Errorf("arbitration: category required for arbitrator selection")
	}

	// Find all candidates across all chains (chain filtering happens at
	// dispute/escrow level, not at selection time).
	candidates := s.FindArbitrators(category, "")
	if len(candidates) == 0 {
		return nil, fmt.Errorf("arbitration: no available arbitrators for category %q (order %s)", category, orderID)
	}

	// Score and select: lowest response time, then fewest active cases.
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.ResponseTime < best.ResponseTime {
			best = c
		} else if c.ResponseTime == best.ResponseTime && c.ActiveCases < best.ActiveCases {
			best = c
		}
	}

	return &best, nil
}

// IncrementActiveCases atomically increments the active case count for an
// arbitrator. Used when a dispute is assigned.
func (s *ArbitrationService) IncrementActiveCases(pubKey ed25519.PublicKey) {
	key := hex.EncodeToString(pubKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	if mp, ok := s.localProfiles[key]; ok {
		mp.ActiveCases++
	}
}

// DecrementActiveCases atomically decrements the active case count for an
// arbitrator. Used when a dispute is closed.
func (s *ArbitrationService) DecrementActiveCases(pubKey ed25519.PublicKey) {
	key := hex.EncodeToString(pubKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	if mp, ok := s.localProfiles[key]; ok && mp.ActiveCases > 0 {
		mp.ActiveCases--
	}
}

// GetProfile returns the Mercury profile for a specific arbitrator, or false
// if not found.
func (s *ArbitrationService) GetProfile(pubKey ed25519.PublicKey) (*MercuryArbitratorProfile, bool) {
	key := hex.EncodeToString(pubKey)
	s.mu.RLock()
	defer s.mu.RUnlock()
	mp, ok := s.localProfiles[key]
	if !ok {
		return nil, false
	}
	cp := *mp
	return &cp, true
}
