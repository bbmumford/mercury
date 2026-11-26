// Copyright (c) 2026 Brandon Miller-Mumford.
// Licensed under the MIT License; see LICENSE.

package mesh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bbmumford/diststore/abuse"
	"github.com/bbmumford/diststore/advertise"
	"github.com/bbmumford/diststore/cache"
	"github.com/bbmumford/diststore/fetch"
	"github.com/bbmumford/diststore/quota"
	"github.com/bbmumford/diststore/rarest"
	"github.com/bbmumford/diststore/replicate"
	"github.com/bbmumford/diststore/resolve"
	mercury "github.com/bbmumford/mercury"
	pfile "github.com/bbmumford/minerva/persistence/file"
	minerva_sdk "github.com/bbmumford/minerva/sdk"
	plutus "github.com/bbmumford/plutus"
	btc "github.com/bbmumford/plutus/adapter/btc"

	"github.com/bbmumford/minerva/messaging"
)

// Role name constants used in Status.EnabledRoles / InactiveRoles.
const (
	RoleReplicator        = "replicator"
	RoleBlobProvider      = "blob_provider"
	RoleBalanceWitness    = "balance_witness"
	RoleTxWitness         = "tx_witness"
	RoleArbitrator        = "arbitrator"
	RoleMessageRelay      = "message_relay"
	RoleMarketplace       = "marketplace"
	RoleBalanceWitnessBTC = "balance_witness.btc"
	RoleBalanceWitnessEVM = "balance_witness.evm"
	RoleTxWitnessBTC      = "tx_witness.btc"
	RoleTxWitnessEVM      = "tx_witness.evm"
)

// shutdownTimeout caps how long Stop will wait for subsystems to
// quiesce.
const shutdownTimeout = 5 * time.Second

// InactiveReasonEVMNoLightClient is the permanent, protocol-level
// reason EVM witness cannot register today.
const InactiveReasonEVMNoLightClient = "evm-witness: no in-process EVM light client available"

// Runtime is the shared mesh-participation runtime used by every
// runtime that wants to participate in the Mercury / Plutus / Minerva
// mesh as a first-class node.
//
// Construct via NewRuntime, drive with Start(ctx) / Stop() / Status().
// The Runtime is not reusable across Start/Stop cycles without re-
// construction — Stop() clears internal references so a subsequent
// Start would need fresh subsystems.
type Runtime struct {
	cfg Config

	minerva  *minerva_sdk.Node
	plutus   *plutus.PlutusSDK
	mercury  *mercury.MercurySDK
	neutrino *btc.NeutrinoBalanceProvider

	// Role-owned subsystems constructed during Start. Nil before Start
	// completes, cleared on Stop.
	blobStore       *cache.Store
	quotaManager    *quota.Manager
	blobAdvertiser  *advertise.Advertiser
	blobResolver    *resolve.Resolver
	blobFetcher     *fetch.Fetcher
	blobReplicator  *replicate.Replicator
	blobRarestFirst *rarest.State
	blobAbuseGuard  *abuse.Guard
	relay           *messaging.MessageRelay

	status *statusTracker

	mu          sync.Mutex
	started     bool
	cancel      context.CancelFunc
	runCtx      context.Context
	activeRoles []string

	// Proof-of-storage on-chain escalation scheduler. Populated on
	// demand from the challenger loop; see proof_of_storage.go.
	expireSchedMu      sync.Mutex
	expireSched        map[string]*time.Timer
	expireScheduleHook func(time.Duration) time.Duration // test hook
}

// NewRuntime constructs a Runtime. Validates config but does NOT start
// any network I/O — call Start.
func NewRuntime(cfg Config) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	r := &Runtime{
		cfg:    cfg,
		status: newStatusTracker(),
	}

	// Build Minerva first — Plutus wires itself to its Whisper engine.
	node, err := buildMinervaNode(cfg)
	if err != nil {
		return nil, err
	}
	r.minerva = node

	// Plutus SDK construction does not need Minerva to be started yet.
	pSDK, err := buildPlutusSDK(cfg)
	if err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	r.plutus = pSDK

	// Minerva shares Plutus' Whisper engine so the mesh sees a single
	// node.
	r.minerva.SetWhisperEngine(pSDK.Engine)

	if cfg.EnableMarketplace {
		mSDK, err := buildMercurySDK(cfg, plutus.Config{
			Mnemonic: pSDK.Mnemonic,
		})
		if err != nil {
			return nil, fmt.Errorf("mesh: %w", err)
		}
		r.mercury = mSDK
	}

	return r, nil
}

