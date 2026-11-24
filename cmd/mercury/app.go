// Copyright (c) 2026 Brandon Miller-Mumford.
// Licensed under the MIT License; see LICENSE.

//go:build wails

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	mercury "github.com/bbmumford/mercury"
	"github.com/bbmumford/mercury/discovery"
	"github.com/bbmumford/mercury/order"
	"github.com/bbmumford/mercury/profile"
	"github.com/bbmumford/mercury/reputation"
	meshruntime "github.com/bbmumford/mercury/runtime/mesh"
	"github.com/bbmumford/mercury/store"
	plutus "github.com/bbmumford/plutus"

	minerva_messaging "github.com/bbmumford/minerva/messaging"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// conversationChannelID returns the deterministic ChannelID for a 1:1 chat
// between a and b, regardless of the argument order. Both peers must
// compute the same ID so they open the same Conversation; using the
// lexicographic sort of hex pubkeys is the cheapest way to guarantee that.
func conversationChannelID(a, b ed25519.PublicKey) minerva_messaging.ChannelID {
	aHex := hex.EncodeToString(a)
	bHex := hex.EncodeToString(b)
	if aHex > bHex {
		aHex, bHex = bHex, aHex
	}
	return minerva_messaging.ChannelID("mercury.dm." + aHex + ":" + bHex)
}

// OrderParams holds the parameters for creating a new order from the frontend.
type OrderParams struct {
	ListingID string `json:"listingId"`
	VariantID string `json:"variantId,omitempty"`
	Quantity  int    `json:"quantity"`
	ChainID   string `json:"chainId"`
	Message   string `json:"message,omitempty"`
}

// SystemInfo describes the host operating system and runtime — returned to
// the frontend so it can conditionally render desktop-only UI elements.
type SystemInfo struct {
	OS        string `json:"os"`        // runtime.GOOS (windows, darwin, linux)
	Arch      string `json:"arch"`      // runtime.GOARCH (amd64, arm64)
	GoVersion string `json:"goVersion"` // runtime.Version()
	NumCPU    int    `json:"numCpu"`
	DataDir   string `json:"dataDir"`   // configured persistence directory
	IsDesktop bool   `json:"isDesktop"` // always true in the Wails build
}

// App is the Wails application backend. It wraps MercurySDK and exposes
// bound methods callable from the JavaScript frontend.
type App struct {
	ctx     context.Context
	sdk     *mercury.MercurySDK
	dataDir string

	// mesh is the shared mesh-participation runtime brought up during
	// startup when MeshSettings.Enabled is true (default). Exposes a
	// Status() accessor to the frontend so the UI can show "I'm
	// seeding X pieces, witnessing BTC, etc." See GetMeshStatus.
	mesh *meshruntime.Runtime
}

// MeshSettings captures the desktop-runtime subset of
// meshruntime.Config that's user-tweakable from the Wails frontend.
// Non-settings fields (DataDir / NeutrinoBirthHeight / AppID) are
// derived at startup rather than round-tripped through the UI.
//
// Persistence path: <dataDir>/mesh-settings.json. Missing file →
// DESIGN §19 Wails-desktop defaults (StorageMaxPct=50, headroom=25).
//
// TODO(mesh-settings-ui): wire a Wails frontend settings form that
// binds to GetMeshSettings / SetMeshSettings below. The backend side
// is complete; the frontend currently ships a stub that reads the
// settings at startup only.
type MeshSettings struct {
	Enabled              bool `json:"enabled"`
	EnableReplication    bool `json:"enableReplication"`
	EnableBlobProvider   bool `json:"enableBlobProvider"`
	EnableMessageRelay   bool `json:"enableMessageRelay"`
	EnableBalanceWitness bool `json:"enableBalanceWitness"`
	EnableTxWitness      bool `json:"enableTxWitness"`
	StorageMaxPct        int  `json:"storageMaxPct"`
	StorageHeadroomPct   int  `json:"storageHeadroomPct"`
	StorageMinReserveGB  int  `json:"storageMinReserveGB"`

	// FeeDistributionHours is the fee-distribution cadence in hours
	// (6/12/24/48). Zero means "use DESIGN §17 default" (24h).
	FeeDistributionHours int `json:"feeDistributionHours"`

	// MaintainerWallets lists per-chain maintainer wallet configuration.
	// Nil / empty disables fee distribution entirely.
	MaintainerWallets []MaintainerWalletSetting `json:"maintainerWallets,omitempty"`
}

