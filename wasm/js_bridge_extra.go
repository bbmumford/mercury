//go:build js && wasm

/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 *
 * Additional JS-callable bridge methods wired into window.Mercury.
 * These extend js_bridge.go with the set of functions the marketplace
 * example expects: identity/auth, passkeys, listings/browse/categories,
 * orders (confirm/dispute), conversations, peers, reviews, profile
 * updates, disputes, and the synchronous Mercury.state snapshot.
 */
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"syscall/js"
	"time"

	"github.com/bbmumford/mercury/arbitration"
	"github.com/bbmumford/mercury/discovery"
	"github.com/bbmumford/mercury/order"
	"github.com/bbmumford/mercury/profile"
	"github.com/bbmumford/mercury/store"

	minerva_messaging "github.com/bbmumford/minerva/messaging"

	plutusArb "github.com/bbmumford/plutus/arbitrator"
	plutusWallet "github.com/bbmumford/plutus/wallet"
)

// --- Identity / auth ---

// generateMnemonic returns a fresh 24-word BIP39 mnemonic as a JS array.
// Synchronous (not a Promise) because the UI calls it from a click handler.
// Mercury.generateMnemonic() -> string[]
func (b *jsBridge) generateMnemonic(this js.Value, args []js.Value) interface{} {
	mnemonic, err := plutusWallet.GenerateMnemonic()
	if err != nil {
		js.Global().Get("console").Call("error", "generateMnemonic: "+err.Error())
		return js.ValueOf([]interface{}{})
	}
	words := strings.Fields(mnemonic)
	out := make([]interface{}, len(words))
	for i, w := range words {
		out[i] = w
	}
	return js.ValueOf(out)
}

// createIdentity derives an Ed25519 identity from a BIP39 mnemonic and
// installs it as the current session identity.
// Mercury.createIdentity(mnemonic) -> Promise<{pubkey, displayName}>
func (b *jsBridge) createIdentity(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("createIdentity: requires mnemonic")
		}
		mnemonic, err := coerceMnemonic(args[0])
		if err != nil {
			return nil, fmt.Errorf("createIdentity: %w", err)
		}
		seed, err := plutusWallet.MnemonicToSeed(mnemonic, "")
		if err != nil {
			return nil, fmt.Errorf("createIdentity: %w", err)
		}
		if err := b.setIdentityFromSeed(seed); err != nil {
			return nil, fmt.Errorf("createIdentity: %w", err)
		}
		b.mu.Lock()
		b.mnemonic = mnemonic
		b.mu.Unlock()
		b.publishState()
		return map[string]interface{}{
			"pubkey":      b.pubHex(),
			"displayName": "",
		}, nil
	})
}

// importIdentity is an alias for createIdentity, used by the UI when the
// user pastes an existing recovery phrase.
// Mercury.importIdentity(mnemonic) -> Promise<{pubkey, displayName}>
func (b *jsBridge) importIdentity(this js.Value, args []js.Value) interface{} {
	return b.createIdentity(this, args)
}

// logout clears the in-memory session state but leaves registered passkey
// metadata intact so the user can sign in again with the same passkey.
// Mercury.logout()
func (b *jsBridge) logout(this js.Value, args []js.Value) interface{} {
	b.mu.Lock()
	b.authenticated = false
	b.mnemonic = ""
	b.currentName = ""
	b.mu.Unlock()
	b.publishState()
	return nil
}

// getMnemonic returns the current identity's 24-word mnemonic as a JS array,
// or an empty array if the identity was generated from raw entropy or the
// user is logged out.
// Mercury.getMnemonic() -> string[]
func (b *jsBridge) getMnemonic(this js.Value, args []js.Value) interface{} {
	b.mu.RLock()
	mnemonic := b.mnemonic
	b.mu.RUnlock()
	if mnemonic == "" {
		return js.ValueOf([]interface{}{})
	}
	words := strings.Fields(mnemonic)
	out := make([]interface{}, len(words))
	for i, w := range words {
		out[i] = w
	}
	return js.ValueOf(out)
}

// getIdentity returns the current identity, or null if logged out.
// Mercury.getIdentity() -> {pubKey, name, joinedAt} | null
func (b *jsBridge) getIdentity(this js.Value, args []js.Value) interface{} {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if !b.authenticated {
		return js.Null()
	}
	return mapToJSObject(map[string]interface{}{
		"pubKey":   hex.EncodeToString(b.identityPub),
		"name":     b.currentName,
		"joinedAt": b.joinedAt.Format(time.RFC3339),
	})
}

// --- Passkeys ---
//
// The WebAuthn ceremony (navigator.credentials.create / get) happens in JS.
// The Go bridge only tracks metadata so the Settings UI can render the list.
// Real PRF-based seed unlock is delegated to Minerva's PasskeyManager, which
// the marketplace example invokes via its own JS glue code and then calls
// authenticatePasskey() here to mark the session authenticated.

