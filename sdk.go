/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package mercury

import (
	"context"
	"crypto/ed25519"
	"fmt"

	"github.com/bbmumford/mercury/arbitration"
	"github.com/bbmumford/mercury/discovery"
	"github.com/bbmumford/mercury/moderation"
	"github.com/bbmumford/mercury/offline"
	"github.com/bbmumford/mercury/order"
	"github.com/bbmumford/mercury/profile"
	"github.com/bbmumford/mercury/reputation"
	"github.com/bbmumford/mercury/store"

	plutus "github.com/bbmumford/plutus"
	"github.com/bbmumford/plutus/observability"

	minerva_identity "github.com/bbmumford/minerva/identity"
	minerva_messaging "github.com/bbmumford/minerva/messaging"
	minerva_sdk "github.com/bbmumford/minerva/sdk"

	"github.com/bbmumford/whisper"
)

// Config configures the MercurySDK at creation time.
type Config struct {
	// AppID scopes this Mercury instance to an application namespace.
	AppID string

	// MinervaConfig configures the underlying Minerva P2P node.
	MinervaConfig minerva_sdk.NodeConfig

	// PlutusConfig configures the Plutus commerce layer (wallet, escrow, witnesses).
	PlutusConfig plutus.Config

	// StoreOwnerKey is the Ed25519 private key for the local store owner.
	// If nil, a new keypair is generated.
	StoreOwnerKey ed25519.PrivateKey

	// StoreName is the display name for the local store.
	StoreName string

	// StoreDescription is a short description of the local store.
	StoreDescription string

	// Categories are the marketplace categories to subscribe to for discovery.
	Categories []string

	// AnnounceConfig configures custom reachability announcements.
	// If nil or Addresses/DNSRecords are empty, no announcer is started.
	AnnounceConfig *discovery.AnnounceConfig
}

// Persistence & Ledger integration note
// --------------------------------------
// Mercury does NOT import the `ledger` package directly. The architecture
// deep-dive flagged this as a potential gap (Ledger provides signed records,
// TTL expiration, blob store, ACL hooks, secondary indexes), but a closer
// review of the stack shows that Minerva is already Mercury's persistence
// ledger:
//
//   - Minerva's `persistence.Store` interface is the canonical state store
//     (SQLite on desktop, IndexedDB on WASM, file on server) and is injected
//     through `minerva_sdk.WithPersistence` / `WithPersistencePath`.
//   - Minerva's `TieredStore` composes a hot `persistence.Store` with warm
//     and cold `ledger.BlobStore` tiers — blob persistence is ALREADY a
//     ledger.BlobStore; there is no separate wiring for Mercury to add.
//   - Signed records (StoreAd, OrderIntent, DisputeCase, ArbitratorRuling)
//     are signed by Plutus-managed Ed25519 keys and transported by Whisper
//     on typed topics; CRDT conflict resolution happens inside Minerva.
//   - Evidence/media blobs are hash-anchored on the containing record and
//     transported over Aether BULK streams — the raw bytes live in the
//     Minerva blob tier that composes ledger.BlobStore, so no additional
//     BlobStore wiring is needed on Mercury.
//   - TTL is handled per-subsystem: FreshnessEnforcer tracks StoreAd freshness,
//     Offline commitments carry their own expiry, arbitrator liveness is
//     the 6-hour re-publish loop in arbitration/register.go.
//
// Importing `ledger` directly into Mercury subsystems would duplicate state
// already managed by Minerva and create two parallel persistence paths. If a
// future feature needs direct ledger access (e.g. a Mercury-specific signed
// audit log distinct from Minerva CRDTs), it should be added here at that
// point — but until then, the dependency remains in go.mod only for the
// transitive ledger.BlobStore interface used by Minerva's tiered store.