// MaintainerWalletSetting is the frontend-facing shape of a per-chain
// maintainer wallet. Mirrors meshruntime.MaintainerWallet plus an
// explicit ChainID so the list can round-trip as an ordered array in
// JSON.
type MaintainerWalletSetting struct {
	ChainID       string `json:"chainId"`
	Address       string `json:"address"`
	KeyPath       string `json:"keyPath"`
	PassphraseEnv string `json:"passphraseEnv"`
}

// defaultMeshSettings returns the DESIGN §19 Wails-desktop defaults.
// Low storage quota because the user's primary workload is the desktop
// UI itself — mesh participation runs in the background.
func defaultMeshSettings() MeshSettings {
	return MeshSettings{
		Enabled:              true,
		EnableReplication:    true,
		EnableBlobProvider:   true,
		EnableMessageRelay:   true,
		EnableBalanceWitness: true,
		EnableTxWitness:      true,
		StorageMaxPct:        50,
		StorageHeadroomPct:   25,
		StorageMinReserveGB:  10,
		FeeDistributionHours: 24,
	}
}

// NewApp creates a new App instance.
func NewApp() *App {
	return &App{}
}

// defaultDataDir returns a sensible platform-specific data directory
// (~/.mercury on unix, %APPDATA%\mercury on windows).
func defaultDataDir() string {
	if cfg, err := os.UserConfigDir(); err == nil {
		return cfg + string(os.PathSeparator) + "mercury"
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home + string(os.PathSeparator) + ".mercury"
	}
	return ".mercury"
}

// startup is called when the Wails app starts. It initializes the MercurySDK
// and starts the Minerva P2P node.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.dataDir = defaultDataDir()

	cfg := mercury.Config{
		AppID:        "mercury",
		StoreName:    "My Store",
		PlutusConfig: plutus.Config{},
	}

	sdk, err := mercury.New(cfg)
	if err != nil {
		log.Printf("mercury-desktop: init SDK: %v", err)
		return
	}
	a.sdk = sdk

	if err := sdk.Node.Start(ctx); err != nil {
		log.Printf("mercury-desktop: start minerva node: %v", err)
		return
	}

	// Start Mercury-owned background loops (arbitrator liveness re-publish, ...).
	sdk.Start(ctx)

	// Bring up the shared mesh runtime (DESIGN §19 desktop defaults).
	// Errors are logged but do not abort startup — the desktop UI stays
	// usable even when mesh participation cannot start.
	if settings := a.loadMeshSettings(); settings.Enabled {
		if err := a.startMeshRuntime(ctx, settings); err != nil {
			log.Printf("mercury-desktop: [mesh] start runtime: %v", err)
		} else {
			log.Printf("mercury-desktop: [mesh] started, roles=%v", a.mesh.Status().EnabledRoles)
		}
	}

	log.Printf("mercury-desktop: started, store=%s", sdk.StoreID())
}

// shutdown is called when the Wails app is closing. It stops the Minerva node
// and cleans up resources.
func (a *App) shutdown(ctx context.Context) {
	if a.sdk == nil {
		return
	}

	// Stop the mesh runtime first so it tears down its role loops before
	// the underlying SDK's Minerva node stops.
	if a.mesh != nil {
		if err := a.mesh.Stop(); err != nil {
			log.Printf("mercury-desktop: [mesh] stop runtime: %v", err)
		}
	}

	a.sdk.Stop()

	if err := a.sdk.Node.Stop(); err != nil {
		log.Printf("mercury-desktop: stop minerva node: %v", err)
	}

	log.Printf("mercury-desktop: shutdown complete")
}

