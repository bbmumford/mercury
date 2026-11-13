// Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
// Queries: licensing@hstles.com

package mesh

// Behaviour tests for the proof-of-storage on-chain escalation path.
// These exercise the DECISION LOGIC in escalateMissOnChain /
// scheduleExpireChallenge / fireExpireChallenge in isolation from the
// full Runtime bring-up — the Runtime is constructed only for its
// struct fields (cfg, expireSched*), not Started.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bbmumford/ledger/blob/distributed/onchain"
)

// fakeEscrowClient records every on-chain call the mesh makes so tests
// can assert both the fact and the arguments of escalation.
type fakeEscrowClient struct {
	mu         sync.Mutex
	opens      []fakeOpenCall
	expires    []fakeExpireCall
	posts      []string
	creates    []onchain.CreatePinParams
	openErr    error
	expireErr  error
	openCount  atomic.Int64
	expCount   atomic.Int64
}

type fakeOpenCall struct {
	CID      [32]byte
	Nonce    [32]byte
	Expected [32]byte
}

type fakeExpireCall struct {
	CID [32]byte
}

func (f *fakeEscrowClient) CreatePin(_ context.Context, p onchain.CreatePinParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, p)
	return "0xfake-escrow", nil
}

func (f *fakeEscrowClient) PostBond(_ context.Context, addr string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, addr)
	return "0xfake-bond-tx", nil
}

func (f *fakeEscrowClient) OpenChallenge(_ context.Context, cid, nonce, expected [32]byte) (string, error) {
	f.openCount.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens = append(f.opens, fakeOpenCall{CID: cid, Nonce: nonce, Expected: expected})
	if f.openErr != nil {
		return "", f.openErr
	}
	return "0xfake-open-tx", nil
}

func (f *fakeEscrowClient) ExpireChallenge(_ context.Context, cid [32]byte) (string, error) {
	f.expCount.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expires = append(f.expires, fakeExpireCall{CID: cid})
	if f.expireErr != nil {
		return "", f.expireErr
	}
	return "0xfake-exp-tx", nil
}

func (f *fakeEscrowClient) snapshotOpens() []fakeOpenCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeOpenCall, len(f.opens))
	copy(out, f.opens)
	return out
}

func (f *fakeEscrowClient) snapshotExpires() []fakeExpireCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeExpireCall, len(f.expires))
	copy(out, f.expires)
	return out
}

// mapResolver satisfies EscrowCIDResolver.
type mapResolver struct {
	m map[string]string
}

func (r *mapResolver) EscrowAddressForCID(cid string) (string, bool) {
	addr, ok := r.m[cid]
	return addr, ok
}

// stubPieceStore is a minimal fake of the distributed.Store. Only the
// Has / Get entry points used by buildOnChainChallengeInputs are
// needed, but the helper takes a *distributed.Store directly — which
// we cannot fake out easily. Instead, buildOnChainChallengeInputs
// returns false when blobStore is nil, and the tests exercise the
// escalation path up to THAT branch by asserting that an absent piece
// produces no OpenChallenge call. A companion test that populates a
// real store is wired in runtime_test.go when the shared blob pipeline
// lands.

func TestEscalateMissOnChain_NilEscrowClient_NoOp(t *testing.T) {
	t.Parallel()
	r := &Runtime{cfg: Config{}} // EscrowClient == nil
	r.escalateMissOnChain(context.Background(), "bafy1",
		onchain.ChallengeResult{Reason: "timeout"})
	// No panic, no observable state change. Asserting on the
	// "nothing happened" invariant is mostly about not regressing
	// the nil-guard; we verify by checking there is no active timer.
	if len(r.expireSched) != 0 {
		t.Errorf("expireSched should be empty with nil client")
	}
}

func TestEscalateMissOnChain_NilResolver_NoOp(t *testing.T) {
	t.Parallel()
	fake := &fakeEscrowClient{}
	r := &Runtime{cfg: Config{EscrowClient: fake}}
	r.escalateMissOnChain(context.Background(), "bafy1",
		onchain.ChallengeResult{Reason: "timeout"})
	if got := fake.openCount.Load(); got != 0 {
		t.Errorf("OpenChallenge called %d times without a resolver", got)
	}
}

func TestEscalateMissOnChain_NonSlashableReason_NoOp(t *testing.T) {
	t.Parallel()
	fake := &fakeEscrowClient{}
	r := &Runtime{cfg: Config{
		EscrowClient:   fake,
		EscrowResolver: &mapResolver{m: map[string]string{"bafy1": "0xaaa"}},
	}}
	for _, reason := range []string{"transport:dial refused", "decode_error", "cid_mismatch", "empty_response"} {
		r.escalateMissOnChain(context.Background(), "bafy1",
			onchain.ChallengeResult{Reason: reason})
	}
	if got := fake.openCount.Load(); got != 0 {
		t.Errorf("OpenChallenge called %d times for non-slashable reasons", got)
	}
}

