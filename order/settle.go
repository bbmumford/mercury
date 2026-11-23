/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package order

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"time"

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

// SettlementService handles order-state-specific settlement logic.
// It delegates fee calculation to Plutus FeeCalculator and escrow operations
// to Plutus EscrowManager, adding Mercury order lifecycle management on top.
type SettlementService struct {
	plutusSettle *settle.FeeCalculator
	plutusEscrow *escrow.EscrowManager
	orders       *OrderManager
	repService   *reputation.ReputationService
	prcResolver  PRCFeeResolver
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

// SetReputationService wires the Plutus reputation service for recording
// dispute outcomes. Optional; if not set, dispute outcomes are not recorded.
func (s *SettlementService) SetReputationService(rep *reputation.ReputationService) {
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
	// Step 1: Get the order and verify status.
	order, err := s.orders.Get(orderID)
	if err != nil {
		return fmt.Errorf("settlement: %w", err)
	}

	if order.Status != OrderFulfilled && order.Status != OrderCompleted {
		return fmt.Errorf("settlement: order %s is in status %s, expected Fulfilled or Completed",
			orderID, order.Status)
	}

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
	_, err = s.orders.Complete(orderID)
	if err != nil {
		return fmt.Errorf("settlement: complete order %s: %w", orderID, err)
	}

	return nil
}

// SettleDispute handles settlement after an arbitrator ruling.
//
// Steps:
//  1. Verify the ruling signature
//  2. Execute the ruling via Plutus escrow (SettleWithRuling)
//  3. Update order status based on the ruling outcome
//  4. Record dispute outcome in Plutus reputation service
func (s *SettlementService) SettleDispute(ctx context.Context, orderID string, ruling arbitration.ArbitratorRuling) error {
	// Step 1: Verify ruling signature.
	if !arbitration.VerifyRuling(&ruling) {
		return fmt.Errorf("settlement: ruling signature verification failed for order %s", orderID)
	}

	// Validate that percentages sum to 100.
	if ruling.BuyerPercent+ruling.SellerPercent != 100 {
		return fmt.Errorf("settlement: ruling percentages must sum to 100, got buyer=%d seller=%d",
			ruling.BuyerPercent, ruling.SellerPercent)
	}

	// Verify the order exists and is in Disputed status.
	order, err := s.orders.Get(orderID)
	if err != nil {
		return fmt.Errorf("settlement: %w", err)
	}
	if order.Status != OrderDisputed {
		return fmt.Errorf("settlement: order %s is in status %s, expected Disputed",
			orderID, order.Status)
	}

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
	_, err = s.orders.ResolveDispute(orderID, fmt.Sprintf("ruling:%s:buyer=%d%%:seller=%d%%",
		ruling.Outcome, ruling.BuyerPercent, ruling.SellerPercent))
	if err != nil {
		return fmt.Errorf("settlement: resolve dispute for order %s: %w", orderID, err)
	}

	// Step 4: Record dispute outcome in reputation.
	if s.repService != nil {
		s.recordDisputeReputation(order, ruling)
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
	// Step 1: Get the order and verify status.
	order, err := s.orders.Get(orderID)
	if err != nil {
		return fmt.Errorf("settlement: %w", err)
	}

	// Refunds are valid from Funded or Accepted states (escrow timeout).
	if order.Status != OrderFunded && order.Status != OrderAccepted {
		return fmt.Errorf("settlement: order %s is in status %s, cannot refund (expected Funded or Accepted)",
			orderID, order.Status)
	}

	// Step 2: Trigger refund via Plutus escrow.
	_, err = s.plutusEscrow.RefundAfterTimeout(ctx, orderID)
	if err != nil {
		return fmt.Errorf("settlement: refund escrow for order %s: %w", orderID, err)
	}

	// Step 3: Update order status to TimedOut.
	_, err = s.orders.Timeout(orderID)
	if err != nil {
		return fmt.Errorf("settlement: timeout order %s: %w", orderID, err)
	}

	return nil
}

// recordDisputeReputation records positive/negative reputation records for
// both buyer and seller based on the arbitrator ruling outcome.
func (s *SettlementService) recordDisputeReputation(order *Order, ruling arbitration.ArbitratorRuling) {
	now := time.Now()

	// Determine scores based on ruling outcome.
	var buyerScore, sellerScore int32
	switch ruling.Outcome {
	case arbitration.RulingBuyerWins:
		buyerScore = 1   // buyer vindicated
		sellerScore = -1 // seller penalised
	case arbitration.RulingSellerWins:
		buyerScore = -1  // buyer penalised (frivolous dispute)
		sellerScore = 1  // seller vindicated
	case arbitration.RulingSplit:
		buyerScore = 0  // neutral
		sellerScore = 0 // neutral
	}

	// Record buyer reputation.
	_ = s.repService.Record(reputation.ReputationRecord{
		SubjectPubKey:  order.Intent.BuyerPubKey,
		RecorderPubKey: ruling.ArbitratorPubKey,
		Score:          buyerScore,
		Type:           reputation.Dispute,
		AppID:          "mercury",
		TxHash:         order.Intent.ID,
		Comment:        fmt.Sprintf("dispute ruling: %s (buyer=%d%% seller=%d%%)", ruling.Outcome, ruling.BuyerPercent, ruling.SellerPercent),
		Timestamp:      now,
	})

	// Record seller reputation.
	_ = s.repService.Record(reputation.ReputationRecord{
		SubjectPubKey:  order.Intent.SellerPubKey,
		RecorderPubKey: ruling.ArbitratorPubKey,
		Score:          sellerScore,
		Type:           reputation.Dispute,
		AppID:          "mercury",
		TxHash:         order.Intent.ID,
		Comment:        fmt.Sprintf("dispute ruling: %s (buyer=%d%% seller=%d%%)", ruling.Outcome, ruling.BuyerPercent, ruling.SellerPercent),
		Timestamp:      now,
	})
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
