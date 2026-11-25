// Copyright (c) 2026 Brandon Miller-Mumford.
// Licensed under the MIT License; see LICENSE.

// Package mesh is the shared mesh-participation runtime consumed by every
// runtime that wants to join the Mercury / Plutus / Minerva mesh as a
// first-class storage replicator + blob provider + message relay +
// witness node:
//
//   - mercury-node (headless CLI)
//   - cmd/mercury (Wails desktop)
//   - ORBTR's agent/internal/modules/mesh_embed (opt-in, build-tag gated)
//
// Each of those callers constructs a Config and a Runtime, calls Start,
// and leaves the runtime attached to the process lifetime until Stop.
//
// The shared package has NO ORBTR dependencies. ORBTR-specific policy (the
// resource governor) is injected via the GovernorHook interface so the
// runtime can pause fee distribution / proof-of-storage during host
// pressure without pulling ORBTR internals into this package.
//
// Design reference: _OTHER/mercury/DESIGN.md §19 "Default behaviors per
// runtime" — Desktop + mercury-node are both "default" for storage +
// blobs + relay + witness; ORBTR is opt-in with stricter quotas. Browser
// WASM is not a target (cannot run witness/replicator per DESIGN §4).
package mesh

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/bbmumford/diststore/onchain"
)

// defaultNeutrinoBirthHeight — pre-Segwit blocks are uninteresting for any
// address format we attest. Operators can override if they need older
// history scanned.
const defaultNeutrinoBirthHeight = 850000

// defaultFeeDistributionWindow matches DESIGN §17 (24h default).
const defaultFeeDistributionWindow = 24 * time.Hour

