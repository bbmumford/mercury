// Copyright (c) 2026 Brandon Miller-Mumford.
// Licensed under the MIT License; see LICENSE.

package mesh

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/bbmumford/minerva/identity"
	minerva_sdk "github.com/bbmumford/minerva/sdk"
	"github.com/bbmumford/diststore/abuse"
	"github.com/bbmumford/diststore/advertise"
	"github.com/bbmumford/diststore/cache"
	dscrypto "github.com/bbmumford/diststore/crypto"
	"github.com/bbmumford/diststore/fetch"
	"github.com/bbmumford/diststore/quota"
	"github.com/bbmumford/diststore/rarest"
	"github.com/bbmumford/diststore/replicate"
	"github.com/bbmumford/diststore/resolve"
	"github.com/bbmumford/diststore/storage"
	plutusblob "github.com/bbmumford/plutus/blob"
	"github.com/bbmumford/whisper"
)

// quotaObservePollInterval is how often we mirror the quota manager's
// state into the runtime's status tracker.
const quotaObservePollInterval = 15 * time.Second

// toRecordTypes converts the string allow-list carried on mesh.Config
// into abuse.RecordType values for AbuseGuard. Nil / empty input lets
// AbuseGuard fall back to its documented default set.
func toRecordTypes(in []string) []abuse.RecordType {
	if len(in) == 0 {
		return nil
	}
	out := make([]abuse.RecordType, 0, len(in))
	for _, s := range in {
		out = append(out, abuse.RecordType(s))
	}
	return out
}

// startBlobReplicator brings up the distributed blob subsystem:
// encrypted content-addressed Store + disk-quota Manager + Advertiser
// (BroadcastOnly "plutus.blob.have") + Resolver (RequestResponse
// "plutus.blob.find") + Fetcher (over Minerva streams) + passive
// replicator.
//
// Safe to call from both startReplicator and startBlobProvider (they
// share the single underlying store) — the first call wins and the
// second returns early via the blobStore != nil guard.
func (r *Runtime) startBlobReplicator(ctx context.Context) error {
	if r.blobStore != nil {
		return nil
	}

	blobDir := filepath.Join(r.cfg.DataDir, "blobs")
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		return fmt.Errorf("mesh: mkdir blob dir: %w", err)
	}

	qCfg := quota.ManagerConfig{
		MaxPct:            r.cfg.StorageMaxPct,
		TargetHeadroomPct: r.cfg.StorageTargetHeadroomPct,
		MinReserveGB:      r.cfg.StorageMinReserveGB,
		HardMinGB:         r.cfg.StorageHardMinGB,
	}
	qMgr := quota.NewManager(qCfg, blobDir, nil)
	qMgr.Start(ctx)
	r.quotaManager = qMgr

	// Derive storage key from the plutus identity seed.
	identityPriv := []byte(r.plutus.IdentityKey)
	if len(identityPriv) < 32 {
		return fmt.Errorf("mesh: plutus identity key too short for storage-key derivation")
	}
	key, err := dscrypto.DeriveStorageKey(identityPriv[:32])
	if err != nil {
		return fmt.Errorf("mesh: derive storage key: %w", err)
	}

	backend, err := storage.NewFilesystemBackend(blobDir)
	if err != nil {
		return fmt.Errorf("mesh: construct blob backend: %w", err)
	}
	store, err := cache.New(cache.Config{
		Backend:    backend,
		StorageKey: key,
		Quota:      qMgr,
	})
	if err != nil {
		return fmt.Errorf("mesh: construct blob store: %w", err)
	}
	r.blobStore = store
	r.status.setStorageBudget(qMgr.CurrentBudget())

	go r.observeQuota(ctx, qMgr)

	pubkey := r.plutus.IdentityKey.Public().(ed25519.PublicKey)
	peerID := string(identity.PeerIDFromPublicKey(pubkey))

	resolver, err := resolve.New(store, r.plutus.Engine, resolve.Config{
		PeerID: peerID,
	})
	if err != nil {
		return fmt.Errorf("mesh: construct blob resolver: %w", err)
	}
	r.blobResolver = resolver

	if err := registerBlobTopic(r.plutus.Engine, resolve.TopicBlobFind, whisper.TopicConfig{
		Mode:    whisper.RequestResponse,
		Handler: resolver,
	}); err != nil {
		return fmt.Errorf("mesh: register %s: %w", resolve.TopicBlobFind, err)
	}
	if err := registerBlobTopic(r.plutus.Engine, advertise.TopicBlobHave, whisper.TopicConfig{
		Mode: whisper.BroadcastOnly,
	}); err != nil {
		return fmt.Errorf("mesh: register %s: %w", advertise.TopicBlobHave, err)
	}

	advertiser, err := advertise.New(store, r.plutus.Engine, advertise.Config{
		PeerID: peerID,
	})
	if err != nil {
		return fmt.Errorf("mesh: construct blob advertiser: %w", err)
	}
	if err := advertiser.Start(ctx); err != nil {
		return fmt.Errorf("mesh: start blob advertiser: %w", err)
	}
	r.blobAdvertiser = advertiser

	transport := plutusblob.NewAetherTransport(&minervaSessionProvider{node: r.minerva})
	fetcher, err := fetch.New(resolver, transport, fetch.Config{})
	if err != nil {
		return fmt.Errorf("mesh: construct blob fetcher: %w", err)
	}
	r.blobFetcher = fetcher
	store.SetFetcher(fetcher)

	if err := r.serveBlobFetches(ctx); err != nil {
		return fmt.Errorf("mesh: serve blob fetches: %w", err)
	}

	rarestState := rarest.New(0)
	rarestState.StartPruner(ctx, 0)
	r.blobRarestFirst = rarestState

	// Shared AbuseGuard gates the opportunistic replication path. The
	// uploader side (ValidateStorable) and replication side (Check) keep
	// independent ledgers inside the guard so a flood of replication
	// traffic cannot exhaust the uploader budget or vice versa.
	r.blobAbuseGuard = abuse.New(abuse.Config{
		MaxReplicationBytesPerPeer:    int64(r.cfg.MaxReplicationMBPerPeer) * 1024 * 1024,
		MaxReplicationBytesTotal:      int64(r.cfg.MaxReplicationTotalMB) * 1024 * 1024,
		ReplicationAllowedRecordTypes: toRecordTypes(r.cfg.ReplicationAllowedRecordTypes),
	})

	replicator, err := replicate.New(replicate.Config{
		PeerID:      peerID,
		RarestFirst: rarestState,
		AbuseGuard:  r.blobAbuseGuard,
	}, store, advertiser, fetcher)
	if err != nil {
		return fmt.Errorf("mesh: construct blob replicator: %w", err)
	}
	if err := replicator.Start(ctx); err != nil {
		return fmt.Errorf("mesh: start blob replicator: %w", err)
	}
	r.blobReplicator = replicator

	return nil
}