// registerPasskey records metadata for a newly registered passkey.
// Mercury.registerPasskey(label?) -> Promise<{id, label, createdAt}>
func (b *jsBridge) registerPasskey(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		label := ""
		if len(args) > 0 && !args[0].IsUndefined() && !args[0].IsNull() {
			label = args[0].String()
		}
		b.mu.RLock()
		n := len(b.passkeys)
		b.mu.RUnlock()
		if label == "" {
			label = fmt.Sprintf("Passkey %d", n+1)
		}
		id, err := generateRandomID()
		if err != nil {
			return nil, fmt.Errorf("registerPasskey: %w", err)
		}
		rec := passkeyRecord{
			ID:        id,
			Label:     label,
			CreatedAt: time.Now(),
		}
		b.mu.Lock()
		b.passkeys = append(b.passkeys, rec)
		b.mu.Unlock()
		b.publishState()
		return map[string]interface{}{
			"id":        rec.ID,
			"label":     rec.Label,
			"createdAt": rec.CreatedAt.Format(time.RFC3339),
		}, nil
	})
}

// authenticatePasskey flips the session to authenticated, assuming the JS
// layer has completed the WebAuthn PRF unlock and installed the seed via
// createIdentity. Returns the current pubkey.
// Mercury.authenticatePasskey() -> Promise<{pubkey}>
func (b *jsBridge) authenticatePasskey(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		b.mu.RLock()
		hasPK := len(b.passkeys) > 0
		b.mu.RUnlock()
		if !hasPK {
			return nil, fmt.Errorf("authenticatePasskey: no passkey registered")
		}
		b.mu.Lock()
		b.authenticated = true
		b.mu.Unlock()
		b.publishState()
		return map[string]interface{}{"pubkey": b.pubHex()}, nil
	})
}

// getPasskeys returns registered passkey metadata.
// Mercury.getPasskeys() -> Promise<[{id, label, createdAt}]>
func (b *jsBridge) getPasskeys(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		b.mu.RLock()
		defer b.mu.RUnlock()
		out := make([]map[string]interface{}, len(b.passkeys))
		for i, p := range b.passkeys {
			out[i] = map[string]interface{}{
				"id":        p.ID,
				"label":     p.Label,
				"createdAt": p.CreatedAt.Format(time.RFC3339),
			}
		}
		return out, nil
	})
}

// removePasskey unregisters a passkey by credential ID.
// Mercury.removePasskey(credentialId) -> Promise<void>
func (b *jsBridge) removePasskey(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("removePasskey: requires credentialId")
		}
		id := args[0].String()
		b.mu.Lock()
		found := false
		for i, p := range b.passkeys {
			if p.ID == id {
				b.passkeys = append(b.passkeys[:i], b.passkeys[i+1:]...)
				found = true
				break
			}
		}
		b.mu.Unlock()
		if !found {
			return nil, fmt.Errorf("removePasskey: credential %s not found", id)
		}
		b.publishState()
		return nil, nil
	})
}

// --- Listings / browse ---

// getListings returns all indexed listings, optionally filtered by category.
// Mercury.getListings(categoryId?) -> Promise<[listing]>
func (b *jsBridge) getListings(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		var cat string
		if len(args) > 0 && !args[0].IsUndefined() && !args[0].IsNull() {
			cat = args[0].String()
		}
		var entries []discovery.SearchEntry
		if cat != "" {
			entries = b.searchIdx.ByCategory(cat)
		} else {
			entries = b.searchIdx.Search("", 500)
		}
		return marshalSearchEntries(entries), nil
	})
}

// getListing returns a single listing by ID (searches all local stores).
// Mercury.getListing(id) -> Promise<listing | null>
func (b *jsBridge) getListing(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("getListing: requires id")
		}
		id := args[0].String()
		b.mu.RLock()
		defer b.mu.RUnlock()
		for _, ss := range b.stores {
			l, err := ss.manager.GetListing(id)
			if err == nil && l != nil {
				return marshalListingFull(l), nil
			}
		}
		return nil, nil
	})
}

// getCategories returns the category taxonomy with counts computed from the
// current search index. The seed taxonomy matches what the UI expects so the
// home / browse pages render consistently even when no listings exist yet.
// Mercury.getCategories() -> Promise<[{id, name, icon, count}]>
func (b *jsBridge) getCategories(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		counts := map[string]int{}
		for _, e := range b.searchIdx.Search("", 10000) {
			if e.Category != "" {
				counts[e.Category]++
			}
		}
		seen := map[string]bool{}
		out := []map[string]interface{}{}
		add := func(id, name, icon string) {
			if seen[id] {
				return
			}
			seen[id] = true
			out = append(out, map[string]interface{}{
				"id":    id,
				"name":  name,
				"icon":  icon,
				"count": counts[id],
			})
		}
		add("electronics", "Electronics", "\U0001F4F1")
		add("art", "Art & Collectibles", "\U0001F3A8")
		add("services", "Services", "\U0001F6E0")
		add("fashion", "Fashion", "\U0001F45F")
		add("books", "Books", "\U0001F4DA")
		add("home", "Home & Garden", "\U0001F3E1")
		add("digital", "Digital Goods", "\U0001F4BE")
		add("other", "Other", "\U0001F4E6")
		for cat := range counts {
			if !seen[cat] {
				add(cat, cat, "\U0001F4E6")
			}
		}
		return out, nil
	})
}

// --- Orders ---

