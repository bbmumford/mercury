/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package store

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"time"

	pb "github.com/bbmumford/mercury/store/proto"
	"google.golang.org/protobuf/proto"
)

var (
	ErrInvalidSignature = errors.New("store: invalid signature")
	ErrNilOwnerKey      = errors.New("store: owner public key is nil")
	ErrNilPrivateKey    = errors.New("store: private key is nil")
	ErrVersionRollback  = errors.New("store: version rollback not allowed")
	ErrListingNotFound  = errors.New("store: listing not found")
	ErrStoreNotFound    = errors.New("store: store not found")
	ErrMediaTooLarge    = errors.New("store: media exceeds size limit")
)

const (
	// MaxMediaFileSize is the maximum size of a single media file (10MB).
	MaxMediaFileSize = 10 * 1024 * 1024
	// MaxMediaPerListing is the maximum total media size per listing (50MB).
	MaxMediaPerListing = 50 * 1024 * 1024
)

// StoreRoot is the signed root of a store's Merkle DAG. The owner signs the
// root to attest to the current state of all listings and policies.
type StoreRoot struct {
	OwnerPubKey ed25519.PublicKey
	Name        string
	Description string
	Categories  []string
	MerkleRoot  []byte // root hash of the listing tree
	Version     uint64
	Timestamp   time.Time
	Signature   []byte // owner signs the root
}

// Listing, Variant, and ShippingOption are defined in listing.go.

// signingPayload produces the deterministic byte sequence that is signed.
// Fields are serialized via protobuf for canonical ordering.
func (sr *StoreRoot) signingPayload() ([]byte, error) {
	msg := &pb.StoreRootProto{
		OwnerPubKey:   sr.OwnerPubKey,
		Name:          sr.Name,
		Description:   sr.Description,
		Categories:    sr.Categories,
		MerkleRoot:    sr.MerkleRoot,
		Version:       sr.Version,
		TimestampUnixMs: sr.Timestamp.UnixMilli(),
	}
	return proto.Marshal(msg)
}

// SignStoreRoot signs the StoreRoot with the given private key and populates
// the Signature field. The private key must correspond to OwnerPubKey.
func SignStoreRoot(root *StoreRoot, privKey ed25519.PrivateKey) error {
	if privKey == nil {
		return ErrNilPrivateKey
	}
	if root.OwnerPubKey == nil {
		return ErrNilOwnerKey
	}
	payload, err := root.signingPayload()
	if err != nil {
		return err
	}
	root.Signature = ed25519.Sign(privKey, payload)
	return nil
}

// VerifyStoreRoot checks that the Signature on the StoreRoot is valid for the
// OwnerPubKey. Returns ErrInvalidSignature on failure.
func VerifyStoreRoot(root *StoreRoot) error {
	if root.OwnerPubKey == nil {
		return ErrNilOwnerKey
	}
	payload, err := root.signingPayload()
	if err != nil {
		return err
	}
	if !ed25519.Verify(root.OwnerPubKey, payload, root.Signature) {
		return ErrInvalidSignature
	}
	return nil
}

// StoreRootToProto converts a StoreRoot to its protobuf representation.
func StoreRootToProto(sr *StoreRoot) *pb.StoreRootProto {
	return &pb.StoreRootProto{
		OwnerPubKey:   sr.OwnerPubKey,
		Name:          sr.Name,
		Description:   sr.Description,
		Categories:    sr.Categories,
		MerkleRoot:    sr.MerkleRoot,
		Version:       sr.Version,
		TimestampUnixMs: sr.Timestamp.UnixMilli(),
		Signature:     sr.Signature,
	}
}

// StoreRootFromProto converts a protobuf StoreRoot to the domain type.
func StoreRootFromProto(msg *pb.StoreRootProto) *StoreRoot {
	return &StoreRoot{
		OwnerPubKey: msg.OwnerPubKey,
		Name:        msg.Name,
		Description: msg.Description,
		Categories:  msg.Categories,
		MerkleRoot:  msg.MerkleRoot,
		Version:     msg.Version,
		Timestamp:   time.UnixMilli(msg.TimestampUnixMs),
		Signature:   msg.Signature,
	}
}