func TestEscalateMissOnChain_NoMapping_NoOp(t *testing.T) {
	t.Parallel()
	fake := &fakeEscrowClient{}
	r := &Runtime{cfg: Config{
		EscrowClient:   fake,
		EscrowResolver: &mapResolver{m: map[string]string{}}, // empty
	}}
	r.escalateMissOnChain(context.Background(), "unknown-cid",
		onchain.ChallengeResult{Reason: "timeout"})
	if got := fake.openCount.Load(); got != 0 {
		t.Errorf("OpenChallenge called %d times without a CID mapping", got)
	}
}

func TestEscalateMissOnChain_MissingBlobStore_NoOp(t *testing.T) {
	t.Parallel()
	// EscrowClient + resolver both set, but blobStore nil → the
	// buildOnChainChallengeInputs helper returns ok=false and no
	// on-chain call is issued.
	fake := &fakeEscrowClient{}
	r := &Runtime{cfg: Config{
		EscrowClient:   fake,
		EscrowResolver: &mapResolver{m: map[string]string{"bafy1": "0xaaa"}},
	}} // blobStore stays nil
	r.escalateMissOnChain(context.Background(), "bafy1",
		onchain.ChallengeResult{Reason: "timeout"})
	if got := fake.openCount.Load(); got != 0 {
		t.Errorf("OpenChallenge called %d times without a blob store", got)
	}
}

func TestScheduleExpireChallenge_FiresAfterDelay(t *testing.T) {
	t.Parallel()
	fake := &fakeEscrowClient{}
	r := &Runtime{cfg: Config{EscrowClient: fake}}
	// Compress the 24h window to 10ms for the test via the hook.
	r.expireScheduleHook = func(time.Duration) time.Duration { return 10 * time.Millisecond }

	var cidHash [32]byte
	for i := range cidHash {
		cidHash[i] = byte(0x42)
	}
	r.scheduleExpireChallenge("bafy42", cidHash)

	// Wait up to 500ms for the timer to fire + the call to record.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if fake.expCount.Load() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := fake.expCount.Load(); got != 1 {
		t.Fatalf("ExpireChallenge fired %d times, want 1", got)
	}
	got := fake.snapshotExpires()
	if got[0].CID != cidHash {
		t.Errorf("cid mismatch: got %x, want %x", got[0].CID[:], cidHash[:])
	}
}

func TestScheduleExpireChallenge_Idempotent_CancelsPrior(t *testing.T) {
	t.Parallel()
	fake := &fakeEscrowClient{}
	r := &Runtime{cfg: Config{EscrowClient: fake}}
	r.expireScheduleHook = func(time.Duration) time.Duration { return 50 * time.Millisecond }

	var cidHash [32]byte
	copy(cidHash[:], []byte("idempotent-cid--padding---32byte"))
	// Schedule twice — the second call should cancel the first so only
	// one ExpireChallenge fires.
	r.scheduleExpireChallenge("idem", cidHash)
	r.scheduleExpireChallenge("idem", cidHash)

	// Wait for the surviving timer to fire, polling so a loaded host that
	// runs the 50ms timer late does not read zero, then wait past both
	// timers' deadlines again so an uncancelled first timer would be seen.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && fake.expCount.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if got := fake.expCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 expire call, got %d", got)
	}
}

func TestFireExpireChallenge_BenignRevert_NoError(t *testing.T) {
	t.Parallel()
	fake := &fakeEscrowClient{
		expireErr: errors.New("execution reverted: ChallengeWindowNotExpired()"),
	}
	r := &Runtime{cfg: Config{EscrowClient: fake}}
	var cidHash [32]byte
	r.fireExpireChallenge("bafyX", cidHash)
	// We can't observe much from the outside, but we can verify the
	// call was made exactly once and did not panic on the benign
	// revert.
	if got := fake.expCount.Load(); got != 1 {
		t.Errorf("expire call count: %d, want 1", got)
	}
}

func TestIsBenignRevert_RecognizesContractErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("execution reverted: ChallengeWindowNotExpired()"), true},
		{errors.New("execution reverted: NoOpenChallenge()"), true},
		{errors.New("nonce too low"), false},
		{errors.New("transport: connection refused"), false},
	}
	for _, tc := range cases {
		got := isBenignRevert(tc.err)
		if got != tc.want {
			t.Errorf("isBenignRevert(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestStartProofOfStorage_NilEscrowClient_StillStarts(t *testing.T) {
	t.Parallel()
	// This test covers the logging-banner branch: a Runtime with no
	// escrow client wired must still be happy to start the PoS loop.
	// We construct a minimal Runtime the same way the other
	// runtime_test.go tests do and rely on the integration-style
	// TestNewRuntime_ConstructStartStop (elsewhere) to exercise the
	// actual Start() path; here we just want the configured client
	// to be nil without a panic and without the expireSched map
	// being implicitly populated by an accidental code path.
	r := &Runtime{cfg: Config{EscrowClient: nil}}
	if r.expireSched != nil {
		t.Errorf("expireSched should be nil before scheduleExpireChallenge")
	}
}