// getOrders returns all orders the current identity is party to.
// Mercury.getOrders() -> Promise<[order]>
func (b *jsBridge) getOrders(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		orders := b.orderMgr.List()
		me := b.identityPub
		out := []map[string]interface{}{}
		for _, o := range orders {
			role := ""
			if ed25519PubEqual(o.Intent.BuyerPubKey, me) {
				role = "buyer"
			} else if ed25519PubEqual(o.Intent.SellerPubKey, me) {
				role = "seller"
			}
			out = append(out, marshalOrder(o, role))
		}
		return out, nil
	})
}

// getOrder returns a single order with a timeline derived from its history.
// Mercury.getOrder(id) -> Promise<order | null>
func (b *jsBridge) getOrder(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("getOrder: requires id")
		}
		id := args[0].String()
		o, err := b.orderMgr.Get(id)
		if err != nil {
			return nil, nil
		}
		me := b.identityPub
		role := ""
		if ed25519PubEqual(o.Intent.BuyerPubKey, me) {
			role = "buyer"
		} else if ed25519PubEqual(o.Intent.SellerPubKey, me) {
			role = "seller"
		}
		return marshalOrder(o, role), nil
	})
}

// confirmReceipt marks an order as completed (buyer received goods). If the
// order is still in Accepted, transitions through Fulfilled first so the
// demo flow can complete without the seller manually marking it fulfilled.
// Mercury.confirmReceipt(orderId) -> Promise<{orderID, status}>
func (b *jsBridge) confirmReceipt(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("confirmReceipt: requires orderId")
		}
		id := args[0].String()
		o, err := b.orderMgr.Get(id)
		if err != nil {
			return nil, fmt.Errorf("confirmReceipt: %w", err)
		}
		if o.Status == order.OrderAccepted {
			_, _ = b.orderMgr.MarkFulfilled(id)
		}
		completed, err := b.orderMgr.Complete(id)
		if err != nil {
			return nil, fmt.Errorf("confirmReceipt: %w", err)
		}
		return map[string]interface{}{
			"orderID": completed.Intent.ID,
			"status":  completed.Status.String(),
		}, nil
	})
}

// fileDispute opens a dispute against an order by id (UI-friendly wrapper).
// Records a DisputeCase for later retrieval via getDisputes/getDispute.
// Mercury.fileDispute(orderId, reason) -> Promise<{caseId, orderID, status, reason}>
func (b *jsBridge) fileDispute(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("fileDispute: requires orderId and reason")
		}
		id := args[0].String()
		reason := args[1].String()
		o, err := b.orderMgr.Dispute(id, reason)
		if err != nil {
			return nil, fmt.Errorf("fileDispute: %w", err)
		}
		caseID, idErr := generateRandomID()
		if idErr != nil {
			return nil, fmt.Errorf("fileDispute: %w", idErr)
		}
		dc := &arbitration.DisputeCase{
			ID:      caseID,
			OrderID: o.Intent.ID,
			FiledBy: b.identityPub,
			Reason:  reason,
			Status:  arbitration.DisputeFiled,
			FiledAt: time.Now(),
		}
		b.mu.Lock()
		b.disputes[caseID] = dc
		b.mu.Unlock()
		return map[string]interface{}{
			"caseId":  caseID,
			"orderID": o.Intent.ID,
			"status":  o.Status.String(),
			"reason":  reason,
		}, nil
	})
}

// --- Messaging extensions ---

// walkConversations iterates every Messenger-managed 1:1 Conversation
// that matches the mercury.dm.<a>:<b> prefix and invokes visit for each
// peer PeerID / Conversation / history tuple. The Messenger's internal
// map is not directly enumerable, so we re-derive candidate ChannelIDs
// from the peerActivity map (every peer we have ever exchanged a message
// with shows up there). This is a deliberate trade-off: conversations
// with peers we have never heard from do not appear until after the first
// inbound message, at which point their ChannelID is discovered.
func (b *jsBridge) walkConversations(visit func(peerHex string, conv *minerva_messaging.Conversation, history []minerva_messaging.Message)) {
	meHex := b.pubHex()
	b.mu.RLock()
	peers := make([]string, 0, len(b.peerActivity))
	for peer := range b.peerActivity {
		if peer == meHex {
			continue
		}
		peers = append(peers, peer)
	}
	b.mu.RUnlock()
	for _, peerHex := range peers {
		peerBytes, err := hex.DecodeString(peerHex)
		if err != nil || len(peerBytes) != ed25519.PublicKeySize {
			continue
		}
		chanID := conversationChannelID(b.identityPub, ed25519.PublicKey(peerBytes))
		conv := b.messenger.Conversation(chanID)
		if conv == nil {
			continue
		}
		visit(peerHex, conv, conv.History(time.Time{}))
	}
}

// getConversations returns one entry per peer with the last message summary.
// Mercury.getConversations() -> Promise<[{peerId, peerName, lastMessage, lastTime, unread}]>
func (b *jsBridge) getConversations(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		out := []map[string]interface{}{}
		b.walkConversations(func(peerHex string, conv *minerva_messaging.Conversation, history []minerva_messaging.Message) {
			if len(history) == 0 {
				return
			}
			last := history[len(history)-1]
			plaintext := b.tryDecrypt(conv, last)
			out = append(out, map[string]interface{}{
				"peerId":      peerHex,
				"peerName":    shortHex(peerHex),
				"lastMessage": plaintext,
				"lastTime":    last.SentAt.Format("15:04"),
				"unread":      0,
			})
		})
		return out, nil
	})
}

