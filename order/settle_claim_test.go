package order

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bbmumford/mercury/arbitration"
)

// holdFirstCall runs first while the escrow holds it, then runs second, and
// answers second's error once it has returned or entered the escrow too; then
// it lets every escrow call finish and answers first's error.
func holdFirstCall(t *testing.T, esc *recordingEscrow, first, second func() error) (firstErr, secondErr error) {
	t.Helper()
	esc.entered = make(chan struct{}, 2)
	esc.gate = make(chan struct{})
	firstDone := make(chan error, 1)
	go func() { firstDone <- first() }()
	select {
	case <-esc.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first settlement never reached the escrow")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- second() }()
	secondReturned := false
	select {
	case secondErr = <-secondDone:
		secondReturned = true
	case <-esc.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the second settlement neither returned nor reached the escrow")
	}
	close(esc.gate)
	firstErr = <-firstDone
	if !secondReturned {
		secondErr = <-secondDone
	}
	return firstErr, secondErr
}

// Two settlements of one disputed order racing past the status check execute
// the ruling once: the second is refused while the first holds the order.
func TestSettleDisputeExecutesARulingOnceUnderConcurrentCalls(t *testing.T) {
	om := NewOrderManager()
	orderIn(t, om, "o-1", OrderDisputed)
	assigned, assignedPriv := newKey(t)
	esc := &recordingEscrow{}
	s := newTestSettlement(om, esc)
	s.SetDisputeCases(casesByID{"case-1": {ID: "case-1", OrderID: "o-1", ArbitratorPubKey: assigned}})
	ruling := signedRulingFor(t, om, "o-1", "case-1", arbitration.RulingSplit, assignedPriv)
	settleIt := func() error { return s.SettleDispute(context.Background(), "o-1", ruling) }

	firstErr, secondErr := holdFirstCall(t, esc, settleIt, settleIt)
	if firstErr != nil {
		t.Fatalf("first settlement: %v", firstErr)
	}
	if !errors.Is(secondErr, ErrSettlementInProgress) {
		t.Fatalf("second settlement: got %v, want ErrSettlementInProgress", secondErr)
	}
	if _, settled, _ := esc.counts(); settled != 1 {
		t.Fatalf("the escrow executed the ruling %d times, want once", settled)
	}
	if st := statusOf(t, om, "o-1"); st != OrderCompleted {
		t.Fatalf("order is %s, want completed", st)
	}
}

// Two refunds of one timed-out order refund it once.
func TestRefundTimedOutRefundsOnceUnderConcurrentCalls(t *testing.T) {
	om := NewOrderManager()
	orderIn(t, om, "o-1", OrderFunded)
	esc := &recordingEscrow{}
	s := newTestSettlement(om, esc)
	refundIt := func() error { return s.RefundTimedOut(context.Background(), "o-1") }

	firstErr, secondErr := holdFirstCall(t, esc, refundIt, refundIt)
	if firstErr != nil {
		t.Fatalf("first refund: %v", firstErr)
	}
	if !errors.Is(secondErr, ErrSettlementInProgress) {
		t.Fatalf("second refund: got %v, want ErrSettlementInProgress", secondErr)
	}
	if _, _, refunded := esc.counts(); refunded != 1 {
		t.Fatalf("the escrow refunded %d times, want once", refunded)
	}
	if st := statusOf(t, om, "o-1"); st != OrderTimedOut {
		t.Fatalf("order is %s, want timed out", st)
	}
}

// While a settlement holds an order, no other status change applies: a
// dispute filed mid-release is refused and the release completes the order.
func TestASettlementHoldsOffOtherStatusChanges(t *testing.T) {
	om := NewOrderManager()
	orderIn(t, om, "o-1", OrderFulfilled)
	esc := &recordingEscrow{}
	s := newTestSettlement(om, esc)
	settleIt := func() error { return s.SettleOrder(context.Background(), "o-1") }
	disputeIt := func() error { _, err := om.Dispute("o-1", "late complaint"); return err }

	firstErr, secondErr := holdFirstCall(t, esc, settleIt, disputeIt)
	if firstErr != nil {
		t.Fatalf("settlement: %v", firstErr)
	}
	if !errors.Is(secondErr, ErrSettlementInProgress) {
		t.Fatalf("dispute during the release: got %v, want ErrSettlementInProgress", secondErr)
	}
	if released, _, _ := esc.counts(); released != 1 {
		t.Fatalf("the escrow released %d times, want once", released)
	}
	if st := statusOf(t, om, "o-1"); st != OrderCompleted {
		t.Fatalf("order is %s, want completed", st)
	}
}

// A failed escrow call leaves the order's status as it was and releases the
// claim, so the settlement can be retried.
func TestAFailedEscrowCallLeavesTheOrderSettleable(t *testing.T) {
	om := NewOrderManager()
	orderIn(t, om, "o-1", OrderDisputed)
	assigned, assignedPriv := newKey(t)
	esc := &recordingEscrow{err: errors.New("chain unreachable")}
	s := newTestSettlement(om, esc)
	s.SetDisputeCases(casesByID{"case-1": {ID: "case-1", OrderID: "o-1", ArbitratorPubKey: assigned}})
	ruling := signedRulingFor(t, om, "o-1", "case-1", arbitration.RulingSplit, assignedPriv)

	if err := s.SettleDispute(context.Background(), "o-1", ruling); err == nil {
		t.Fatal("a failed escrow call was reported settled")
	}
	if st := statusOf(t, om, "o-1"); st != OrderDisputed {
		t.Fatalf("order is %s after a failed escrow call, want still disputed", st)
	}
	esc.mu.Lock()
	esc.err = nil
	esc.mu.Unlock()
	if err := s.SettleDispute(context.Background(), "o-1", ruling); err != nil {
		t.Fatalf("retry after the escrow recovered: %v", err)
	}
	if st := statusOf(t, om, "o-1"); st != OrderCompleted {
		t.Fatalf("order is %s after the retry, want completed", st)
	}
}

// A settlement claims only an order in a status it settles, and touches no
// escrow otherwise.
func TestSettlementRefusesAnOrderInTheWrongStatus(t *testing.T) {
	om := NewOrderManager()
	orderIn(t, om, "funded", OrderFunded)
	orderIn(t, om, "disputed", OrderDisputed)
	assigned, assignedPriv := newKey(t)
	esc := &recordingEscrow{}
	s := newTestSettlement(om, esc)
	s.SetDisputeCases(casesByID{"case-1": {ID: "case-1", OrderID: "funded", ArbitratorPubKey: assigned}})

	if err := s.SettleDispute(context.Background(), "funded", signedRulingFor(t, om, "funded", "case-1", arbitration.RulingSplit, assignedPriv)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("a ruling on an order not disputed: got %v, want ErrInvalidTransition", err)
	}
	if err := s.RefundTimedOut(context.Background(), "disputed"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("a refund of a disputed order: got %v, want ErrInvalidTransition", err)
	}
	if err := s.SettleOrder(context.Background(), "funded"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("settling an order not fulfilled: got %v, want ErrInvalidTransition", err)
	}
	if err := s.SettleOrder(context.Background(), "missing"); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("settling an unknown order: got %v, want ErrOrderNotFound", err)
	}
	if released, settled, refunded := esc.counts(); released+settled+refunded != 0 {
		t.Fatalf("the escrow was called (%d, %d, %d) for orders in the wrong status", released, settled, refunded)
	}
}
