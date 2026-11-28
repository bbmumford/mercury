// Copyright (c) 2026 Brandon Miller-Mumford.
// Licensed under the MIT License; see LICENSE.

package mesh

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/bbmumford/minerva/messaging"
	plutus "github.com/bbmumford/plutus"
	plutuswallet "github.com/bbmumford/plutus/wallet"
	"github.com/bbmumford/plutus/witness"
)

// chainIDBitcoin is the canonical chain-ID used for BTC attestations.
const chainIDBitcoin = "bitcoin"

// feeDistributionInitialDelay avoids broadcasting the first payout at
// t=0 so that concurrent witnesses introduce small natural jitter.
const feeDistributionInitialDelay = 5 * time.Minute

// startReplicator spins up the Minerva CRDT replicator + distributed-
// blob seeder. Best-effort: a disk-quota probe failure must not abort
// the whole runtime.
func (r *Runtime) startReplicator(ctx context.Context) error {
	if r.plutus == nil {
		return errors.New("mesh: replicator requires Plutus SDK")
	}
	if err := r.startBlobReplicator(ctx); err != nil {
		r.status.markRoleInactive(RoleReplicator, fmt.Sprintf("replicator: %v", err))
		return nil
	}
	r.activeRoles = append(r.activeRoles, RoleReplicator)
	r.status.setEnabledRoles(r.activeRoles)
	return nil
}

// startBlobProvider participates in Mercury's distributed blob storage
// mesh. Reuses the same Store/Quota bring-up as startReplicator; the two
// roles share that infrastructure.
//
// Additionally, when enabled, the BlobProvider role activates the
// proof-of-storage challenge + response loops.
func (r *Runtime) startBlobProvider(ctx context.Context) error {
	if r.plutus == nil {
		return errors.New("mesh: blob provider requires Plutus SDK")
	}
	if r.blobStore == nil {
		if err := r.startBlobReplicator(ctx); err != nil {
			r.status.markRoleInactive(RoleBlobProvider, fmt.Sprintf("blob_provider: %v", err))
			return nil
		}
	}
	if err := r.startProofOfStorage(ctx); err != nil {
		r.status.recordError(fmt.Errorf("mesh: proof-of-storage: %w", err))
	}
	r.activeRoles = append(r.activeRoles, RoleBlobProvider)
	r.status.setEnabledRoles(r.activeRoles)
	return nil
}

// startBalanceWitness registers the balance-attestation capability and
// wires the Neutrino BTC SPV as the in-process ChainBalanceProvider.
//
// EVM balance-witness is NEVER registered because there is no
// production-ready in-process Go EVM light client. The status tracker
// records the inactive reason so operators can tell the difference
// between misconfig and protocol-level unavailability.
func (r *Runtime) startBalanceWitness(ctx context.Context) error {
	if r.plutus == nil {
		return errors.New("mesh: balance witness requires Plutus SDK")
	}
	if r.neutrino != nil {
		r.registerChainBalanceProvider(chainIDBitcoin, r.neutrino)
		r.activeRoles = append(r.activeRoles, RoleBalanceWitness, RoleBalanceWitnessBTC)
		r.status.recordAttestationCapability(RoleBalanceWitnessBTC)
		go func() {
			if err := r.plutus.BalanceVerifier.Run(ctx); err != nil && ctx.Err() == nil {
				r.status.recordError(fmt.Errorf("mesh: balance verifier exited: %w", err))
			}
		}()
		r.registerWitnessProfile(ctx, []string{chainIDBitcoin}, []string{"balance"})
		go r.runFeeDistributionLoop(ctx)
	} else {
		r.status.markRoleInactive(RoleBalanceWitnessBTC,
			"balance_witness.btc: embedded neutrino SPV unavailable")
	}
	r.status.markRoleInactive(RoleBalanceWitnessEVM, InactiveReasonEVMNoLightClient)
	log.Printf("mesh: %s", InactiveReasonEVMNoLightClient)
	r.status.setEnabledRoles(r.activeRoles)
	return nil
}