// Config captures everything the shared mesh runtime needs to decide
// which roles to run and how aggressively to consume the host's spare
// resources. Defaults are conservative: a host's primary mission
// (security, app UI, ...) always takes priority over mesh participation.
//
// The trustless invariant is enforced here and in LoadConfigFromTOML:
// there is no ChainRPCs / ChainNodes field. Witness attestations are
// signed ONLY when the chain source is the embedded Neutrino SPV.
// Operators who want display-only RPC queries configure that through
// Mercury SDK's separate RPC config, which is orthogonal and never used
// for signing.
type Config struct {
	// --- Role flags -------------------------------------------------------

	EnableReplication    bool
	EnableBlobProvider   bool
	EnableMessageRelay   bool
	EnableBalanceWitness bool
	EnableTxWitness      bool
	EnableArbitration    bool
	EnableMarketplace    bool

	// --- Storage quotas ---------------------------------------------------

	// StorageMaxPct is the hard ceiling (% of total disk).
	StorageMaxPct int

	// StorageTargetHeadroomPct is the target free-space headroom we aim
	// to preserve (% of total disk).
	StorageTargetHeadroomPct int

	// StorageMinReserveGB is the minimum free space we always leave on
	// disk.
	StorageMinReserveGB int

	// StorageHardMinGB is the absolute floor — under this we halt
	// replication regardless of any other knob.
	StorageHardMinGB int

	// --- Replication admission (opportunistic fetch) ---------------------

	// MaxReplicationMBPerPeer caps bytes (in MB) any single source peer
	// can contribute to the opportunistic replication cache. The blob
	// replicator consults this via AbuseGuard.Check on every fetch, so a
	// misbehaving peer that tries to shove content into our cache runs
	// out of room before starving the uploader quota.
	//
	// Defaults: mercury-node 1000, Wails desktop 250, ORBTR agent 500.
	// Zero disables the per-peer cap.
	MaxReplicationMBPerPeer int

	// MaxReplicationTotalMB caps the aggregate bytes (in MB) stored via
	// opportunistic replication across ALL source peers. Distinct from
	// the storage-manager disk quota: this caps only the replication
	// path so a flood of replication traffic cannot drain the disk
	// budget reserved for legitimate uploads.
	//
	// Defaults: mercury-node 10000, Wails desktop 2000, ORBTR agent 5000.
	// Zero disables the aggregate cap.
	MaxReplicationTotalMB int

	// ReplicationAllowedRecordTypes restricts which record types the
	// replicator's admission gate will accept. Empty slice falls back to
	// distributed.DefaultReplicationAllowedRecordTypes.
	ReplicationAllowedRecordTypes []string

	// --- Network ----------------------------------------------------------

	// NetworkBudgetMbps caps outbound replication bandwidth.
	NetworkBudgetMbps int

	// --- Data -------------------------------------------------------------

	// AppID is the logical app ID passed to Minerva / Plutus when
	// constructing the SDK. Callers SHOULD set a descriptive ID so
	// observability labels distinguish the three runtimes.
	AppID string

	// StoreName / StoreDescription only matter when EnableMarketplace is
	// true — they flow through to the Mercury SDK.
	StoreName        string
	StoreDescription string

	// DataDir is the per-runtime working directory. Must be set by the
	// caller (absolute or derived from the host's app data dir).
	DataDir string

	// NeutrinoDataDir overrides where the embedded SPV stores headers +
	// compact filters. Defaults to <DataDir>/neutrino.
	NeutrinoDataDir string

	// NeutrinoPeers is an optional bootstrap peer list. Empty means use
	// the Bitcoin DNS seeds baked into btcd's chaincfg.
	NeutrinoPeers []string

	// NeutrinoBirthHeight is the earliest block the balance scanner
	// considers. Default: defaultNeutrinoBirthHeight (~850000 mainnet).
	NeutrinoBirthHeight int

	// RelayEndpoints are bootstrap WebSocket relay URLs Minerva should try
	// before PEX/mDNS discovery.
	RelayEndpoints []string

	// --- Fee distribution / maintainer wallets ----------------------------

	// MaintainerWallets maps chainID → per-chain maintainer wallet
	// configuration. Each entry supplies the on-chain address used both
	// as the maintainer fee-settlement payout target and as the witness
	// payout-wallet that signs fee-distribution batch transactions.
	//
	// An empty map disables fee distribution entirely. An entry missing a
	// KeyPath is skipped at startup with a warning (the wallet is treated
	// as settlement-only on that chain).
	MaintainerWallets map[string]MaintainerWallet

	// FeeDistributionWindow is the cadence at which fee distribution
	// runs. Zero defaults to 24h (DESIGN §17). Operators should only
	// override for test/devnet deployments.
	FeeDistributionWindow time.Duration

	// FeeDistributionUseMulticall3 enables EVM Multicall3 batching when
	// available; falls back to individual transfers otherwise.
	FeeDistributionUseMulticall3 bool

	// PRCAddresses maps chainID → on-chain Protocol Registry Contract
	// address. When populated, EVM fee-distribution payouts target the
	// PRC's `distributeWindow` entry point (DESIGN §17 first-writer-wins
	// + anti-omission). Empty map falls back to Multicall3; missing
	// individual entries fall back per-chain.
	//
	// Production deployments typically leave this nil and let the runtime
	// default it from plutus/canonical.PRC (the compiled-in canonical
	// CREATE2 deployments). Callers that want to pin a different PRC
	// (testnet, custom deployment, rollback scenario) set it explicitly.
	PRCAddresses map[string]string

	// Governor is an optional resource-pressure hook. When set, fee
	// distribution and proof-of-storage ticks skip when Throttled()
	// returns true. Leave nil to disable throttling (default behaviour
	// for mercury-node / Wails — they're dedicated processes).
	Governor GovernorHook

	// EscrowClient is the on-chain driver used by the proof-of-storage
	// loop to submit miss proofs to PlutusStorageEscrow. When non-nil,
	// the challenger loop automatically calls OpenChallenge on any
	// CID+peer combination that times out / signature-fails / responds
	// with a nonce mismatch, then schedules an ExpireChallenge 24h
	// later so the contract can record the miss and (on 2nd miss in a
	// week) slash the replicator's bond.
	//
	// When nil, the challenger loop still records misses to the local
	// Scoreboard but does NOT attempt any on-chain activity. This is
	// the expected configuration for callers that have not yet wired
	// a chain adapter (e.g. browser WASM nodes) — it is NOT an error.
	//
	// The concrete implementation is *plutus/blob.StorageEscrowClient;
	// tests may pass any onchain.EscrowClient.
	EscrowClient onchain.EscrowClient

	// EscrowResolver resolves a CID string to the PlutusStorageEscrow
	// contract address for that CID. The proof-of-storage loop queries
	// this when it needs to know WHICH contract to submit an
	// openChallenge() call against for a given miss.
	//
	// Optional. When nil OR when the resolver returns ("", false) for
	// a given CID, on-chain auto-submit is skipped for that CID (the
	// challenger still records the miss locally). The canonical
	// implementation is *plutus/blob.MemoryRegistry populated from the
	// mesh-observed pin acceptances.
	EscrowResolver EscrowCIDResolver
}

// EscrowCIDResolver resolves a CID string to the on-chain escrow
// contract address. Kept as a separate small interface (instead of
// hanging off EscrowClient) so test runtimes can supply an explicit
// cid→addr table without implementing the full EscrowClient surface.
type EscrowCIDResolver interface {
	// EscrowAddressForCID returns the deployed PlutusStorageEscrow
	// address for the given CID string (the same string used as an
	// onchain.PieceSource key). Second return is false when the
	// caller has not yet recorded a mapping for this CID.
	EscrowAddressForCID(cid string) (string, bool)
}

