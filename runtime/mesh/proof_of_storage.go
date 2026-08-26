// Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
// Queries: licensing@hstles.com

package mesh

import (
	"context"
	"errors"
	"log"
	"math/rand"
	"time"

	"github.com/bbmumford/ledger/blob/distributed/onchain"
	"github.com/bbmumford/whisper"
)

// challengeTickInterval is how often the challenger loop wakes to roll
// the stochastic dice.
const challengeTickInterval = 10 * time.Minute

// expectedChallengeInterval is the per-CID target inter-challenge time
// assumed by each tick's probability calculation.
const expectedChallengeInterval = 24 * time.Hour

// minKnownPeersForStochastic caps the "pretend there are many peers"
// divisor so a bootstrapping node still emits challenges at a sane
// rate.
const minKnownPeersForStochastic = 3

// proofChallengeResponseTimeout bounds the per-challenge wait.
const proofChallengeResponseTimeout = 45 * time.Second

// onChainChallengeWindow mirrors the PlutusStorageEscrow.sol
// CHALLENGE_WINDOW_SECONDS (24h). Must match the Solidity constant
// exactly — a smaller value risks firing expireChallenge before the
// contract is ready (which reverts with ChallengeWindowNotExpired),
// a larger value leaks the 10%-bounty opportunity to any other peer
// watching the same pin.
const onChainChallengeWindow = 24 * time.Hour

// onChainChallengeBuffer is added to onChainChallengeWindow when
// scheduling the expireChallenge call, defending against block-time
// drift / RPC clock skew. Empirically 5 minutes is plenty on every
// supported chain (Polygon ~2s blocks, Ethereum ~12s).
const onChainChallengeBuffer = 5 * time.Minute

// onChainMissReasons is the set of ChallengeResult.Reason values we
// treat as "replicator failed — escalate on-chain". Any other reason
// (e.g. transport:..., decode_error, cid_mismatch) may still be a
// local bug on the CHALLENGER side, so we don't slash over it.
var onChainMissReasons = map[string]struct{}{
	"timeout":        {},
	"sig_fail":       {},
	"nonce_mismatch": {},
	"wrong_hash":     {},
}

// whisperChallengeTransport adapts whisper.Engine.Query to the
// onchain.ChallengeTransport contract.
type whisperChallengeTransport struct {
	engine *whisper.Engine
	topic  string
}

func (t *whisperChallengeTransport) Ask(ctx context.Context, peerID string, payload []byte) ([]byte, error) {
	if t.engine == nil {
		return nil, errors.New("mesh: challenge transport has no engine")
	}
	responses, err := t.engine.Query(ctx, t.topic, payload)
	if err != nil {
		return nil, err
	}
	if len(responses) == 0 {
		return nil, nil
	}
	for _, rsp := range responses {
		if len(rsp) > 0 {
			return rsp, nil
		}
	}
	return nil, nil
}

// startProofOfStorage brings up two loops:
//
//  1. Responder: registers a ChallengeResponder on
//     plutus.blob.challenge so inbound challenges get served from the
//     local blob store.
//
//  2. Challenger: periodically picks a CID from the local store and
//     issues a stochastic challenge to a random known peer.
//
// Returns an error only when a hard wiring problem prevents startup.
func (r *Runtime) startProofOfStorage(ctx context.Context) error {
	if r.blobStore == nil {
		return errors.New("mesh: proof-of-storage needs an active blob store")
	}
	if r.plutus == nil || r.plutus.Engine == nil {
		return errors.New("mesh: proof-of-storage needs a Whisper engine")
	}
	if len(r.plutus.IdentityKey) == 0 {
		return errors.New("mesh: proof-of-storage needs an identity key")
	}

	responder, err := onchain.NewChallengeResponder(r.blobStore, onchain.ChallengeResponderConfig{
		PeerID:      r.peerIDOrEmpty(),
		IdentityKey: r.plutus.IdentityKey,
	})
	if err != nil {
		return err
	}
	if err := registerBlobTopic(r.plutus.Engine, onchain.TopicBlobChallenge, whisper.TopicConfig{
		Mode:    whisper.RequestResponse,
		Handler: responder,
	}); err != nil {
		return err
	}

	transport := &whisperChallengeTransport{
		engine: r.plutus.Engine,
		topic:  onchain.TopicBlobChallenge,
	}
	issuer, err := onchain.NewChallengeIssuerWithPieceSource(transport, r.blobStore, onchain.ChallengeIssuerConfig{
		IssuerPeerID:    r.peerIDOrEmpty(),
		ResponseTimeout: proofChallengeResponseTimeout,
	})
	if err != nil {
		return err
	}

	go r.runProofChallengerLoop(ctx, issuer)

	// Log a one-shot banner so operators can tell — from the first
	// boot log — whether on-chain miss submission is wired. A nil
	// EscrowClient is an explicit configuration, not a bug.
	if r.cfg.EscrowClient == nil {
		log.Printf("mesh: proof_of_storage: on-chain miss submission DISABLED (no EscrowClient configured)")
	} else {
		log.Printf("mesh: proof_of_storage: on-chain miss submission enabled")
	}
	return nil
}

