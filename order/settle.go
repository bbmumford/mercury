/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package order

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"

	"github.com/bbmumford/mercury/arbitration"
	"github.com/bbmumford/plutus/adapter"
	"github.com/bbmumford/plutus/escrow"
	"github.com/bbmumford/plutus/reputation"
	"github.com/bbmumford/plutus/settle"
)

// PRCFeeResolver resolves the maintainer fee BPS from the Plutus PRC for a
// given chain. If nil, a default must be passed explicitly.
type PRCFeeResolver interface {
	// ResolveMaintainerBPS returns the maintainer fee in basis points for the chain.
	ResolveMaintainerBPS(chainID string) (uint32, error)
}

// escrowSettler is the escrow side of a settlement: releasing an order's escrow
// to the seller, executing an arbitrator's ruling, and refunding after a
// timeout. *escrow.EscrowManager satisfies it; SettlementService holds the
// port so its order bookkeeping around each escrow call can be exercised
// against an escrow that answers as a test needs.
type escrowSettler interface {
	Release(ctx context.Context, escrowAddr string, params escrow.ReleaseParams) (string, error)
	SettleWithRuling(ctx context.Context, escrowAddr string, ruling escrow.Ruling) (string, error)
	RefundAfterTimeout(ctx context.Context, escrowAddr string) (string, error)
}

var _ escrowSettler = (*escrow.EscrowManager)(nil)

// DisputeCases answers a dispute case by its id; *arbitration.DisputeManager
// satisfies it. SettleDispute reads the case a ruling names to learn which
// arbitrator the dispute was assigned.
type DisputeCases interface {
	GetCase(caseID string) (*arbitration.DisputeCase, error)
}

// Refusals SettleDispute answers before any escrow call, each for a ruling it
// cannot attribute to the dispute's assigned arbitrator.
var (
	// ErrNoDisputeCases: SetDisputeCases was never called, so no ruling can be
	// checked against its dispute.
	ErrNoDisputeCases = errors.New("settlement: no dispute cases to check a ruling against")
	// ErrRulingForAnotherOrder: the ruling's case is a dispute of another order.
	ErrRulingForAnotherOrder = errors.New("settlement: the ruling's dispute is for another order")
	// ErrRulingNotByAssignedArbitrator: the ruling is signed by a key other
	// than the arbitrator its dispute was assigned.
	ErrRulingNotByAssignedArbitrator = errors.New("settlement: the ruling is not signed by the dispute's assigned arbitrator")
	// ErrDisputeReputationUnsigned: the ruling does not carry the
	// arbitrator's valid signatures over the buyer's and the seller's
	// dispute reputation records for the order; nothing is settled.
	ErrDisputeReputationUnsigned = errors.New("settlement: the ruling does not carry the arbitrator's signatures over both parties' dispute reputation records")
	// ErrReputationNotRecorded: the dispute settled and the order completed,
	// but storing a party's dispute reputation record failed. It wraps the
	// store's error, so a caller can tell "settled, not recorded" (do not
	// settle again) from a refusal that settled nothing.
	ErrReputationNotRecorded = errors.New("settlement: dispute settled, but its reputation records were not recorded")
)

// reputationRecorder stores a signed reputation record. SetReputationService
// wires a *reputation.ReputationService as it.
type reputationRecorder interface {
	Record(rec reputation.ReputationRecord) error
}

// SettlementService handles order-state-specific settlement logic.
// It delegates fee calculation to Plutus FeeCalculator and escrow operations
// to Plutus EscrowManager, adding Mercury order lifecycle management on top.
type SettlementService struct {
	plutusSettle *settle.FeeCalculator
	plutusEscrow escrowSettler
	orders       *OrderManager
	repService   reputationRecorder
	prcResolver  PRCFeeResolver
	disputes     DisputeCases
}

// NewSettlementService creates a SettlementService wired to Plutus for fee
// calculation and escrow management, and Mercury's OrderManager for order state.
func NewSettlementService(
	feeCalc *settle.FeeCalculator,
	escrowMgr *escrow.EscrowManager,
	orders *OrderManager,
) *SettlementService {
	return &SettlementService{
		plutusSettle: feeCalc,
		plutusEscrow: escrowMgr,
		orders:       orders,
	}
}

// SetPRCFeeResolver wires the PRC fee resolver for reading maintainer BPS
// from the on-chain PRC. If not set, SettleOrder falls back to the default
// fee (200 BPS / 2%).
func (s *SettlementService) SetPRCFeeResolver(resolver PRCFeeResolver) {
	s.prcResolver = resolver
}

// SetDisputeCases wires the dispute cases SettleDispute checks each ruling
// against. Until it is set, SettleDispute refuses every ruling.
func (s *SettlementService) SetDisputeCases(cases DisputeCases) {
	s.disputes = cases
}

// SetReputationService wires the Plutus reputation service for recording
// dispute outcomes. Optional; if not set (or set to nil), dispute outcomes
// are not recorded.
func (s *SettlementService) SetReputationService(rep *reputation.ReputationService) {
	if rep == nil {
		s.repService = nil
		return
	}
	s.repService = rep
}

