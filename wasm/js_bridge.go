//go:build js && wasm

/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall/js"
	"time"

	"github.com/bbmumford/mercury/arbitration"
	"github.com/bbmumford/mercury/discovery"
	"github.com/bbmumford/mercury/order"
	"github.com/bbmumford/mercury/profile"
	"github.com/bbmumford/mercury/reputation"
	"github.com/bbmumford/mercury/store"

	minerva_identity "github.com/bbmumford/minerva/identity"
	minerva_messaging "github.com/bbmumford/minerva/messaging"

	plutusArb "github.com/bbmumford/plutus/arbitrator"
	"github.com/bbmumford/plutus/escrow"
	plutusObs "github.com/bbmumford/plutus/observability"
	plutusRep "github.com/bbmumford/plutus/reputation"
	"github.com/bbmumford/plutus/settle"

	"github.com/bbmumford/ledger/cache"

	"github.com/bbmumford/whisper"
)

// conversationChannelID returns the deterministic ChannelID for a 1:1 chat
// between peers a and b. Both sides compute the same ID so they open the
// same Minerva Conversation.
func conversationChannelID(a, b ed25519.PublicKey) minerva_messaging.ChannelID {
	aHex := hex.EncodeToString(a)
	bHex := hex.EncodeToString(b)
	if aHex > bHex {
		aHex, bHex = bHex, aHex
	}
	return minerva_messaging.ChannelID("mercury.dm." + aHex + ":" + bHex)
}

// jsBridge holds the WASM-side state for the Mercury JS bridge.
type jsBridge struct {
	mu sync.RWMutex

	// Handle counters
	nextStoreHandle atomic.Int64
	nextOrderHandle atomic.Int64

	// Handle maps
	stores map[int64]*storeState
	orders map[int64]*orderState

	// Persistent identity keypair for message signing, reviews, and profiles.
	identityPub  ed25519.PublicKey
	identityPriv ed25519.PrivateKey

	// Current user's BIP39 mnemonic (kept in memory only while unlocked).
	// Empty if the identity was generated from raw randomness or logged out.
	mnemonic string

	// authenticated is true once an identity has been created/imported/unlocked.
	authenticated bool

	// Display name / profile set on the current user.
	currentName string

	// Handle of the "own" store for the current user (listings they sell).
	// -1 if no store has been created yet.
	myStoreHandle int64

	// Joined timestamp for the current identity.
	joinedAt time.Time

	// Registered passkey metadata. Each entry: id, label, createdAt.
	// The actual passkey material lives in the browser's credential store.
	passkeys []passkeyRecord

	// Filed disputes keyed by case ID.
	disputes map[string]*arbitration.DisputeCase

	// Shared services
	engine      *whisper.Engine
	orderMgr    *order.OrderManager
	searchIdx   *discovery.SearchIndex
	catMgr      *discovery.CategoryManager
	messenger   *minerva_messaging.Messenger
	profService *profile.ProfileService
	reviewSvc   *reputation.ReviewService
	mercuryRep  *reputation.MercuryReputation
	feeCalc     *settle.FeeCalculator
	arbService  *arbitration.ArbitrationService
	disputeMgr  *arbitration.DisputeManager
	plutusArbReg *plutusArb.ArbitratorRegistry

	// convSubs tracks registered Conversation.Subscribe handles so the UI
	// receives fan-out when a peer sends to us. Keyed by ChannelID string;
	// populated lazily when openOrAttachConversation is called.
	convSubs map[minerva_messaging.ChannelID]func()

	// outboundPlaintext caches the plaintext of messages we sent so
	// getConversation can render "me" side text — our own Send stores
	// ciphertext in the Conversation log, and decrypting our own messages
	// via the Minerva ratchet still works, but caching the original body
	// avoids the extra work on a hot path.
	outboundPlaintext map[minerva_messaging.MessageID]string

	// peerActivity tracks the last time we observed activity from a peer
	// (incoming message, gossip, etc.). Used by getPeerPresence as a
	// secondary "last seen" signal alongside the PEX manager's KnownPeers.
	peerActivity map[string]time.Time
}

