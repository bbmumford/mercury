/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */

package reputation

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"github.com/bbmumford/whisper"
)

// signedReview returns a review of seller signed by a fresh reviewer key,
// stamped by its author at at, and its wire form.
func signedReview(t *testing.T, seller ed25519.PublicKey, order string, rating int, at time.Time) []byte {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	r := Review{ReviewerPubKey: pub, SellerPubKey: seller, OrderID: order, Rating: rating, Title: "t", Timestamp: at}
	if err := r.Sign(priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func newReviewStore() (*ReviewService, *reviewStateStore) {
	rs := NewReviewService(whisper.NewEngine())
	return rs, &reviewStateStore{service: rs}
}

func applyReviews(t *testing.T, s *reviewStateStore, records ...[]byte) {
	t.Helper()
	for _, rec := range records {
		if err := s.Apply(rec); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
}

func sellerKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// Whisper's G2 digest skips the G1 exchange when two peers report the same
// non-zero fingerprint, so peers holding different reviews must report
// different fingerprints even when the review counts and times line up.
func TestReviewStateStore_DifferentReviewsDifferentFingerprints(t *testing.T) {
	seller := sellerKey(t)
	at := time.Now().Add(-2 * time.Hour).UTC()
	x := signedReview(t, seller, "order-x", 5, at)
	y := signedReview(t, seller, "order-y", 1, at)

	_, a := newReviewStore()
	applyReviews(t, a, x)
	_, b := newReviewStore()
	applyReviews(t, b, y)

	if a.Fingerprint() == b.Fingerprint() {
		t.Fatalf("peers holding different reviews reported the same fingerprint %#x", a.Fingerprint())
	}
}

// Two peers holding the same reviews report the same non-zero fingerprint
// whatever order the reviews reached them in.
func TestReviewStateStore_SameReviewsSameFingerprint(t *testing.T) {
	seller := sellerKey(t)
	at := time.Now().Add(-2 * time.Hour).UTC()
	x := signedReview(t, seller, "order-x", 5, at)
	y := signedReview(t, seller, "order-y", 4, at.Add(time.Second))

	_, a := newReviewStore()
	applyReviews(t, a, x, y)
	_, b := newReviewStore()
	applyReviews(t, b, y, x)

	if a.Fingerprint() == 0 {
		t.Fatal("a non-empty store reported fingerprint 0, which the digest never counts as agreement")
	}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("peers holding the same reviews reported %#x and %#x", a.Fingerprint(), b.Fingerprint())
	}
}

// Whisper passes Delta the local time of this node's last exchange with a
// peer. A review its author stamped before that moment but that reached
// this node after it must still be in the next Delta for that peer.
func TestReviewStateStore_LateArrivalReachesTheNextDelta(t *testing.T) {
	late := signedReview(t, sellerKey(t), "order-late", 4, time.Now().Add(-time.Hour).UTC())

	_, s := newReviewStore()
	lastExchange := time.Now()
	time.Sleep(2 * time.Millisecond)
	applyReviews(t, s, late)

	if got := s.Delta(lastExchange); len(got) != 1 {
		t.Fatalf("Delta(last exchange) returned %d records, want the 1 review stored after it", len(got))
	}
}

// A review a peer sends through state sync is admitted only under the rule
// Submit applies, the rule gossip (OnBroadcast) applies too: a signed review
// with a rating outside 1-5 is refused on both paths.
func TestReviewStateStore_ApplyRefusesWhatSubmitRefuses(t *testing.T) {
	seller := sellerKey(t)
	bad := signedReview(t, seller, "order-bad", 9, time.Now().UTC())

	rs, s := newReviewStore()
	if err := s.Apply(bad); err == nil {
		t.Fatal("Apply admitted a signed review rated 9, which Submit refuses")
	}
	(&reviewSubscriber{service: rs}).OnBroadcast(topicReviews, "peer", bad)
	if rs.ReviewCount(seller) != 0 || s.Fingerprint() != 0 {
		t.Fatalf("a refused review reached the store (%d held, fingerprint %#x)", rs.ReviewCount(seller), s.Fingerprint())
	}
}

// One review per reviewer per order: a second review of the same order by
// the same reviewer leaves what the store serves unchanged.
func TestReviewStateStore_OneReviewPerReviewerPerOrder(t *testing.T) {
	seller := sellerKey(t)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour).UTC()
	mk := func(rating int, when time.Time) []byte {
		r := Review{ReviewerPubKey: pub, SellerPubKey: seller, OrderID: "order-1", Rating: rating, Timestamp: when}
		if err := r.Sign(priv); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r)
		return b
	}
	rs, s := newReviewStore()
	applyReviews(t, s, mk(5, at))
	fp := s.Fingerprint()
	applyReviews(t, s, mk(1, at.Add(time.Minute)))
	if rs.ReviewCount(seller) != 1 || s.Fingerprint() != fp || len(s.Snapshot()) != 1 {
		t.Fatalf("a second review of the same order changed the store: %d held, %d served", rs.ReviewCount(seller), len(s.Snapshot()))
	}
}
