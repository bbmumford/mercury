/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package arbitration

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/bbmumford/plutus/escrow"
	"github.com/bbmumford/plutus/observability"
	"go.opentelemetry.io/otel/attribute"
)

// RulingOutcome represents the result of an arbitrator's ruling.
type RulingOutcome int

const (
	RulingBuyerWins  RulingOutcome = iota // full refund to buyer
	RulingSellerWins                      // full payment to seller
	RulingSplit                           // split between buyer and seller
)

// String returns a human-readable name for the ruling outcome.
func (ro RulingOutcome) String() string {
	switch ro {
	case RulingBuyerWins:
		return "BuyerWins"
	case RulingSellerWins:
		return "SellerWins"
	case RulingSplit:
		return "Split"
	default:
		return fmt.Sprintf("RulingOutcome(%d)", ro)
	}
}

// ArbitratorRuling is the arbitrator's decision on a disputed case.
// The ruling includes the split percentages and is signed by the arbitrator's
// Ed25519 key. Percentages must sum to 100.
type ArbitratorRuling struct {
	CaseID           string            `json:"caseID"`
	ArbitratorPubKey ed25519.PublicKey  `json:"arbitratorPubKey"`
	Outcome          RulingOutcome     `json:"outcome"`
	BuyerPercent     uint32            `json:"buyerPercent"`
	SellerPercent    uint32            `json:"sellerPercent"`
	Rationale        string            `json:"rationale"`
	EvidenceHashes   [][]byte          `json:"evidenceHashes"`
	Timestamp        time.Time         `json:"timestamp"`
	Signature        []byte            `json:"signature"`
}

// rulingDigest computes the SHA-256 hash of all ArbitratorRuling fields
// except Signature. This is the message that gets signed by the arbitrator.
func rulingDigest(r *ArbitratorRuling) []byte {
	h := sha256.New()
	h.Write([]byte(r.CaseID))
	h.Write(r.ArbitratorPubKey)

	var outBuf [4]byte
	binary.BigEndian.PutUint32(outBuf[:], uint32(r.Outcome))
	h.Write(outBuf[:])

	var buyerBuf [4]byte
	binary.BigEndian.PutUint32(buyerBuf[:], r.BuyerPercent)
	h.Write(buyerBuf[:])

	var sellerBuf [4]byte
	binary.BigEndian.PutUint32(sellerBuf[:], r.SellerPercent)
	h.Write(sellerBuf[:])

	h.Write([]byte(r.Rationale))

	// Evidence hashes: count + each hash.
	var countBuf [4]byte
	binary.BigEndian.PutUint32(countBuf[:], uint32(len(r.EvidenceHashes)))
	h.Write(countBuf[:])
	for _, eh := range r.EvidenceHashes {
		h.Write(eh)
	}

	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(r.Timestamp.UnixNano()))
	h.Write(tsBuf[:])

	return h.Sum(nil)
}

// SignRuling signs the ruling with the arbitrator's Ed25519 private key.
func SignRuling(r *ArbitratorRuling, privKey ed25519.PrivateKey) error {
	if len(privKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("arbitration: invalid private key size %d", len(privKey))
	}
	if r.BuyerPercent+r.SellerPercent != 100 {
		return fmt.Errorf("arbitration: ruling percentages must sum to 100, got buyer=%d seller=%d",
			r.BuyerPercent, r.SellerPercent)
	}
	digest := rulingDigest(r)
	r.Signature = ed25519.Sign(privKey, digest)
	return nil
}

// VerifyRuling checks that the ruling's signature is valid against
// ArbitratorPubKey and that the percentages sum to 100.
func VerifyRuling(r *ArbitratorRuling) bool {
	if len(r.ArbitratorPubKey) != ed25519.PublicKeySize {
		return false
	}
	if len(r.Signature) != ed25519.SignatureSize {
		return false
	}
	if r.BuyerPercent+r.SellerPercent != 100 {
		return false
	}
	digest := rulingDigest(r)
	return ed25519.Verify(r.ArbitratorPubKey, digest, r.Signature)
}

