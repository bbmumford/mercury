/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package arbitration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/bbmumford/plutus/observability"
	"go.opentelemetry.io/otel/attribute"
)

// EvidenceWindow is the duration after FiledAt during which parties may
// submit additional evidence for a dispute case. Per the architecture plan
// (M17), the evidence window is 72 hours. After this window, SubmitEvidence
// rejects new submissions and the case transitions to UnderReview on the
// arbitrator's next poll.
const EvidenceWindow = 72 * time.Hour

// DisputeStatus represents the current phase of a dispute case.
type DisputeStatus int

const (
	DisputeFiled              DisputeStatus = iota // initial filing
	DisputeEvidenceCollection                      // parties submitting evidence
	DisputeUnderReview                             // arbitrator reviewing
	DisputeRuled                                   // ruling issued
	DisputeClosed                                  // ruling executed, case closed
)

// String returns a human-readable name for the dispute status.
func (ds DisputeStatus) String() string {
	switch ds {
	case DisputeFiled:
		return "Filed"
	case DisputeEvidenceCollection:
		return "EvidenceCollection"
	case DisputeUnderReview:
		return "UnderReview"
	case DisputeRuled:
		return "Ruled"
	case DisputeClosed:
		return "Closed"
	default:
		return fmt.Sprintf("DisputeStatus(%d)", ds)
	}
}

// DisputeCase represents a marketplace dispute between buyer and seller.
// Evidence data is hashed (SHA-256) and the hash stored on the case;
// actual evidence bytes are sent to the arbitrator via Aether BULK stream.
type DisputeCase struct {
	ID               string            `json:"id"`
	OrderID          string            `json:"orderID"`
	FiledBy          ed25519.PublicKey `json:"filedBy"`
	ArbitratorPubKey ed25519.PublicKey `json:"arbitratorPubKey"`
	Reason           string            `json:"reason"`
	EvidenceHashes   [][]byte          `json:"evidenceHashes"`
	Status           DisputeStatus     `json:"status"`
	FiledAt          time.Time         `json:"filedAt"`
	Signature        []byte            `json:"signature"`
}

// disputeDigest computes the SHA-256 hash of all DisputeCase fields except
// Signature and Status. This is the message that gets signed by the filer.
func disputeDigest(dc *DisputeCase) []byte {
	h := sha256.New()
	h.Write([]byte(dc.ID))
	h.Write([]byte(dc.OrderID))
	h.Write(dc.FiledBy)
	h.Write(dc.ArbitratorPubKey)
	h.Write([]byte(dc.Reason))

	// Hash each evidence hash deterministically
	var countBuf [4]byte
	binary.BigEndian.PutUint32(countBuf[:], uint32(len(dc.EvidenceHashes)))
	h.Write(countBuf[:])
	for _, eh := range dc.EvidenceHashes {
		h.Write(eh)
	}

	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(dc.FiledAt.UnixNano()))
	h.Write(tsBuf[:])

	return h.Sum(nil)
}

// SignDispute signs the dispute case with the filer's Ed25519 private key.
func SignDispute(dc *DisputeCase, privKey ed25519.PrivateKey) error {
	if len(privKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("arbitration: invalid private key size %d", len(privKey))
	}
	digest := disputeDigest(dc)
	dc.Signature = ed25519.Sign(privKey, digest)
	return nil
}

// VerifyDispute checks that the dispute case signature is valid against FiledBy.
func VerifyDispute(dc *DisputeCase) bool {
	if len(dc.FiledBy) != ed25519.PublicKeySize {
		return false
	}
	if len(dc.Signature) != ed25519.SignatureSize {
		return false
	}
	digest := disputeDigest(dc)
	return ed25519.Verify(dc.FiledBy, digest, dc.Signature)
}

