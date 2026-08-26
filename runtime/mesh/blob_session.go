// Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
// Queries: licensing@hstles.com

package mesh

import (
	"context"
	"errors"

	"github.com/ORBTR/aether"
	minerva_sdk "github.com/bbmumford/minerva/sdk"
	"github.com/bbmumford/plutus/blob"
)

// minervaSessionProvider satisfies blob.SessionProvider by resolving a
// peerID to a thin wrapper around minerva_sdk.Node that can open new
// BlobFetchStreamID streams. Client side of the blob fetch sub-
// protocol; the server side is wired via minerva_sdk.Node.OnStream in
// serveBlobFetches.
type minervaSessionProvider struct {
	node *minerva_sdk.Node
}

// SessionFor returns an AetherSession wired to the given peerID.
func (p *minervaSessionProvider) SessionFor(peerID string) (blob.AetherSession, error) {
	if p == nil || p.node == nil {
		return nil, errors.New("mesh: minervaSessionProvider has no node")
	}
	if peerID == "" {
		return nil, errors.New("mesh: empty peerID")
	}
	return &minervaAetherSession{node: p.node, peerID: peerID}, nil
}

type minervaAetherSession struct {
	node   *minerva_sdk.Node
	peerID string
}

func (s *minervaAetherSession) OpenStream(ctx context.Context, cfg blob.StreamConfig) (blob.AetherStream, error) {
	stream, err := s.node.OpenStreamTo(ctx, s.peerID, minerva_sdk.StreamConfig{
		Name:        "blob-fetch",
		StreamID:    cfg.StreamID,
		Reliability: aether.Reliability(cfg.Reliability),
	})
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (s *minervaAetherSession) AcceptStream(ctx context.Context) (blob.AetherStream, error) {
	return nil, errors.New("mesh: AcceptStream not supported — use minerva_sdk.Node.OnStream for server-side dispatch")
}
