package order

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bbmumford/mercury/arbitration"
	"github.com/bbmumford/plutus/reputation"
	"github.com/bbmumford/whisper"
)

// signedRulingFor returns a ruling on caseID signed by priv, with the
// arbitrator's signatures over orderID's buyer's and seller's dispute
// reputation records.
func signedRulingFor(t *testing.T, om *OrderManager, orderID, caseID string, outcome arbitration.RulingOutcome, priv ed25519.PrivateKey) arbitration.ArbitratorRuling {
	t.Helper()
	pct := map[arbitration.RulingOutcome][2]uint32{
		arbitration.RulingBuyerWins: {100, 0}, arbitration.RulingSellerWins: {0, 100}, arbitration.RulingSplit: {50, 50},
	}[outcome]
	r := arbitration.ArbitratorRuling{CaseID: caseID, ArbitratorPubKey: priv.Public().(ed25519.PublicKey),
		Outcome: outcome, BuyerPercent: pct[0], SellerPercent: pct[1], Timestamp: time.Unix(1_700_000_100, 7)}
	if err := arbitration.SignRuling(&r, priv); err != nil {
		t.Fatal(err)
	}
	o, err := om.Get(orderID)
	if err != nil {
		t.Fatal(err)
	}
	if err := arbitration.SignDisputeReputation(&r, orderID, o.Intent.BuyerPubKey, o.Intent.SellerPubKey, priv); err != nil {
		t.Fatal(err)
	}
	return r
}

// disputedOrder sets up order o-1 disputed under case-1 with an assigned
// arbitrator, and a settlement service over a recording escrow.
func disputedOrder(t *testing.T) (*OrderManager, *recordingEscrow, *SettlementService, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	om := NewOrderManager()
	orderIn(t, om, "o-1", OrderDisputed)
	assigned, assignedPriv := newKey(t)
	esc := &recordingEscrow{}
	s := newTestSettlement(om, esc)
	s.SetDisputeCases(casesByID{"case-1": {ID: "case-1", OrderID: "o-1", ArbitratorPubKey: assigned}})
	return om, esc, s, assigned, assignedPriv
}

func TestSettleDisputeStoresBothPartiesRecordsAsTheArbitratorSignedThem(t *testing.T) {
	om, esc, s, assigned, assignedPriv := disputedOrder(t)
	rep := reputation.NewReputationService(whisper.NewEngine(), nil)
	s.SetReputationService(rep)
	ruling := signedRulingFor(t, om, "o-1", "case-1", arbitration.RulingBuyerWins, assignedPriv)

	if err := s.SettleDispute(context.Background(), "o-1", ruling); err != nil {
		t.Fatalf("SettleDispute: %v", err)
	}
	if _, settled, _ := esc.counts(); settled != 1 {
		t.Fatalf("the escrow executed %d rulings, want 1", settled)
	}
	o, _ := om.Get("o-1")
	for _, party := range []struct {
		name    string
		subject []byte
		score   int32
	}{{"buyer", o.Intent.BuyerPubKey, 1}, {"seller", o.Intent.SellerPubKey, -1}} {
		hist := rep.History(party.subject, 10)
		if len(hist) != 1 {
			t.Fatalf("%s: %d reputation records stored, want 1", party.name, len(hist))
		}
		got := hist[0]
		if got.Type != reputation.Dispute || got.Score != party.score || got.TxHash != "o-1" ||
			got.AppID != arbitration.DisputeReputationAppID || !got.Timestamp.Equal(ruling.Timestamp) ||
			!bytes.Equal(got.RecorderPubKey, assigned) || !reputation.VerifyRecord(&got) {
			t.Fatalf("%s: stored record %+v; want the arbitrator's signed dispute record, score %d", party.name, got, party.score)
		}
	}
}

