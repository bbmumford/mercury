/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package order

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// OrderStatus represents the current phase of an order.
type OrderStatus int

const (
	OrderPending   OrderStatus = iota // 0 — created, awaiting escrow funding
	OrderFunded                       // 1 — escrow funded on-chain
	OrderAccepted                     // 2 — seller accepted
	OrderFulfilled                    // 3 — seller shipped / delivered
	OrderCompleted                    // 4 — buyer confirmed receipt, settled
	OrderDisputed                     // 5 — dispute opened
	OrderCancelled                    // 6 — cancelled before funding
	OrderTimedOut                     // 7 — escrow timed out, refund triggered
)

// String returns a human-readable name for the order status.
func (s OrderStatus) String() string {
	switch s {
	case OrderPending:
		return "Pending"
	case OrderFunded:
		return "Funded"
	case OrderAccepted:
		return "Accepted"
	case OrderFulfilled:
		return "Fulfilled"
	case OrderCompleted:
		return "Completed"
	case OrderDisputed:
		return "Disputed"
	case OrderCancelled:
		return "Cancelled"
	case OrderTimedOut:
		return "TimedOut"
	default:
		return fmt.Sprintf("OrderStatus(%d)", s)
	}
}

// FulfillmentCommitment represents the seller's commitment to fulfill an order.
type FulfillmentCommitment struct {
	OrderID      string
	SellerPubKey ed25519.PublicKey
	EstDelivery  time.Time
	TrackingInfo string // encrypted
	Timestamp    time.Time
	Signature    []byte
}

// fulfillmentDigest computes the SHA-256 hash of all FulfillmentCommitment
// fields except Signature.
func fulfillmentDigest(fc *FulfillmentCommitment) []byte {
	h := sha256.New()
	h.Write([]byte(fc.OrderID))
	h.Write(fc.SellerPubKey)

	var edBuf [8]byte
	binary.BigEndian.PutUint64(edBuf[:], uint64(fc.EstDelivery.UnixNano()))
	h.Write(edBuf[:])

	h.Write([]byte(fc.TrackingInfo))

	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(fc.Timestamp.UnixNano()))
	h.Write(tsBuf[:])

	return h.Sum(nil)
}

// SignOrderIntent signs the order intent with the buyer's private key.
// This is a package-level function; see also OrderIntent.Sign().
func SignOrderIntent(oi *OrderIntent, buyerPriv ed25519.PrivateKey) error {
	if oi == nil {
		return errors.New("order: nil OrderIntent")
	}
	return oi.Sign(buyerPriv)
}

// VerifyOrderIntent verifies the buyer's signature on the OrderIntent.
// This is a package-level function; see also OrderIntent.Verify().
func VerifyOrderIntent(oi *OrderIntent) error {
	if oi == nil {
		return errors.New("order: nil OrderIntent")
	}
	if !oi.Verify() {
		return errors.New("order: signature verification failed")
	}
	return nil
}

// SignFulfillment signs the fulfillment commitment with the seller's private key.
func SignFulfillment(fc *FulfillmentCommitment, sellerPriv ed25519.PrivateKey) error {
	if fc == nil {
		return errors.New("order: nil FulfillmentCommitment")
	}
	if len(sellerPriv) != ed25519.PrivateKeySize {
		return errors.New("order: invalid seller private key")
	}
	digest := fulfillmentDigest(fc)
	fc.Signature = ed25519.Sign(sellerPriv, digest)
	return nil
}

// VerifyFulfillment verifies the seller's signature on the FulfillmentCommitment.
func VerifyFulfillment(fc *FulfillmentCommitment) error {
	if fc == nil {
		return errors.New("order: nil FulfillmentCommitment")
	}
	if len(fc.SellerPubKey) != ed25519.PublicKeySize {
		return errors.New("order: invalid seller public key")
	}
	if len(fc.Signature) != ed25519.SignatureSize {
		return errors.New("order: invalid signature length")
	}
	digest := fulfillmentDigest(fc)
	if !ed25519.Verify(fc.SellerPubKey, digest, fc.Signature) {
		return errors.New("order: fulfillment signature verification failed")
	}
	return nil
}