// startTxWitness mirrors startBalanceWitness: BTC subrole gates on
// Neutrino readiness per attestation; EVM is permanently inactive.
func (r *Runtime) startTxWitness(ctx context.Context) error {
	if r.plutus == nil {
		return errors.New("mesh: tx witness requires Plutus SDK")
	}
	if r.neutrino != nil {
		r.activeRoles = append(r.activeRoles, RoleTxWitness, RoleTxWitnessBTC)
		r.status.recordAttestationCapability(RoleTxWitnessBTC)
		caps := []string{"tx-confirm"}
		if r.cfg.EnableBalanceWitness {
			caps = append([]string{"balance"}, caps...)
		}
		r.registerWitnessProfile(ctx, []string{chainIDBitcoin}, caps)
	} else {
		r.status.markRoleInactive(RoleTxWitnessBTC,
			"tx_witness.btc: embedded neutrino SPV unavailable")
	}
	r.status.markRoleInactive(RoleTxWitnessEVM, InactiveReasonEVMNoLightClient)
	r.status.setEnabledRoles(r.activeRoles)
	return nil
}

// startMessageRelay boots Minerva's store-and-forward relay in relay
// mode. Defaults: 7-day TTL, ring buffer of 500 messages per recipient.
func (r *Runtime) startMessageRelay(ctx context.Context) error {
	if r.minerva == nil {
		return errors.New("mesh: relay requires Minerva node")
	}
	if r.plutus == nil || r.plutus.Engine == nil {
		return errors.New("mesh: relay requires Whisper engine")
	}
	transport := messaging.NewWhisperTransport(r.plutus.Engine)
	relay := messaging.NewMessageRelay(
		transport,
		messaging.RelayRoleRelay,
		messaging.WithRelayMaxPerPeer(messaging.RelayMaxPerPeer),
		messaging.WithRelayTTL(messaging.RelayDefaultTTL),
	)
	if err := relay.Start(ctx); err != nil {
		r.status.markRoleInactive(RoleMessageRelay, fmt.Sprintf("message_relay: %v", err))
		return nil
	}
	r.relay = relay
	r.activeRoles = append(r.activeRoles, RoleMessageRelay)
	r.status.setEnabledRoles(r.activeRoles)
	go r.observeRelayBuffer(ctx)
	return nil
}

// startMarketplace launches the full Mercury SDK lifecycle loops.
func (r *Runtime) startMarketplace(ctx context.Context) error {
	if r.mercury == nil {
		return errors.New("mesh: marketplace requires Mercury SDK")
	}
	r.mercury.Start(ctx)
	r.activeRoles = append(r.activeRoles, RoleMarketplace)
	r.status.setEnabledRoles(r.activeRoles)
	return nil
}

// registerWitnessProfile publishes our witness profile into the
// registry so other peers can discover us as an attester for the given
// chains. A chain marked untrustworthy is left out of the profile and its
// ErrChainUntrustworthy is recorded in Status; when every chain is marked,
// no profile is signed at all.
func (r *Runtime) registerWitnessProfile(ctx context.Context, chainIDs, capabilities []string) {
	if r.plutus == nil || r.plutus.WitnessRegistry == nil {
		return
	}
	trusted := make([]string, 0, len(chainIDs))
	for _, chainID := range chainIDs {
		if err := r.chainTrusted(chainID); err != nil {
			r.status.recordError(fmt.Errorf("mesh: witness profile: %w", err))
			continue
		}
		trusted = append(trusted, chainID)
	}
	if len(trusted) == 0 {
		return
	}
	pub := r.plutus.PublicKey()
	if len(pub) != ed25519.PublicKeySize {
		return
	}
	var payoutAddrs map[string]string
	if len(r.cfg.MaintainerWallets) > 0 {
		payoutAddrs = make(map[string]string, len(r.cfg.MaintainerWallets))
		for chainID, w := range r.cfg.MaintainerWallets {
			if w.Address != "" {
				payoutAddrs[chainID] = w.Address
			}
		}
	}
	reg := witness.WitnessRegistration{
		PubKey:          pub,
		ChainIDs:        trusted,
		Capabilities:    append([]string(nil), capabilities...),
		PayoutAddresses: payoutAddrs,
		RegisteredAt:    time.Now(),
	}
	if err := reg.Sign(r.plutus.IdentityKey); err != nil {
		r.status.recordError(fmt.Errorf("mesh: sign witness profile: %w", err))
		return
	}
	if err := r.plutus.WitnessRegistry.Register(reg); err != nil {
		r.status.recordError(fmt.Errorf("mesh: register witness profile: %w", err))
	}
}