// Start brings up Minerva, Neutrino (if witnessing), then the
// configured role loops. Safe to call again after Stop only if the
// Runtime was re-constructed.
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("mesh: runtime already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.runCtx = runCtx
	r.cancel = cancel
	r.started = true
	r.activeRoles = r.activeRoles[:0]
	r.mu.Unlock()

	// --- Minerva ---------------------------------------------------------
	if err := r.minerva.Start(runCtx); err != nil {
		r.status.recordError(err)
		cancel()
		r.mu.Lock()
		r.started = false
		r.mu.Unlock()
		return fmt.Errorf("mesh: start minerva: %w", err)
	}

	// --- Plutus BTC watchers (no-op without adapters registered) --------
	r.plutus.StartBTCWatchers(runCtx)

	// --- Embedded Neutrino SPV (only for witnessing roles) --------------
	if r.cfg.EnableBalanceWitness || r.cfg.EnableTxWitness {
		if err := r.startNeutrino(runCtx); err != nil {
			r.status.recordError(err)
			// Do not abort Start — witnessing roles simply stay inactive.
		}
	}

	// --- Role loops ------------------------------------------------------
	if err := r.startAll(runCtx); err != nil {
		r.status.markStarted()
		r.status.setEnabledRoles(r.activeRoles)
		return err
	}

	r.status.markStarted()
	r.status.setEnabledRoles(r.activeRoles)
	return nil
}

// Stop halts all role loops, stops Plutus watchers, stops Neutrino, and
// shuts Minerva down. Blocks up to shutdownTimeout.
func (r *Runtime) Stop() error {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	cancel := r.cancel
	r.cancel = nil
	r.started = false
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	done := make(chan error, 1)
	go func() {
		var firstErr error
		if r.mercury != nil {
			r.mercury.Stop()
		}
		if r.relay != nil {
			if err := r.relay.Stop(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("mesh: stop relay: %w", err)
			}
		}
		if r.blobReplicator != nil {
			r.blobReplicator.Stop()
		}
		if r.blobRarestFirst != nil {
			r.blobRarestFirst.StopPruner()
		}
		if r.blobAdvertiser != nil {
			r.blobAdvertiser.Stop()
		}
		if r.plutus != nil {
			r.plutus.StopBTCWatchers()
		}
		if r.quotaManager != nil {
			r.quotaManager.Stop()
		}
		if r.neutrino != nil {
			if err := r.neutrino.Stop(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("mesh: stop neutrino: %w", err)
			}
		}
		if r.minerva != nil {
			if err := r.minerva.Stop(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("mesh: stop minerva: %w", err)
			}
		}
		r.relay = nil
		r.blobStore = nil
		r.quotaManager = nil
		r.blobAbuseGuard = nil
		done <- firstErr
	}()

	select {
	case err := <-done:
		r.status.markStopped()
		return err
	case <-time.After(shutdownTimeout):
		r.status.markStopped()
		return fmt.Errorf("mesh: shutdown exceeded %s", shutdownTimeout)
	}
}

// Status returns a snapshot of the runtime's observable state. Safe to
// call before Start, after Stop, or concurrently with either.
func (r *Runtime) Status() Status {
	snap := r.status.snapshot()
	// Overlay live Neutrino state on every call — the status tracker
	// only retains the last published values, but the chain service is
	// canonical.
	if r.neutrino != nil {
		snap.Neutrino.Synced = r.neutrino.IsCaughtUp()
	}
	return snap
}

// MarkUntrustworthy disables signing for the given chain. Called by
// operators (or by role loops) when they detect a desync / reorg.
// Idempotent.
func (r *Runtime) MarkUntrustworthy(chainID, reason string) {
	r.status.markChainUntrustworthy(chainID, reason)
}

// buildMinervaNode constructs the Minerva node with file-backed
// persistence rooted at <DataDir>/minerva. The caller is responsible
// for Start/Stop.
func buildMinervaNode(cfg Config) (*minerva_sdk.Node, error) {
	minervaDir := filepath.Join(cfg.DataDir, "minerva")
	if err := os.MkdirAll(minervaDir, 0o700); err != nil {
		return nil, fmt.Errorf("mesh: mkdir minerva dir: %w", err)
	}
	fileStore, err := pfile.New(minervaDir)
	if err != nil {
		return nil, fmt.Errorf("mesh: create minerva file store: %w", err)
	}
	opts := []minerva_sdk.NodeOption{
		minerva_sdk.WithAppID(cfg.AppID),
		minerva_sdk.WithPersistence(fileStore),
		minerva_sdk.WithPersistencePath(minervaDir),
	}
	for _, url := range cfg.RelayEndpoints {
		opts = append(opts, minerva_sdk.WithRelay(url))
	}
	return minerva_sdk.NewNode(opts...), nil
}