// peerIDOrEmpty returns a best-effort PeerID string. Safe when the
// plutus identity key isn't fully derived yet.
func (r *Runtime) peerIDOrEmpty() string {
	pub := r.plutus.PublicKey()
	if len(pub) < 20 {
		return ""
	}
	const hex = "0123456789abcdef"
	buf := make([]byte, 40)
	for i, b := range pub[:20] {
		buf[2*i] = hex[b>>4]
		buf[2*i+1] = hex[b&0x0f]
	}
	return string(buf)
}

// runProofChallengerLoop is the stochastic challenge issuer. On each
// tick: respect the governor; enumerate CIDs; roll per-CID biased
// coin; pick a target peer; fire the challenge.
func (r *Runtime) runProofChallengerLoop(ctx context.Context, issuer *onchain.ChallengeIssuer) {
	ticker := time.NewTicker(challengeTickInterval)
	defer ticker.Stop()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if r.throttled() {
			continue
		}
		r.runOneChallengeTick(ctx, issuer, rng)
	}
}

// runOneChallengeTick is factored out for test-friendliness.
func (r *Runtime) runOneChallengeTick(ctx context.Context, issuer *onchain.ChallengeIssuer, rng *rand.Rand) {
	if r.blobRarestFirst == nil {
		return
	}
	cids := r.blobRarestFirst.ActiveCIDs()
	if len(cids) == 0 {
		return
	}
	knownPeers := r.blobRarestFirst.KnownPeerCount()
	if knownPeers < minKnownPeersForStochastic {
		knownPeers = minKnownPeersForStochastic
	}
	prob := float64(challengeTickInterval) / (float64(expectedChallengeInterval) * float64(knownPeers))
	if prob <= 0 {
		return
	}
	if prob > 1 {
		prob = 1
	}
	const maxPerTick = 4
	fired := 0
	for _, cid := range cids {
		if fired >= maxPerTick {
			break
		}
		if rng.Float64() >= prob {
			continue
		}
		peer := r.pickTargetPeer(cid, rng)
		if peer == "" {
			continue
		}
		ctxAttempt, cancel := context.WithTimeout(ctx, proofChallengeResponseTimeout+5*time.Second)
		res, err := issuer.Challenge(ctxAttempt, peer, cid)
		cancel()
		if err != nil {
			log.Printf("mesh: proof_of_storage: challenge error cid=%s peer=%s: %v", cid, peer, err)
			continue
		}
		fired++
		if !res.OK {
			log.Printf("mesh: proof_of_storage: miss cid=%s peer=%s reason=%s", cid, peer, res.Reason)
			r.escalateMissOnChain(ctx, cid, res)
		}
	}
}