// getConversation returns the full message history with a peer, plaintext-
// decrypted where possible. Incoming messages run through the
// Conversation's sender-key / double-ratchet path; our own outgoing
// messages are sourced from the plaintext cache populated by sendMessage.
// Mercury.getConversation(peerId) -> Promise<{peerId, peerName, messages:[]}>
func (b *jsBridge) getConversation(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("getConversation: requires peerId")
		}
		peerHex := args[0].String()
		peerBytes, err := hex.DecodeString(peerHex)
		if err != nil {
			return nil, fmt.Errorf("getConversation: invalid peer hex: %w", err)
		}
		if len(peerBytes) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("getConversation: invalid peer pubkey size %d", len(peerBytes))
		}

		conv, err := b.openOrAttachConversation(ed25519.PublicKey(peerBytes))
		if err != nil {
			return nil, fmt.Errorf("getConversation: %w", err)
		}
		history := conv.History(time.Time{})
		meHex := b.pubHex()
		msgs := []map[string]interface{}{}
		for _, m := range history {
			side := "them"
			if string(m.Author) == meHex {
				side = "me"
			}
			plaintext := b.tryDecrypt(conv, m)
			msgs = append(msgs, map[string]interface{}{
				"id":   string(m.ID),
				"from": side,
				"text": plaintext,
				"time": m.SentAt.Format("15:04"),
			})
		}
		return map[string]interface{}{
			"peerId":   peerHex,
			"peerName": shortHex(peerHex),
			"messages": msgs,
		}, nil
	})
}

// getPeers returns known peers (currently those with whom we have observed
// message activity). In a fuller build this would also include active
// connections from the Minerva discovery layer.
// Mercury.getPeers() -> Promise<[{id, name, online, latency}]>
func (b *jsBridge) getPeers(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		meHex := b.pubHex()
		seen := map[string]bool{}
		out := []map[string]interface{}{}
		b.mu.RLock()
		peers := make([]string, 0, len(b.peerActivity))
		for peer := range b.peerActivity {
			peers = append(peers, peer)
		}
		b.mu.RUnlock()
		for _, peerHex := range peers {
			if peerHex == meHex || seen[peerHex] {
				continue
			}
			seen[peerHex] = true
			out = append(out, map[string]interface{}{
				"id":      peerHex,
				"name":    shortHex(peerHex),
				"online":  false,
				"latency": "-",
			})
		}
		return out, nil
	})
}

// --- Reputation extensions ---

// getReviews returns all reviews for a seller.
// Mercury.getReviews(sellerPubkey) -> Promise<[{author, rating, text, date}]>
func (b *jsBridge) getReviews(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("getReviews: requires sellerPubkey")
		}
		pkHex := args[0].String()
		pk, err := hex.DecodeString(pkHex)
		if err != nil {
			return nil, fmt.Errorf("getReviews: invalid hex: %w", err)
		}
		reviews := b.reviewSvc.ForSeller(ed25519.PublicKey(pk))
		out := []map[string]interface{}{}
		for _, r := range reviews {
			author := hex.EncodeToString(r.ReviewerPubKey)
			text := r.Body
			if r.Title != "" {
				text = r.Title + " — " + r.Body
			}
			out = append(out, map[string]interface{}{
				"author":     author,
				"authorName": shortHex(author),
				"rating":     r.Rating,
				"text":       text,
				"date":       r.Timestamp.Format("2006-01-02"),
			})
		}
		return out, nil
	})
}

// --- Profiles ---

// updateProfile updates and publishes the current user's profile. Accepts
// {name|displayName, bio, avatarCid, location}.
// Mercury.updateProfile(data) -> Promise<{pubKey}>
func (b *jsBridge) updateProfile(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 || args[0].IsNull() || args[0].IsUndefined() {
			return nil, fmt.Errorf("updateProfile: requires profile object")
		}
		p := args[0]
		now := time.Now()
		prof := profile.UserProfile{
			PubKey:    b.identityPub,
			JoinedAt:  b.joinedAt,
			UpdatedAt: now,
		}
		if prof.JoinedAt.IsZero() {
			prof.JoinedAt = now
		}
		if v := p.Get("name"); !v.IsUndefined() && !v.IsNull() {
			prof.Name = v.String()
		}
		if v := p.Get("displayName"); !v.IsUndefined() && !v.IsNull() {
			prof.Name = v.String()
		}
		if v := p.Get("bio"); !v.IsUndefined() && !v.IsNull() {
			prof.Bio = v.String()
		}
		if v := p.Get("avatarCid"); !v.IsUndefined() && !v.IsNull() {
			prof.AvatarCID = v.String()
		}
		if v := p.Get("location"); !v.IsUndefined() && !v.IsNull() {
			prof.Location = v.String()
		}
		if err := prof.Sign(b.identityPriv); err != nil {
			return nil, fmt.Errorf("updateProfile: sign: %w", err)
		}
		if err := b.profService.Set(prof); err != nil {
			return nil, fmt.Errorf("updateProfile: %w", err)
		}
		b.mu.Lock()
		b.currentName = prof.Name
		b.mu.Unlock()
		b.publishState()
		return map[string]interface{}{
			"pubKey": hex.EncodeToString(b.identityPub),
		}, nil
	})
}