func TestSettleDisputeRefusesReputationSignaturesThatAreNotTheArbitratorsOverThisRuling(t *testing.T) {
	_, otherPriv := newKey(t)
	cases := map[string]func(t *testing.T, om *OrderManager, priv ed25519.PrivateKey) arbitration.ArbitratorRuling{
		"swapped": func(t *testing.T, om *OrderManager, priv ed25519.PrivateKey) arbitration.ArbitratorRuling {
			r := signedRulingFor(t, om, "o-1", "case-1", arbitration.RulingSellerWins, priv)
			r.BuyerReputationSignature, r.SellerReputationSignature = r.SellerReputationSignature, r.BuyerReputationSignature
			return r
		},
		"the seller's by another key": func(t *testing.T, om *OrderManager, priv ed25519.PrivateKey) arbitration.ArbitratorRuling {
			r := signedRulingFor(t, om, "o-1", "case-1", arbitration.RulingSellerWins, priv)
			foreign := signedRulingFor(t, om, "o-1", "case-1", arbitration.RulingSellerWins, otherPriv)
			r.SellerReputationSignature = foreign.SellerReputationSignature
			return r
		},
		"missing": func(t *testing.T, om *OrderManager, priv ed25519.PrivateKey) arbitration.ArbitratorRuling {
			r := signedRulingFor(t, om, "o-1", "case-1", arbitration.RulingSplit, priv)
			r.BuyerReputationSignature = nil
			return r
		},
		"another outcome": func(t *testing.T, om *OrderManager, priv ed25519.PrivateKey) arbitration.ArbitratorRuling {
			r := signedRulingFor(t, om, "o-1", "case-1", arbitration.RulingSellerWins, priv)
			sellerWins := r
			r.Outcome, r.BuyerPercent, r.SellerPercent = arbitration.RulingBuyerWins, 100, 0
			if err := arbitration.SignRuling(&r, priv); err != nil {
				t.Fatal(err)
			}
			r.BuyerReputationSignature, r.SellerReputationSignature = sellerWins.BuyerReputationSignature, sellerWins.SellerReputationSignature
			return r
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			om, esc, s, _, assignedPriv := disputedOrder(t)
			err := s.SettleDispute(context.Background(), "o-1", build(t, om, assignedPriv))
			if !errors.Is(err, ErrDisputeReputationUnsigned) {
				t.Fatalf("got %v, want ErrDisputeReputationUnsigned", err)
			}
			if _, settled, _ := esc.counts(); settled != 0 {
				t.Fatalf("the escrow executed %d rulings, want none", settled)
			}
			if st := statusOf(t, om, "o-1"); st != OrderDisputed {
				t.Fatalf("order is %s, want it still disputed", st)
			}
		})
	}
}

// failingRecorder refuses the records of one subject with cause.
type failingRecorder struct {
	mu      sync.Mutex
	refuse  []byte
	cause   error
	records []reputation.ReputationRecord
}

func (f *failingRecorder) Record(rec reputation.ReputationRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if bytes.Equal(rec.SubjectPubKey, f.refuse) {
		return f.cause
	}
	f.records = append(f.records, rec)
	return nil
}

// A store failure after the escrow moved is not a refusal: the order is
// settled and completed, and the error says so apart from every refusal.
func TestAReputationStoreFailureAfterSettlementIsReportedAsSettledButNotRecorded(t *testing.T) {
	om, esc, s, _, assignedPriv := disputedOrder(t)
	o, _ := om.Get("o-1")
	cause := errors.New("state store unavailable")
	rec := &failingRecorder{refuse: o.Intent.SellerPubKey, cause: cause}
	s.repService = rec

	err := s.SettleDispute(context.Background(), "o-1", signedRulingFor(t, om, "o-1", "case-1", arbitration.RulingSplit, assignedPriv))
	if !errors.Is(err, ErrReputationNotRecorded) || !errors.Is(err, cause) {
		t.Fatalf("got %v, want ErrReputationNotRecorded wrapping the store's error", err)
	}
	if _, settled, _ := esc.counts(); settled != 1 {
		t.Fatalf("the escrow executed %d rulings, want 1", settled)
	}
	if st := statusOf(t, om, "o-1"); st != OrderCompleted {
		t.Fatalf("order is %s, want completed", st)
	}
	if len(rec.records) != 1 || !bytes.Equal(rec.records[0].SubjectPubKey, o.Intent.BuyerPubKey) {
		t.Fatalf("stored %d records; want the buyer's, tried despite the seller's failure", len(rec.records))
	}
}

func TestSettleDisputeWithTheReputationServiceUnsetStillSettles(t *testing.T) {
	om, esc, s, _, assignedPriv := disputedOrder(t)
	s.SetReputationService(nil)
	if err := s.SettleDispute(context.Background(), "o-1", signedRulingFor(t, om, "o-1", "case-1", arbitration.RulingSplit, assignedPriv)); err != nil {
		t.Fatalf("SettleDispute: %v", err)
	}
	if _, settled, _ := esc.counts(); settled != 1 {
		t.Fatalf("the escrow executed %d rulings, want 1", settled)
	}
}
