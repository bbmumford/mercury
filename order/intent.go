/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package order

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"
)

// EscrowPolicy defines the escrow parameters for an order.
type EscrowPolicy struct {
	TimeoutSeconds uint64 // escrow timeout in seconds
	MultisigType   string // e.g. "2-of-3", "2-of-2"
}

// OrderIntent represents a buyer's desire to purchase a listing. It is signed
// by the buyer's Ed25519 key and references both the listing and seller.
type OrderIntent struct {
	ID           string
	BuyerPubKey  ed25519.PublicKey
	SellerPubKey ed25519.PublicKey
	ListingID    string
	VariantID    string
	Quantity     int
	TotalPrice   uint64 // price in smallest currency unit
	Currency     string // e.g. "USDC", "BTC"
	ChainID      string // e.g. "polygon", "bitcoin"
	ShippingAddr []byte // encrypted with seller's pubkey (NaCl box)
	Memo         string
	Timestamp    time.Time
	Signature    []byte

	// RatingKeys are the buyer's per-order Ed25519 public keys used for
	// pseudonymous ratings. One key per order allows unlinkable reviews.
	RatingKeys []ed25519.PublicKey

	// ReplicatorReceiptHashes are SHA-256 hashes of replicator storage receipts
	// proving the order data has been replicated for durability.
	ReplicatorReceiptHashes [][]byte

	// ArbitratorSet is the set of arbitrator public keys eligible to resolve
	// disputes for this order. Agreed upon at order creation time.
	ArbitratorSet []ed25519.PublicKey

	// EscrowPolicy defines the escrow parameters (timeout, multisig type).
	EscrowPolicy EscrowPolicy
}

// intentDigest computes the SHA-256 hash of all OrderIntent fields except Signature.
func intentDigest(o *OrderIntent) []byte {
	h := sha256.New()
	h.Write([]byte(o.ID))
	h.Write(o.BuyerPubKey)
	h.Write(o.SellerPubKey)
	h.Write([]byte(o.ListingID))
	h.Write([]byte(o.VariantID))

	var qBuf [8]byte
	binary.BigEndian.PutUint64(qBuf[:], uint64(o.Quantity))
	h.Write(qBuf[:])

	var pBuf [8]byte
	binary.BigEndian.PutUint64(pBuf[:], o.TotalPrice)
	h.Write(pBuf[:])

	h.Write([]byte(o.Currency))
	h.Write([]byte(o.ChainID))
	h.Write(o.ShippingAddr)
	h.Write([]byte(o.Memo))

	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(o.Timestamp.UnixNano()))
	h.Write(tsBuf[:])

	// Include new fields in the digest for signature coverage.
	for _, rk := range o.RatingKeys {
		h.Write(rk)
	}
	for _, rh := range o.ReplicatorReceiptHashes {
		h.Write(rh)
	}
	for _, arb := range o.ArbitratorSet {
		h.Write(arb)
	}

	var epBuf [8]byte
	binary.BigEndian.PutUint64(epBuf[:], o.EscrowPolicy.TimeoutSeconds)
	h.Write(epBuf[:])
	h.Write([]byte(o.EscrowPolicy.MultisigType))

	return h.Sum(nil)
}

// Sign signs the OrderIntent with the buyer's Ed25519 private key.
func (o *OrderIntent) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("order: invalid private key size %d", len(privateKey))
	}
	digest := intentDigest(o)
	o.Signature = ed25519.Sign(privateKey, digest)
	return nil
}

// Verify checks that the OrderIntent's signature is valid against the buyer's pubkey.
func (o *OrderIntent) Verify() bool {
	if len(o.BuyerPubKey) != ed25519.PublicKeySize {
		return false
	}
	if len(o.Signature) != ed25519.SignatureSize {
		return false
	}
	digest := intentDigest(o)
	return ed25519.Verify(o.BuyerPubKey, digest, o.Signature)
}