// outcomeToString converts a RulingOutcome to the Plutus escrow ruling
// outcome string format.
func outcomeToString(outcome RulingOutcome) string {
	switch outcome {
	case RulingBuyerWins:
		return "buyer_wins"
	case RulingSellerWins:
		return "seller_wins"
	case RulingSplit:
		return "split"
	default:
		return "unknown"
	}
}

// ExecuteRuling verifies the ruling signature, converts it to a Plutus
// escrow.Ruling, and calls SettleWithRuling on the Plutus EscrowManager
// to settle the on-chain escrow according to the arbitrator's decision.
//
// Steps:
//  1. Verify the ruling signature
//  2. Look up the dispute case to get the order/escrow context
//  3. Validate the ruling matches the assigned arbitrator
//  4. Convert to Plutus escrow.Ruling
//  5. Call SettleWithRuling on the Plutus escrow manager
//  6. Update case status to Ruled, then Closed
//  7. Decrement arbitrator active case count
func (dm *DisputeManager) ExecuteRuling(ctx context.Context, ruling ArbitratorRuling, plutusEscrow escrow.Manager) error {
	ctx, span := observability.Tracer("mercury.arbitration").Start(ctx, "DisputeManager.ExecuteRuling")
	defer span.End()
	span.SetAttributes(
		attribute.String("arbitration.case_id", ruling.CaseID),
		attribute.Int("arbitration.buyer_pct", int(ruling.BuyerPercent)),
		attribute.Int("arbitration.seller_pct", int(ruling.SellerPercent)),
	)

	// Step 1: Verify ruling signature.
	if !VerifyRuling(&ruling) {
		err := fmt.Errorf("arbitration: ruling signature verification failed")
		observability.RecordError(span, err)
		return err
	}

	// Step 2: Look up the dispute case.
	dm.mu.RLock()
	dc, ok := dm.cases[ruling.CaseID]
	if !ok {
		dm.mu.RUnlock()
		return fmt.Errorf("arbitration: case %s not found", ruling.CaseID)
	}
	// Copy fields under read lock to avoid holding it during escrow call.
	caseArbitrator := dc.ArbitratorPubKey
	caseStatus := dc.Status
	orderID := dc.OrderID
	dm.mu.RUnlock()

	// Step 3: Validate the ruling is from the assigned arbitrator.
	if len(caseArbitrator) != ed25519.PublicKeySize ||
		len(ruling.ArbitratorPubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("arbitration: invalid arbitrator key on case or ruling")
	}
	if !pubKeysEqual(caseArbitrator, ruling.ArbitratorPubKey) {
		return fmt.Errorf("arbitration: ruling arbitrator does not match assigned arbitrator for case %s", ruling.CaseID)
	}

	// Verify case is in a state that accepts rulings.
	if caseStatus != DisputeUnderReview && caseStatus != DisputeEvidenceCollection && caseStatus != DisputeFiled {
		return fmt.Errorf("arbitration: case %s in status %s cannot accept ruling", ruling.CaseID, caseStatus)
	}

	// Step 4: Convert to Plutus escrow.Ruling.
	plutusRuling := escrow.Ruling{
		Outcome:       outcomeToString(ruling.Outcome),
		BuyerPercent:  ruling.BuyerPercent,
		SellerPercent: ruling.SellerPercent,
		ArbitratorSig: ruling.Signature,
	}

	// Step 5: Call SettleWithRuling. The escrow address is the orderID in
	// Mercury's model (orders map 1:1 to escrows).
	_, err := plutusEscrow.SettleWithRuling(ctx, orderID, plutusRuling)
	if err != nil {
		return fmt.Errorf("arbitration: settle escrow for case %s: %w", ruling.CaseID, err)
	}

	// Step 6: Update case status (Ruled -> Closed under a single lock).
	dm.mu.Lock()
	if dc, ok := dm.cases[ruling.CaseID]; ok {
		dc.Status = DisputeClosed
	}
	dm.mu.Unlock()

	// Step 7: Decrement arbitrator active case count.
	dm.arbService.DecrementActiveCases(ruling.ArbitratorPubKey)

	observability.ArbitrationResolved().Add(ctx, 1,
		observability.AttrSet(attribute.String(observability.AttrOrderID, orderID)))
	return nil
}

// pubKeysEqual compares two Ed25519 public keys for equality.
func pubKeysEqual(a, b ed25519.PublicKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
