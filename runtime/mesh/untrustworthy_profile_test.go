// Copyright (c) 2026 Brandon Miller-Mumford.
// Licensed under the MIT License; see LICENSE.

package mesh

import (
	"context"
	"reflect"
	"testing"
)

// newIdleRuntime builds a runtime that is never started: the witness
// profile path needs only its Plutus SDK and status tracker.
func newIdleRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := NewRuntime(minimalConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

// A witness profile is the tx witness's signed answer to "which chains can
// this node attest": signing one for a chain an operator marked
// untrustworthy would keep peers sending it attestation work for that chain.
func TestNoWitnessProfileIsSignedForAChainMarkedUntrustworthy(t *testing.T) {
	rt := newIdleRuntime(t)
	rt.MarkUntrustworthy(chainIDBitcoin, "headers diverged from the checkpoint")

	rt.registerWitnessProfile(context.Background(), []string{chainIDBitcoin}, []string{"tx-confirm"})

	if reg := rt.plutus.WitnessRegistry.Get(rt.plutus.PublicKey()); reg != nil {
		t.Fatalf("a witness profile was signed for a chain marked untrustworthy: chains %v", reg.ChainIDs)
	}
	if rt.Status().LastError == "" {
		t.Fatal("leaving the marked chain out recorded no error in Status")
	}
}

func TestAWitnessProfileLeavesOutOnlyTheMarkedChain(t *testing.T) {
	rt := newIdleRuntime(t)
	rt.MarkUntrustworthy(chainIDBitcoin, "reorg deeper than the confirmation depth")

	rt.registerWitnessProfile(context.Background(), []string{chainIDBitcoin, "litecoin"}, []string{"balance", "tx-confirm"})

	reg := rt.plutus.WitnessRegistry.Get(rt.plutus.PublicKey())
	if reg == nil {
		t.Fatal("no witness profile was signed for the chain that is still trusted")
	}
	if want := []string{"litecoin"}; !reflect.DeepEqual(reg.ChainIDs, want) {
		t.Fatalf("profile chains = %v, want %v", reg.ChainIDs, want)
	}
}