// SettleOrder handles the full settlement flow for a completed order.
//
// Steps:
//  1. Get the order and verify its status is Fulfilled or Completed
//  2. Calculate fees via Plutus FeeCalculator
//  3. Release escrow via Plutus EscrowManager
//  4. Update order status to Completed
func (s *SettlementService) SettleOrder(ctx context.Context, orderID string) error {
	// Step 1: Claim the order, checking its status in the same step: a
	// concurrent settlement, or any other status change, is refused until the
	// escrow call's outcome is recorded.
	claimed, err := s.orders.beginSettlement(orderID, OrderFulfilled, OrderCompleted)
	if err != nil {
		return fmt.Errorf("settlement: %w", err)
	}
	order := &claimed
	finished := false
	defer func() {
		if !finished {
			s.orders.abandonSettlement(orderID)
		}
	}()

	// If already completed, nothing to do.
	if order.Status == OrderCompleted {
		return nil
	}

	// Step 2: Calculate fees via Plutus.
	// Read maintainer BPS from the PRC if a resolver is configured,
	// otherwise fall back to the default 200 BPS (2%).
	var maintainerBPS uint32 = 200
	if s.prcResolver != nil {
		if resolved, err := s.prcResolver.ResolveMaintainerBPS(order.Intent.ChainID); err == nil {
			maintainerBPS = resolved
		}
		// On resolver error, fall back to default 200 BPS and continue.
	}

	amount := new(big.Int).SetUint64(order.Intent.TotalPrice)
	feeOpts := adapter.FeeOptions{
		MaintainerBPS: maintainerBPS,
	}

	fees, err := s.plutusSettle.Calculate(amount, feeOpts)
	if err != nil {
		return fmt.Errorf("settlement: fee calculation for order %s: %w", orderID, err)
	}

	// Step 3: Release escrow via Plutus.
	// In Mercury's model, the escrow address is the order ID (orders map 1:1 to escrows).
	receiptHash := orderReceiptHash(order)
	_, err = s.plutusEscrow.Release(ctx, orderID, escrow.ReleaseParams{
		ReceiptHashes: [][]byte{receiptHash},
		Memo:          fmt.Sprintf("mercury:settle:%s:fees=%s", orderID, fees.Total.String()),
	})
	if err != nil {
		return fmt.Errorf("settlement: release escrow for order %s: %w", orderID, err)
	}

	// Step 4: Update order status to Completed.
	finished = true
	_, err = s.orders.finishSettlement(orderID, OrderCompleted, completedReason)
	if err != nil {
		return fmt.Errorf("settlement: complete order %s: %w", orderID, err)
	}
	countSettled(orderID)

	return nil
}

// SettleDispute handles settlement after an arbitrator ruling.
//
// Steps:
//  1. Verify the ruling signature, and that its signer is the arbitrator the
//     ruling's dispute was assigned, for this order, and that it carries the
//     arbitrator's signatures over both parties' dispute reputation records
//  2. Execute the ruling via Plutus escrow (SettleWithRuling)
//  3. Update order status based on the ruling outcome
//  4. Store the two signed dispute records in the Plutus reputation service;
//     a store failure returns ErrReputationNotRecorded with the order settled
func (s *SettlementService) SettleDispute(ctx context.Context, orderID string, ruling arbitration.ArbitratorRuling) error {
	// Step 1: Verify ruling signature.
	if !arbitration.VerifyRuling(&ruling) {
		return fmt.Errorf("settlement: ruling signature verification failed for order %s", orderID)
	}
	// A valid signature proves only that the ruling's own key signed it; the
	// key must be the one the dispute was assigned, or any key could move the
	// escrow.
	if s.disputes == nil {
		return ErrNoDisputeCases
	}
	dc, err := s.disputes.GetCase(ruling.CaseID)
	if err != nil {
		return fmt.Errorf("settlement: dispute %s of order %s: %w", ruling.CaseID, orderID, err)
	}
	if dc.OrderID != orderID {
		return fmt.Errorf("%w: case %s is a dispute of order %s, not %s", ErrRulingForAnotherOrder, ruling.CaseID, dc.OrderID, orderID)
	}
	if !bytes.Equal(dc.ArbitratorPubKey, ruling.ArbitratorPubKey) {
		return fmt.Errorf("%w: case %s", ErrRulingNotByAssignedArbitrator, ruling.CaseID)
	}

	// Validate that percentages sum to 100.
	if ruling.BuyerPercent+ruling.SellerPercent != 100 {
		return fmt.Errorf("settlement: ruling percentages must sum to 100, got buyer=%d seller=%d",
			ruling.BuyerPercent, ruling.SellerPercent)
	}

	// The dispute's reputation records are the arbitrator's to sign, as the
	// ruling is: check both signatures now, so a ruling without them moves
	// no money, and keep the signed records to store after settlement.
	disputed, err := s.orders.Get(orderID)
	if err != nil {
		return fmt.Errorf("settlement: %w", err)
	}
	buyerRec, sellerRec, ok := arbitration.VerifyDisputeReputation(&ruling, orderID,
		disputed.Intent.BuyerPubKey, disputed.Intent.SellerPubKey)
	if !ok {
		return fmt.Errorf("%w: order %s", ErrDisputeReputationUnsigned, orderID)
	}

	// Claim the disputed order, checking its status in the same step, so the
	// ruling executes at most once: a concurrent settlement, or any other
	// status change, is refused until the escrow call's outcome is recorded.
	if _, err := s.orders.beginSettlement(orderID, OrderDisputed); err != nil {
		return fmt.Errorf("settlement: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			s.orders.abandonSettlement(orderID)
		}
	}()

	// Step 2: Execute ruling via Plutus escrow.
	plutusRuling := escrow.Ruling{
		Outcome:       rulingOutcomeString(ruling.Outcome),
		BuyerPercent:  ruling.BuyerPercent,
		SellerPercent: ruling.SellerPercent,
		ArbitratorSig: ruling.Signature,
	}

	_, err = s.plutusEscrow.SettleWithRuling(ctx, orderID, plutusRuling)
	if err != nil {
		return fmt.Errorf("settlement: settle dispute for order %s: %w", orderID, err)
	}

	// Step 3: Update order status to Completed (dispute resolved).
	finished = true
	_, err = s.orders.finishSettlement(orderID, OrderCompleted, fmt.Sprintf("dispute resolved: ruling:%s:buyer=%d%%:seller=%d%%",
		ruling.Outcome, ruling.BuyerPercent, ruling.SellerPercent))
	if err != nil {
		return fmt.Errorf("settlement: resolve dispute for order %s: %w", orderID, err)
	}

	// Step 4: Store the arbitrator's signed dispute records.
	if s.repService != nil {
		if err := s.recordDisputeReputation(buyerRec, sellerRec); err != nil {
			return fmt.Errorf("%w: order %s: %w", ErrReputationNotRecorded, orderID, err)
		}
	}

	return nil
}

