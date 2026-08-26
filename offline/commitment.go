/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package offline

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"
)

// ListingCommitment is a pre-authorised commitment signed by the seller before
// going offline. It allows replicators to accept orders on the seller's behalf.
type ListingCommitment struct {
	ListingID    string
	SellerPubKey ed25519.PublicKey
	AutoAccept   bool      // auto-accept orders when seller offline
	MaxQuantity  int       // max items that can be sold under this commitment
	PriceSats    uint64    // price per unit in smallest currency unit
	Currency     string    // e.g. "BTC", "ETH"
	ValidUntil   time.Time // commitment expires after this time
	Signature    []byte
}

// commitmentDigest computes the SHA-256 hash of all ListingCommitment fields
// except Signature, for signing and verification.
func commitmentDigest(c *ListingCommitment) []byte {
	h := sha256.New()
	h.Write([]byte(c.ListingID))
	h.Write(c.SellerPubKey)

	var autoBuf [1]byte
	if c.AutoAccept {
		autoBuf[0] = 1
	}
	h.Write(autoBuf[:])

	var qBuf [8]byte
	binary.BigEndian.PutUint64(qBuf[:], uint64(c.MaxQuantity))
	h.Write(qBuf[:])

	var pBuf [8]byte
	binary.BigEndian.PutUint64(pBuf[:], c.PriceSats)
	h.Write(pBuf[:])

	h.Write([]byte(c.Currency))

	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(c.ValidUntil.UnixNano()))
	h.Write(tsBuf[:])

	return h.Sum(nil)
}

// Sign signs the commitment with the seller's Ed25519 private key.
func (c *ListingCommitment) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("offline: invalid private key size %d", len(privateKey))
	}
	digest := commitmentDigest(c)
	c.Signature = ed25519.Sign(privateKey, digest)
	return nil
}

// Verify checks that the commitment signature is valid against the seller's pubkey.
func (c *ListingCommitment) Verify() bool {
	if len(c.SellerPubKey) != ed25519.PublicKeySize {
		return false
	}
	if len(c.Signature) != ed25519.SignatureSize {
		return false
	}
	digest := commitmentDigest(c)
	return ed25519.Verify(c.SellerPubKey, digest, c.Signature)
}

// IsExpired returns true if the commitment is past its ValidUntil time.
func (c *ListingCommitment) IsExpired() bool {
	return time.Now().After(c.ValidUntil)
}

// IsValid performs full validation: signature check, expiry check, and field validation.
func (c *ListingCommitment) IsValid() error {
	if c.ListingID == "" {
		return fmt.Errorf("offline: listing ID is required")
	}
	if len(c.SellerPubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("offline: invalid seller public key size %d", len(c.SellerPubKey))
	}
	if c.MaxQuantity <= 0 {
		return fmt.Errorf("offline: max quantity must be positive, got %d", c.MaxQuantity)
	}
	if c.ValidUntil.IsZero() {
		return fmt.Errorf("offline: valid_until is required")
	}
	if c.IsExpired() {
		return fmt.Errorf("offline: commitment expired at %s", c.ValidUntil.Format(time.RFC3339))
	}
	if !c.Verify() {
		return fmt.Errorf("offline: invalid signature")
	}
	return nil
}