// startMeshRuntime builds the shared mesh.Runtime from the provided
// settings plus desktop-appropriate fixed values (DataDir rooted under
// the app data dir, AppID="mercury-desktop"). Safe to call at most
// once per App lifetime — subsequent calls are no-ops.
func (a *App) startMeshRuntime(ctx context.Context, settings MeshSettings) error {
	if a.mesh != nil {
		return nil
	}
	feeWindow := time.Duration(settings.FeeDistributionHours) * time.Hour
	if feeWindow <= 0 {
		feeWindow = 24 * time.Hour
	}
	var wallets map[string]meshruntime.MaintainerWallet
	if len(settings.MaintainerWallets) > 0 {
		wallets = make(map[string]meshruntime.MaintainerWallet, len(settings.MaintainerWallets))
		for _, w := range settings.MaintainerWallets {
			if w.ChainID == "" {
				continue
			}
			wallets[w.ChainID] = meshruntime.MaintainerWallet{
				Address:       w.Address,
				KeyPath:       w.KeyPath,
				PassphraseEnv: w.PassphraseEnv,
			}
		}
	}
	cfg := meshruntime.Config{
		EnableReplication:            settings.EnableReplication,
		EnableBlobProvider:           settings.EnableBlobProvider,
		EnableMessageRelay:           settings.EnableMessageRelay,
		EnableBalanceWitness:         settings.EnableBalanceWitness,
		EnableTxWitness:              settings.EnableTxWitness,
		StorageMaxPct:                settings.StorageMaxPct,
		StorageTargetHeadroomPct:     settings.StorageHeadroomPct,
		StorageMinReserveGB:          settings.StorageMinReserveGB,
		StorageHardMinGB:             2,
		NetworkBudgetMbps:            10,
		AppID:                        "mercury-desktop",
		DataDir:                      filepath.Join(a.dataDir, "mesh"),
		MaintainerWallets:            wallets,
		FeeDistributionWindow:        feeWindow,
		FeeDistributionUseMulticall3: true,
	}
	cfg.ApplyDefaults(a.dataDir)
	rt, err := meshruntime.NewRuntime(cfg)
	if err != nil {
		return err
	}
	if err := rt.Start(ctx); err != nil {
		return err
	}
	a.mesh = rt
	return nil
}

// loadMeshSettings returns the user's persisted mesh settings, or the
// DESIGN §19 desktop defaults when no settings file exists. The JSON
// file lives at <dataDir>/mesh-settings.json; a read failure degrades
// silently to defaults so a corrupted file never prevents startup.
func (a *App) loadMeshSettings() MeshSettings {
	if a.dataDir == "" {
		return defaultMeshSettings()
	}
	path := filepath.Join(a.dataDir, "mesh-settings.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return defaultMeshSettings()
	}
	var s MeshSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		log.Printf("mercury-desktop: [mesh] settings parse failed, using defaults: %v", err)
		return defaultMeshSettings()
	}
	return s
}

// GetMeshSettings returns the persisted mesh settings for the
// frontend.
func (a *App) GetMeshSettings() MeshSettings {
	return a.loadMeshSettings()
}