// --- Disputes ---

// getDisputes returns all dispute cases known to this session.
// Mercury.getDisputes() -> Promise<[dispute]>
func (b *jsBridge) getDisputes(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		b.mu.RLock()
		defer b.mu.RUnlock()
		out := []map[string]interface{}{}
		for _, dc := range b.disputes {
			out = append(out, b.marshalDispute(dc))
		}
		return out, nil
	})
}

// getDispute returns a single dispute by case ID.
// Mercury.getDispute(id) -> Promise<dispute | null>
func (b *jsBridge) getDispute(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("getDispute: requires id")
		}
		id := args[0].String()
		b.mu.RLock()
		defer b.mu.RUnlock()
		dc, ok := b.disputes[id]
		if !ok {
			return nil, nil
		}
		return b.marshalDispute(dc), nil
	})
}

// --- State snapshot ---

// buildState constructs the Mercury.state object the UI reads synchronously.
func (b *jsBridge) buildState() js.Value {
	b.mu.RLock()
	defer b.mu.RUnlock()

	passkeys := make([]interface{}, len(b.passkeys))
	for i, p := range b.passkeys {
		passkeys[i] = mapToJSObject(map[string]interface{}{
			"id":        p.ID,
			"label":     p.Label,
			"createdAt": p.CreatedAt.Format(time.RFC3339),
		})
	}

	var currentUser interface{} = js.Null()
	pubHex := hex.EncodeToString(b.identityPub)
	if b.authenticated {
		currentUser = mapToJSObject(map[string]interface{}{
			"pubkey":      pubHex,
			"displayName": b.currentName,
			"avatar":      firstChar(b.currentName),
		})
	}

	obj := js.Global().Get("Object").New()
	obj.Set("authenticated", b.authenticated)
	obj.Set("currentUser", currentUser)
	obj.Set("pubkey", pubHex)
	obj.Set("passkeys", js.ValueOf(passkeys))
	obj.Set("conversations", js.ValueOf([]interface{}{}))
	obj.Set("listings", js.ValueOf([]interface{}{}))
	obj.Set("peers", js.ValueOf([]interface{}{}))
	return obj
}

// publishState re-publishes the current state snapshot to window.Mercury.state.
// Called from mutation paths (login/logout, profile update, passkey changes).
func (b *jsBridge) publishState() {
	mercury := js.Global().Get("Mercury")
	if mercury.IsUndefined() || mercury.IsNull() {
		return
	}
	mercury.Set("state", b.buildState())
}

// --- Helpers used by the extension methods ---

// coerceMnemonic accepts either a JS string (space-separated words) or a JS
// array of strings and returns a canonical space-joined mnemonic.
func coerceMnemonic(v js.Value) (string, error) {
	if v.IsUndefined() || v.IsNull() {
		return "", fmt.Errorf("mnemonic is required")
	}
	if v.Type() == js.TypeString {
		return strings.Join(strings.Fields(v.String()), " "), nil
	}
	n := v.Length()
	words := make([]string, 0, n)
	for i := 0; i < n; i++ {
		w := strings.TrimSpace(v.Index(i).String())
		if w == "" {
			continue
		}
		words = append(words, w)
	}
	return strings.Join(words, " "), nil
}

