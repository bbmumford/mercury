/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package order

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// RatingKey holds an Ed25519 keypair used for anonymous order ratings.
// A fresh rating key is generated per order so that the buyer's identity
// is not linked to the rating (unlinkable pseudonymous reviews).
type RatingKey struct {
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

// GenerateRatingKey creates a new random Ed25519 keypair for rating.
func GenerateRatingKey() (*RatingKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("order: generate rating key: %w", err)
	}
	return &RatingKey{
		Public:  pub,
		Private: priv,
	}, nil
}

// RatingData holds the signed rating for an order. The buyer signs with
// their per-order RatingKey so ratings are verifiable but pseudonymous.
type RatingData struct {
	OrderID      string
	SellerPubKey []byte
	Rating       int    // 1-5 stars
	Comment      string
	RatingKeyPub []byte // public key of the RatingKey used to sign
	Signature    []byte
}

// ratingDigest computes the SHA-256 hash of all RatingData fields except Signature.
func ratingDigest(d *RatingData) []byte {
	h := sha256.New()
	h.Write([]byte(d.OrderID))
	h.Write(d.SellerPubKey)

	var rBuf [4]byte
	binary.BigEndian.PutUint32(rBuf[:], uint32(d.Rating))
	h.Write(rBuf[:])

	h.Write([]byte(d.Comment))
	h.Write(d.RatingKeyPub)
	return h.Sum(nil)
}

// SignRating signs the RatingData with the given RatingKey.
func SignRating(data *RatingData, ratingKey *RatingKey) error {
	if data == nil {
		return errors.New("order: nil RatingData")
	}
	if ratingKey == nil || len(ratingKey.Private) != ed25519.PrivateKeySize {
		return errors.New("order: invalid rating key")
	}
	if data.Rating < 1 || data.Rating > 5 {
		return fmt.Errorf("order: rating must be 1-5, got %d", data.Rating)
	}

	data.RatingKeyPub = ratingKey.Public
	digest := ratingDigest(data)
	data.Signature = ed25519.Sign(ratingKey.Private, digest)
	return nil
}

// VerifyRating checks that the RatingData signature is valid for the
// declared RatingKeyPub.
func VerifyRating(data *RatingData) bool {
	if data == nil {
		return false
	}
	if len(data.RatingKeyPub) != ed25519.PublicKeySize {
		return false
	}
	if len(data.Signature) != ed25519.SignatureSize {
		return false
	}
	digest := ratingDigest(data)
	return ed25519.Verify(ed25519.PublicKey(data.RatingKeyPub), digest, data.Signature)
}