// ListingToProto converts a Listing to its protobuf representation.
func ListingToProto(l *Listing) *pb.ListingProto {
	variants := make([]*pb.VariantProto, len(l.Variants))
	for i, v := range l.Variants {
		variants[i] = &pb.VariantProto{
			Name:          v.Name,
			Value:         v.Value,
			PriceModifier: v.PriceModifier,
		}
	}
	opts := make([]*pb.ShippingOptionProto, len(l.ShippingOpts))
	for i, s := range l.ShippingOpts {
		opts[i] = &pb.ShippingOptionProto{
			Name:    s.Name,
			Price:   s.Price,
			EstDays: int32(s.EstDays),
		}
	}
	return &pb.ListingProto{
		Id:              l.ID,
		Title:           l.Title,
		Description:     l.Description,
		Price:           l.Price,
		Currency:        l.Currency,
		Images:          l.Images,
		Variants:        variants,
		ShippingOpts:    opts,
		Digital:         l.Digital,
		DigitalCid:      l.DigitalCID,
		CreatedAtUnixMs: l.CreatedAt.UnixMilli(),
		UpdatedAtUnixMs: l.UpdatedAt.UnixMilli(),
	}
}

// ListingFromProto converts a protobuf Listing to the domain type.
func ListingFromProto(msg *pb.ListingProto) *Listing {
	variants := make([]Variant, len(msg.Variants))
	for i, v := range msg.Variants {
		variants[i] = Variant{
			Name:          v.Name,
			Value:         v.Value,
			PriceModifier: v.PriceModifier,
		}
	}
	opts := make([]ShippingOption, len(msg.ShippingOpts))
	for i, s := range msg.ShippingOpts {
		opts[i] = ShippingOption{
			Name:    s.Name,
			Price:   s.Price,
			EstDays: int(s.EstDays),
		}
	}
	return &Listing{
		ID:           msg.Id,
		Title:        msg.Title,
		Description:  msg.Description,
		Price:        msg.Price,
		Currency:     msg.Currency,
		Images:       msg.Images,
		Variants:     variants,
		ShippingOpts: opts,
		Digital:      msg.Digital,
		DigitalCID:   msg.DigitalCid,
		CreatedAt:    time.UnixMilli(msg.CreatedAtUnixMs),
		UpdatedAt:    time.UnixMilli(msg.UpdatedAtUnixMs),
	}
}

// MarshalListing serializes a Listing to protobuf bytes.
func MarshalListing(l *Listing) ([]byte, error) {
	return proto.Marshal(ListingToProto(l))
}

// UnmarshalListing deserializes protobuf bytes into a Listing.
func UnmarshalListing(data []byte) (*Listing, error) {
	msg := &pb.ListingProto{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, err
	}
	return ListingFromProto(msg), nil
}

// HashListing returns the SHA-256 hash of the listing's protobuf serialization.
func HashListing(l *Listing) ([32]byte, error) {
	data, err := MarshalListing(l)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(data), nil
}

// HashMedia returns the SHA-256 content-addressed hash of raw media bytes.
func HashMedia(data []byte) [32]byte {
	return sha256.Sum256(data)
}

// MediaCID returns the hex-encoded SHA-256 CID for media bytes.
func MediaCID(data []byte) string {
	h := sha256.Sum256(data)
	return encodeHex(h[:])
}

// encodeHex returns a lowercase hex encoding of data.
func encodeHex(data []byte) string {
	const hexChars = "0123456789abcdef"
	buf := make([]byte, len(data)*2)
	for i, b := range data {
		buf[i*2] = hexChars[b>>4]
		buf[i*2+1] = hexChars[b&0x0f]
	}
	return string(buf)
}

// StoreID derives a stable store identifier from the owner's public key.
// It is the hex-encoded SHA-256 of the public key bytes.
func StoreID(ownerPubKey ed25519.PublicKey) string {
	h := sha256.Sum256(ownerPubKey)
	return encodeHex(h[:])
}