// passkeyRecord is per-passkey metadata held on the WASM side.
// The actual WebAuthn credential lives in the browser credential store;
// we only track labels and creation times for UI display.
type passkeyRecord struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`
}

// storeState holds per-store state on the WASM side.
type storeState struct {
	manager *store.StoreManager
	privKey ed25519.PrivateKey
}

// orderState holds per-order handle state on the WASM side.
type orderState struct {
	orderID    string
	storeHandle int64
	intent     interface{} // raw JS params preserved for reference
}

func newJSBridge() *jsBridge {
	engine := whisper.NewEngine()

	// Generate a persistent identity keypair for the bridge lifetime.
	identityPub, identityPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic("mercury/wasm: generate identity key: " + err.Error())
	}

	// Build reputation from Plutus + Mercury layers
	plutusRepSvc := plutusRep.NewReputationService(engine, nil)
	reviewSvc := reputation.NewReviewService(engine)
	mercuryRep := reputation.NewMercuryReputation(plutusRepSvc, reviewSvc)

	// Wire the arbitration service onto a Plutus arbitrator registry so the
	// bridge can answer getArbitrators() / submitEvidence().
	plutusArbReg := plutusArb.NewArbitratorRegistry(engine, (*cache.DirectoryCache)(nil))
	arbService := arbitration.NewArbitrationService(plutusArbReg, engine)
	disputeMgr := arbitration.NewDisputeManager(arbService)

	// Build a Minerva Messenger over a WhisperTransport so the WASM bridge
	// uses the exact same messaging stack as the native SDK. The identity
	// is the bridge's persistent keypair; passing it through a
	// KeypairIdentity exposes Sign / Public / Private to the Messenger.
	ident := &minerva_messaging.KeypairIdentity{
		KP: &minerva_identity.Keypair{
			PublicKey:  identityPub,
			PrivateKey: identityPriv,
		},
	}
	trans := minerva_messaging.NewWhisperTransport(engine)
	messenger, err := minerva_messaging.NewMessenger(ident, trans)
	if err != nil {
		panic("mercury/wasm: init messenger: " + err.Error())
	}

	// Observability: mirror the MessagingSentCount metric on every send
	// so OTel counters match the native SDK. The hook is idempotent and
	// nil-safe so it is fine to register unconditionally at bridge init.
	minerva_messaging.SetMessageSentHook(func(ctx context.Context) {
		plutusObs.MessagingSentCount().Add(ctx, 1)
	})

	b := &jsBridge{
		stores:            make(map[int64]*storeState),
		orders:            make(map[int64]*orderState),
		identityPub:       identityPub,
		identityPriv:      identityPriv,
		myStoreHandle:     -1,
		passkeys:          []passkeyRecord{},
		disputes:          make(map[string]*arbitration.DisputeCase),
		engine:            engine,
		orderMgr:          order.NewOrderManager(),
		searchIdx:         discovery.NewSearchIndex(),
		catMgr:            discovery.NewCategoryManager(engine),
		messenger:         messenger,
		profService:       profile.NewProfileService(engine),
		reviewSvc:         reviewSvc,
		mercuryRep:        mercuryRep,
		feeCalc:           settle.NewFeeCalculator(),
		arbService:        arbService,
		disputeMgr:        disputeMgr,
		plutusArbReg:      plutusArbReg,
		convSubs:          make(map[minerva_messaging.ChannelID]func()),
		outboundPlaintext: make(map[minerva_messaging.MessageID]string),
		peerActivity:      make(map[string]time.Time),
	}

	return b
}

// openOrAttachConversation resolves (or opens) the 1:1 Conversation with
// peer and installs an activity-tracking subscriber exactly once per
// ChannelID so getPeerPresence sees a meaningful lastSeen.
func (b *jsBridge) openOrAttachConversation(peer ed25519.PublicKey) (*minerva_messaging.Conversation, error) {
	chanID := conversationChannelID(b.identityPub, peer)
	peerID := minerva_messaging.PeerIDFromBytes(peer)
	conv, err := b.messenger.OpenConversation(chanID, []minerva_messaging.PeerID{peerID})
	if err != nil {
		return nil, err
	}

	// Attach a single subscriber per channel. Repeat calls are idempotent:
	// the convSubs map guards against duplicate activity double-counts.
	b.mu.Lock()
	_, alreadyAttached := b.convSubs[chanID]
	if !alreadyAttached {
		// placeholder to claim the slot before releasing the lock
		b.convSubs[chanID] = func() {}
	}
	b.mu.Unlock()
	if alreadyAttached {
		return conv, nil
	}

	unsub := conv.Subscribe(func(m minerva_messaging.Message) {
		authorHex := string(m.Author)
		if authorHex == "" {
			return
		}
		b.mu.Lock()
		b.peerActivity[authorHex] = time.Now()
		b.mu.Unlock()
	})
	b.mu.Lock()
	b.convSubs[chanID] = unsub
	b.mu.Unlock()
	return conv, nil
}

// setIdentityFromSeed derives a deterministic Ed25519 keypair from a 64-byte
// BIP39 seed (the first 32 bytes are used as the ed25519 seed) and installs
// it as the current identity. Rebuilds the Minerva Messenger against the
// new identity so subsequent Conversations sign / decrypt with the right
// key.
func (b *jsBridge) setIdentityFromSeed(seed []byte) error {
	if len(seed) < ed25519.SeedSize {
		return fmt.Errorf("seed too short: need %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
	pub := priv.Public().(ed25519.PublicKey)

	ident := &minerva_messaging.KeypairIdentity{
		KP: &minerva_identity.Keypair{
			PublicKey:  pub,
			PrivateKey: priv,
		},
	}
	trans := minerva_messaging.NewWhisperTransport(b.engine)
	messenger, err := minerva_messaging.NewMessenger(ident, trans)
	if err != nil {
		return fmt.Errorf("rebuild messenger: %w", err)
	}

	b.mu.Lock()
	// Tear down the previous messenger's conversations (closes transport
	// subscriptions and clears per-channel state).
	if b.messenger != nil {
		_ = b.messenger.Close()
	}
	for _, unsub := range b.convSubs {
		if unsub != nil {
			unsub()
		}
	}
	b.convSubs = make(map[minerva_messaging.ChannelID]func())
	b.outboundPlaintext = make(map[minerva_messaging.MessageID]string)
	b.identityPub = pub
	b.identityPriv = priv
	b.messenger = messenger
	b.authenticated = true
	if b.joinedAt.IsZero() {
		b.joinedAt = time.Now()
	}
	b.mu.Unlock()
	return nil
}

// pubHex returns the current identity's hex-encoded public key.
func (b *jsBridge) pubHex() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return hex.EncodeToString(b.identityPub)
}

// promiseWrapper wraps an async Go operation as a JS Promise.
func promiseWrapper(fn func() (interface{}, error)) js.Value {
	handler := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		resolve := args[0]
		reject := args[1]
		go func() {
			result, err := fn()
			if err != nil {
				reject.Invoke(js.Global().Get("Error").New(err.Error()))
				return
			}
			resolve.Invoke(toJSValue(result))
		}()
		return nil
	})
	return js.Global().Get("Promise").New(handler)
}

// --- Store operations ---

// createStore creates a new Mercury store.
// Mercury.createStore(nodeHandle, name) -> storeHandle
func (b *jsBridge) createStore(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("createStore: requires nodeHandle and name")
		}

		name := args[1].String()

		// Generate a fresh Ed25519 keypair for the store owner.
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			return nil, fmt.Errorf("createStore: generate key: %w", err)
		}

		cfg := store.StoreManagerConfig{
			OwnerPubKey:  pub,
			OwnerPrivKey: priv,
			Name:         name,
			NodeID:       store.StoreID(pub),
		}

		mgr := store.NewStoreManager(cfg)

		handle := b.nextStoreHandle.Add(1)

		b.mu.Lock()
		b.stores[handle] = &storeState{
			manager: mgr,
			privKey: priv,
		}
		b.mu.Unlock()

		return handle, nil
	})
}

// addListing adds a listing to a store.
// Mercury.addListing(storeHandle, listing) -> listingID
func (b *jsBridge) addListing(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("addListing: requires storeHandle and listing")
		}

		handle := int64(args[0].Float())

		b.mu.RLock()
		ss, ok := b.stores[handle]
		b.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("addListing: unknown store handle %d", handle)
		}

		listingJS := args[1]

		// Generate a listing ID if not provided.
		listingID := ""
		if v := listingJS.Get("id"); !v.IsUndefined() && !v.IsNull() {
			listingID = v.String()
		}
		if listingID == "" {
			var err error
			listingID, err = generateListingID()
			if err != nil {
				return nil, fmt.Errorf("addListing: %w", err)
			}
		}

		listing := &store.Listing{
			ID:       listingID,
			StoreID:  ss.manager.StoreID(),
			Active:   true,
		}

		if v := listingJS.Get("title"); !v.IsUndefined() {
			listing.Title = v.String()
		}
		if v := listingJS.Get("description"); !v.IsUndefined() {
			listing.Description = v.String()
		}
		if v := listingJS.Get("price"); !v.IsUndefined() {
			listing.Price = uint64(v.Float())
		}
		if v := listingJS.Get("currency"); !v.IsUndefined() {
			listing.Currency = v.String()
		}
		if v := listingJS.Get("category"); !v.IsUndefined() {
			listing.Category = v.String()
		}
		if v := listingJS.Get("digital"); !v.IsUndefined() {
			listing.Digital = v.Bool()
		}
		if v := listingJS.Get("digitalCid"); !v.IsUndefined() {
			listing.DigitalCID = v.String()
		}
		if v := listingJS.Get("images"); !v.IsUndefined() && !v.IsNull() {
			imgs := make([]string, v.Length())
			for i := range imgs {
				imgs[i] = v.Index(i).String()
			}
			listing.Images = imgs
		}
		if v := listingJS.Get("keywords"); !v.IsUndefined() && !v.IsNull() {
			kws := make([]string, v.Length())
			for i := range kws {
				kws[i] = v.Index(i).String()
			}
			listing.Keywords = kws
		}
		if v := listingJS.Get("variants"); !v.IsUndefined() && !v.IsNull() {
			n := v.Length()
			variants := make([]store.Variant, 0, n)
			for i := 0; i < n; i++ {
				vi := v.Index(i)
				// Accept either plain strings or {name, value, priceModifier} objects.
				if vi.Type() == js.TypeString {
					variants = append(variants, store.Variant{Name: "option", Value: vi.String()})
					continue
				}
				variant := store.Variant{}
				if nv := vi.Get("name"); !nv.IsUndefined() && !nv.IsNull() {
					variant.Name = nv.String()
				}
				if vv := vi.Get("value"); !vv.IsUndefined() && !vv.IsNull() {
					variant.Value = vv.String()
				}
				if pm := vi.Get("priceModifier"); !pm.IsUndefined() && !pm.IsNull() {
					variant.PriceModifier = int64(pm.Float())
				}
				variants = append(variants, variant)
			}
			listing.Variants = variants
		}
		if v := listingJS.Get("shippingOptions"); !v.IsUndefined() && !v.IsNull() {
			n := v.Length()
			opts := make([]store.ShippingOption, 0, n)
			for i := 0; i < n; i++ {
				si := v.Index(i)
				opt := store.ShippingOption{}
				if nv := si.Get("name"); !nv.IsUndefined() && !nv.IsNull() {
					opt.Name = nv.String()
				}
				if pv := si.Get("price"); !pv.IsUndefined() && !pv.IsNull() {
					opt.Price = uint64(pv.Float())
				}
				if ev := si.Get("estDays"); !ev.IsUndefined() && !ev.IsNull() {
					opt.EstDays = ev.Int()
				}
				opts = append(opts, opt)
			}
			listing.ShippingOpts = opts
		}

		if err := ss.manager.AddListing(listing); err != nil {
			return nil, fmt.Errorf("addListing: %w", err)
		}

		// Index for search.
		b.searchIdx.Add(discovery.SearchEntry{
			ListingID:   listing.ID,
			Title:       listing.Title,
			Description: listing.Description,
			Category:    listing.Category,
			Price:       listing.Price,
			SellerID:    listing.StoreID,
			Keywords:    listing.Keywords,
			UpdatedAt:   time.Now(),
		})

		return listingID, nil
	})
}

// removeListing removes a listing from a store.
// Mercury.removeListing(storeHandle, listingID)
func (b *jsBridge) removeListing(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("removeListing: requires storeHandle and listingID")
		}

		handle := int64(args[0].Float())
		listingID := args[1].String()

		b.mu.RLock()
		ss, ok := b.stores[handle]
		b.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("removeListing: unknown store handle %d", handle)
		}

		if err := ss.manager.RemoveListing(listingID); err != nil {
			return nil, fmt.Errorf("removeListing: %w", err)
		}

		// Remove from search index.
		b.searchIdx.Remove(listingID)

		return nil, nil
	})
}

// getStore returns the store root data as JSON.
// Mercury.getStore(storeHandle) -> storeRoot
func (b *jsBridge) getStore(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("getStore: requires storeHandle")
		}

		handle := int64(args[0].Float())

		b.mu.RLock()
		ss, ok := b.stores[handle]
		b.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("getStore: unknown store handle %d", handle)
		}

		// Publish to get the latest signed root.
		root, err := ss.manager.Publish()
		if err != nil {
			return nil, fmt.Errorf("getStore: %w", err)
		}

		listings := ss.manager.AllListings()

		data, err := json.Marshal(map[string]interface{}{
			"storeId":      ss.manager.StoreID(),
			"name":         root.Name,
			"description":  root.Description,
			"categories":   root.Categories,
			"version":      root.Version,
			"merkleRoot":   hex.EncodeToString(root.MerkleRoot),
			"listingCount": len(listings),
			"listings":     marshalListings(listings),
		})
		if err != nil {
			return nil, fmt.Errorf("getStore: marshal: %w", err)
		}

		return string(data), nil
	})
}

// --- Order operations ---

// createOrder creates a new order.
// Mercury.createOrder(params) -> orderHandle
func (b *jsBridge) createOrder(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 || args[0].IsNull() || args[0].IsUndefined() {
			return nil, fmt.Errorf("createOrder: requires params")
		}

		p := args[0]
		orderID := ""
		if v := p.Get("id"); !v.IsUndefined() && !v.IsNull() {
			orderID = v.String()
		}
		if orderID == "" {
			var err error
			orderID, err = generateOrderID()
			if err != nil {
				return nil, fmt.Errorf("createOrder: %w", err)
			}
		}

		// Build an OrderIntent from JS parameters.
		intent := order.OrderIntent{
			ID:          orderID,
			BuyerPubKey: b.identityPub,
			Timestamp:   time.Now(),
		}
		if v := p.Get("sellerPubKey"); !v.IsUndefined() && !v.IsNull() {
			sellerPub, err := hex.DecodeString(v.String())
			if err != nil {
				return nil, fmt.Errorf("createOrder: invalid sellerPubKey hex: %w", err)
			}
			intent.SellerPubKey = sellerPub
		}
		if v := p.Get("listingId"); !v.IsUndefined() && !v.IsNull() {
			intent.ListingID = v.String()
		}
		if v := p.Get("variantId"); !v.IsUndefined() && !v.IsNull() {
			intent.VariantID = v.String()
		}
		if v := p.Get("quantity"); !v.IsUndefined() && !v.IsNull() {
			intent.Quantity = v.Int()
		}
		if v := p.Get("totalPrice"); !v.IsUndefined() && !v.IsNull() {
			intent.TotalPrice = uint64(v.Float())
		}
		if v := p.Get("currency"); !v.IsUndefined() && !v.IsNull() {
			intent.Currency = v.String()
		}
		if v := p.Get("chainId"); !v.IsUndefined() && !v.IsNull() {
			intent.ChainID = v.String()
		}
		if v := p.Get("memo"); !v.IsUndefined() && !v.IsNull() {
			intent.Memo = v.String()
		}

		// Sign the intent with the bridge identity key.
		if err := order.SignOrderIntent(&intent, b.identityPriv); err != nil {
			return nil, fmt.Errorf("createOrder: sign intent: %w", err)
		}

		// Register with the real OrderManager so subsequent operations work.
		createdOrder, err := b.orderMgr.CreateOrder(intent)
		if err != nil {
			return nil, fmt.Errorf("createOrder: %w", err)
		}

		handle := b.nextOrderHandle.Add(1)

		b.mu.Lock()
		b.orders[handle] = &orderState{
			orderID:     createdOrder.Intent.ID,
			storeHandle: -1,
			intent:      nil,
		}
		b.mu.Unlock()

		return handle, nil
	})
}

// acceptOrder marks an order as accepted.
// Mercury.acceptOrder(orderHandle)
func (b *jsBridge) acceptOrder(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("acceptOrder: requires orderHandle")
		}

		handle := int64(args[0].Float())

		b.mu.RLock()
		os, ok := b.orders[handle]
		b.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("acceptOrder: unknown order handle %d", handle)
		}

		// Bridge flow: create -> fund -> accept (simplified for browser).
		// MarkFunded transitions Pending -> Funded before we can accept.
		if _, err := b.orderMgr.MarkFunded(os.orderID); err != nil {
			return nil, fmt.Errorf("acceptOrder: mark funded: %w", err)
		}

		// Transition order through the state machine: Funded -> Accepted.
		fc := order.FulfillmentCommitment{
			OrderID:      os.orderID,
			SellerPubKey: b.identityPub,
			EstDelivery:  time.Now().Add(7 * 24 * time.Hour),
			Timestamp:    time.Now(),
		}
		if err := order.SignFulfillment(&fc, b.identityPriv); err != nil {
			return nil, fmt.Errorf("acceptOrder: sign fulfillment: %w", err)
		}

		o, err := b.orderMgr.AcceptOrder(os.orderID, fc)
		if err != nil {
			return nil, fmt.Errorf("acceptOrder: %w", err)
		}

		return map[string]interface{}{
			"orderID": o.Intent.ID,
			"status":  o.Status.String(),
		}, nil
	})
}

// completeOrder marks an order as completed.
// Mercury.completeOrder(orderHandle)
func (b *jsBridge) completeOrder(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("completeOrder: requires orderHandle")
		}

		handle := int64(args[0].Float())

		b.mu.RLock()
		os, ok := b.orders[handle]
		b.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("completeOrder: unknown order handle %d", handle)
		}

		o, err := b.orderMgr.Complete(os.orderID)
		if err != nil {
			return nil, fmt.Errorf("completeOrder: %w", err)
		}

		return map[string]interface{}{
			"orderID": o.Intent.ID,
			"status":  o.Status.String(),
		}, nil
	})
}

// disputeOrder files a dispute on an order.
// Mercury.disputeOrder(orderHandle, reason, evidence)
func (b *jsBridge) disputeOrder(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 3 {
			return nil, fmt.Errorf("disputeOrder: requires orderHandle, reason, and evidence")
		}

		handle := int64(args[0].Float())
		reason := args[1].String()
		evidence := args[2].String()

		b.mu.RLock()
		os, ok := b.orders[handle]
		b.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("disputeOrder: unknown order handle %d", handle)
		}

		o, err := b.orderMgr.Dispute(os.orderID, reason)
		if err != nil {
			return nil, fmt.Errorf("disputeOrder: %w", err)
		}

		return map[string]interface{}{
			"orderID":  o.Intent.ID,
			"status":   o.Status.String(),
			"reason":   reason,
			"evidence": evidence,
		}, nil
	})
}

// --- Messaging ---

// sendMessage sends a message to a recipient via a Minerva 1:1
// Conversation. Encryption + signing is handled by the Conversation
// (sender-key or double-ratchet depending on peer capability); the
// JS surface stays the same.
// Mercury.sendMessage(recipientID, content)
func (b *jsBridge) sendMessage(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("sendMessage: requires recipientID and content")
		}

		recipientHex := args[0].String()
		content := args[1].String()

		recipientPubKey, err := hex.DecodeString(recipientHex)
		if err != nil {
			return nil, fmt.Errorf("sendMessage: invalid recipient hex: %w", err)
		}
		if len(recipientPubKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("sendMessage: invalid recipient pubkey size %d", len(recipientPubKey))
		}

		senderPub := b.identityPub

		conv, err := b.openOrAttachConversation(ed25519.PublicKey(recipientPubKey))
		if err != nil {
			return nil, fmt.Errorf("sendMessage: open conversation: %w", err)
		}

		msg, err := conv.Send(context.Background(), []byte(content))
		if err != nil {
			return nil, fmt.Errorf("sendMessage: %w", err)
		}

		// Cache plaintext for our own messages so the UI can render them
		// without re-decrypting (decrypt would still work, but the hot path
		// stays cheap).
		b.mu.Lock()
		b.outboundPlaintext[msg.ID] = content
		b.mu.Unlock()

		return map[string]interface{}{
			"id":        string(msg.ID),
			"from":      hex.EncodeToString(senderPub),
			"to":        recipientHex,
			"text":      content,
			"timestamp": msg.SentAt.Format(time.RFC3339),
			"time":      msg.SentAt.Format("15:04"),
		}, nil
	})
}

// getMessages returns messages for a peer (conversation history, decrypted).
// Mercury.getMessages(peerID) -> messages
func (b *jsBridge) getMessages(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("getMessages: requires peerID")
		}

		peerID := args[0].String()
		peerBytes, err := hex.DecodeString(peerID)
		if err != nil {
			return nil, fmt.Errorf("getMessages: invalid peer hex: %w", err)
		}
		if len(peerBytes) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("getMessages: invalid peer pubkey size %d", len(peerBytes))
		}

		conv, err := b.openOrAttachConversation(ed25519.PublicKey(peerBytes))
		if err != nil {
			return nil, fmt.Errorf("getMessages: %w", err)
		}
		msgs := conv.History(time.Time{})

		data, err := json.Marshal(b.marshalConvMessages(conv, msgs))
		if err != nil {
			return nil, fmt.Errorf("getMessages: marshal: %w", err)
		}

		return string(data), nil
	})
}

// --- Discovery ---

// searchListings performs a full-text search across indexed listings.
// Mercury.searchListings(query, limit) -> results
func (b *jsBridge) searchListings(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("searchListings: requires query")
		}

		query := args[0].String()
		limit := 50
		if len(args) > 1 && !args[1].IsUndefined() && !args[1].IsNull() {
			limit = args[1].Int()
		}

		results := b.searchIdx.Search(query, limit)

		data, err := json.Marshal(results)
		if err != nil {
			return nil, fmt.Errorf("searchListings: marshal: %w", err)
		}

		return string(data), nil
	})
}

// browseCategory returns listings in a category.
// Mercury.browseCategory(category) -> listings
func (b *jsBridge) browseCategory(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("browseCategory: requires category")
		}

		category := args[0].String()

		// Try the search index first (has all indexed listings).
		results := b.searchIdx.ByCategory(category)
		if len(results) > 0 {
			data, err := json.Marshal(results)
			if err != nil {
				return nil, fmt.Errorf("browseCategory: marshal: %w", err)
			}
			return string(data), nil
		}

		// Fall back to category manager gossip cache.
		listings := b.catMgr.Browse(category)
		data, err := json.Marshal(listings)
		if err != nil {
			return nil, fmt.Errorf("browseCategory: marshal: %w", err)
		}

		return string(data), nil
	})
}

// --- Reputation ---

// getReputation returns the composite reputation score for a pubkey.
// Mercury.getReputation(pubkey) -> score
func (b *jsBridge) getReputation(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("getReputation: requires pubkey")
		}

		pubkeyHex := args[0].String()
		pubkey, err := hex.DecodeString(pubkeyHex)
		if err != nil {
			return nil, fmt.Errorf("getReputation: invalid hex pubkey: %w", err)
		}

		score := b.mercuryRep.CompositeScore(ed25519.PublicKey(pubkey))
		summary := b.mercuryRep.Summary(ed25519.PublicKey(pubkey))

		data, err := json.Marshal(map[string]interface{}{
			"score":         score,
			"isNew":         summary.IsNew,
			"totalTrades":   summary.TotalTrades,
			"positiveRate":  summary.PositiveRate,
			"averageRating": summary.AverageRating,
			"reviewCount":   summary.ReviewCount,
		})
		if err != nil {
			return nil, fmt.Errorf("getReputation: marshal: %w", err)
		}

		return string(data), nil
	})
}

// submitReview submits a marketplace review for a seller.
// Mercury.submitReview(sellerPubKey, orderID, rating, title, body)
func (b *jsBridge) submitReview(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 5 {
			return nil, fmt.Errorf("submitReview: requires sellerPubKey, orderID, rating, title, body")
		}

		sellerHex := args[0].String()
		orderID := args[1].String()
		rating := args[2].Int()
		title := args[3].String()
		body := args[4].String()

		sellerPubKey, err := hex.DecodeString(sellerHex)
		if err != nil {
			return nil, fmt.Errorf("submitReview: invalid seller pubkey hex: %w", err)
		}

		// Use the bridge's persistent identity keypair.
		reviewerPub := b.identityPub
		reviewerPriv := b.identityPriv

		review := reputation.Review{
			ReviewerPubKey: reviewerPub,
			SellerPubKey:   ed25519.PublicKey(sellerPubKey),
			OrderID:        orderID,
			Rating:         rating,
			Title:          title,
			Body:           body,
			Timestamp:      time.Now(),
		}

		if err := review.Sign(reviewerPriv); err != nil {
			return nil, fmt.Errorf("submitReview: sign: %w", err)
		}

		if err := b.reviewSvc.Submit(review); err != nil {
			return nil, fmt.Errorf("submitReview: %w", err)
		}

		return nil, nil
	})
}

// --- Profile ---

// getProfile returns the user profile for a pubkey.
// Mercury.getProfile(pubkey) -> profile
func (b *jsBridge) getProfile(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("getProfile: requires pubkey")
		}

		pubkeyHex := args[0].String()
		pubkey, err := hex.DecodeString(pubkeyHex)
		if err != nil {
			return nil, fmt.Errorf("getProfile: invalid hex pubkey: %w", err)
		}

		prof, err := b.profService.Get(ed25519.PublicKey(pubkey))
		if err != nil {
			return nil, fmt.Errorf("getProfile: %w", err)
		}
		if prof == nil {
			return nil, nil
		}

		data, err := json.Marshal(map[string]interface{}{
			"pubKey":    hex.EncodeToString(prof.PubKey),
			"name":      prof.Name,
			"bio":       prof.Bio,
			"avatarCid": prof.AvatarCID,
			"location":  prof.Location,
			"joinedAt":  prof.JoinedAt.Format(time.RFC3339),
			"updatedAt": prof.UpdatedAt.Format(time.RFC3339),
		})
		if err != nil {
			return nil, fmt.Errorf("getProfile: marshal: %w", err)
		}

		return string(data), nil
	})
}

// setProfile publishes a user profile.
// Mercury.setProfile(profile)
func (b *jsBridge) setProfile(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 || args[0].IsNull() || args[0].IsUndefined() {
			return nil, fmt.Errorf("setProfile: requires profile object")
		}

		p := args[0]

		// Always use the bridge's persistent identity keypair.
		pub := b.identityPub
		priv := b.identityPriv

		now := time.Now()
		prof := profile.UserProfile{
			PubKey:    pub,
			JoinedAt:  now,
			UpdatedAt: now,
		}

		if v := p.Get("name"); !v.IsUndefined() {
			prof.Name = v.String()
		}
		if v := p.Get("bio"); !v.IsUndefined() {
			prof.Bio = v.String()
		}
		if v := p.Get("avatarCid"); !v.IsUndefined() {
			prof.AvatarCID = v.String()
		}
		if v := p.Get("location"); !v.IsUndefined() {
			prof.Location = v.String()
		}

		if err := prof.Sign(priv); err != nil {
			return nil, fmt.Errorf("setProfile: sign: %w", err)
		}

		if err := b.profService.Set(prof); err != nil {
			return nil, fmt.Errorf("setProfile: %w", err)
		}

		return hex.EncodeToString(pub), nil
	})
}

// --- Utility functions ---

// toJSValue converts a Go value to a JS value.
func toJSValue(v interface{}) interface{} {
	if v == nil {
		return js.Null()
	}
	switch val := v.(type) {
	case string:
		return val
	case int64:
		return val
	case float64:
		return val
	case bool:
		return val
	case map[string]interface{}:
		data, err := json.Marshal(val)
		if err != nil {
			return js.Null()
		}
		return string(data)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return js.Null()
		}
		return string(data)
	}
}

// marshalListings converts a slice of listings to JSON-friendly maps.
func marshalListings(listings []*store.Listing) []map[string]interface{} {
	result := make([]map[string]interface{}, len(listings))
	for i, l := range listings {
		result[i] = map[string]interface{}{
			"id":          l.ID,
			"title":       l.Title,
			"description": l.Description,
			"price":       l.Price,
			"currency":    l.Currency,
			"category":    l.Category,
			"images":      l.Images,
			"digital":     l.Digital,
			"digitalCid":  l.DigitalCID,
			"createdAt":   l.CreatedAt.Format(time.RFC3339),
			"updatedAt":   l.UpdatedAt.Format(time.RFC3339),
		}
	}
	return result
}

// marshalConvMessages converts a Minerva Conversation history to the
// JSON-friendly shape the UI consumes. Each message body is decrypted
// against the Conversation's sender-key / ratchet state; our own outgoing
// plaintexts are sourced from the in-memory cache so we never re-run our
// own ratchet path.
func (b *jsBridge) marshalConvMessages(conv *minerva_messaging.Conversation, msgs []minerva_messaging.Message) []map[string]interface{} {
	meHex := hex.EncodeToString(b.identityPub)
	result := make([]map[string]interface{}, 0, len(msgs))
	b.mu.RLock()
	out := make(map[minerva_messaging.MessageID]string, len(b.outboundPlaintext))
	for k, v := range b.outboundPlaintext {
		out[k] = v
	}
	b.mu.RUnlock()
	for _, m := range msgs {
		fromHex := string(m.Author)
		text := ""
		if cached, ok := out[m.ID]; ok && fromHex == meHex {
			text = cached
		} else {
			if plain, err := conv.DecryptBody(&m); err == nil {
				text = string(plain)
			} else {
				text = string(m.Body)
			}
		}
		result = append(result, map[string]interface{}{
			"id":        string(m.ID),
			"from":      fromHex,
			"content":   text,
			"timestamp": m.SentAt.Format(time.RFC3339),
		})
	}
	return result
}

// generateRandomID creates a cryptographically random hex ID using crypto/rand.
func generateRandomID() (string, error) {
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return "", fmt.Errorf("generate random ID: %w", err)
	}
	return hex.EncodeToString(idBytes[:]), nil
}

// generateListingID creates a unique listing ID.
func generateListingID() (string, error) {
	return generateRandomID()
}

// generateOrderID creates a unique order ID.
func generateOrderID() (string, error) {
	return generateRandomID()
}

// generateMessageID creates a unique message ID.
func generateMessageID() (string, error) {
	return generateRandomID()
}

// Ensure unused imports are referenced.
var (
	_ = escrow.StatusPending
)