// escalateMissOnChain kicks the on-chain bond-slashing flow for a
// miss that fits the slashable categories (timeout / sig_fail /
// nonce_mismatch / wrong_hash). Silently skipped when:
//
//   - no EscrowClient is configured (browser/WASM, or an operator
//     explicitly opted out),
//   - no EscrowResolver is configured OR the resolver has no mapping
//     for this CID (the challenger has not yet observed a pin
//     acceptance naming an escrow address for this content),
//   - the miss reason is not in the slashable set (we don't escalate
//     transport-level failures — the bug is likely local),
//   - the challenger's local piece is unavailable (we must produce
//     keccak256(piece||nonce) and can't fabricate it).
//
// On success, schedules an ExpireChallenge call 24h + buffer later
// via the in-memory expireScheduler. The schedule is best-effort —
// a process restart drops the queue. Any other peer that witnessed
// the same miss can file their own expireChallenge; the contract
// only allows one to succeed, so concurrent claims are wasteful
// but not dangerous.
func (r *Runtime) escalateMissOnChain(ctx context.Context, cid string, res onchain.ChallengeResult) {
	if r.cfg.EscrowClient == nil {
		return
	}
	if r.cfg.EscrowResolver == nil {
		return
	}
	if _, slashable := onChainMissReasons[res.Reason]; !slashable {
		return
	}
	addr, ok := r.cfg.EscrowResolver.EscrowAddressForCID(cid)
	if !ok || addr == "" {
		// No known on-chain escrow for this CID (yet). Not an error —
		// opportunistic replication without paid pinning is the common
		// case.
		return
	}

	// Reconstruct the (piece, nonce) pair so we can compute
	// expectedHash = keccak256(piece||nonce). We have the piece
	// locally (we issued the challenge, which requires local piece
	// access in NewChallengeIssuerWithPieceSource's verification
	// path) and the nonce was shipped as part of ChallengeResult's
	// Issued/Received metadata... except ChallengeResult does NOT
	// carry the nonce back to the caller today (see proof.go —
	// Challenge throws the nonce away after the Whisper ask). To
	// keep this path sound, we re-derive a FRESH pair:
	//
	//   - Generate a new nonce.
	//   - Fetch the piece bytes locally.
	//   - Compute expectedHash.
	//
	// This is semantically equivalent to "re-issue the challenge
	// on-chain" — the Whisper tick just told us the replicator
	// can't (or won't) answer, so we're now asking the SAME
	// replicator to prove custody in front of the contract. The
	// 24h window gives them time to pull the piece back if they
	// evicted it in good faith.
	piece, nonce, expectedHash, ok := r.buildOnChainChallengeInputs(ctx, cid)
	if !ok {
		return
	}
	_ = piece // kept for symmetry with ComputeOnChainExpectedHash signature

	var cidHash [32]byte
	copy(cidHash[:], keccakCIDASCII(cid))

	var nonce32 [32]byte
	copy(nonce32[:], nonce)

	openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	txHash, err := r.cfg.EscrowClient.OpenChallenge(openCtx, cidHash, nonce32, expectedHash)
	cancel()
	if err != nil {
		log.Printf("mesh: proof_of_storage: openChallenge failed cid=%s addr=%s: %v",
			cid, addr, err)
		return
	}
	log.Printf("mesh: proof_of_storage: openChallenge tx=%s cid=%s addr=%s (expire in %s)",
		txHash, cid, addr, onChainChallengeWindow+onChainChallengeBuffer)

	// Schedule the expireChallenge call. In-memory scheduler; see
	// expireScheduler docs for the persistence trade-off.
	r.scheduleExpireChallenge(cid, cidHash)
}

// buildOnChainChallengeInputs assembles the (piece, nonce, expected)
// triple for a fresh on-chain challenge. Returns ok=false when the
// local piece store cannot satisfy the lookup — typically because
// the piece has rolled off our own cache since we issued the
// off-chain challenge.
func (r *Runtime) buildOnChainChallengeInputs(ctx context.Context, cid string) (piece []byte, nonce []byte, expectedHash [32]byte, ok bool) {
	if r.blobStore == nil {
		return nil, nil, [32]byte{}, false
	}
	has, err := r.blobStore.Has(ctx, cid)
	if err != nil || !has {
		return nil, nil, [32]byte{}, false
	}
	p, err := r.blobStore.Get(ctx, cid)
	if err != nil || len(p) == 0 {
		return nil, nil, [32]byte{}, false
	}
	n, err := onchain.NewNonce()
	if err != nil {
		return nil, nil, [32]byte{}, false
	}
	h := onchain.ComputeOnChainExpectedHash(p, n)
	return p, n, h, true
}

// keccakCIDASCII returns keccak256(cid) — the same value the
// contract stores as its immutable cidHash, mirroring the helper in
// plutus/blob.CIDHashForString but redefined here to avoid importing
// the concrete EscrowClient implementation from this shared package
// (mercury/runtime/mesh must remain buildable without plutus/blob
// pulled in as a runtime dep — the user injects the concrete client
// via Config.EscrowClient).
func keccakCIDASCII(cid string) []byte {
	h := newKeccak256()
	h.Write([]byte(cid))
	return h.Sum(nil)
}