// runFeeDistributionLoop is every witness's baseline fee duty. Wakes on the
// configured window and drives a plutus.WitnessFeeDistributor round.
//
// Chains with no MaintainerWallets entry are skipped at startup — fee
// distribution for a chain requires both an address and a signing key.
// Operators that want to receive maintainer settlements on a chain but
// not sign distribution batches leave KeyPath empty; this loop warns
// and continues.
func (r *Runtime) runFeeDistributionLoop(ctx context.Context) {
	if r.plutus == nil {
		return
	}
	if len(r.cfg.MaintainerWallets) == 0 {
		log.Printf("mesh: fee_distribution: no maintainer wallets configured — distribution disabled")
		return
	}

	keyFiles := map[string]string{}
	passphrases := map[string][]byte{}
	payoutAddresses := map[string]string{}
	for chainID, wallet := range r.cfg.MaintainerWallets {
		if wallet.Address == "" {
			log.Printf("mesh: fee_distribution[%s]: empty address, skipping", chainID)
			continue
		}
		payoutAddresses[chainID] = wallet.Address
		if wallet.KeyPath == "" {
			log.Printf("mesh: fee_distribution[%s]: no key_path, signing disabled for this chain", chainID)
			continue
		}
		keyFiles[chainID] = wallet.KeyPath
		if wallet.PassphraseEnv != "" {
			pw, err := plutuswallet.PassphraseFromEnv(wallet.PassphraseEnv)
			if err != nil {
				log.Printf("mesh: fee_distribution[%s]: passphrase load: %v", chainID, err)
				continue
			}
			passphrases[chainID] = pw
		} else {
			passphrases[chainID] = nil
		}
	}

	if len(keyFiles) == 0 {
		log.Printf("mesh: fee_distribution: no chains have signing keys configured — distribution disabled")
		return
	}

	signer, err := witness.NewWalletSigner(witness.WalletSignerConfig{
		KeyFiles:    keyFiles,
		Passphrases: passphrases,
	})
	if err != nil {
		log.Printf("mesh: fee_distribution: construct wallet signer: %v", err)
		return
	}

	finalizer := &witness.EVMEnvelopeFinalizer{
		Clients: map[string]witness.EVMChainClient{},
	}
	finalizingSigner := &witness.FinalizingSigner{
		Inner:     signer,
		Finalizer: finalizer,
	}

	distributor, err := r.plutus.NewFeeDistributor(plutus.FeeDistributorHelperConfig{
		PayoutAddresses: payoutAddresses,
		Signer:          finalizingSigner,
		Window:          windowOrDefault(r.cfg.FeeDistributionWindow),
		UseMulticall3:   r.cfg.FeeDistributionUseMulticall3,
	})
	if err != nil {
		log.Printf("mesh: fee_distribution: construct distributor: %v", err)
		return
	}

	throttle := r.throttled
	if err := distributor.RunTickerWith(ctx, feeDistributionInitialDelay, throttle); err != nil && ctx.Err() == nil {
		log.Printf("mesh: fee_distribution: ticker exited: %v", err)
	}
}

// observeRelayBuffer polls the message relay buffer size and pushes it
// into the status tracker so Status() is self-updating.
func (r *Runtime) observeRelayBuffer(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if r.relay != nil {
				r.status.setRelayBufferSize(r.relay.TotalBuffered())
			}
		}
	}
}