// MaintainerWallet is a per-chain wallet entry: where the encrypted
// keyfile lives + where the passphrase comes from + what address to use
// for payouts.
//
// A single wallet serves two roles per chain:
//
//  1. Maintainer 1% settlement payouts (received here at settlement time).
//  2. Witness payout-wallet for fee-distribution batch tx signing.
type MaintainerWallet struct {
	// Address is the on-chain address for both roles above. Format is
	// chain-specific: 0x… for EVM, bech32 for BTC.
	Address string `toml:"address"`

	// KeyPath is the filesystem path to the encrypted wallet key. Empty
	// means no signing key is available for this chain — the address
	// still receives maintainer settlements, but witness fee distribution
	// is skipped.
	KeyPath string `toml:"key_path"`

	// PassphraseEnv names an environment variable holding the passphrase.
	// Empty means the keyfile is unencrypted (passphrase = empty bytes).
	PassphraseEnv string `toml:"passphrase_env"`
}

// GovernorHook lets callers inject a resource-pressure signal. The
// runtime consults Throttled() before each fee-distribution round and
// each proof-of-storage probe tick; when true, those activities skip
// until the hook reports false.
//
// Implementations MUST be safe for concurrent use. A nil GovernorHook
// on the Config is equivalent to "never throttled" — the common case
// for a dedicated headless node.
type GovernorHook interface {
	// Throttled returns true when the runtime should pause
	// optional / opportunistic work because the host is under pressure.
	Throttled() bool
}

// MercuryNodeDefaults returns the DESIGN §19 defaults for a dedicated
// headless mercury-node process (most permissive storage quota because
// operators running mercury-node are opting into mesh participation as
// the primary workload).
func MercuryNodeDefaults() Config {
	return Config{
		EnableReplication:             true,
		EnableBlobProvider:            true,
		EnableMessageRelay:            true,
		EnableBalanceWitness:          true,
		EnableTxWitness:               true,
		EnableArbitration:             false,
		EnableMarketplace:             false,
		StorageMaxPct:                 70,
		StorageTargetHeadroomPct:      50,
		StorageMinReserveGB:           5,
		StorageHardMinGB:              2,
		MaxReplicationMBPerPeer:       1000,
		MaxReplicationTotalMB:         10000,
		ReplicationAllowedRecordTypes: defaultAllowedRecordTypes(),
		NetworkBudgetMbps:             50,
		NeutrinoBirthHeight:           defaultNeutrinoBirthHeight,
		AppID:                         "mercury-node",
	}
}

// WailsDesktopDefaults returns the DESIGN §19 defaults for Mercury
// Desktop (the Wails app). Quotas are tighter because the user's
// primary workload is the desktop UI itself — mesh participation runs
// in the background.
//
// Replication caps are deliberately the tightest of the three runtimes
// (250 MB per peer, 2000 MB total) because Mercury Desktop runs on
// user-owned endpoints with constrained disk. DESIGN §19 explicitly
// calls for Desktop to carry a lower replication ceiling than
// mercury-node / the ORBTR agent.
func WailsDesktopDefaults() Config {
	return Config{
		EnableReplication:             true,
		EnableBlobProvider:            true,
		EnableMessageRelay:            true,
		EnableBalanceWitness:          true,
		EnableTxWitness:               true,
		EnableArbitration:             false,
		EnableMarketplace:             false,
		StorageMaxPct:                 50,
		StorageTargetHeadroomPct:      25,
		StorageMinReserveGB:           10,
		StorageHardMinGB:              2,
		MaxReplicationMBPerPeer:       250,
		MaxReplicationTotalMB:         2000,
		ReplicationAllowedRecordTypes: defaultAllowedRecordTypes(),
		NetworkBudgetMbps:             10,
		NeutrinoBirthHeight:           defaultNeutrinoBirthHeight,
		AppID:                         "mercury-desktop",
	}
}

// ORBTRAgentDefaults returns the DESIGN §19 defaults for the ORBTR
// agent's mesh_embed module. ORBTR's primary device-security mission
// always wins conflicts → stricter quotas than mercury-node, looser
// than Desktop because ORBTR agents run on servers more often than
// laptops.
func ORBTRAgentDefaults() Config {
	return Config{
		EnableReplication:             true,
		EnableBlobProvider:            true,
		EnableMessageRelay:            true,
		EnableBalanceWitness:          true,
		EnableTxWitness:               true,
		EnableArbitration:             false,
		EnableMarketplace:             false,
		StorageMaxPct:                 60,
		StorageTargetHeadroomPct:      40,
		StorageMinReserveGB:           10,
		StorageHardMinGB:              2,
		MaxReplicationMBPerPeer:       500,
		MaxReplicationTotalMB:         5000,
		ReplicationAllowedRecordTypes: defaultAllowedRecordTypes(),
		NetworkBudgetMbps:             10,
		NeutrinoBirthHeight:           defaultNeutrinoBirthHeight,
		AppID:                         "orbtr-mesh-embed",
	}
}