// scheduleExpireChallenge posts an ExpireChallenge call 24h+buffer
// after the OpenChallenge fired. Uses time.AfterFunc backed by a
// package-internal map so multiple outstanding expirations don't
// leak goroutines on cancel.
//
// TRADE-OFF: Persistence gap. A restart between openChallenge and
// the scheduled expireChallenge drops the claim locally. Any other
// peer that observed the same miss (via their own proof-of-storage
// loop) can file their own expireChallenge — the contract accepts
// expireChallenge() from "anyone" — so the system is resilient in
// aggregate even though this single node forgoes the 10% bounty.
//
// A future improvement is to persist the scheduled expirations to
// disk (cf. the DistributionStore pattern used by fee distribution
// in witness/persistent_distribution_store.go). Tracked as a TODO.
func (r *Runtime) scheduleExpireChallenge(cid string, cidHash [32]byte) {
	r.expireSchedMu.Lock()
	defer r.expireSchedMu.Unlock()
	if r.expireSched == nil {
		r.expireSched = make(map[string]*time.Timer)
	}
	// Cancel any prior timer for this cid (idempotent under flapping).
	if old, ok := r.expireSched[cid]; ok {
		old.Stop()
	}
	delay := onChainChallengeWindow + onChainChallengeBuffer
	if hook := r.expireScheduleHook; hook != nil {
		// Tests inject a short-circuit clock.
		delay = hook(delay)
	}
	r.expireSched[cid] = time.AfterFunc(delay, func() {
		r.fireExpireChallenge(cid, cidHash)
	})
}

// fireExpireChallenge is the AfterFunc callback. Hydrates a fresh
// context (the tick context may have exited by now) and calls
// ExpireChallenge on the configured client, classifying benign
// reverts (replicator responded in time) as success-with-a-shrug.
func (r *Runtime) fireExpireChallenge(cid string, cidHash [32]byte) {
	client := r.cfg.EscrowClient
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.backgroundCtxOrTODO(), 60*time.Second)
	defer cancel()
	txHash, err := client.ExpireChallenge(ctx, cidHash)
	if err != nil {
		if isBenignRevert(err) {
			log.Printf("mesh: proof_of_storage: expireChallenge no-op cid=%s (replicator responded in time): %v",
				cid, err)
			return
		}
		log.Printf("mesh: proof_of_storage: expireChallenge failed cid=%s: %v", cid, err)
		return
	}
	log.Printf("mesh: proof_of_storage: expireChallenge tx=%s cid=%s", txHash, cid)

	r.expireSchedMu.Lock()
	delete(r.expireSched, cid)
	r.expireSchedMu.Unlock()
}

// backgroundCtxOrTODO returns the runtime's live context if the
// process hasn't stopped; otherwise a fresh background context so
// the scheduled expire call can still reach the chain. (A stopped
// runtime may still want to flush its pending expirations on best-
// effort basis.)
func (r *Runtime) backgroundCtxOrTODO() context.Context {
	r.mu.Lock()
	ctx := r.runCtx
	r.mu.Unlock()
	if ctx != nil && ctx.Err() == nil {
		return ctx
	}
	return context.Background()
}

// isBenignRevert mirrors plutus/blob.IsBenignChallengeExpireRevert
// but without the blob-package dependency — mesh must stay
// chain-adapter-agnostic. Substring match is sufficient because the
// broadcaster wraps the reason in its own error message.
func isBenignRevert(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, needle := range benignRevertSubstrings {
		if containsASCII(msg, needle) {
			return true
		}
	}
	return false
}

var benignRevertSubstrings = []string{
	"ChallengeWindowNotExpired",
	"NoOpenChallenge",
}

func containsASCII(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// expireChallengeState holds the in-memory scheduler for
// expireChallenge timers. Kept in a tiny struct so the Runtime's
// own field block doesn't balloon. See Runtime struct in runtime.go
// for the field declarations.

// newKeccak256 wraps the sha3 primitive for the keccakCIDASCII helper.
// Kept factored so the test file can stub in a deterministic hasher
// if ever needed (currently not required).
type keccak256er interface {
	Write(p []byte) (int, error)
	Sum(b []byte) []byte
}

func newKeccak256() keccak256er {
	return keccak256NewFn()
}

// keccak256NewFn is package-level so tests can override the hash
// construction if needed. Default delegates to sha3.NewLegacyKeccak256
// imported in keccak_newkeccak.go (see below).
var keccak256NewFn = defaultKeccak256New

// pickTargetPeer returns a peerID to challenge for the given cid.
func (r *Runtime) pickTargetPeer(cid string, rng *rand.Rand) string {
	if r.blobRarestFirst == nil {
		return ""
	}
	peers := r.blobRarestFirst.PeersForCID(cid)
	if len(peers) == 0 {
		return ""
	}
	return peers[rng.Intn(len(peers))]
}
