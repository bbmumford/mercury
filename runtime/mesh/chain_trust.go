// Copyright (c) 2026 Brandon Miller-Mumford.
// Licensed under the MIT License; see LICENSE.

package mesh

import (
	"context"
	"errors"
	"fmt"

	"github.com/bbmumford/plutus/witness"
)

// ErrChainUntrustworthy is the answer every witness path of this runtime
// gives for a chain marked by MarkUntrustworthy, until ClearUntrustworthy
// lifts the mark: the chain's balance provider returns it instead of a
// balance, and the witness profile leaves the chain out, so the node signs
// no attestation and no profile for that chain.
var ErrChainUntrustworthy = errors.New("mesh: chain marked untrustworthy")

// chainTrusted returns ErrChainUntrustworthy, wrapped with the chain and the
// mark's reason, while chainID is marked, and nil otherwise. It reads the
// status tracker, which MarkUntrustworthy and ClearUntrustworthy write, so a
// mark or a clear takes effect on the next answer without a restart.
func (r *Runtime) chainTrusted(chainID string) error {
	if reason, marked := r.status.chainUntrustworthy(chainID); marked {
		return fmt.Errorf("%w: %s: %s", ErrChainUntrustworthy, chainID, reason)
	}
	return nil
}

// registerChainBalanceProvider registers provider as chainID's balance
// provider on the Plutus BalanceVerifier behind the untrustworthy-chain
// gate. The verifier signs an attestation only for a provider answer
// without error, so the gate's error is what keeps a marked chain unsigned.
// startBalanceWitness registers every chain it serves through here.
func (r *Runtime) registerChainBalanceProvider(chainID string, provider witness.ChainBalanceProvider) {
	r.plutus.BalanceVerifier.RegisterProvider(chainID, trustGatedBalanceProvider{
		chainID: chainID,
		trusted: r.chainTrusted,
		inner:   provider,
	})
}

// trustGatedBalanceProvider answers a balance query for chainID through
// inner only while trusted reports the chain trusted; otherwise it returns
// trusted's error without querying inner.
type trustGatedBalanceProvider struct {
	chainID string
	trusted func(chainID string) error
	inner   witness.ChainBalanceProvider
}

// QueryBalance implements witness.ChainBalanceProvider.
func (p trustGatedBalanceProvider) QueryBalance(ctx context.Context, addr, token string) (witness.BalanceSnapshot, error) {
	if err := p.trusted(p.chainID); err != nil {
		return witness.BalanceSnapshot{}, err
	}
	return p.inner.QueryBalance(ctx, addr, token)
}