// RefundTimedOut handles expired orders by triggering escrow refund.
//
// Steps:
//  1. Get the order and verify it is in a refundable state (Funded or Accepted)
//  2. Trigger refund via Plutus escrow RefundAfterTimeout
//  3. Update order status to TimedOut
func (s *SettlementService) RefundTimedOut(ctx context.Context, orderID string) error {
	// Step 1: Claim the order, checking in the same step that it is refundable
	// (Funded or Accepted, the states an escrow timeout refunds), so the refund
	// runs at most once: a concurrent settlement, or any other status change, is
	// refused until the escrow call's outcome is recorded.
	if _, err := s.orders.beginSettlement(orderID, OrderFunded, OrderAccepted); err != nil {
		return fmt.Errorf("settlement: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			s.orders.abandonSettlement(orderID)
		}
	}()

	// Step 2: Trigger refund via Plutus escrow.
	_, err := s.plutusEscrow.RefundAfterTimeout(ctx, orderID)
	if err != nil {
		return fmt.Errorf("settlement: refund escrow for order %s: %w", orderID, err)
	}

	// Step 3: Update order status to TimedOut.
	finished = true
	_, err = s.orders.finishSettlement(orderID, OrderTimedOut, "escrow timeout")
	if err != nil {
		return fmt.Errorf("settlement: timeout order %s: %w", orderID, err)
	}

	return nil
}

// recordDisputeReputation stores the buyer's and the seller's signed
// dispute records, trying both even when the first fails, and returns every
// failure joined.
func (s *SettlementService) recordDisputeReputation(buyerRec, sellerRec reputation.ReputationRecord) error {
	var errs []error
	if err := s.repService.Record(buyerRec); err != nil {
		errs = append(errs, fmt.Errorf("buyer's record: %w", err))
	}
	if err := s.repService.Record(sellerRec); err != nil {
		errs = append(errs, fmt.Errorf("seller's record: %w", err))
	}
	return errors.Join(errs...)
}

// rulingOutcomeString converts an arbitration.RulingOutcome to the Plutus
// escrow ruling outcome string.
func rulingOutcomeString(outcome arbitration.RulingOutcome) string {
	switch outcome {
	case arbitration.RulingBuyerWins:
		return "buyer_wins"
	case arbitration.RulingSellerWins:
		return "seller_wins"
	case arbitration.RulingSplit:
		return "split"
	default:
		return "unknown"
	}
}

// orderReceiptHash computes a SHA-256 receipt hash from order data for
// escrow release proof. Combines order ID, buyer, seller, and amount.
func orderReceiptHash(o *Order) []byte {
	h := sha256.New()
	h.Write([]byte(o.Intent.ID))
	h.Write(o.Intent.BuyerPubKey)
	h.Write(o.Intent.SellerPubKey)
	price := new(big.Int).SetUint64(o.Intent.TotalPrice)
	h.Write(price.Bytes())
	return h.Sum(nil)
}
