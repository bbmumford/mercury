/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */

package profile

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bbmumford/whisper"
)

// signedProfile returns a profile signed by a fresh key, stamped by its
// author at updatedAt, and its wire form.
func signedProfile(t *testing.T, name string, updatedAt time.Time) (UserProfile, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	p := UserProfile{
		PubKey:    pub,
		Name:      name,
		JoinedAt:  updatedAt.Add(-time.Hour),
		UpdatedAt: updatedAt,
	}
	if err := p.Sign(priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return p, data
}

func newProfileStore() (*ProfileService, *profileStateStore) {
	ps := NewProfileService(whisper.NewEngine())
	return ps, &profileStateStore{service: ps}
}

func applyAll(t *testing.T, s *profileStateStore, records ...[]byte) {
	t.Helper()
	for _, rec := range records {
		if err := s.Apply(rec); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
}

// Whisper's G2 digest skips the G1 exchange when two peers report the same
// non-zero fingerprint, so peers holding different profiles must report
// different fingerprints even when the profiles share their authors' times.
func TestProfileStateStore_DifferentProfilesDifferentFingerprints(t *testing.T) {
	at := time.Now().Add(-2 * time.Hour).UTC()
	later := at.Add(time.Minute)
	_, x := signedProfile(t, "x", at)
	_, y := signedProfile(t, "y", at)
	_, z := signedProfile(t, "z", later)

	_, a := newProfileStore()
	applyAll(t, a, x, y, z)
	_, b := newProfileStore()
	applyAll(t, b, z)

	if a.Fingerprint() == b.Fingerprint() {
		t.Fatalf("a peer holding {x, y, z} and a peer holding {z} reported the same fingerprint %#x", a.Fingerprint())
	}
}

// Two peers holding the same profiles report the same fingerprint whatever
// order the profiles reached them in, so the digest can skip the exchange.
func TestProfileStateStore_SameProfilesSameFingerprint(t *testing.T) {
	at := time.Now().Add(-2 * time.Hour).UTC()
	_, x := signedProfile(t, "x", at)
	_, y := signedProfile(t, "y", at.Add(time.Second))

	_, a := newProfileStore()
	applyAll(t, a, x, y)
	_, b := newProfileStore()
	applyAll(t, b, y, x)

	if a.Fingerprint() == 0 {
		t.Fatal("a non-empty store reported fingerprint 0, which the digest never counts as agreement")
	}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("peers holding the same profiles reported %#x and %#x", a.Fingerprint(), b.Fingerprint())
	}
}

// Whisper passes Delta the local time of this node's last exchange with a
// peer. A profile its author stamped before that moment but that reached
// this node after it must still be in the next Delta for that peer.
func TestProfileStateStore_LateArrivalReachesTheNextDelta(t *testing.T) {
	authored := time.Now().Add(-time.Hour).UTC()
	_, late := signedProfile(t, "late", authored)

	_, s := newProfileStore()
	lastExchange := time.Now()
	time.Sleep(2 * time.Millisecond)
	applyAll(t, s, late)

	if got := s.Delta(lastExchange); len(got) != 1 {
		t.Fatalf("Delta(last exchange) returned %d records, want the 1 profile stored after it", len(got))
	}
}

// A profile a peer sends through state sync is admitted only under the rule
// Set applies, the rule gossip (OnBroadcast) applies too: a signed profile
// whose bio is over the limit is refused on both paths.
func TestProfileStateStore_ApplyRefusesWhatSetRefuses(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p := UserProfile{PubKey: pub, Name: "long", Bio: strings.Repeat("b", maxBioLength+1), JoinedAt: now, UpdatedAt: now}
	if err := p.Sign(priv); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(p)

	ps, s := newProfileStore()
	if err := s.Apply(data); err == nil {
		t.Fatal("Apply admitted a signed profile whose bio is over the limit Set enforces")
	}
	(&profileSubscriber{service: ps}).OnBroadcast(topicProfiles, "peer", data)
	if ps.CachedProfiles() != 0 || s.Fingerprint() != 0 {
		t.Fatalf("a refused profile reached the cache (%d held, fingerprint %#x)", ps.CachedProfiles(), s.Fingerprint())
	}
}

// The newer profile of the same author replaces the older one in what the
// store serves, and an older copy arriving afterwards changes nothing.
func TestProfileStateStore_NewerProfileReplacesTheServedRecord(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour).UTC()
	mk := func(name string, when time.Time) []byte {
		p := UserProfile{PubKey: pub, Name: name, JoinedAt: at, UpdatedAt: when}
		if err := p.Sign(priv); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(p)
		return b
	}
	older, newer := mk("old", at), mk("new", at.Add(time.Minute))

	_, s := newProfileStore()
	applyAll(t, s, older, newer)
	fp := s.Fingerprint()
	applyAll(t, s, older)
	if s.Fingerprint() != fp {
		t.Fatal("an older copy of a held profile changed the fingerprint")
	}
	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("Snapshot returned %d records, want 1", len(snap))
	}
	var got UserProfile
	if err := json.Unmarshal(snap[0], &got); err != nil || got.Name != "new" {
		t.Fatalf("Snapshot served %q (err %v), want the newer profile", got.Name, err)
	}
}
