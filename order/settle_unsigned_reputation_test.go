package order

import (
	"context"
	"testing"
)

// A dispute's reputation records are the arbitrator's to sign, as the
// ruling is. A ruling that carries no signatures over them must move no
// money: the settling node holds no key that could sign them, so settling
// would leave the dispute's outcome unrecorded for good.
func TestARulingWithoutTheArbitratorsReputationSignaturesMovesNoMoney(t *testing.T) {
	om := NewOrderManager()
	orderIn(t, om, "o-1", OrderDisputed)
	assigned, assignedPriv := newKey(t)
	esc := &recordingEscrow{}
	s := newTestSettlement(om, esc)
	s.SetDisputeCases(casesByID{"case-1": {ID: "case-1", OrderID: "o-1", ArbitratorPubKey: assigned}})

	err := s.SettleDispute(context.Background(), "o-1", signedRuling(t, "case-1", assignedPriv))
	if err == nil {
		t.Fatal("a ruling without reputation signatures was settled")
	}
	if _, settled, _ := esc.counts(); settled != 0 {
		t.Fatalf("the escrow executed %d rulings, want none", settled)
	}
	if st := statusOf(t, om, "o-1"); st != OrderDisputed {
		t.Fatalf("order is %s, want it still disputed", st)
	}
}
