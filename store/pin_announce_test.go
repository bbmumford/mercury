/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package store

import (
	"testing"

	"github.com/bbmumford/whisper"
)

// A pin whose announcement cannot be published is reported and taken back, so
// replicating the same root again (once the topic is registered) succeeds
// instead of answering ErrStaleVersion for a pin no peer heard of.
func TestReplicateReportsAPinItCouldNotAnnounce(t *testing.T) {
	engine := whisper.NewEngine()
	sm := testStoreManager(t)
	if err := sm.AddListing(testListing("item-1")); err != nil {
		t.Fatalf("AddListing: %v", err)
	}
	root, err := sm.Publish()
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	rm := NewReplicationManager(sm, engine) // topics not registered: the announcement cannot be published
	repPub, repPriv := generateKeypair(t)
	rm.SetReplicatorIdentity(repPub, repPriv)

	if err := rm.Replicate(t.Context(), *root); err == nil {
		t.Fatal("Replicate reported success for a pin it could not announce")
	}
	if rm.IsPinned(sm.StoreID()) {
		t.Fatal("the unannounced pin is still held")
	}

	if err := rm.RegisterTopics(); err != nil {
		t.Fatalf("RegisterTopics: %v", err)
	}
	if err := rm.Replicate(t.Context(), *root); err != nil {
		t.Fatalf("Replicate after the topic is registered: %v", err)
	}
	if !rm.IsPinned(sm.StoreID()) || rm.PinnedVersion(sm.StoreID()) != root.Version {
		t.Fatal("the announced pin is not held at the root's version")
	}
}
