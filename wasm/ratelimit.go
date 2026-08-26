//go:build js && wasm

/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package main

import (
	"syscall/js"

	"github.com/bbmumford/plutus/wasmratelimit"
)

// mercuryMethodCategories is the default method->category mapping for the
// Mercury JS bridge.
var mercuryMethodCategories = map[string]wasmratelimit.Category{
	// Destructive / broadcast
	"createOrder":     wasmratelimit.CategoryDestructive,
	"completeOrder":   wasmratelimit.CategoryDestructive,
	"disputeOrder":    wasmratelimit.CategoryDestructive,
	"submitReview":    wasmratelimit.CategoryDestructive,
	"registerPasskey": wasmratelimit.CategoryDestructive,
	"fileDispute":     wasmratelimit.CategoryDestructive,
	"confirmReceipt":  wasmratelimit.CategoryDestructive,
	"submitEvidence":  wasmratelimit.CategoryDestructive,
	"sendMessage":     wasmratelimit.CategoryDestructive,

	// State mutation
	"createStore":   wasmratelimit.CategoryStateMutation,
	"addListing":    wasmratelimit.CategoryStateMutation,
	"removeListing": wasmratelimit.CategoryStateMutation,
	"updateProfile": wasmratelimit.CategoryStateMutation,
	"setProfile":    wasmratelimit.CategoryStateMutation,
	"acceptOrder":   wasmratelimit.CategoryStateMutation,
	"removePasskey": wasmratelimit.CategoryStateMutation,

	// Read
	"getStore":            wasmratelimit.CategoryRead,
	"getListings":         wasmratelimit.CategoryRead,
	"getListing":          wasmratelimit.CategoryRead,
	"getMyListings":       wasmratelimit.CategoryRead,
	"getSellerListings":   wasmratelimit.CategoryRead,
	"getOrders":           wasmratelimit.CategoryRead,
	"getOrder":            wasmratelimit.CategoryRead,
	"getMessages":         wasmratelimit.CategoryRead,
	"getConversations":    wasmratelimit.CategoryRead,
	"getConversation":     wasmratelimit.CategoryRead,
	"getPeers":            wasmratelimit.CategoryRead,
	"getProfile":          wasmratelimit.CategoryRead,
	"getReputation":       wasmratelimit.CategoryRead,
	"getReviews":          wasmratelimit.CategoryRead,
	"searchListings":      wasmratelimit.CategoryRead,
	"browseCategory":      wasmratelimit.CategoryRead,
	"getCategories":       wasmratelimit.CategoryRead,
	"getIdentity":         wasmratelimit.CategoryRead,
	"getPasskeys":         wasmratelimit.CategoryRead,
	"authenticatePasskey": wasmratelimit.CategoryRead,
	"getDisputes":         wasmratelimit.CategoryRead,
	"getDispute":          wasmratelimit.CategoryRead,
	"getArbitrators":      wasmratelimit.CategoryRead,
	"getPeerPresence":     wasmratelimit.CategoryRead,

	// Crypto-gated
	"createIdentity":   wasmratelimit.CategoryCryptoGated,
	"importIdentity":   wasmratelimit.CategoryCryptoGated,
	"getMnemonic":      wasmratelimit.CategoryCryptoGated,
	"generateMnemonic": wasmratelimit.CategoryCryptoGated,
	"logout":           wasmratelimit.CategoryCryptoGated,
}

// newMercuryRateLimiter builds a rate limiter pre-registered with the Mercury
// bridge's method->category defaults.
func newMercuryRateLimiter() *wasmratelimit.Limiter {
	r := wasmratelimit.New()
	r.RegisterMethods(mercuryMethodCategories)
	return r
}

// rateLimited wraps a bridge handler with per-method rate limiting. If the
// bucket is empty the returned Promise is rejected with
// "rate limit exceeded: <method> limited to N per minute".
func rateLimited(r *wasmratelimit.Limiter, method string, handler func(this js.Value, args []js.Value) interface{}) func(this js.Value, args []js.Value) interface{} {
	return func(this js.Value, args []js.Value) interface{} {
		if err := r.Allow(method); err != nil {
			return promiseWrapper(func() (interface{}, error) {
				return nil, err
			})
		}
		return handler(this, args)
	}
}

// installInitRateLimits exposes `Mercury.initRateLimits({method: {perMinute, burst}})`
// on the given JS object. Callers use it once at startup to raise or lower the
// defaults (e.g., the desktop app can permit higher throughput; the example
// web page keeps the stock defaults).
func installInitRateLimits(target js.Value, r *wasmratelimit.Limiter) {
	target.Set("initRateLimits", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		if len(args) == 0 || args[0].IsNull() || args[0].IsUndefined() {
			return nil
		}
		obj := args[0]
		keys := js.Global().Get("Object").Call("keys", obj)
		n := keys.Length()
		for i := 0; i < n; i++ {
			method := keys.Index(i).String()
			entry := obj.Get(method)
			if entry.IsNull() || entry.IsUndefined() {
				continue
			}
			var limit wasmratelimit.MethodLimit
			if v := entry.Get("perMinute"); !v.IsUndefined() && !v.IsNull() {
				limit.PerMinute = v.Int()
			}
			if v := entry.Get("burst"); !v.IsUndefined() && !v.IsNull() {
				limit.Burst = v.Int()
			}
			r.SetLimit(method, limit)
		}
		return nil
	}))
}
