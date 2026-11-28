package order

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bbmumford/mercury/arbitration"
	"github.com/bbmumford/plutus/escrow"
	"github.com/bbmumford/plutus/settle"
)

// recordingEscrow counts the escrow calls a settlement makes. When gate is set,
// each call first signals entered and then waits for gate to close, so a test
// can hold one settlement inside its escrow call.
type recordingEscrow struct {
	mu                          sync.Mutex
	released, settled, refunded int
	entered                     chan struct{}
	gate                        chan struct{}
	err                         error
}

func (e *recordingEscrow) call(n *int) (string, error) {
	e.mu.Lock()
	*n++
	entered, gate, err := e.entered, e.gate, e.err
	e.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	return "tx", err
}

func (e *recordingEscrow) Release(context.Context, string, escrow.ReleaseParams) (string, error) {
	return e.call(&e.released)
}

func (e *recordingEscrow) SettleWithRuling(context.Context, string, escrow.Ruling) (string, error) {
	return e.call(&e.settled)
}

func (e *recordingEscrow) RefundAfterTimeout(context.Context, string) (string, error) {
	return e.call(&e.refunded)
}

func (e *recordingEscrow) counts() (released, settled, refunded int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.released, e.settled, e.refunded
}

// casesByID serves dispute cases from a map, as DisputeManager.GetCase does.
type casesByID map[string]*arbitration.DisputeCase

func (c casesByID) GetCase(id string) (*arbitration.DisputeCase, error) {
	dc, ok := c[id]
	if !ok {
		return nil, errors.New("no such case")
	}
	cp := *dc
	return &cp, nil
}

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// orderIn creates a signed order and walks it to status through valid
// transitions (Pending, Funded, then Disputed or Accepted and Fulfilled).
func orderIn(t *testing.T, om *OrderManager, id string, status OrderStatus) {
	t.Helper()
	buyer, buyerPriv := newKey(t)
	seller, _ := newKey(t)
	intent := OrderIntent{ID: id, BuyerPubKey: buyer, SellerPubKey: seller, ListingID: "l-1", Quantity: 1,
		TotalPrice: 10_000, Currency: "USDC", ChainID: "polygon", Timestamp: time.Unix(1_700_000_000, 0)}
	if err := intent.Sign(buyerPriv); err != nil {
		t.Fatal(err)
	}
	if _, err := om.CreateOrder(intent); err != nil {
		t.Fatal(err)
	}
	if _, err := om.MarkFunded(id); err != nil {
		t.Fatal(err)
	}
	switch status {
	case OrderFunded:
	case OrderDisputed:
		if _, err := om.Dispute(id, "not as described"); err != nil {
			t.Fatal(err)
		}
	case OrderFulfilled:
		om.mu.Lock()
		om.orders[id].Status = OrderFulfilled
		om.mu.Unlock()
	default:
		t.Fatalf("orderIn: status %s not supported", status)
	}
}

func signedRuling(t *testing.T, caseID string, priv ed25519.PrivateKey) arbitration.ArbitratorRuling {
	t.Helper()
	r := arbitration.ArbitratorRuling{CaseID: caseID, ArbitratorPubKey: priv.Public().(ed25519.PublicKey),
		Outcome: arbitration.RulingSplit, BuyerPercent: 50, SellerPercent: 50, Timestamp: time.Unix(1_700_000_100, 0)}
	if err := arbitration.SignRuling(&r, priv); err != nil {
		t.Fatal(err)
	}
	return r
}

func newTestSettlement(om *OrderManager, esc *recordingEscrow) *SettlementService {
	s := NewSettlementService(settle.NewFeeCalculator(), nil, om)
	s.plutusEscrow = esc
	return s
}

func statusOf(t *testing.T, om *OrderManager, id string) OrderStatus {
	t.Helper()
	o, err := om.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return o.Status
}

