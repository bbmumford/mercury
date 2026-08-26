//go:build js && wasm

/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package main

import (
	"syscall/js"
)

func main() {
	mercury := js.Global().Get("Object").New()

	bridge := newJSBridge()
	limiter := newMercuryRateLimiter()

	// set binds a handler to a method name with per-method rate limiting.
	set := func(method string, fn func(this js.Value, args []js.Value) interface{}) {
		mercury.Set(method, js.FuncOf(rateLimited(limiter, method, fn)))
	}

	// Store operations
	set("createStore", bridge.createStore)
	set("addListing", bridge.addListing)
	set("removeListing", bridge.removeListing)
	set("getStore", bridge.getStore)

	// Order operations
	set("createOrder", bridge.createOrder)
	set("acceptOrder", bridge.acceptOrder)
	set("completeOrder", bridge.completeOrder)
	set("disputeOrder", bridge.disputeOrder)
	set("confirmReceipt", bridge.confirmReceipt)
	set("fileDispute", bridge.fileDispute)
	set("getOrders", bridge.getOrders)
	set("getOrder", bridge.getOrder)

	// Messaging
	set("sendMessage", bridge.sendMessage)
	set("getMessages", bridge.getMessages)
	set("getConversations", bridge.getConversations)
	set("getConversation", bridge.getConversation)
	set("getPeers", bridge.getPeers)

	// Discovery
	set("searchListings", bridge.searchListings)
	set("browseCategory", bridge.browseCategory)
	set("getListings", bridge.getListings)
	set("getListing", bridge.getListing)
	set("getCategories", bridge.getCategories)

	// Reputation
	set("getReputation", bridge.getReputation)
	set("submitReview", bridge.submitReview)
	set("getReviews", bridge.getReviews)

	// Profile
	set("getProfile", bridge.getProfile)
	set("setProfile", bridge.setProfile)
	set("updateProfile", bridge.updateProfile)

	// Identity / auth
	set("generateMnemonic", bridge.generateMnemonic)
	set("createIdentity", bridge.createIdentity)
	set("importIdentity", bridge.importIdentity)
	set("logout", bridge.logout)
	set("getMnemonic", bridge.getMnemonic)
	set("getIdentity", bridge.getIdentity)

	// Passkeys
	set("registerPasskey", bridge.registerPasskey)
	set("authenticatePasskey", bridge.authenticatePasskey)
	set("getPasskeys", bridge.getPasskeys)
	set("removePasskey", bridge.removePasskey)

	// Disputes
	set("getDisputes", bridge.getDisputes)
	set("getDispute", bridge.getDispute)
	set("submitEvidence", bridge.submitEvidence)

	// Arbitrators
	set("getArbitrators", bridge.getArbitrators)

	// Listings: per-user / per-seller
	set("getMyListings", bridge.getMyListings)
	set("getSellerListings", bridge.getSellerListings)

	// Peer presence
	set("getPeerPresence", bridge.getPeerPresence)

	// Rate-limit configuration hook (called once at startup by JS to raise or
	// lower the bridge defaults). This call itself is NOT rate-limited.
	installInitRateLimits(mercury, limiter)

	js.Global().Set("Mercury", mercury)

	// Install the initial state snapshot (authenticated=false, passkeys=[]).
	// The JS side reads this synchronously before any async call resolves.
	bridge.publishState()

	// Block forever to keep the WASM module alive
	select {}
}