// serveBlobFetches registers the OnStream handler for inbound
// fetch.BlobFetchStreamID streams on Minerva's Node. Safe to call
// multiple times (each call replaces the handler).
func (r *Runtime) serveBlobFetches(ctx context.Context) error {
	if r.minerva == nil {
		return errors.New("mesh: minerva node not constructed")
	}
	if r.blobStore == nil {
		return errors.New("mesh: blob store not ready")
	}
	store := r.blobStore

	r.minerva.OnStream(fetch.BlobFetchStreamID, minerva_sdk.StreamHandlerFunc(func(hctx context.Context, peerID identity.PeerID, stream *minerva_sdk.Stream) error {
		reqBytes, err := readMinervaFrame(hctx, stream)
		if err != nil {
			return err
		}
		req, err := fetch.DecodeFetchRequest(reqBytes)
		if err != nil {
			replyBlobFetch(hctx, stream, fetch.FetchResponse{Error: fmt.Sprintf("decode request: %v", err)})
			return err
		}
		if req.CID == "" {
			replyBlobFetch(hctx, stream, fetch.FetchResponse{Error: "empty cid"})
			return nil
		}
		has, herr := store.Has(hctx, req.CID)
		if herr != nil {
			replyBlobFetch(hctx, stream, fetch.FetchResponse{CID: req.CID, Error: fmt.Sprintf("has: %v", herr)})
			return herr
		}
		if !has {
			replyBlobFetch(hctx, stream, fetch.FetchResponse{CID: req.CID, Error: "not held"})
			return nil
		}
		payload, gerr := store.Get(hctx, req.CID)
		if gerr != nil {
			if errors.Is(gerr, cache.ErrBlobNotFound) {
				replyBlobFetch(hctx, stream, fetch.FetchResponse{CID: req.CID, Error: "not held"})
				return nil
			}
			replyBlobFetch(hctx, stream, fetch.FetchResponse{CID: req.CID, Error: fmt.Sprintf("get: %v", gerr)})
			return gerr
		}
		replyBlobFetch(hctx, stream, fetch.FetchResponse{CID: req.CID, Payload: payload})
		return nil
	}))
	return nil
}

