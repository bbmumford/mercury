/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package store

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"sort"
)

var (
	ErrEmptyTree     = errors.New("merkle: cannot build tree from empty leaf set")
	ErrInvalidProof  = errors.New("merkle: proof verification failed")
	ErrLeafNotFound  = errors.New("merkle: leaf not found in tree")
)

// MerkleTree is a binary hash tree built from a sorted set of leaf hashes.
// It supports inclusion proofs for individual leaves.
type MerkleTree struct {
	// layers[0] = leaf hashes (sorted), layers[len-1] = [root]
	layers [][]([32]byte)
	// leafIndex maps leaf hash -> position in layers[0] for proof generation
	leafIndex map[[32]byte]int
}

// BuildTree constructs a MerkleTree from a slice of listings.
// Listings are serialized to protobuf, hashed with SHA-256, sorted by hash,
// and then paired up the tree. If the leaf count at any level is odd, the
// last hash is duplicated (standard Merkle padding).
func BuildTree(listings []*Listing) (*MerkleTree, error) {
	if len(listings) == 0 {
		return nil, ErrEmptyTree
	}

	// Compute leaf hashes.
	leaves := make([][32]byte, len(listings))
	for i, l := range listings {
		h, err := HashListing(l)
		if err != nil {
			return nil, err
		}
		leaves[i] = h
	}

	return BuildTreeFromHashes(leaves)
}

// BuildTreeFromHashes constructs a MerkleTree from pre-computed leaf hashes.
// Hashes are sorted for deterministic ordering.
func BuildTreeFromHashes(leaves [][32]byte) (*MerkleTree, error) {
	if len(leaves) == 0 {
		return nil, ErrEmptyTree
	}

	// Sort leaves for deterministic ordering.
	sort.Slice(leaves, func(i, j int) bool {
		return bytes.Compare(leaves[i][:], leaves[j][:]) < 0
	})

	// Build index.
	leafIndex := make(map[[32]byte]int, len(leaves))
	for i, h := range leaves {
		leafIndex[h] = i
	}

	// Build layers bottom-up.
	layers := make([][][32]byte, 0)
	layers = append(layers, leaves)

	current := leaves
	for len(current) > 1 {
		next := make([][32]byte, 0, (len(current)+1)/2)
		for i := 0; i < len(current); i += 2 {
			if i+1 < len(current) {
				next = append(next, hashPair(current[i], current[i+1]))
			} else {
				// Odd leaf: duplicate the last hash.
				next = append(next, hashPair(current[i], current[i]))
			}
		}
		layers = append(layers, next)
		current = next
	}

	return &MerkleTree{
		layers:    layers,
		leafIndex: leafIndex,
	}, nil
}

// Root returns the Merkle root hash (top of the tree).
func (t *MerkleTree) Root() [32]byte {
	return t.layers[len(t.layers)-1][0]
}

// RootBytes returns the Merkle root as a byte slice.
func (t *MerkleTree) RootBytes() []byte {
	root := t.Root()
	return root[:]
}

// LeafCount returns the number of leaf nodes.
func (t *MerkleTree) LeafCount() int {
	return len(t.layers[0])
}

// ProofNode is a single node in a Merkle inclusion proof.
type ProofNode struct {
	Hash    [32]byte
	IsRight bool // true if this sibling is on the right
}

// MerkleProof is an inclusion proof for a single leaf in the tree.
type MerkleProof struct {
	LeafHash [32]byte
	Path     []ProofNode
}

// GenerateProof creates an inclusion proof for the given leaf hash.
func (t *MerkleTree) GenerateProof(leafHash [32]byte) (*MerkleProof, error) {
	idx, ok := t.leafIndex[leafHash]
	if !ok {
		return nil, ErrLeafNotFound
	}

	proof := &MerkleProof{
		LeafHash: leafHash,
		Path:     make([]ProofNode, 0, len(t.layers)-1),
	}

	currentIdx := idx
	for level := 0; level < len(t.layers)-1; level++ {
		layer := t.layers[level]
		var siblingIdx int
		var isRight bool

		if currentIdx%2 == 0 {
			// Current is left, sibling is right.
			siblingIdx = currentIdx + 1
			isRight = true
			if siblingIdx >= len(layer) {
				// Odd count: sibling is self (duplicate).
				siblingIdx = currentIdx
			}
		} else {
			// Current is right, sibling is left.
			siblingIdx = currentIdx - 1
			isRight = false
		}

		proof.Path = append(proof.Path, ProofNode{
			Hash:    layer[siblingIdx],
			IsRight: isRight,
		})
		currentIdx /= 2
	}

	return proof, nil
}

// VerifyProof verifies that a listing is included in a tree with the given root.
// It recomputes the hash path from the leaf to the root and checks equality.
func VerifyProof(listing *Listing, proof *MerkleProof, root [32]byte) (bool, error) {
	leafHash, err := HashListing(listing)
	if err != nil {
		return false, err
	}
	if leafHash != proof.LeafHash {
		return false, nil
	}
	return VerifyProofHash(proof, root), nil
}

// VerifyProofHash verifies a Merkle proof given only the proof and root hash.
func VerifyProofHash(proof *MerkleProof, root [32]byte) bool {
	current := proof.LeafHash
	for _, node := range proof.Path {
		if node.IsRight {
			current = hashPair(current, node.Hash)
		} else {
			current = hashPair(node.Hash, current)
		}
	}
	return current == root
}

// hashPair computes SHA-256(left || right).
func hashPair(left, right [32]byte) [32]byte {
	var buf [64]byte
	copy(buf[:32], left[:])
	copy(buf[32:], right[:])
	return sha256.Sum256(buf[:])
}