// MercurySDK is the top-level entry point that wires together all Mercury
// subsystems: Minerva (P2P), Plutus (commerce), and Mercury-specific services
// (store, orders, messaging, discovery, moderation, profiles, reputation).
type MercurySDK struct {
	// Minerva layer (P2P, CRDTs, identity)
	Node *minerva_sdk.Node

	// Plutus layer (commerce, escrow, witnesses)
	Commerce *plutus.PlutusSDK

	// Mercury-specific services
	Store       *store.StoreManager
	Replication *store.ReplicationManager
	Orders      *order.OrderManager
	Settlement  *order.SettlementService
	Arbitration *arbitration.ArbitrationService
	Disputes    *arbitration.DisputeManager
	Categories  *discovery.CategoryManager
	Search      *discovery.SearchIndex
	// Messenger is the unified Minerva messaging front-door. Conversations,
	// broadcasts, receipts, presence, attachments, ratchet and relay are all
	// reachable via this single handle — Mercury no longer ships its own
	// messaging implementation.
	Messenger  *minerva_messaging.Messenger
	Profiles   *profile.ProfileService
	Moderation *moderation.ModerationService
	Reviews    *reputation.ReviewService
	Offline    *offline.OfflineManager

	// Combined reputation (Plutus + Mercury reviews)
	Reputation *reputation.MercuryReputation

	// Freshness enforcement for replicated stores
	Freshness *offline.FreshnessEnforcer

	// Custom reachability announcements
	Announcer        *discovery.Announcer
	AnnounceResolver *discovery.AnnounceResolver

	// Infrastructure
	Engine *whisper.Engine
}

