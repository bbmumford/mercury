/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package store

import "time"

// Listing represents a single item for sale in a store.
type Listing struct {
	ID           string
	StoreID      string // hex-encoded store owner pubkey hash
	Title        string
	Description  string
	Category     string // dot-separated category path (e.g. "electronics.phones")
	Price        uint64 // in smallest currency unit (e.g., cents)
	Currency     string
	Images       []string // content-addressed CIDs
	Keywords     []string // search keywords
	SellerID     string   // seller identity
	Variants     []Variant
	ShippingOpts []ShippingOption
	Digital      bool
	DigitalCID   string // CID of digital content (encrypted)
	Active       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Variant represents a product variant (e.g., size=Large with a price modifier).
type Variant struct {
	Name          string
	Value         string
	PriceModifier int64
}

// ShippingOption represents a shipping method with price and estimated delivery.
type ShippingOption struct {
	Name    string
	Price   uint64
	EstDays int
}
