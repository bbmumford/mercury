/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	minerva_sdk "github.com/bbmumford/minerva/sdk"
	mercury "github.com/bbmumford/mercury"
	"github.com/bbmumford/mercury/arbitration"
	"github.com/bbmumford/mercury/discovery"
	meshruntime "github.com/bbmumford/mercury/runtime/mesh"
	"github.com/bbmumford/mercury/store"
	plutus "github.com/bbmumford/plutus"
	plutusbtc "github.com/bbmumford/plutus/adapter/btc"
	plutusevm "github.com/bbmumford/plutus/adapter/evm"
	"github.com/bbmumford/plutus/observability/exporters"
	plutuswallet "github.com/bbmumford/plutus/wallet"
	plutuswitness "github.com/bbmumford/plutus/witness"
)

// Usage:
//
//	mercury-node --roles=replicator,arbitrator --port=41641 --data-dir=./data
//	mercury-node --roles=replicator --stores=seller1pubkey,seller2pubkey
//	mercury-node --roles=arbitrator --arbitrator-key=./arb.key
//	mercury-node --headless  (all roles enabled)

func main() {
	roles := flag.String("roles", "replicator", "comma-separated: replicator,arbitrator,seller")
	port := flag.Int("port", 41641, "listen port")
	dataDir := flag.String("data-dir", "./mercury-data", "data directory")
	stores := flag.String("stores", "", "comma-separated store owner pubkeys (hex) to replicate")
	headless := flag.Bool("headless", false, "enable all roles")
	arbSpecs := flag.String("arbitrator-specs", "physical-goods", "comma-separated arbitrator specializations")
	arbLangs := flag.String("arbitrator-langs", "en", "comma-separated arbitrator languages")
	storeName := flag.String("store-name", "Mercury Store", "display name for seller store")
	storeDesc := flag.String("store-desc", "", "store description")
	categories := flag.String("categories", "", "comma-separated marketplace categories to subscribe")
	capacityMB := flag.Uint64("capacity-mb", 1024, "storage capacity in MB for replicator role")
	announce := flag.String("announce", "", "comma-separated custom announce addresses (e.g., wss://my-store.com:443)")
	dnsRecords := flag.String("dns-txt", "", "comma-separated DNS TXT records to publish")
	announceInterval := flag.Duration("announce-interval", 5*time.Minute, "how often to re-announce")
	relayBootstrap := flag.String("relay-bootstrap", "", "comma-separated relay node addresses for bootstrap (e.g., wss://relay1.mercury.net:443)")

	// balance-witness flags: RPC URLs used when --roles includes balance-witness.
	// Each URL is a direct HTTPS JSON-RPC endpoint for its chain. Esplora URL
	// points at a Blockstream-compatible BTC indexer.
	rpcEthereum := flag.String("rpc-ethereum", "", "Ethereum JSON-RPC URL (balance-witness)")
	rpcPolygon := flag.String("rpc-polygon", "", "Polygon JSON-RPC URL (balance-witness)")
	rpcArbitrum := flag.String("rpc-arbitrum", "", "Arbitrum JSON-RPC URL (balance-witness)")
	rpcBSC := flag.String("rpc-bsc", "", "BSC JSON-RPC URL (balance-witness)")
	esploraURL := flag.String("esplora-url", "", "BTC Esplora API URL (default https://blockstream.info/api)")
	// evmBalanceMode: "single" uses the per-chain rpc-* URLs (legacy; default
	// for back-compat). "multi" ignores the single URLs and instead runs every
	// chain through a MultiRPCBalanceProvider over the stock public-RPC list,
	// requiring --evm-balance-quorum endpoints to agree before any balance is
	// returned. See _OTHER/mercury/portal-network-roadmap.md for the planned
	// migration to a fully trustless Portal Network backend.
	evmBalanceMode := flag.String("evm-balance-mode", "single", "EVM balance provider mode: single (per-chain RPC URL) or multi (public-RPC quorum)")
	evmBalanceQuorum := flag.Int("evm-balance-quorum", 3, "number of RPC endpoints that must agree when --evm-balance-mode=multi")
	evmBalanceTimeout := flag.Duration("evm-balance-timeout", 8*time.Second, "wall-clock timeout per multi-RPC query")

	// Witness fee distribution: when enabled, mercury-node periodically sweeps
	// the maintainer pool balance and pays out individual witnesses based on
	// recorded attestation earnings. The signer key is a SEPARATE hot wallet
	// from the Offline Root / Platform Key — compromise is bounded to one
	// window's worth of fees. See plutus/witness/fee_distributor.go for the
	// full architectural note.
	distributeFees := flag.Bool("distribute-witness-fees", false, "enable periodic witness-fee distribution round")
	feeDistWindow := flag.Duration("fee-distribution-window", 24*time.Hour, "window covered by each distribution round (default 24h)")
	feeDistUseMulticall3 := flag.Bool("fee-distribution-use-multicall3", true, "use EVM Multicall3 batching when available (falls back to individual transfers)")
	feeDistMinPayout := flag.String("fee-distribution-min-payout", "0", "decimal dust threshold — below this, carry forward to next round")
	// Maintainer wallet config. Repeatable flag; each value:
	//   --maintainer-wallet='bitcoin|bc1qabc…|/var/mercury/btc.key|BTC_PASS'
	//   --maintainer-wallet='ethereum|0xabc…|C:\Users\op\eth.key|ETH_PASS'
	// Format: chainID|address|keypath|passenv   (passenv may be empty)
	//
	// The delimiter is deliberately '|' rather than ':' so Windows keyfile
	// paths with a drive letter (C:\path\...) don't get shredded by a naive
	// SplitN on ':'. Operators upgrading from the old ':' form should switch
	// to '|' — see OPEN-DECISIONS for the rollover note.
	maintainerWalletFlags := newMaintainerWalletFlag()
	flag.Var(maintainerWalletFlags, "maintainer-wallet", "per-chain maintainer wallet spec: chainID|address|keypath|passenv (repeatable)")

	// Shared mesh runtime role flags. When --mesh is enabled (default),
	// mercury-node brings up the shared _PACKAGES/mercury/runtime/mesh
	// Runtime alongside the existing role-specific loops. Role defaults
	// per DESIGN §19 (mercury-node row): everything enabled.
	meshEnabled := flag.Bool("mesh", true, "enable the shared mesh runtime (storage replicator + blob provider + relay + witness)")
	meshEnableReplicator := flag.Bool("enable-replicator", true, "mesh runtime: enable storage replicator role")
	meshEnableBlobProvider := flag.Bool("enable-blob-provider", true, "mesh runtime: enable distributed blob provider role")
	meshEnableRelay := flag.Bool("enable-relay", true, "mesh runtime: enable message relay role (store-and-forward)")
	meshEnableWitness := flag.Bool("enable-witness", true, "mesh runtime: enable BTC balance + tx witness roles (embedded Neutrino SPV)")
	meshStorageMaxPct := flag.Int("storage-max-pct", 70, "mesh runtime: hard disk-usage ceiling (%, DESIGN §19 default=70 for mercury-node)")
	meshStorageTargetHeadroom := flag.Int("storage-target-headroom-pct", 50, "mesh runtime: target free-space headroom (%)")
	meshStorageMinReserveGB := flag.Int("storage-min-reserve-gb", 5, "mesh runtime: minimum reserved free disk (GB)")
	meshStorageHardMinGB := flag.Int("storage-hard-min-gb", 2, "mesh runtime: absolute free-disk floor below which replication halts (GB)")
	meshFeeDistributionWindow := flag.Duration("mesh-fee-distribution-window", 24*time.Hour, "mesh runtime: fee-distribution window (DESIGN §17 default 24h)")

	// Neutrino (embedded BIP157/158 SPV) BTC witness options. When
	// btc-mode=neutrino the witness serves BTC balance queries from its own
	// P2P-synced light client — no Esplora, no bitcoind sidecar. See
	// _OTHER/mercury/neutrino-bitcoin-guide.md for trade-offs.
	btcMode := flag.String("btc-mode", "esplora", "BTC balance backend: esplora|neutrino (core reserved for future)")
	neutrinoDataDir := flag.String("neutrino-data-dir", "", "neutrino SPV data dir (default <data-dir>/neutrino)")
	neutrinoPeers := flag.String("btc-peers", "", "comma-separated BTC P2P peers (host:port); empty → DNS seeds")
	neutrinoNet := flag.String("btc-network", "bitcoin", "BTC network: bitcoin|testnet|regtest|signet")
	btcBirthHeight := flag.Int("btc-birth-height", 850000, "earliest block to scan for maintainer-sponsored addresses (mid-2024 ~= 850000)")

	// OpenTelemetry observability flags. When --otel-endpoint is empty and
	// --metrics-listen is empty, the process falls back to stdout JSON span
	// output (zero-config dev mode).
	otelEndpoint := flag.String("otel-endpoint", "", "OTLP gRPC endpoint for traces (e.g. localhost:4317); empty = stdout")
	otelServiceName := flag.String("otel-service-name", "mercury-node", "service.name resource attribute reported with telemetry")
	metricsListen := flag.String("metrics-listen", "", "listen address for Prometheus /metrics endpoint (e.g. :9090); empty = disabled")
	flag.Parse()

	// Install OpenTelemetry providers before any Plutus/Mercury calls so
	// spans and metrics are captured from startup onwards.
	obsCtx, obsCancel := context.WithTimeout(context.Background(), 10*time.Second)
	installed, err := exporters.Install(obsCtx, exporters.Options{
		OTLPEndpoint:   *otelEndpoint,
		ServiceName:    *otelServiceName,
		MetricsEnabled: *metricsListen != "",
		StdoutFallback: *otelEndpoint == "" && *metricsListen == "",
	})
	obsCancel()
	if err != nil {
		log.Fatalf("mercury-node: install telemetry: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := installed.Shutdown(shutdownCtx); err != nil {
			log.Printf("mercury-node: telemetry shutdown: %v", err)
		}
	}()

	// Spin up the Prometheus scrape endpoint if the operator requested one.
	if *metricsListen != "" && installed.MetricsHandler != nil {
		mux := http.NewServeMux()
		mux.Handle("/metrics", installed.MetricsHandler)
		go func() {
			log.Printf("mercury-node: prometheus /metrics listening on %s", *metricsListen)
			srv := &http.Server{
				Addr:              *metricsListen,
				Handler:           mux,
				ReadHeaderTimeout: 5 * time.Second,
			}
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("mercury-node: metrics listener stopped: %v", err)
			}
		}()
	}

	// Determine active roles.
	activeRoles := parseRoles(*roles, *headless)
	log.Printf("mercury-node: starting with roles=%v port=%d data-dir=%s",
		activeRoles, *port, *dataDir)

	// Ensure data directory exists.
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("mercury-node: create data dir: %v", err)
	}

	// Parse categories.
	var cats []string
	if *categories != "" {
		cats = splitTrimmed(*categories)
	}

	// Build announce config if addresses or DNS records are provided.
	var annCfg *discovery.AnnounceConfig
	if *announce != "" || *dnsRecords != "" {
		annCfg = &discovery.AnnounceConfig{
			Interval: *announceInterval,
		}
		if *announce != "" {
			annCfg.Addresses = splitTrimmed(*announce)
		}
		if *dnsRecords != "" {
			annCfg.DNSRecords = splitTrimmed(*dnsRecords)
		}
	}

	// Parse relay bootstrap addresses for initial peer discovery.
	var relayAddrs []string
	if *relayBootstrap != "" {
		relayAddrs = splitTrimmed(*relayBootstrap)
		log.Printf("mercury-node: relay bootstrap addresses=%v", relayAddrs)
	}

	// Build SDK config.
	cfg := mercury.Config{
		AppID:            "mercury",
		StoreName:        *storeName,
		StoreDescription: *storeDesc,
		Categories:       cats,
		AnnounceConfig:   annCfg,
		MinervaConfig: minerva_sdk.NodeConfig{
			AppID:           "mercury",
			ListenAddress:   fmt.Sprintf(":%d", *port),
			PersistencePath: *dataDir,
		},
		PlutusConfig: plutus.Config{},
	}

	// Initialize MercurySDK.
	sdk, err := mercury.New(cfg)
	if err != nil {
		log.Fatalf("mercury-node: init SDK: %v", err)
	}

	// Start the Minerva P2P node.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sdk.Node.Start(ctx); err != nil {
		log.Fatalf("mercury-node: start minerva node: %v", err)
	}
	log.Printf("mercury-node: minerva node started, store=%s", sdk.StoreID())

	// Start the announce resolver (always — receives peer announcements).
	if err := sdk.AnnounceResolver.Start(ctx); err != nil {
		log.Printf("mercury-node: start announce resolver: %v", err)
	}

	// If relay bootstrap addresses are provided, create and start an
	// announcer that broadcasts this node's address to the relay peers.
	if len(relayAddrs) > 0 && sdk.Announcer == nil {
		bootstrapCfg := discovery.AnnounceConfig{
			Addresses:  relayAddrs,
			Interval:   *announceInterval,
			NodePubKey: sdk.PublicKey(),
		}
		sdk.Announcer = discovery.NewAnnouncer(bootstrapCfg, sdk.Engine, sdk.Commerce.IdentityKey)
		log.Printf("mercury-node: created announcer from --relay-bootstrap, addresses=%v", relayAddrs)
	}

	// Start the announcer if custom addresses are configured.
	if sdk.Announcer != nil {
		go func() {
			if err := sdk.Announcer.Start(ctx); err != nil && ctx.Err() == nil {
				log.Printf("mercury-node: announcer stopped: %v", err)
			}
		}()
		if annCfg != nil {
			log.Printf("mercury-node: announcer started, addresses=%v", annCfg.Addresses)
		} else {
			log.Printf("mercury-node: announcer started via relay bootstrap")
		}
	}

	// Start enabled roles.
	if activeRoles["replicator"] {
		startReplicator(sdk, *stores, *capacityMB)
	}
	if activeRoles["arbitrator"] {
		startArbitrator(sdk, *arbSpecs, *arbLangs)
	}
	if activeRoles["seller"] {
		startSeller(sdk)
	}
	if activeRoles["balance-witness"] {
		rpcURLs := map[string]string{
			"ethereum": *rpcEthereum,
			"polygon":  *rpcPolygon,
			"arbitrum": *rpcArbitrum,
			"bsc":      *rpcBSC,
		}
		btcOpts := btcBalanceOptions{
			Mode:        *btcMode,
			EsploraURL:  *esploraURL,
			DataDir:     *neutrinoDataDir,
			Network:     *neutrinoNet,
			Peers:       splitTrimmed(*neutrinoPeers),
			BirthHeight: int32(*btcBirthHeight),
			DefaultDir:  *dataDir,
		}
		startBalanceWitness(ctx, sdk, rpcURLs, btcOpts, *evmBalanceMode, *evmBalanceQuorum, *evmBalanceTimeout)
	}

	if *distributeFees {
		wallets, err := loadMaintainerWallets(maintainerWalletFlags.specs)
		if err != nil {
			log.Fatalf("mercury-node: load maintainer wallets: %v", err)
		}
		startFeeDistributor(ctx, sdk, feeDistributorOptions{
			Window:            *feeDistWindow,
			UseMulticall3:     *feeDistUseMulticall3,
			MinPayoutDec:      *feeDistMinPayout,
			MaintainerWallets: wallets,
		})
	}

	// ------------------------------------------------------------------
	// Shared mesh runtime (DESIGN §19). Brings up storage replicator +
	// blob provider + message relay + witness with DESIGN-§19 mercury-
	// node defaults. Runs alongside the legacy role plumbing above —
	// both use the same MercurySDK instance so there's no duplicate
	// Minerva node.
	// ------------------------------------------------------------------
	var meshRuntime *meshruntime.Runtime
	if *meshEnabled {
		meshWallets, err := loadMaintainerWallets(maintainerWalletFlags.specs)
		if err != nil {
			log.Fatalf("mercury-node: [mesh] load maintainer wallets: %v", err)
		}
		meshMaintainers := make(map[string]meshruntime.MaintainerWallet, len(meshWallets))
		for chainID, w := range meshWallets {
			meshMaintainers[chainID] = meshruntime.MaintainerWallet{
				Address:       w.Address,
				KeyPath:       w.KeyPath,
				PassphraseEnv: w.PassphraseEnv,
			}
		}
		meshCfg := meshruntime.Config{
			EnableReplication:            *meshEnableReplicator,
			EnableBlobProvider:           *meshEnableBlobProvider,
			EnableMessageRelay:           *meshEnableRelay,
			EnableBalanceWitness:         *meshEnableWitness,
			EnableTxWitness:              *meshEnableWitness,
			StorageMaxPct:                *meshStorageMaxPct,
			StorageTargetHeadroomPct:     *meshStorageTargetHeadroom,
			StorageMinReserveGB:          *meshStorageMinReserveGB,
			StorageHardMinGB:             *meshStorageHardMinGB,
			NetworkBudgetMbps:            50,
			AppID:                        "mercury-node",
			DataDir:                      filepath.Join(*dataDir, "mesh"),
			NeutrinoBirthHeight:          *btcBirthHeight,
			RelayEndpoints:               relayAddrs,
			MaintainerWallets:            meshMaintainers,
			FeeDistributionWindow:        *meshFeeDistributionWindow,
			FeeDistributionUseMulticall3: *feeDistUseMulticall3,
		}
		rt, err := meshruntime.NewRuntime(meshCfg)
		if err != nil {
			log.Fatalf("mercury-node: [mesh] construct runtime: %v", err)
		}
		if err := rt.Start(ctx); err != nil {
			log.Fatalf("mercury-node: [mesh] start runtime: %v", err)
		}
		meshRuntime = rt
		log.Printf("mercury-node: [mesh] started, roles=%v", rt.Status().EnabledRoles)
	}

	// Status ticker: log status every 60 seconds.
	statusTicker := time.NewTicker(60 * time.Second)
	defer statusTicker.Stop()

	go func() {
		for {
			select {
			case <-statusTicker.C:
				logStatus(sdk, activeRoles)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Block until SIGINT or SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("mercury-node: received %s, shutting down...", sig)

	// Graceful shutdown.
	cancel()

	// Stop the shared mesh runtime first so it tears down its role loops
	// before the underlying SDK's Minerva node stops.
	if meshRuntime != nil {
		if err := meshRuntime.Stop(); err != nil {
			log.Printf("mercury-node: [mesh] stop runtime: %v", err)
		}
	}

	// Stop announcer and resolver.
	if sdk.Announcer != nil {
		sdk.Announcer.Stop()
	}
	sdk.AnnounceResolver.Stop()

	if err := sdk.Node.Stop(); err != nil {
		log.Printf("mercury-node: stop minerva node: %v", err)
	}

	log.Printf("mercury-node: shutdown complete")
}

// parseRoles parses the --roles flag into a set of active role names.
// If headless is true, all roles are enabled regardless of the flag value.
func parseRoles(rolesStr string, headless bool) map[string]bool {
	if headless {
		return map[string]bool{
			"replicator": true,
			"arbitrator": true,
			"seller":     true,
		}
	}
	active := make(map[string]bool)
	for _, r := range splitTrimmed(rolesStr) {
		switch r {
		case "replicator", "arbitrator", "seller", "balance-witness":
			active[r] = true
		default:
			log.Printf("mercury-node: unknown role %q (valid: replicator, arbitrator, seller, balance-witness)", r)
		}
	}
	if len(active) == 0 {
		log.Fatal("mercury-node: no valid roles specified")
	}
	return active
}

// splitTrimmed splits a comma-separated string and trims whitespace from each element.
func splitTrimmed(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// startReplicator initializes the replicator role: sets capacity and pins
// any stores specified via --stores.
func startReplicator(sdk *mercury.MercurySDK, storesFlag string, capacityMB uint64) {
	log.Printf("mercury-node: [replicator] starting, capacity=%dMB", capacityMB)

	sdk.Replication.SetCapacity(capacityMB)

	if storesFlag == "" {
		log.Printf("mercury-node: [replicator] no --stores specified, will accept ad-hoc pin requests")
		return
	}

	pubkeys := splitTrimmed(storesFlag)
	log.Printf("mercury-node: [replicator] pinning %d stores", len(pubkeys))

	for _, pkHex := range pubkeys {
		pkBytes, err := hex.DecodeString(pkHex)
		if err != nil {
			log.Printf("mercury-node: [replicator] invalid pubkey hex %q: %v", pkHex, err)
			continue
		}

		// Create a minimal StoreRoot for the initial pin. The replication manager
		// will sync the full root from the network once connected.
		storeRoot := store.StoreRoot{
			OwnerPubKey: pkBytes,
			Version:     0,
			Timestamp:   time.Now(),
		}

		ctx := context.Background()
		if err := sdk.Replication.Replicate(ctx, storeRoot); err != nil {
			log.Printf("mercury-node: [replicator] pin store %s: %v", pkHex[:16]+"...", err)
			continue
		}
		log.Printf("mercury-node: [replicator] pinned store %s", pkHex[:16]+"...")
	}
}

// startArbitrator registers this node as an arbitrator in the Plutus registry
// and sets up the Mercury arbitrator profile.
func startArbitrator(sdk *mercury.MercurySDK, specsStr, langsStr string) {
	specs := splitTrimmed(specsStr)
	langs := splitTrimmed(langsStr)

	log.Printf("mercury-node: [arbitrator] starting, specs=%v langs=%v", specs, langs)

	profile := arbitration.MercuryArbitratorProfile{
		PlutusPubKey:    sdk.PublicKey(),
		Specializations: specs,
		Languages:       langs,
		ResponseTime:    24 * time.Hour,
		MaxConcurrent:   10,
	}

	if err := sdk.Arbitration.RegisterAsArbitrator(profile); err != nil {
		log.Printf("mercury-node: [arbitrator] registration failed: %v", err)
		log.Printf("mercury-node: [arbitrator] ensure this key is registered in Plutus first")
		return
	}

	log.Printf("mercury-node: [arbitrator] registered as arbitrator")
}

// httpRPCTransport implements plutusevm.MeshTransport over direct HTTPS
// JSON-RPC. Used only by the balance-witness role — normal Plutus clients go
// through the witness mesh. Each chain maps to one rpcURL; the transport
// ignores the chainID arg on a per-instance basis (each instance is already
// bound to its chain).
type httpRPCTransport struct {
	rpcURL string
	client *http.Client
	id     atomic.Uint64
}

func newHTTPRPCTransport(rpcURL string) *httpRPCTransport {
	return &httpRPCTransport{
		rpcURL: rpcURL,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (t *httpRPCTransport) SendRPC(ctx context.Context, chainID string, method string, params []byte) (json.RawMessage, error) {
	if len(params) == 0 {
		params = []byte("[]")
	}
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  json.RawMessage(params),
		"id":      t.id.Add(1),
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("rpc HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("rpc decode: %w", err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", env.Error.Code, env.Error.Message)
	}
	return env.Result, nil
}

// startBalanceWitness registers ChainBalanceProviders for every chain the user
// supplied an RPC URL for, then runs the BalanceVerifier in witness mode.
//
// Costs (per witness-balance-query-guide.md §Cost):
//   - 1 vCPU / 2 GB VPS: ~$5/mo
//   - Polygon RPC (Alchemy free): $0
//   - Ethereum archive RPC: $49/mo for Growth tier, or ~$200/mo self-hosted
//   - Blockstream Esplora: $0
//
// A Polygon+BTC witness can run free-tier indefinitely.
// btcBalanceOptions captures all BTC-backend-selection flags so the
// startBalanceWitness signature doesn't keep growing unboundedly as more BTC
// backends are added.
type btcBalanceOptions struct {
	Mode        string   // "esplora" | "neutrino"
	EsploraURL  string   // used when Mode=="esplora"
	DataDir     string   // neutrino header/filter DB root (overrides DefaultDir)
	DefaultDir  string   // fallback when DataDir is empty
	Network     string   // bitcoin|testnet|regtest|signet
	Peers       []string // empty → DNS seeds
	BirthHeight int32    // earliest block to scan for address activity
}

func startBalanceWitness(ctx context.Context, sdk *mercury.MercurySDK, rpcURLs map[string]string, btcOpts btcBalanceOptions, evmMode string, evmQuorum int, evmTimeout time.Duration) {
	if sdk.Commerce == nil || sdk.Commerce.BalanceVerifier == nil {
		log.Printf("mercury-node: [balance-witness] plutus BalanceVerifier not available, skipping")
		return
	}
	verifier := sdk.Commerce.BalanceVerifier

	registered := []string{}
	switch evmMode {
	case "multi":
		// Public-RPC quorum mode. Per chain, wrap every default endpoint in a
		// local httpRPCTransport and hand the bundle to
		// MultiRPCBalanceProvider. Operators with chain-specific overrides
		// (e.g. a private Alchemy URL) can still use --rpc-<chain> in single
		// mode; mixed mode is deliberately not supported — pick one trust
		// model per witness.
		for _, chainID := range []string{"ethereum", "polygon", "arbitrum", "bsc"} {
			specs := plutusevm.DefaultEndpointsFor(chainID)
			if len(specs) == 0 {
				log.Printf("mercury-node: [balance-witness] no default endpoints for %s, skipping", chainID)
				continue
			}
			endpoints := make([]plutusevm.RPCEndpoint, 0, len(specs))
			for _, s := range specs {
				endpoints = append(endpoints, plutusevm.RPCEndpoint{
					Name:           s.Name,
					Transport:      newHTTPRPCTransport(s.RPCURL),
					ChainID:        s.ChainID,
					NativeDecimals: 18,
				})
			}
			provider, err := plutusevm.NewMultiRPCBalanceProvider(endpoints, evmQuorum, evmTimeout, chainID)
			if err != nil {
				log.Printf("mercury-node: [balance-witness] %s multi-rpc: %v", chainID, err)
				continue
			}
			verifier.RegisterProvider(chainID, provider)
			registered = append(registered, fmt.Sprintf("%s(multi %d/%d)", chainID, provider.Quorum(), provider.EndpointCount()))
		}
	default: // "single" (and anything unknown)
		if evmMode != "single" {
			log.Printf("mercury-node: [balance-witness] unknown --evm-balance-mode=%q, falling back to single", evmMode)
		}
		for chainID, url := range rpcURLs {
			if url == "" {
				continue
			}
			transport := newHTTPRPCTransport(url)
			client, err := plutusevm.NewEVMClient(transport, chainID)
			if err != nil {
				log.Printf("mercury-node: [balance-witness] %s: %v", chainID, err)
				continue
			}
			provider, err := plutusevm.NewEVMBalanceProvider(client, 18)
			if err != nil {
				log.Printf("mercury-node: [balance-witness] %s provider: %v", chainID, err)
				continue
			}
			verifier.RegisterProvider(chainID, provider)
			registered = append(registered, chainID)
		}
	}

	switch btcOpts.Mode {
	case "neutrino":
		neutrinoDir := btcOpts.DataDir
		if neutrinoDir == "" {
			neutrinoDir = filepath.Join(btcOpts.DefaultDir, "neutrino")
		}
		if err := os.MkdirAll(neutrinoDir, 0o755); err != nil {
			log.Printf("mercury-node: [balance-witness] create neutrino dir: %v — falling back to esplora", err)
			btcProvider := plutusbtc.NewEsploraBalanceProvider(btcOpts.EsploraURL, nil)
			verifier.RegisterProvider("bitcoin", btcProvider)
			registered = append(registered, "bitcoin(esplora-fallback)")
			break
		}
		np, err := plutusbtc.NewNeutrinoBalanceProvider(neutrinoDir, btcOpts.Network, btcOpts.Peers, btcOpts.BirthHeight)
		if err != nil {
			log.Printf("mercury-node: [balance-witness] neutrino init failed: %v — falling back to esplora", err)
			btcProvider := plutusbtc.NewEsploraBalanceProvider(btcOpts.EsploraURL, nil)
			verifier.RegisterProvider("bitcoin", btcProvider)
			registered = append(registered, "bitcoin(esplora-fallback)")
			break
		}
		if err := np.Start(ctx); err != nil {
			log.Printf("mercury-node: [balance-witness] neutrino start failed: %v — falling back to esplora", err)
			_ = np.Stop()
			btcProvider := plutusbtc.NewEsploraBalanceProvider(btcOpts.EsploraURL, nil)
			verifier.RegisterProvider("bitcoin", btcProvider)
			registered = append(registered, "bitcoin(esplora-fallback)")
			break
		}
		verifier.RegisterProvider("bitcoin", np)
		registered = append(registered, fmt.Sprintf("bitcoin(neutrino net=%s birth=%d)", btcOpts.Network, btcOpts.BirthHeight))
		// Shutdown on ctx cancel.
		go func() {
			<-ctx.Done()
			if err := np.Stop(); err != nil {
				log.Printf("mercury-node: [balance-witness] neutrino stop: %v", err)
			}
		}()
	default:
		if btcOpts.Mode != "" && btcOpts.Mode != "esplora" {
			log.Printf("mercury-node: [balance-witness] unknown --btc-mode=%q, defaulting to esplora", btcOpts.Mode)
		}
		btcProvider := plutusbtc.NewEsploraBalanceProvider(btcOpts.EsploraURL, nil)
		verifier.RegisterProvider("bitcoin", btcProvider)
		registered = append(registered, "bitcoin")
	}

	log.Printf("mercury-node: [balance-witness] starting, chains=%v", registered)
	go func() {
		if err := verifier.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("mercury-node: [balance-witness] stopped: %v", err)
		}
	}()
	_ = plutuswitness.BalancePolicy // referenced to document default quorum
}

// feeDistributorOptions bundles the --distribute-witness-fees CLI flags so
// startFeeDistributor's signature stays compact.
type feeDistributorOptions struct {
	Window            time.Duration
	UseMulticall3     bool
	MinPayoutDec      string
	MaintainerWallets map[string]maintainerWalletEntry
}

// maintainerWalletEntry mirrors the mesh_embed MaintainerWalletConfig shape:
// an on-chain address, an encrypted-keyfile path, and the env var holding
// the passphrase for that keyfile. Defined locally so mercury-node does not
// depend on the ORBTR agent package.
type maintainerWalletEntry struct {
	Address       string `toml:"address"`
	KeyPath       string `toml:"key_path"`
	PassphraseEnv string `toml:"passphrase_env"`
}

// maintainerWalletFlag implements flag.Value so --maintainer-wallet can be
// repeated on the command line. Each call appends one spec of the form
// "chainID:address:keypath:passenv".
type maintainerWalletFlag struct {
	specs []string
}

func newMaintainerWalletFlag() *maintainerWalletFlag { return &maintainerWalletFlag{} }

func (f *maintainerWalletFlag) String() string { return strings.Join(f.specs, ",") }

func (f *maintainerWalletFlag) Set(s string) error {
	if s == "" {
		return fmt.Errorf("empty maintainer-wallet spec")
	}
	f.specs = append(f.specs, s)
	return nil
}

// loadMaintainerWallets resolves the final per-chain wallet map from the
// repeated --maintainer-wallet CLI specs. Format per spec:
//
//	chainID|address|keypath[|passenv]
//
// passenv is optional; if omitted, the keyfile is treated as unencrypted
// (matching wallet.PassphraseFromEnv semantics when the env var is unset).
// The '|' delimiter is chosen deliberately so Windows keyfile paths with
// a drive letter (C:\path\to\key.json) pass through intact — a ':'
// delimiter would shred them at the drive-letter colon.
//
// For richer config (e.g. per-chain witness capabilities), operators should
// run the ORBTR agent with its mesh_embed TOML config instead — this helper
// deliberately keeps the mercury-node surface minimal.
func loadMaintainerWallets(cliSpecs []string) (map[string]maintainerWalletEntry, error) {
	out := map[string]maintainerWalletEntry{}
	for _, spec := range cliSpecs {
		parts := strings.SplitN(spec, "|", 4)
		if len(parts) < 3 {
			return nil, fmt.Errorf("invalid --maintainer-wallet %q (need chainID|address|keypath[|passenv])", spec)
		}
		chainID := strings.ToLower(strings.TrimSpace(parts[0]))
		if chainID == "" {
			return nil, fmt.Errorf("invalid --maintainer-wallet %q: empty chain ID", spec)
		}
		entry := maintainerWalletEntry{
			Address: strings.TrimSpace(parts[1]),
			KeyPath: strings.TrimSpace(parts[2]),
		}
		if len(parts) == 4 {
			entry.PassphraseEnv = strings.TrimSpace(parts[3])
		}
		out[chainID] = entry
	}
	return out, nil
}

// startFeeDistributor boots the WitnessFeeDistributor on a ticker goroutine.
//
// SIGNER-KEY ARCHITECTURAL NOTE (for the deployment guide):
//
// The fee-distribution signer is a SEPARATE key from the Offline Root (OR) /
// Platform Key (PK) that signs protocol attestations and adapter descriptors.
// It is a hot-wallet key with custody of the maintainer pool balance only.
// Compromise of this key allows an attacker to steal at most one distribution
// window's worth of accumulated witness fees (typically 24h of fees), NOT the
// protocol's trust chain. Operators SHOULD:
//
//   - Rotate the fee-distribution signer on a regular cadence (monthly).
//   - Cap the pool balance by sweeping excess to cold storage.
//   - Store the key in an HSM or encrypted-at-rest secret manager, never on
//     disk as plaintext.
//   - Never reuse the OR/PK for this role; doing so elevates a hot-wallet
//     compromise into a protocol-wide trust compromise.
//
// This startup helper logs a warning when the signer key path is empty so
// operators cannot silently run the distributor in a no-op state.
func startFeeDistributor(ctx context.Context, sdk *mercury.MercurySDK, opts feeDistributorOptions) {
	if sdk.Commerce == nil {
		log.Printf("mercury-node: [fee-distributor] plutus SDK not available, skipping")
		return
	}
	if len(opts.MaintainerWallets) == 0 {
		log.Printf("mercury-node: [fee-distributor] WARNING: no --maintainer-wallet / --maintainer-wallets-config supplied; distributor will not start")
		return
	}

	keyFiles := map[string]string{}
	passphrases := map[string][]byte{}
	payoutAddresses := map[string]string{}
	for chainID, w := range opts.MaintainerWallets {
		if w.Address == "" {
			log.Printf("mercury-node: [fee-distributor] %s: empty address, skipping", chainID)
			continue
		}
		payoutAddresses[chainID] = w.Address
		if w.KeyPath == "" {
			log.Printf("mercury-node: [fee-distributor] %s: no key_path, signing disabled for this chain", chainID)
			continue
		}
		keyFiles[chainID] = w.KeyPath
		if w.PassphraseEnv != "" {
			pw, err := plutuswallet.PassphraseFromEnv(w.PassphraseEnv)
			if err != nil {
				log.Printf("mercury-node: [fee-distributor] %s: passphrase load: %v", chainID, err)
				continue
			}
			passphrases[chainID] = pw
		} else {
			passphrases[chainID] = nil
		}
	}
	if len(keyFiles) == 0 {
		log.Printf("mercury-node: [fee-distributor] no chains have signing keys configured; distributor will not start")
		return
	}

	signer, err := plutuswitness.NewWalletSigner(plutuswitness.WalletSignerConfig{
		KeyFiles:    keyFiles,
		Passphrases: passphrases,
	})
	if err != nil {
		log.Printf("mercury-node: [fee-distributor] construct wallet signer: %v", err)
		return
	}

	// EVM envelope finalizer — clients map intentionally empty until a live
	// Portal Network / go-ethereum RPC client is wired up. See
	// TODO(mesh_embed/evm-rpc-client) in plutus/witness/evm_envelope_finalizer.go.
	finalizer := &plutuswitness.EVMEnvelopeFinalizer{
		Clients: map[string]plutuswitness.EVMChainClient{},
	}
	finalizingSigner := &plutuswitness.FinalizingSigner{
		Inner:     signer,
		Finalizer: finalizer,
	}

	// Parse the dust threshold (decimal big integer in smallest chain units).
	minPayout := new(big.Int)
	if opts.MinPayoutDec != "" {
		if _, ok := minPayout.SetString(opts.MinPayoutDec, 10); !ok {
			log.Printf("mercury-node: [fee-distributor] invalid --fee-distribution-min-payout %q, using zero", opts.MinPayoutDec)
			minPayout = new(big.Int)
		}
	}

	distributor, err := sdk.Commerce.NewFeeDistributor(plutus.FeeDistributorHelperConfig{
		PayoutAddresses: payoutAddresses,
		Signer:          finalizingSigner,
		Window:          opts.Window,
		UseMulticall3:   opts.UseMulticall3,
		MinPayout:       minPayout,
	})
	if err != nil {
		log.Printf("mercury-node: [fee-distributor] construct distributor: %v", err)
		return
	}

	log.Printf("mercury-node: [fee-distributor] starting: window=%s multicall3=%v min-payout=%s chains=%v",
		opts.Window, opts.UseMulticall3, opts.MinPayoutDec, payoutAddresses)

	// Run every window unconditionally — mercury-node has no ORBTR-style
	// resource governor and is expected to be a dedicated node. The
	// distributor's own idempotency (dedupe by round ID) protects against
	// duplicate rounds if the process is restarted.
	go func() {
		if err := distributor.RunTicker(ctx); err != nil && ctx.Err() == nil {
			log.Printf("mercury-node: [fee-distributor] ticker exited: %v", err)
		}
	}()
}

// startSeller enables the seller role by activating the offline manager
// for pre-authorized order acceptance when the seller is away.
func startSeller(sdk *mercury.MercurySDK) {
	log.Printf("mercury-node: [seller] starting, store=%s", sdk.StoreID())

	// Purge any expired commitments on startup.
	purged := sdk.Offline.PurgeExpired()
	if purged > 0 {
		log.Printf("mercury-node: [seller] purged %d expired commitments", purged)
	}

	active := sdk.Offline.ActiveCommitments()
	log.Printf("mercury-node: [seller] %d active offline commitments", len(active))

	// Advertise the store on the replication directory.
	ctx := context.Background()
	if err := sdk.Replication.Advertise(ctx); err != nil {
		log.Printf("mercury-node: [seller] advertise store: %v", err)
	} else {
		log.Printf("mercury-node: [seller] store advertised on directory")
	}
}

// logStatus prints a periodic status summary to the log.
func logStatus(sdk *mercury.MercurySDK, activeRoles map[string]bool) {
	peers := sdk.Replication.PeerCount()
	pinned := sdk.Replication.PinnedStoreIDs()

	parts := []string{
		fmt.Sprintf("peers=%d", peers),
		fmt.Sprintf("pinned_stores=%d", len(pinned)),
	}

	if activeRoles["seller"] {
		commitments := sdk.Offline.ActiveCommitments()
		listings := sdk.Store.AllListings()
		parts = append(parts,
			fmt.Sprintf("listings=%d", len(listings)),
			fmt.Sprintf("offline_commitments=%d", len(commitments)),
		)
	}

	if activeRoles["arbitrator"] {
		// Note: active cases are tracked per-profile in the arbitration service.
		parts = append(parts, "arbitrator=active")
	}

	orders := sdk.Orders.List()
	parts = append(parts, fmt.Sprintf("orders=%d", len(orders)))

	log.Printf("mercury-node: [status] %s", strings.Join(parts, " "))
}