// New creates and wires a fully initialized MercurySDK from the given config.
// All subsystems are created, connected, and ready to use.
func New(config Config) (*MercurySDK, error) {
	sdk := &MercurySDK{}

	// --- Minerva layer ---

	minervaConfig := config.MinervaConfig
	if minervaConfig.AppID == "" {
		minervaConfig.AppID = config.AppID
	}
	if minervaConfig.AppID == "" {
		minervaConfig.AppID = "mercury"
	}

	sdk.Node = minerva_sdk.NewNode(
		minerva_sdk.WithAppID(minervaConfig.AppID),
		minerva_sdk.WithMaxPeers(minervaConfig.MaxPeers),
	)

	// --- Plutus layer ---

	plutusCfg := config.PlutusConfig
	plutusSDK, err := plutus.New(plutusCfg)
	if err != nil {
		return nil, fmt.Errorf("mercury: init plutus: %w", err)
	}
	sdk.Commerce = plutusSDK

	// Share the Whisper engine from Plutus for gossip consistency.
	sdk.Engine = plutusSDK.Engine
	sdk.Node.SetWhisperEngine(sdk.Engine)

	// --- Store ---

	storeOwnerKey := config.StoreOwnerKey
	var storeOwnerPub ed25519.PublicKey
	if storeOwnerKey == nil {
		// Derive store key from Plutus identity.
		storeOwnerKey = plutusSDK.IdentityKey
	}
	storeOwnerPub = storeOwnerKey.Public().(ed25519.PublicKey)

	storeName := config.StoreName
	if storeName == "" {
		storeName = "Mercury Store"
	}

	sdk.Store = store.NewStoreManager(store.StoreManagerConfig{
		OwnerPubKey:  storeOwnerPub,
		OwnerPrivKey: storeOwnerKey,
		Name:         storeName,
		Description:  config.StoreDescription,
		Categories:   config.Categories,
		NodeID:       store.StoreID(storeOwnerPub),
	})

	// --- Replication ---

	sdk.Replication = store.NewReplicationManager(sdk.Store, sdk.Engine)
	sdk.Replication.SetReplicatorIdentity(storeOwnerPub, storeOwnerKey)

	if err := sdk.Replication.RegisterTopics(); err != nil {
		return nil, fmt.Errorf("mercury: register replication topics: %w", err)
	}

	// --- Orders ---

	sdk.Orders = order.NewOrderManager()

	// --- Settlement ---

	sdk.Settlement = order.NewSettlementService(
		plutusSDK.FeeCalculator,
		plutusSDK.EscrowManager,
		sdk.Orders,
	)
	sdk.Settlement.SetReputationService(plutusSDK.ReputationService)

	// --- Arbitration ---

	sdk.Arbitration = arbitration.NewArbitrationService(
		plutusSDK.ArbitratorRegistry,
		sdk.Engine,
	)
	sdk.Disputes = arbitration.NewDisputeManager(sdk.Arbitration)

	// --- Discovery ---

	sdk.Categories = discovery.NewCategoryManager(sdk.Engine)
	sdk.Search = discovery.NewSearchIndex()

	// Subscribe to configured categories.
	for _, cat := range config.Categories {
		if err := sdk.Categories.Subscribe(cat); err != nil {
			// Non-fatal: log and continue with other categories.
			continue
		}
	}

	// --- Messaging ---
	//
	// Construct Minerva's Messenger over a WhisperTransport that shares the
	// Plutus-owned Whisper engine, so every Mercury subsystem observes the
	// same gossip fan-out. The identity is derived from the store owner key
	// (ed25519) so message signatures verify against the store owner's
	// public key on the wire. Register the observability hook so every
	// Conversation.Send mirrors the MessagingSentCount metric that used to
	// live in Mercury's own messaging package — this keeps the OTel counter
	// without forcing Minerva to import plutus/observability.

	minervaIdent := &minerva_messaging.KeypairIdentity{
		KP: &minerva_identity.Keypair{
			PublicKey:  storeOwnerPub,
			PrivateKey: storeOwnerKey,
		},
	}
	minervaTrans := minerva_messaging.NewWhisperTransport(sdk.Engine)
	messenger, err := minerva_messaging.NewMessenger(minervaIdent, minervaTrans)
	if err != nil {
		return nil, fmt.Errorf("mercury: init messenger: %w", err)
	}
	sdk.Messenger = messenger

	// Observability bridge — Conversation.Send invokes the hook once per
	// successful send; repeat registrations overwrite so multiple
	// MercurySDK instances in the same process still produce one count per
	// actual send (the hook is process-global by design).
	minerva_messaging.SetMessageSentHook(func(ctx context.Context) {
		observability.MessagingSentCount().Add(ctx, 1)
	})

	// --- Profiles ---

	sdk.Profiles = profile.NewProfileService(sdk.Engine)

	// --- Moderation ---

	sdk.Moderation = moderation.NewModerationService(sdk.Engine)

	// --- Reviews & Reputation ---

	sdk.Reviews = reputation.NewReviewService(sdk.Engine)
	sdk.Reputation = reputation.NewMercuryReputation(
		plutusSDK.ReputationService,
		sdk.Reviews,
	)

	// --- Offline ---

	sdk.Offline = offline.NewOfflineManager()

	// --- Freshness Enforcer ---

	sdk.Freshness = offline.NewFreshnessEnforcer()

	// Wire freshness enforcer to replication: track store versions on
	// incoming StoreAd broadcasts.
	sdk.Replication.OnStoreAd(func(ad store.StoreAd) {
		sellerHex := store.StoreID(ad.OwnerPubKey)
		sdk.Freshness.RecordVersion(sellerHex, ad.Version, sellerHex)
	})

	// --- Announce Resolver (always active — receives peer announcements) ---

	sdk.AnnounceResolver = discovery.NewAnnounceResolver(sdk.Engine)

	// --- Announcer (only if custom addresses or DNS records are configured) ---

	if config.AnnounceConfig != nil &&
		(len(config.AnnounceConfig.Addresses) > 0 || len(config.AnnounceConfig.DNSRecords) > 0) {

		annCfg := *config.AnnounceConfig
		annCfg.NodePubKey = storeOwnerPub
		sdk.Announcer = discovery.NewAnnouncer(annCfg, sdk.Engine, storeOwnerKey)

		// Wire announce addresses into replication so StoreAds include them.
		sdk.Replication.SetAnnounceAddresses(config.AnnounceConfig.Addresses)
	}

	return sdk, nil
}

// Start boots Mercury-owned background loops that depend on a running
// context (liveness re-publish, etc.). Callers must invoke Start after
// starting the underlying Minerva node. Stop releases the loops.
func (s *MercurySDK) Start(ctx context.Context) {
	if s.Arbitration != nil {
		s.Arbitration.StartLivenessLoop(ctx)
	}
}

// Stop halts Mercury-owned background loops started by Start. It also
// tears down the Messenger, closing every open conversation / broadcast /
// presence / receipt channel. Stop is idempotent.
func (s *MercurySDK) Stop() {
	if s.Arbitration != nil {
		s.Arbitration.StopLivenessLoop()
	}
	if s.Messenger != nil {
		_ = s.Messenger.Close()
	}
}

// PublicKey returns the store owner's Ed25519 public key.
func (s *MercurySDK) PublicKey() ed25519.PublicKey {
	return s.Store.OwnerPubKey()
}

// StoreID returns the hex-encoded store identifier derived from the owner key.
func (s *MercurySDK) StoreID() string {
	return s.Store.StoreID()
}