// blobFetchHeaderSize is the 4-byte big-endian length prefix on blob
// transport request and response frames.
const blobFetchHeaderSize = 4

// maxBlobFetchFrame matches plutusblob.MaxFrameSize (300 KiB) — a
// single piece is 256 KiB, with headroom for the JSON envelope.
const maxBlobFetchFrame = 300 * 1024

// blobFetchReadTimeout bounds how long the server-side handler will
// wait to finish reading one request frame from a peer.
const blobFetchReadTimeout = 30 * time.Second

// readMinervaFrame reads a single length-prefixed frame from a
// minerva_sdk.Stream.
func readMinervaFrame(ctx context.Context, stream *minerva_sdk.Stream) ([]byte, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, blobFetchReadTimeout)
	defer cancel()

	var buf []byte
	for {
		if err := deadlineCtx.Err(); err != nil {
			return nil, err
		}
		if len(buf) >= blobFetchHeaderSize {
			length := uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])
			if int(length) > maxBlobFetchFrame {
				return nil, fmt.Errorf("mesh: blob fetch frame exceeds max (%d > %d)", length, maxBlobFetchFrame)
			}
			total := blobFetchHeaderSize + int(length)
			if len(buf) >= total {
				return buf[blobFetchHeaderSize:total], nil
			}
		}
		chunk, err := stream.Receive(deadlineCtx)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			return nil, errors.New("mesh: stream closed mid-frame")
		}
		buf = append(buf, chunk...)
	}
}

// writeMinervaFrame writes a 4-byte big-endian length prefix followed
// by payload.
func writeMinervaFrame(ctx context.Context, stream *minerva_sdk.Stream, payload []byte) error {
	if len(payload) > maxBlobFetchFrame {
		return fmt.Errorf("mesh: blob fetch payload exceeds max (%d > %d)", len(payload), maxBlobFetchFrame)
	}
	frame := make([]byte, 0, blobFetchHeaderSize+len(payload))
	frame = append(frame,
		byte(len(payload)>>24),
		byte(len(payload)>>16),
		byte(len(payload)>>8),
		byte(len(payload)),
	)
	frame = append(frame, payload...)
	return stream.Send(ctx, frame)
}

// replyBlobFetch encodes + sends a FetchResponse frame.
func replyBlobFetch(ctx context.Context, stream *minerva_sdk.Stream, resp fetch.FetchResponse) {
	respBytes, err := fetch.EncodeFetchResponse(resp)
	if err != nil {
		log.Printf("mesh: encode blob response: %v", err)
		if cerr := stream.Close(); cerr != nil {
			log.Printf("mesh: close stream after encode failure: %v", cerr)
		}
		return
	}
	if err := writeMinervaFrame(ctx, stream, respBytes); err != nil {
		log.Printf("mesh: write blob response: %v", err)
	}
}

// registerBlobTopic swallows the "already registered" sentinel so the
// call is safe when Plutus itself has pre-registered the topic.
func registerBlobTopic(engine *whisper.Engine, name string, cfg whisper.TopicConfig) error {
	if err := engine.RegisterTopic(name, cfg); err != nil {
		if _, exists := engine.Registry().Get(name); exists {
			return nil
		}
		return err
	}
	return nil
}

// observeQuota pushes qMgr's current budget + pressure into the status
// tracker on a cheap interval. qMgr is passed in explicitly so the
// goroutine cannot race Stop's nil-clear of that field.
func (r *Runtime) observeQuota(ctx context.Context, qMgr *quota.Manager) {
	if qMgr == nil {
		return
	}
	t := time.NewTicker(quotaObservePollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			state := qMgr.LastState()
			r.status.setStorageBudget(qMgr.CurrentBudget())
			r.status.setStoragePressure(int(state.Pressure() * 100))
		}
	}
}
