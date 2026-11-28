// Copyright (c) 2026 Brandon Miller-Mumford.
// Licensed under the MIT License; see LICENSE.

package mesh

import (
	"context"
	"errors"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bbmumford/plutus/witness"
)

// countingProvider answers every balance query with 1000 and counts them.
type countingProvider struct{ calls atomic.Int32 }

func (p *countingProvider) QueryBalance(context.Context, string, string) (witness.BalanceSnapshot, error) {
	p.calls.Add(1)
	return witness.BalanceSnapshot{Balance: big.NewInt(1000), BlockNumber: 900_000, BlockHash: "00ab", Timestamp: time.Now()}, nil
}

// The balance provider a witness registers answers through the mark: the
// runtime's own BalanceVerifier attests while the chain is trusted, signs
// nothing while it is marked, and attests again once an operator clears it.
func TestTheBalanceWitnessSignsNothingForAMarkedChainUntilCleared(t *testing.T) {
	rt := newIdleRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = rt.plutus.BalanceVerifier.Run(ctx) }()

	provider := &countingProvider{}
	rt.registerChainBalanceProvider(chainIDBitcoin, provider)

	client := witness.NewBalanceVerifier(rt.plutus.Engine, witness.BalanceVerifierConfig{
		Policy: witness.VerificationPolicy{MinWitnesses: 1, MinAgreement: 1, Timeout: 3 * time.Second},
	})
	query := func(addr string, timeout time.Duration) (*witness.BalanceResult, error) {
		qctx, qcancel := context.WithTimeout(ctx, timeout)
		defer qcancel()
		return client.QueryBalance(qctx, witness.BalanceQuery{ChainID: chainIDBitcoin, Address: addr, MaxAgeSeconds: 60})
	}

	if res, err := query("bc1qtrusted", 5*time.Second); err != nil || res.Balance.Int64() != 1000 {
		t.Fatalf("trusted chain: result %v, err %v; want an attested balance of 1000", res, err)
	}

	rt.MarkUntrustworthy(chainIDBitcoin, "headers diverged from the checkpoint")
	before := provider.calls.Load()
	if res, err := query("bc1qmarked", 1500*time.Millisecond); err == nil {
		t.Fatalf("marked chain: the witness attested a balance (%v)", res.Balance)
	}
	if got := provider.calls.Load(); got != before {
		t.Fatalf("marked chain: the chain was queried %d times; want 0", got-before)
	}

	rt.ClearUntrustworthy(chainIDBitcoin)
	if res, err := query("bc1qcleared", 5*time.Second); err != nil || res.Balance.Int64() != 1000 {
		t.Fatalf("cleared chain: result %v, err %v; want an attested balance of 1000", res, err)
	}
}

func TestAChainMarkedWithAnEmptyReasonIsStillMarked(t *testing.T) {
	rt := newIdleRuntime(t)
	rt.MarkUntrustworthy(chainIDBitcoin, "")
	if err := rt.chainTrusted(chainIDBitcoin); !errors.Is(err, ErrChainUntrustworthy) {
		t.Fatalf("chainTrusted = %v; want ErrChainUntrustworthy", err)
	}
	gated := trustGatedBalanceProvider{chainID: chainIDBitcoin, trusted: rt.chainTrusted, inner: &countingProvider{}}
	if _, err := gated.QueryBalance(context.Background(), "bc1q", ""); !errors.Is(err, ErrChainUntrustworthy) {
		t.Fatalf("QueryBalance = %v; want ErrChainUntrustworthy", err)
	}
}

func TestClearUntrustworthyLiftsOnlyThatChainsMark(t *testing.T) {
	rt := newIdleRuntime(t)
	rt.MarkUntrustworthy(chainIDBitcoin, "reorg")
	rt.MarkUntrustworthy("litecoin", "stalled")

	rt.ClearUntrustworthy(chainIDBitcoin)
	rt.ClearUntrustworthy("dogecoin") // never marked: nothing happens

	if err := rt.chainTrusted(chainIDBitcoin); err != nil {
		t.Fatalf("cleared chain: chainTrusted = %v; want nil", err)
	}
	if err := rt.chainTrusted("litecoin"); !errors.Is(err, ErrChainUntrustworthy) {
		t.Fatalf("other marked chain: chainTrusted = %v; want ErrChainUntrustworthy", err)
	}
	marks := rt.Status().UntrustworthyChains
	if _, ok := marks[chainIDBitcoin]; ok || marks["litecoin"] != "stalled" {
		t.Fatalf("Status marks = %v; want only litecoin (stalled)", marks)
	}
}
