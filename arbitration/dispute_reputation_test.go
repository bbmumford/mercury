/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package arbitration

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestDisputeScoresFollowTheRulingsOutcome(t *testing.T) {
	for outcome, want := range map[RulingOutcome][2]int32{
		RulingBuyerWins:  {1, -1},
		RulingSellerWins: {-1, 1},
		RulingSplit:      {0, 0},
	} {
		if b, s := DisputeScores(outcome); b != want[0] || s != want[1] {
			t.Errorf("%s: scores buyer %d seller %d, want %d %d", outcome, b, s, want[0], want[1])
		}
	}
}

func TestDisputeReputationSignedByTheArbitratorVerifiesForItsOwnOrderOnly(t *testing.T) {
	arbPub, arbPriv, _ := ed25519.GenerateKey(rand.Reader)
	buyer, _, _ := ed25519.GenerateKey(rand.Reader)
	seller, _, _ := ed25519.GenerateKey(rand.Reader)
	r := ArbitratorRuling{CaseID: "case-1", ArbitratorPubKey: arbPub, Outcome: RulingBuyerWins,
		BuyerPercent: 100, Timestamp: time.Unix(1_700_000_100, 0)}
	if err := SignDisputeReputation(&r, "o-1", buyer, seller, arbPriv); err != nil {
		t.Fatal(err)
	}

	b, s, ok := VerifyDisputeReputation(&r, "o-1", buyer, seller)
	if !ok || b.Score != 1 || s.Score != -1 || b.TxHash != "o-1" || b.AppID != DisputeReputationAppID {
		t.Fatalf("VerifyDisputeReputation = %+v %+v %v; want the two signed records", b, s, ok)
	}
	if _, _, ok := VerifyDisputeReputation(&r, "o-2", buyer, seller); ok {
		t.Fatal("the signatures verify for another order")
	}
	if _, _, ok := VerifyDisputeReputation(&r, "o-1", seller, buyer); ok {
		t.Fatal("the signatures verify with buyer and seller swapped")
	}
}

func TestSignDisputeReputationRefusesAKeyOtherThanTheRulingsArbitrator(t *testing.T) {
	arbPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	buyer, _, _ := ed25519.GenerateKey(rand.Reader)
	seller, _, _ := ed25519.GenerateKey(rand.Reader)
	r := ArbitratorRuling{ArbitratorPubKey: arbPub, Outcome: RulingSplit, BuyerPercent: 50, SellerPercent: 50, Timestamp: time.Unix(1, 0)}
	if err := SignDisputeReputation(&r, "o-1", buyer, seller, otherPriv); err == nil {
		t.Fatal("signed with a key that is not the ruling's arbitrator")
	}
	if r.BuyerReputationSignature != nil || r.SellerReputationSignature != nil {
		t.Fatal("a refused signing left signatures on the ruling")
	}
}