// buildPlutusSDK wires a Plutus SDK. Minerva wires engine sharing
// separately after construction.
func buildPlutusSDK(cfg Config) (*plutus.PlutusSDK, error) {
	// Copy PRCAddresses into the plutus.Config so the SDK sees the same
	// canonical trust anchors as the runtime. When the runtime's
	// PRCAddresses is nil we leave plutus.Config's field empty — plutus
	// will fall back to its canonical.PRC defaults at wallet verification
	// time. An explicitly non-nil map (including empty) overrides those
	// defaults, which is what testnet / rollback operators rely on.
	pcfg := plutus.Config{
		PRCAddresses: map[string]string{},
	}
	for k, v := range cfg.PRCAddresses {
		pcfg.PRCAddresses[k] = v
	}
	if cfg.EnableBalanceWitness {
		pcfg.WitnessRoles = append(pcfg.WitnessRoles, "balance")
	}
	if cfg.EnableTxWitness {
		pcfg.WitnessRoles = append(pcfg.WitnessRoles, "tx")
	}
	sdk, err := plutus.New(pcfg)
	if err != nil {
		return nil, fmt.Errorf("mesh: create plutus sdk: %w", err)
	}
	return sdk, nil
}

// buildMercurySDK constructs the Mercury SDK. Only called when
// cfg.EnableMarketplace is true.
func buildMercurySDK(cfg Config, plutusCfg plutus.Config) (*mercury.MercurySDK, error) {
	mcfg := mercury.Config{
		AppID:            cfg.AppID,
		PlutusConfig:     plutusCfg,
		StoreName:        cfg.StoreName,
		StoreDescription: cfg.StoreDescription,
	}
	sdk, err := mercury.New(mcfg)
	if err != nil {
		return nil, fmt.Errorf("mesh: create mercury sdk: %w", err)
	}
	return sdk, nil
}

// startNeutrino constructs and starts the embedded BTC light client
// when a witness role requires it. The witness role registers its
// attestation capability immediately but signing is gated per-
// attestation until Neutrino reports caught-up.
func (r *Runtime) startNeutrino(ctx context.Context) error {
	if err := os.MkdirAll(r.cfg.NeutrinoDataDir, 0o700); err != nil {
		return fmt.Errorf("mesh: mkdir neutrino dir: %w", err)
	}
	provider, err := btc.NewNeutrinoBalanceProvider(
		r.cfg.NeutrinoDataDir,
		"bitcoin",
		r.cfg.NeutrinoPeers,
		int32(r.cfg.NeutrinoBirthHeight),
	)
	if err != nil {
		return fmt.Errorf("mesh: init neutrino: %w", err)
	}
	if err := provider.Start(ctx); err != nil {
		return fmt.Errorf("mesh: start neutrino: %w", err)
	}
	r.neutrino = provider
	return nil
}

// startAll invokes the per-role starters in dependency order. Mutates
// r's activeRoles / status in place. Does not roll back on partial
// failure — Stop() handles cleanup of whatever did start.
func (r *Runtime) startAll(ctx context.Context) error {
	var firstErr error
	record := func(err error) {
		if err == nil {
			return
		}
		r.status.recordError(err)
		if firstErr == nil {
			firstErr = err
		}
	}

	if r.cfg.EnableReplication {
		record(r.startReplicator(ctx))
	}
	if r.cfg.EnableBlobProvider {
		record(r.startBlobProvider(ctx))
	}
	if r.cfg.EnableMessageRelay {
		record(r.startMessageRelay(ctx))
	}
	if r.cfg.EnableBalanceWitness {
		record(r.startBalanceWitness(ctx))
	}
	if r.cfg.EnableTxWitness {
		record(r.startTxWitness(ctx))
	}
	if r.cfg.EnableMarketplace {
		record(r.startMarketplace(ctx))
	}

	if firstErr != nil {
		return errors.Join(firstErr,
			fmt.Errorf("mesh: one or more roles failed to start"))
	}
	return nil
}

// throttled reports whether the governor wants optional work paused.
// Nil governor == never throttled.
func (r *Runtime) throttled() bool {
	if r.cfg.Governor == nil {
		return false
	}
	return r.cfg.Governor.Throttled()
}