// ed25519PubEqual compares two public keys by raw bytes.
func ed25519PubEqual(a, b ed25519.PublicKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tryDecrypt decrypts an incoming Conversation message. Outgoing messages
// whose plaintext we cached at send time are served from the cache so the
// UI can render "me" side text even when the ciphertext would otherwise
// only decrypt under the peer's ratchet. Falls back to raw body bytes if
// everything else fails — the UI already renders best-effort strings.
func (b *jsBridge) tryDecrypt(conv *minerva_messaging.Conversation, m minerva_messaging.Message) string {
	meHex := b.pubHex()
	if string(m.Author) == meHex {
		b.mu.RLock()
		cached, ok := b.outboundPlaintext[m.ID]
		b.mu.RUnlock()
		if ok {
			return cached
		}
	}
	if conv != nil {
		if plain, err := conv.DecryptBody(&m); err == nil {
			return string(plain)
		}
	}
	return string(m.Body)
}

// marshalListingFull returns a listing in the full shape the UI expects
// for a listing detail page.
func marshalListingFull(l *store.Listing) map[string]interface{} {
	variants := make([]string, 0, len(l.Variants))
	for _, v := range l.Variants {
		variants = append(variants, v.Value)
	}
	shipping := make([]map[string]interface{}, 0, len(l.ShippingOpts))
	for _, s := range l.ShippingOpts {
		shipping = append(shipping, map[string]interface{}{
			"name":    s.Name,
			"price":   s.Price,
			"estDays": s.EstDays,
		})
	}
	return map[string]interface{}{
		"id":           l.ID,
		"title":        l.Title,
		"description":  l.Description,
		"price":        formatPrice(l.Price, l.Currency),
		"priceRaw":     l.Price,
		"priceUsd":     "",
		"currency":     l.Currency,
		"category":     l.Category,
		"images":       l.Images,
		"digital":      l.Digital,
		"digitalCid":   l.DigitalCID,
		"seller":       l.StoreID,
		"sellerName":   shortHex(l.StoreID),
		"rating":       0,
		"reviews":      0,
		"stock":        1,
		"variants":     variants,
		"shippingOpts": shipping,
		"createdAt":    l.CreatedAt.Format(time.RFC3339),
		"updatedAt":    l.UpdatedAt.Format(time.RFC3339),
	}
}

// marshalSearchEntries converts discovery search entries into listing cards.
func marshalSearchEntries(entries []discovery.SearchEntry) []map[string]interface{} {
	out := make([]map[string]interface{}, len(entries))
	for i, e := range entries {
		out[i] = map[string]interface{}{
			"id":         e.ListingID,
			"title":      e.Title,
			"price":      formatPrice(e.Price, ""),
			"priceRaw":   e.Price,
			"category":   e.Category,
			"seller":     e.SellerID,
			"sellerName": shortHex(e.SellerID),
			"rating":     0,
			"reviews":    0,
		}
	}
	return out
}

// marshalOrder converts an Order into the shape the orders UI expects,
// including a timeline built from the Order's state transition history.
func marshalOrder(o *order.Order, role string) map[string]interface{} {
	timeline := make([]map[string]interface{}, 0, len(o.History)+1)
	timeline = append(timeline, map[string]interface{}{
		"step": "Created",
		"time": o.CreatedAt.Format(time.RFC3339),
		"done": true,
	})
	for _, h := range o.History {
		timeline = append(timeline, map[string]interface{}{
			"step": h.To.String(),
			"time": h.Timestamp.Format(time.RFC3339),
			"done": true,
		})
	}
	return map[string]interface{}{
		"id":           o.Intent.ID,
		"listingId":    o.Intent.ListingID,
		"listingTitle": o.Intent.ListingID,
		"status":       strings.ToLower(o.Status.String()),
		"role":         role,
		"buyer":        hex.EncodeToString(o.Intent.BuyerPubKey),
		"seller":       hex.EncodeToString(o.Intent.SellerPubKey),
		"sellerName":   shortHex(hex.EncodeToString(o.Intent.SellerPubKey)),
		"price":        formatPrice(o.Intent.TotalPrice, o.Intent.Currency),
		"priceRaw":     o.Intent.TotalPrice,
		"currency":     o.Intent.Currency,
		"variant":      o.Intent.VariantID,
		"quantity":     o.Intent.Quantity,
		"createdAt":    o.CreatedAt.Format(time.RFC3339),
		"updatedAt":    o.UpdatedAt.Format(time.RFC3339),
		"timeline":     timeline,
	}
}

// marshalDispute converts an arbitration DisputeCase into a UI-friendly map.
// The arbitrator block is populated from the live ArbitratorProfile (Plutus
// fee + Mercury extension) when one is available; otherwise the field is
// emitted as null so the UI can render its own empty state instead of "—".
func (b *jsBridge) marshalDispute(dc *arbitration.DisputeCase) map[string]interface{} {
	out := map[string]interface{}{
		"id":         dc.ID,
		"orderId":    dc.OrderID,
		"reason":     dc.Reason,
		"status":     dc.Status.String(),
		"filedAt":    dc.FiledAt.Format(time.RFC3339),
		"filedBy":    hex.EncodeToString(dc.FiledBy),
		"evidence":   marshalEvidenceHashes(dc.EvidenceHashes),
		"timeline":   []interface{}{},
		"arbitrator": nil,
	}
	if len(dc.ArbitratorPubKey) == ed25519.PublicKeySize {
		mp, found := b.arbService.GetProfile(dc.ArbitratorPubKey)
		pp, ppFound := b.plutusArbReg.Get(dc.ArbitratorPubKey)
		if found && ppFound {
			out["arbitrator"] = marshalArbitrator(*mp, pp)
		} else {
			// Fall back to a minimal record with just the pubkey so the UI
			// can still link to the arbitrator's profile page.
			out["arbitrator"] = map[string]interface{}{
				"pubkey": hex.EncodeToString(dc.ArbitratorPubKey),
			}
		}
	}
	return out
}

// marshalEvidenceHashes renders the [][]byte evidence hashes as an array of
// hex strings — what the dispute detail UI expects.
func marshalEvidenceHashes(hashes [][]byte) []interface{} {
	out := make([]interface{}, len(hashes))
	for i, h := range hashes {
		out[i] = hex.EncodeToString(h)
	}
	return out
}

// formatPrice renders a uint64 minor-unit price as a currency-qualified
// decimal string. Decimals are inferred from the currency code.
func formatPrice(v uint64, currency string) string {
	if currency == "" {
		return fmt.Sprintf("%d", v)
	}
	decimals := 2
	switch strings.ToUpper(currency) {
	case "BTC":
		decimals = 8
	case "ETH":
		decimals = 18
	case "USDC", "USDT":
		decimals = 6
	}
	div := uint64(1)
	for i := 0; i < decimals; i++ {
		div *= 10
	}
	whole := v / div
	frac := v % div
	return fmt.Sprintf("%d.%0*d %s", whole, decimals, frac, currency)
}

// shortHex returns a truncated form of a long hex string suitable for UI
// display (first 6 + last 4 characters separated by an ellipsis).
func shortHex(s string) string {
	if len(s) <= 10 {
		return s
	}
	return s[:6] + "..." + s[len(s)-4:]
}

// firstChar returns the uppercase first character of s, or "U" if s is empty.
func firstChar(s string) string {
	if s == "" {
		return "U"
	}
	return strings.ToUpper(s[:1])
}

// mapToJSObject builds a JS Object from a Go map, walking nested values.
func mapToJSObject(m map[string]interface{}) js.Value {
	obj := js.Global().Get("Object").New()
	for k, v := range m {
		obj.Set(k, goToJS(v))
	}
	return obj
}

// --- Arbitrators ---

// getArbitrators returns all Mercury arbitrator profiles known to this
// session. Each entry is enriched with the Plutus profile fee + chain data
// so the UI can render fee, response time, and specialisation directly.
// Mercury.getArbitrators() -> Promise<[arbitrator]>
func (b *jsBridge) getArbitrators(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		// FindArbitrators with empty category/chainID returns every Mercury
		// profile that has a corresponding Plutus profile (verified identity).
		mercuryProfiles := b.arbService.FindArbitrators("", "")
		out := make([]map[string]interface{}, 0, len(mercuryProfiles))
		for _, mp := range mercuryProfiles {
			pp, ok := b.plutusArbReg.Get(mp.PlutusPubKey)
			if !ok {
				continue
			}
			out = append(out, marshalArbitrator(mp, pp))
		}
		return out, nil
	})
}

