// Copyright (c) 2026 Brandon Miller-Mumford.
// Licensed under the MIT License; see LICENSE.

package mesh

// Isolated default keccak256 constructor for the proof-of-storage
// on-chain escalation path. Kept in a tiny standalone file so the
// sha3 import stays scoped to the exact helper that needs it — and
// so a future refactor that pushes the keccak primitive up into a
// leaf util package does not churn proof_of_storage.go.

import "golang.org/x/crypto/sha3"

func defaultKeccak256New() keccak256er {
	return sha3.NewLegacyKeccak256()
}