// generateCaseID creates a cryptographically random case identifier.
func generateCaseID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("arbitration: generate case ID: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// DisputeManager manages the lifecycle of marketplace dispute cases.
// It coordinates with ArbitrationService for arbitrator assignment and
// capacity tracking.
type DisputeManager struct {
	arbService *ArbitrationService

	mu    sync.RWMutex
	cases map[string]*DisputeCase // caseID -> case
	order map[string][]string     // orderID -> []caseID
}

// NewDisputeManager creates a DisputeManager wired to the given
// ArbitrationService for arbitrator lookup and capacity tracking.
func NewDisputeManager(arbService *ArbitrationService) *DisputeManager {
	return &DisputeManager{
		arbService: arbService,
		cases:      make(map[string]*DisputeCase),
		order:      make(map[string][]string),
	}
}

// FileDispute creates a new dispute case for an order. The evidence bytes
// are hashed (SHA-256) and the hash stored on the case. An arbitrator is
// automatically selected based on the order's category. The filer's public
// key must be set on the returned case and signed externally by the caller.
//
// Steps:
//  1. Hash the evidence for on-case storage
//  2. Select an arbitrator via ArbitrationService
//  3. Generate a unique case ID
//  4. Create the DisputeCase with status DisputeFiled
//  5. Track the case and increment arbitrator active count
func (dm *DisputeManager) FileDispute(orderID string, reason string, evidence []byte, category string) (*DisputeCase, error) {
	if orderID == "" {
		return nil, fmt.Errorf("arbitration: order ID required")
	}
	if reason == "" {
		return nil, fmt.Errorf("arbitration: dispute reason required")
	}
	if len(evidence) == 0 {
		return nil, fmt.Errorf("arbitration: evidence required to file dispute")
	}

	// Step 1: Hash evidence.
	evidenceHash := sha256.Sum256(evidence)

	// Step 2: Select an arbitrator. Use the order's category as the primary
	// search, falling back to broader categories only when no match is found.
	var arb *MercuryArbitratorProfile
	var err error

	if category != "" {
		arb, err = dm.arbService.SelectArbitrator(orderID, category)
	}
	if arb == nil {
		arb, err = dm.arbService.SelectArbitrator(orderID, "physical-goods")
	}
	if arb == nil {
		arb, err = dm.arbService.SelectArbitrator(orderID, "digital-delivery")
	}
	if arb == nil {
		arb, err = dm.arbService.SelectArbitrator(orderID, "services")
	}
	if arb == nil {
		return nil, fmt.Errorf("arbitration: no arbitrator available for order %s: %w", orderID, err)
	}

	// Step 3: Generate case ID.
	caseID, err := generateCaseID()
	if err != nil {
		return nil, err
	}

	// Step 4: Create the dispute case.
	dc := &DisputeCase{
		ID:               caseID,
		OrderID:          orderID,
		ArbitratorPubKey: arb.PlutusPubKey,
		Reason:           reason,
		EvidenceHashes:   [][]byte{evidenceHash[:]},
		Status:           DisputeFiled,
		FiledAt:          time.Now(),
	}

	// Step 5: Track the case.
	dm.mu.Lock()
	dm.cases[caseID] = dc
	dm.order[orderID] = append(dm.order[orderID], caseID)
	dm.mu.Unlock()

	// Increment arbitrator active case count.
	dm.arbService.IncrementActiveCases(arb.PlutusPubKey)

	observability.ArbitrationOpened().Add(context.Background(), 1,
		observability.AttrSet(attribute.String(observability.AttrOrderID, orderID)))
	return dc, nil
}

// SubmitEvidence adds additional evidence to an existing dispute case.
// The evidence bytes are hashed (SHA-256) and the hash appended to the case.
// Evidence can only be submitted during the Filed or EvidenceCollection phases.
func (dm *DisputeManager) SubmitEvidence(caseID string, evidence []byte) error {
	if len(evidence) == 0 {
		return fmt.Errorf("arbitration: evidence must not be empty")
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	dc, ok := dm.cases[caseID]
	if !ok {
		return fmt.Errorf("arbitration: case %s not found", caseID)
	}

	// Evidence can only be submitted in early phases.
	if dc.Status != DisputeFiled && dc.Status != DisputeEvidenceCollection {
		return fmt.Errorf("arbitration: cannot submit evidence in status %s", dc.Status)
	}

	// Enforce the 72-hour evidence window (M17). Evidence submitted after
	// FiledAt + EvidenceWindow is rejected — the arbitrator rules on the
	// record collected within the window.
	if time.Since(dc.FiledAt) > EvidenceWindow {
		return fmt.Errorf("arbitration: evidence window of %s has closed for case %s",
			EvidenceWindow, caseID)
	}

	// Hash and store. The evidence bytes are hash-anchored on the case; the
	// actual blob is transported out-of-band via Aether BULK stream or stored
	// by the caller in Minerva's blob-aware persistence tier. Only the SHA-256
	// hash is retained on the DisputeCase record for auditability.
	evidenceHash := sha256.Sum256(evidence)
	dc.EvidenceHashes = append(dc.EvidenceHashes, evidenceHash[:])

	// Transition to evidence collection if still in filed status.
	if dc.Status == DisputeFiled {
		dc.Status = DisputeEvidenceCollection
	}

	return nil
}

// GetCase returns the dispute case for the given ID, or an error if not found.
func (dm *DisputeManager) GetCase(caseID string) (*DisputeCase, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	dc, ok := dm.cases[caseID]
	if !ok {
		return nil, fmt.Errorf("arbitration: case %s not found", caseID)
	}

	// Return a copy.
	cp := *dc
	cp.EvidenceHashes = make([][]byte, len(dc.EvidenceHashes))
	for i, eh := range dc.EvidenceHashes {
		cp.EvidenceHashes[i] = make([]byte, len(eh))
		copy(cp.EvidenceHashes[i], eh)
	}
	return &cp, nil
}

// CasesForOrder returns all dispute cases filed against an order.
func (dm *DisputeManager) CasesForOrder(orderID string) []*DisputeCase {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	caseIDs, ok := dm.order[orderID]
	if !ok {
		return nil
	}

	results := make([]*DisputeCase, 0, len(caseIDs))
	for _, cid := range caseIDs {
		dc, exists := dm.cases[cid]
		if !exists {
			continue
		}
		cp := *dc
		cp.EvidenceHashes = make([][]byte, len(dc.EvidenceHashes))
		for i, eh := range dc.EvidenceHashes {
			cp.EvidenceHashes[i] = make([]byte, len(eh))
			copy(cp.EvidenceHashes[i], eh)
		}
		results = append(results, &cp)
	}

	return results
}

// TransitionToReview moves a dispute case from EvidenceCollection to
// UnderReview. Called when the arbitrator begins evaluating the case.
func (dm *DisputeManager) TransitionToReview(caseID string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	dc, ok := dm.cases[caseID]
	if !ok {
		return fmt.Errorf("arbitration: case %s not found", caseID)
	}

	if dc.Status != DisputeFiled && dc.Status != DisputeEvidenceCollection {
		return fmt.Errorf("arbitration: cannot transition to review from status %s", dc.Status)
	}

	dc.Status = DisputeUnderReview
	return nil
}