// A ruling moves the escrow only when its signer is the arbitrator the dispute
// was assigned; a valid signature by any other key is refused before escrow.
func TestSettleDisputeRefusesARulingNotSignedByTheAssignedArbitrator(t *testing.T) {
	om := NewOrderManager()
	orderIn(t, om, "o-1", OrderDisputed)
	assigned, _ := newKey(t)
	_, roguePriv := newKey(t)
	esc := &recordingEscrow{}
	s := newTestSettlement(om, esc)
	s.SetDisputeCases(casesByID{"case-1": {ID: "case-1", OrderID: "o-1", ArbitratorPubKey: assigned}})

	err := s.SettleDispute(context.Background(), "o-1", signedRuling(t, "case-1", roguePriv))
	if !errors.Is(err, ErrRulingNotByAssignedArbitrator) {
		t.Fatalf("got %v, want ErrRulingNotByAssignedArbitrator", err)
	}
	if _, settled, _ := esc.counts(); settled != 0 {
		t.Fatalf("the escrow executed %d rulings, want none", settled)
	}
	if st := statusOf(t, om, "o-1"); st != OrderDisputed {
		t.Fatalf("order is %s, want it still disputed", st)
	}
}

// The ruling's dispute must be a dispute of the order being settled.
func TestSettleDisputeRefusesARulingForAnotherOrdersDispute(t *testing.T) {
	om := NewOrderManager()
	orderIn(t, om, "o-1", OrderDisputed)
	assigned, assignedPriv := newKey(t)
	esc := &recordingEscrow{}
	s := newTestSettlement(om, esc)
	s.SetDisputeCases(casesByID{"case-2": {ID: "case-2", OrderID: "o-2", ArbitratorPubKey: assigned}})

	err := s.SettleDispute(context.Background(), "o-1", signedRuling(t, "case-2", assignedPriv))
	if !errors.Is(err, ErrRulingForAnotherOrder) {
		t.Fatalf("got %v, want ErrRulingForAnotherOrder", err)
	}
	if _, settled, _ := esc.counts(); settled != 0 {
		t.Fatalf("the escrow executed %d rulings, want none", settled)
	}
}

// With no dispute cases wired, or a case id nothing knows, no ruling can be
// attributed, so every ruling is refused.
func TestSettleDisputeRefusesWhenTheDisputeCannotBeRead(t *testing.T) {
	om := NewOrderManager()
	orderIn(t, om, "o-1", OrderDisputed)
	_, priv := newKey(t)
	esc := &recordingEscrow{}
	s := newTestSettlement(om, esc)
	if err := s.SettleDispute(context.Background(), "o-1", signedRuling(t, "case-1", priv)); !errors.Is(err, ErrNoDisputeCases) {
		t.Fatalf("no dispute cases: got %v, want ErrNoDisputeCases", err)
	}
	s.SetDisputeCases(casesByID{})
	if err := s.SettleDispute(context.Background(), "o-1", signedRuling(t, "case-1", priv)); err == nil {
		t.Fatal("an unknown case was settled")
	}
	if _, settled, _ := esc.counts(); settled != 0 {
		t.Fatalf("the escrow executed %d rulings, want none", settled)
	}
	cases := casesByID{"case-1": {ID: "case-1", OrderID: "o-1"}}
	s.SetDisputeCases(cases)
	if err := s.SettleDispute(context.Background(), "o-1", signedRuling(t, "case-1", priv)); !errors.Is(err, ErrRulingNotByAssignedArbitrator) {
		t.Fatalf("a case with no assigned arbitrator: got %v, want ErrRulingNotByAssignedArbitrator", err)
	}
}

// The assigned arbitrator's ruling executes once and completes the order.
func TestSettleDisputeExecutesTheAssignedArbitratorsRuling(t *testing.T) {
	om := NewOrderManager()
	orderIn(t, om, "o-1", OrderDisputed)
	assigned, assignedPriv := newKey(t)
	esc := &recordingEscrow{}
	s := newTestSettlement(om, esc)
	s.SetDisputeCases(casesByID{"case-1": {ID: "case-1", OrderID: "o-1", ArbitratorPubKey: assigned}})

	if err := s.SettleDispute(context.Background(), "o-1", signedRulingFor(t, om, "o-1", "case-1", arbitration.RulingSplit, assignedPriv)); err != nil {
		t.Fatalf("SettleDispute: %v", err)
	}
	if _, settled, _ := esc.counts(); settled != 1 {
		t.Fatalf("the escrow executed %d rulings, want 1", settled)
	}
	if st := statusOf(t, om, "o-1"); st != OrderCompleted {
		t.Fatalf("order is %s, want completed", st)
	}
}