// submitEvidence appends signed evidence to a dispute case and anchors the
// SHA-256 hash of the payload on the case record. The evidence object accepts
// either {note, fileBytes?} or {description, files?} (the marketplace UI uses
// description+files; the prompt spec uses note+fileBytes).
// Mercury.submitEvidence(disputeId, payload) -> Promise<{hash, count}>
func (b *jsBridge) submitEvidence(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("submitEvidence: requires (disputeId, payload)")
		}
		disputeID := args[0].String()
		payload := args[1]

		var note string
		if v := payload.Get("note"); !v.IsUndefined() && !v.IsNull() {
			note = v.String()
		}
		if note == "" {
			if v := payload.Get("description"); !v.IsUndefined() && !v.IsNull() {
				note = v.String()
			}
		}

		// Concatenate note + every attached file's bytes into the canonical
		// evidence blob that gets hashed and anchored.
		buf := []byte(note)
		// fileBytes — single Uint8Array
		if v := payload.Get("fileBytes"); !v.IsUndefined() && !v.IsNull() && v.Length() > 0 {
			file := make([]byte, v.Length())
			js.CopyBytesToGo(file, v)
			buf = append(buf, file...)
		}
		// files — array of {name, bytes?} or strings (UI passes file names by
		// default; the marketplace uploader provides bytes when present).
		if v := payload.Get("files"); !v.IsUndefined() && !v.IsNull() {
			n := v.Length()
			for i := 0; i < n; i++ {
				fi := v.Index(i)
				if fi.Type() == js.TypeString {
					buf = append(buf, []byte(fi.String())...)
					continue
				}
				if name := fi.Get("name"); !name.IsUndefined() && !name.IsNull() {
					buf = append(buf, []byte(name.String())...)
				}
				if bytes := fi.Get("bytes"); !bytes.IsUndefined() && !bytes.IsNull() && bytes.Length() > 0 {
					file := make([]byte, bytes.Length())
					js.CopyBytesToGo(file, bytes)
					buf = append(buf, file...)
				}
			}
		}
		if len(buf) == 0 {
			return nil, fmt.Errorf("submitEvidence: payload is empty")
		}

		// Try the dispute manager first (real dispute lifecycle); fall back
		// to the in-memory disputes table populated by fileDispute() so the
		// marketplace demo flow still records evidence even when the case
		// hasn't been registered with the manager.
		if err := b.disputeMgr.SubmitEvidence(disputeID, buf); err != nil {
			b.mu.Lock()
			dc, ok := b.disputes[disputeID]
			if !ok {
				b.mu.Unlock()
				return nil, fmt.Errorf("submitEvidence: %w", err)
			}
			h := sha256.Sum256(buf)
			dc.EvidenceHashes = append(dc.EvidenceHashes, h[:])
			if dc.Status == arbitration.DisputeFiled {
				dc.Status = arbitration.DisputeEvidenceCollection
			}
			count := len(dc.EvidenceHashes)
			b.mu.Unlock()
			return map[string]interface{}{
				"hash":  hex.EncodeToString(h[:]),
				"count": count,
			}, nil
		}

		// Pull the updated case to report the new evidence count.
		dc, _ := b.disputeMgr.GetCase(disputeID)
		h := sha256.Sum256(buf)
		count := 0
		if dc != nil {
			count = len(dc.EvidenceHashes)
		}
		return map[string]interface{}{
			"hash":  hex.EncodeToString(h[:]),
			"count": count,
		}, nil
	})
}

// --- Listings: per-user / per-seller ---

// getMyListings returns every listing owned by the current authenticated
// identity across every store the bridge has created locally.
// Mercury.getMyListings() -> Promise<[listing]>
func (b *jsBridge) getMyListings(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		me := b.identityPub
		b.mu.RLock()
		defer b.mu.RUnlock()
		out := []map[string]interface{}{}
		for _, ss := range b.stores {
			if !ed25519PubEqual(ss.manager.OwnerPubKey(), me) {
				continue
			}
			for _, l := range ss.manager.AllListings() {
				out = append(out, marshalListingFull(l))
			}
		}
		return out, nil
	})
}