// defaultAllowedRecordTypes returns the documented allow-list of record
// types the opportunistic replication admission gate accepts. Exported
// helper so every default constructor picks up the same list.
func defaultAllowedRecordTypes() []string {
	return []string{"replication", "listing", "attachment", "dispute"}
}

// ApplyDefaults fills in zero-valued fields on an already-loaded config.
// Distinct from the default constructors so that operators who
// explicitly set (say) StorageMaxPct=0 in TOML get the documented
// default instead of an effectively disabled replicator.
//
// baseDir is used to derive DataDir + NeutrinoDataDir when those are
// left unset; callers typically pass their app's data root.
func (c *Config) ApplyDefaults(baseDir string) {
	def := ORBTRAgentDefaults()
	if c.StorageMaxPct == 0 {
		c.StorageMaxPct = def.StorageMaxPct
	}
	if c.StorageTargetHeadroomPct == 0 {
		c.StorageTargetHeadroomPct = def.StorageTargetHeadroomPct
	}
	if c.StorageMinReserveGB == 0 {
		c.StorageMinReserveGB = def.StorageMinReserveGB
	}
	if c.StorageHardMinGB == 0 {
		c.StorageHardMinGB = def.StorageHardMinGB
	}
	if c.NetworkBudgetMbps == 0 {
		c.NetworkBudgetMbps = def.NetworkBudgetMbps
	}
	if c.MaxReplicationMBPerPeer == 0 {
		c.MaxReplicationMBPerPeer = def.MaxReplicationMBPerPeer
	}
	if c.MaxReplicationTotalMB == 0 {
		c.MaxReplicationTotalMB = def.MaxReplicationTotalMB
	}
	if len(c.ReplicationAllowedRecordTypes) == 0 {
		c.ReplicationAllowedRecordTypes = def.ReplicationAllowedRecordTypes
	}
	if c.NeutrinoBirthHeight == 0 {
		c.NeutrinoBirthHeight = def.NeutrinoBirthHeight
	}
	if c.AppID == "" {
		c.AppID = def.AppID
	}
	if c.DataDir == "" && baseDir != "" {
		c.DataDir = filepath.Join(baseDir, "mesh")
	}
	if c.NeutrinoDataDir == "" && c.DataDir != "" {
		c.NeutrinoDataDir = filepath.Join(c.DataDir, "neutrino")
	}
}

// Validate returns an error describing any field that is
// self-inconsistent or incompatible with an enabled role.
func (c Config) Validate() error {
	if c.StorageMaxPct < 0 || c.StorageMaxPct > 100 {
		return fmt.Errorf("mesh: StorageMaxPct out of range: %d", c.StorageMaxPct)
	}
	if c.StorageTargetHeadroomPct < 0 || c.StorageTargetHeadroomPct > 100 {
		return fmt.Errorf("mesh: StorageTargetHeadroomPct out of range: %d", c.StorageTargetHeadroomPct)
	}
	if c.StorageMinReserveGB < 0 {
		return errors.New("mesh: StorageMinReserveGB must be non-negative")
	}
	if c.StorageHardMinGB < 0 {
		return errors.New("mesh: StorageHardMinGB must be non-negative")
	}
	if c.StorageHardMinGB > c.StorageMinReserveGB {
		return fmt.Errorf("mesh: StorageHardMinGB (%d) must be <= StorageMinReserveGB (%d)",
			c.StorageHardMinGB, c.StorageMinReserveGB)
	}
	if c.NetworkBudgetMbps < 0 {
		return errors.New("mesh: NetworkBudgetMbps must be non-negative")
	}
	if c.MaxReplicationMBPerPeer < 0 {
		return errors.New("mesh: MaxReplicationMBPerPeer must be non-negative")
	}
	if c.MaxReplicationTotalMB < 0 {
		return errors.New("mesh: MaxReplicationTotalMB must be non-negative")
	}
	if c.NeutrinoBirthHeight < 0 {
		return errors.New("mesh: NeutrinoBirthHeight must be non-negative")
	}
	if c.DataDir == "" {
		return errors.New("mesh: DataDir is required (set explicitly or call ApplyDefaults with baseDir)")
	}
	return nil
}

// windowOrDefault returns the configured fee-distribution window or the
// default when unset / non-positive.
func windowOrDefault(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return defaultFeeDistributionWindow
}