// SetMeshSettings persists new mesh settings to disk. The runtime is
// NOT restarted — changes take effect on next app launch. A future
// enhancement (TODO: mesh-hot-restart) could teardown-and-restart in
// place to avoid the relaunch.
func (a *App) SetMeshSettings(settings MeshSettings) error {
	if a.dataDir == "" {
		return fmt.Errorf("data dir not initialized")
	}
	if err := os.MkdirAll(a.dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	path := filepath.Join(a.dataDir, "mesh-settings.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}

// GetMeshStatus returns the shared mesh runtime's status snapshot for
// the desktop UI. When the runtime is not running (e.g. disabled in
// settings or failed to start), returns a zero-valued Status.
func (a *App) GetMeshStatus() meshruntime.Status {
	if a.mesh == nil {
		return meshruntime.Status{}
	}
	return a.mesh.Status()
}

// GetStore returns the current store root with all metadata.
func (a *App) GetStore() (*store.StoreRoot, error) {
	if a.sdk == nil {
		return nil, fmt.Errorf("sdk not initialized")
	}
	root := a.sdk.Store.LastRoot()
	if root == nil {
		// No store published yet, publish one now to return fresh state.
		published, err := a.sdk.Store.Publish()
		if err != nil {
			return nil, fmt.Errorf("publish store: %w", err)
		}
		return published, nil
	}
	return root, nil
}

// AddListing adds a new listing to the local store and returns its ID.
func (a *App) AddListing(listing store.Listing) (string, error) {
	if a.sdk == nil {
		return "", fmt.Errorf("sdk not initialized")
	}
	if listing.ID == "" {
		listing.ID = generateListingID()
	}
	listing.StoreID = a.sdk.StoreID()
	listing.SellerID = hex.EncodeToString(a.sdk.PublicKey())
	listing.Active = true
	listing.CreatedAt = time.Now()
	listing.UpdatedAt = time.Now()

	if err := a.sdk.Store.AddListing(&listing); err != nil {
		return "", fmt.Errorf("add listing: %w", err)
	}

	// Index the listing for search.
	a.sdk.Search.Add(discovery.SearchEntry{
		ListingID:   listing.ID,
		Title:       listing.Title,
		Description: listing.Description,
		Category:    listing.Category,
		Price:       listing.Price,
		SellerID:    listing.SellerID,
		Keywords:    listing.Keywords,
		UpdatedAt:   listing.UpdatedAt,
	})

	return listing.ID, nil
}

// RemoveListing removes a listing from the local store by ID.
func (a *App) RemoveListing(id string) error {
	if a.sdk == nil {
		return fmt.Errorf("sdk not initialized")
	}
	return a.sdk.Store.RemoveListing(id)
}

// AllListings returns all listings in the local store.
func (a *App) AllListings() ([]store.Listing, error) {
	if a.sdk == nil {
		return nil, fmt.Errorf("sdk not initialized")
	}
	ptrs := a.sdk.Store.AllListings()
	listings := make([]store.Listing, len(ptrs))
	for i, p := range ptrs {
		listings[i] = *p
	}
	return listings, nil
}

// CreateOrder creates a new order from the given parameters. The buyer signs
// the order intent and it enters Pending state awaiting escrow funding.
func (a *App) CreateOrder(params OrderParams) (string, error) {
	if a.sdk == nil {
		return "", fmt.Errorf("sdk not initialized")
	}

	intentID := generateOrderID()
	intent := order.OrderIntent{
		ID:          intentID,
		BuyerPubKey: a.sdk.PublicKey(),
		ListingID:   params.ListingID,
		VariantID:   params.VariantID,
		Quantity:    params.Quantity,
		ChainID:     params.ChainID,
		Timestamp:   time.Now(),
	}

	// Sign the intent with our identity key.
	if err := order.SignOrderIntent(&intent, a.sdk.Commerce.IdentityKey); err != nil {
		return "", fmt.Errorf("sign order intent: %w", err)
	}

	created, err := a.sdk.Orders.CreateOrder(intent)
	if err != nil {
		return "", fmt.Errorf("create order: %w", err)
	}

	// Send a message to the seller if a message was provided.
	if params.Message != "" {
		_ = a.sendOrderMessage(created.Intent.SellerPubKey, intentID, params.Message)
	}

	return intentID, nil
}

// AcceptOrder transitions an order from Funded to Accepted (seller flow).
func (a *App) AcceptOrder(orderID string) error {
	if a.sdk == nil {
		return fmt.Errorf("sdk not initialized")
	}

	fc := order.FulfillmentCommitment{
		OrderID:      orderID,
		SellerPubKey: a.sdk.PublicKey(),
		EstDelivery:  time.Now().Add(7 * 24 * time.Hour),
		Timestamp:    time.Now(),
	}

	if err := order.SignFulfillment(&fc, a.sdk.Commerce.IdentityKey); err != nil {
		return fmt.Errorf("sign fulfillment: %w", err)
	}

	if _, err := a.sdk.Orders.AcceptOrder(orderID, fc); err != nil {
		return fmt.Errorf("accept order: %w", err)
	}

	return nil
}

// GetOrders returns all orders (both as buyer and seller).
func (a *App) GetOrders() ([]order.Order, error) {
	if a.sdk == nil {
		return nil, fmt.Errorf("sdk not initialized")
	}
	ptrs := a.sdk.Orders.List()
	orders := make([]order.Order, len(ptrs))
	for i, p := range ptrs {
		orders[i] = *p
	}
	return orders, nil
}

// SendMessage sends an encrypted direct message to a peer via a
// Minerva 2-member Conversation. The Conversation's ChannelID is
// deterministic so both sides converge on the same channel handle.
func (a *App) SendMessage(to string, content string) error {
	if a.sdk == nil {
		return fmt.Errorf("sdk not initialized")
	}

	toBytes, err := hex.DecodeString(to)
	if err != nil {
		return fmt.Errorf("invalid recipient pubkey: %w", err)
	}
	if len(toBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid recipient pubkey size %d", len(toBytes))
	}

	chanID := conversationChannelID(a.sdk.PublicKey(), ed25519.PublicKey(toBytes))
	peerID := minerva_messaging.PeerIDFromBytes(toBytes)
	conv, err := a.sdk.Messenger.OpenConversation(chanID, []minerva_messaging.PeerID{peerID})
	if err != nil {
		return fmt.Errorf("open conversation: %w", err)
	}

	if _, err := conv.Send(a.ctx, []byte(content)); err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return nil
}

// GetMessages returns the decrypted message history for a 1:1 conversation
// with peerID (hex-encoded Ed25519 public key), sorted by SentAt ascending.
func (a *App) GetMessages(peerID string) ([]minerva_messaging.Message, error) {
	if a.sdk == nil {
		return nil, fmt.Errorf("sdk not initialized")
	}
	peerBytes, err := hex.DecodeString(peerID)
	if err != nil {
		return nil, fmt.Errorf("invalid peer pubkey: %w", err)
	}
	if len(peerBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid peer pubkey size %d", len(peerBytes))
	}
	chanID := conversationChannelID(a.sdk.PublicKey(), ed25519.PublicKey(peerBytes))
	conv := a.sdk.Messenger.Conversation(chanID)
	if conv == nil {
		return nil, nil
	}
	return conv.History(time.Time{}), nil
}

// SearchListings performs a full-text search across the listing index.
func (a *App) SearchListings(query string) ([]discovery.SearchEntry, error) {
	if a.sdk == nil {
		return nil, fmt.Errorf("sdk not initialized")
	}
	results := a.sdk.Search.Search(query, 50)
	return results, nil
}

// GetProfile returns the local user's profile.
func (a *App) GetProfile() (*profile.UserProfile, error) {
	if a.sdk == nil {
		return nil, fmt.Errorf("sdk not initialized")
	}
	p, err := a.sdk.Profiles.Get(a.sdk.PublicKey())
	if err != nil {
		return nil, fmt.Errorf("get profile: %w", err)
	}
	return p, nil
}

// SetProfile updates the local user's profile and broadcasts it via gossip.
func (a *App) SetProfile(p profile.UserProfile) error {
	if a.sdk == nil {
		return fmt.Errorf("sdk not initialized")
	}

	p.PubKey = a.sdk.PublicKey()
	p.UpdatedAt = time.Now()
	if p.JoinedAt.IsZero() {
		p.JoinedAt = time.Now()
	}

	if err := p.Sign(a.sdk.Commerce.IdentityKey); err != nil {
		return fmt.Errorf("sign profile: %w", err)
	}

	return a.sdk.Profiles.Set(p)
}

// GetReputation returns the composite reputation summary for a seller.
func (a *App) GetReputation(pubkey string) (*reputation.SellerSummary, error) {
	if a.sdk == nil {
		return nil, fmt.Errorf("sdk not initialized")
	}

	pkBytes, err := hex.DecodeString(pubkey)
	if err != nil {
		return nil, fmt.Errorf("invalid pubkey: %w", err)
	}

	summary := a.sdk.Reputation.Summary(ed25519.PublicKey(pkBytes))
	return &summary, nil
}

// GetWalletAddress derives the wallet address for a given chain ID.
func (a *App) GetWalletAddress(chainID string) (string, error) {
	if a.sdk == nil {
		return "", fmt.Errorf("sdk not initialized")
	}
	return a.sdk.Commerce.ChainAddress(chainID)
}

// GetPeerCount returns the number of connected peers.
func (a *App) GetPeerCount() int {
	if a.sdk == nil {
		return 0
	}
	return a.sdk.Replication.PeerCount()
}

// sendOrderMessage is a helper that sends a message linked to an order.
// The order association is prepended to the body as a pseudo-header so it
// round-trips through the standard Conversation encryption path — the
// consumer side parses it back out when rendering.
func (a *App) sendOrderMessage(toPubKey ed25519.PublicKey, orderID string, content string) error {
	chanID := conversationChannelID(a.sdk.PublicKey(), toPubKey)
	peerID := minerva_messaging.PeerIDFromBytes(toPubKey)
	conv, err := a.sdk.Messenger.OpenConversation(chanID, []minerva_messaging.PeerID{peerID})
	if err != nil {
		return fmt.Errorf("open conversation: %w", err)
	}
	body := []byte(fmt.Sprintf("order:%s\n%s", orderID, content))
	if _, err := conv.Send(a.ctx, body); err != nil {
		return fmt.Errorf("send order message: %w", err)
	}
	return nil
}

// generateListingID creates a unique listing identifier.
func generateListingID() string {
	return fmt.Sprintf("lst_%s_%d", randomHex(8), time.Now().UnixNano())
}

// generateOrderID creates a unique order identifier.
func generateOrderID() string {
	return fmt.Sprintf("ord_%s_%d", randomHex(8), time.Now().UnixNano())
}

// generateMessageID creates a unique message identifier.
func generateMessageID() string {
	return fmt.Sprintf("msg_%s_%d", randomHex(8), time.Now().UnixNano())
}

// randomHex returns n random hex characters.
func randomHex(n int) string {
	b := make([]byte, n/2+1)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// ---------------------------------------------------------------------------
// Native-only desktop methods
//
// These methods are exposed only in the Wails build and give the marketplace
// UI access to OS integration that is impossible from the browser/WASM target.
// The frontend can detect Wails by checking `window.runtime` and conditionally
// render menu items that invoke these bindings.
// ---------------------------------------------------------------------------

// OpenDataDir reveals the Mercury data directory in the OS file manager
// (Explorer on Windows, Finder on macOS, xdg-open on Linux). The directory
// is created if it does not yet exist so the reveal call always succeeds.
func (a *App) OpenDataDir() error {
	if a.dataDir == "" {
		a.dataDir = defaultDataDir()
	}
	if err := os.MkdirAll(a.dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	wailsruntime.BrowserOpenURL(a.ctx, "file://"+a.dataDir)
	return nil
}

// ImportIdentityFromFile opens a native file-picker and returns the bytes
// of the selected identity file. Typical use: the frontend triggers this,
// receives the bytes, and hands them to the SDK to load an existing wallet.
// Returns an empty byte slice if the user cancels.
func (a *App) ImportIdentityFromFile() ([]byte, error) {
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "Import Mercury Identity",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Mercury Identity (*.key, *.json)", Pattern: "*.key;*.json"},
			{DisplayName: "All Files", Pattern: "*.*"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open dialog: %w", err)
	}
	if path == "" {
		return nil, nil // user cancelled
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read identity file: %w", err)
	}
	return data, nil
}

// ExportWalletToFile opens a native save dialog and writes the provided
// wallet bytes to the chosen path. Returns the written path on success, or
// an empty string if the user cancels.
func (a *App) ExportWalletToFile(data []byte) (string, error) {
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "Export Mercury Wallet",
		DefaultFilename: fmt.Sprintf("mercury-wallet-%d.json", time.Now().Unix()),
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Mercury Wallet (*.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("save dialog: %w", err)
	}
	if path == "" {
		return "", nil // user cancelled
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write wallet file: %w", err)
	}
	return path, nil
}

// MinimizeToTray minimizes the window to the taskbar. Wails v2 does not ship
// a cross-platform system tray — a real tray requires the v3 preview or a
// third-party systray library. Until Mercury adopts one, this method simply
// hides the window (closest native equivalent) and logs a note so the
// frontend can surface the caveat in a tooltip.
func (a *App) MinimizeToTray() {
	wailsruntime.WindowHide(a.ctx)
	log.Printf("mercury-desktop: window hidden (system tray not yet available in Wails v2)")
}

// GetSystemInfo returns host OS/arch info for the frontend to conditionally
// render desktop-only menu items and show diagnostic details.
func (a *App) GetSystemInfo() SystemInfo {
	return SystemInfo{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		GoVersion: runtime.Version(),
		NumCPU:    runtime.NumCPU(),
		DataDir:   a.dataDir,
		IsDesktop: true,
	}
}