// getSellerListings returns listings belonging to a specific seller pubkey,
// found by walking every locally-known store and matching ownership.
// Mercury.getSellerListings(pubkey) -> Promise<[listing]>
func (b *jsBridge) getSellerListings(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("getSellerListings: requires pubkey")
		}
		pubHex := args[0].String()
		pub, err := hex.DecodeString(pubHex)
		if err != nil {
			return nil, fmt.Errorf("getSellerListings: invalid pubkey hex: %w", err)
		}
		b.mu.RLock()
		defer b.mu.RUnlock()
		out := []map[string]interface{}{}
		for _, ss := range b.stores {
			if !ed25519PubEqual(ss.manager.OwnerPubKey(), pub) {
				continue
			}
			for _, l := range ss.manager.AllListings() {
				out = append(out, marshalListingFull(l))
			}
		}
		return out, nil
	})
}

// --- Peer presence ---

// getPeerPresence returns {online, lastSeen} for a peer pubkey, derived from
// PEXManager.KnownPeers() (gossip last-seen) combined with the local
// peerActivity map (most-recent inbound message timestamp). A peer is
// considered online when either signal is fresher than peerOnlineWindow.
// Mercury.getPeerPresence(pubkey) -> Promise<{online, lastSeen}>
func (b *jsBridge) getPeerPresence(this js.Value, args []js.Value) interface{} {
	return promiseWrapper(func() (interface{}, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("getPeerPresence: requires pubkey")
		}
		pubHex := strings.ToLower(args[0].String())

		var lastSeen time.Time

		// PEX-manager last-seen: scan KnownPeers for an entry whose NodeID
		// matches the requested pubkey hex.
		if pex := b.engine.PEXManager(); pex != nil {
			for _, e := range pex.KnownPeers() {
				if strings.EqualFold(e.NodeID, pubHex) {
					if e.SignedAt.After(lastSeen) {
						lastSeen = e.SignedAt
					}
					break
				}
			}
		}

		// Whisper recent-activity tracker: most recent inbound message.
		b.mu.RLock()
		if t, ok := b.peerActivity[pubHex]; ok && t.After(lastSeen) {
			lastSeen = t
		}
		b.mu.RUnlock()

		online := false
		var lastSeenStr string
		if !lastSeen.IsZero() {
			lastSeenStr = lastSeen.UTC().Format(time.RFC3339)
			if time.Since(lastSeen) < peerOnlineWindow {
				online = true
			}
		}
		return map[string]interface{}{
			"online":   online,
			"lastSeen": lastSeenStr,
		}, nil
	})
}

// peerOnlineWindow is the freshness window beyond which a peer is reported
// offline regardless of whether we've ever seen them.
const peerOnlineWindow = 5 * time.Minute

// marshalArbitrator renders a combined Mercury+Plutus arbitrator profile in
// the shape the arbitration UI expects (id/pubkey/specialisation/fee/
// responseTime/rating/activeCases).
func marshalArbitrator(mp arbitration.MercuryArbitratorProfile, pp *plutusArb.ArbitratorProfile) map[string]interface{} {
	pubHex := hex.EncodeToString(mp.PlutusPubKey)
	specs := strings.Join(mp.Specializations, ", ")
	feeStr := fmt.Sprintf("%.2f%%", float64(pp.FeeBPS)/100.0)
	respStr := humanizeDuration(mp.ResponseTime)
	return map[string]interface{}{
		"id":             pubHex,
		"pubkey":         pubHex,
		"specialisation": specs,
		"specialization": specs, // both spellings for UI compat
		"fee":            feeStr,
		"feeBPS":         pp.FeeBPS,
		"responseTime":   respStr,
		"responseHours":  int(mp.ResponseTime / time.Hour),
		"languages":      mp.Languages,
		"activeCases":    mp.ActiveCases,
		"maxConcurrent":  mp.MaxConcurrent,
		"availability":   pp.Availability,
		"bio":            pp.Bio,
		"rating":         0, // composite rating populated by reputation overlay client-side
	}
}

// humanizeDuration renders a Duration as a short human string ("12h", "2d").
func humanizeDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	hours := int(d / time.Hour)
	if hours >= 24 {
		days := hours / 24
		return fmt.Sprintf("%dd", days)
	}
	if hours >= 1 {
		return fmt.Sprintf("%dh", hours)
	}
	mins := int(d / time.Minute)
	return fmt.Sprintf("%dm", mins)
}

// goToJS converts a Go value into a js.Value, with support for nested maps
// and slices (unlike plain js.ValueOf which rejects nested maps).
func goToJS(v interface{}) interface{} {
	switch val := v.(type) {
	case nil:
		return js.Null()
	case map[string]interface{}:
		return mapToJSObject(val)
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, e := range val {
			out[i] = goToJS(e)
		}
		return js.ValueOf(out)
	case []map[string]interface{}:
		out := make([]interface{}, len(val))
		for i, e := range val {
			out[i] = mapToJSObject(e)
		}
		return js.ValueOf(out)
	case []string:
		out := make([]interface{}, len(val))
		for i, s := range val {
			out[i] = s
		}
		return js.ValueOf(out)
	default:
		return js.ValueOf(val)
	}
}
