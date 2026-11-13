/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package profile

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/bbmumford/whisper"
)

const (
	// topicProfiles is the StatefulMerge topic for user profiles.
	topicProfiles = "mercury.profiles"

	// maxBioLength is the maximum bio length in characters.
	maxBioLength = 500

	// maxNameLength is the maximum display name length.
	maxNameLength = 64
)

// UserProfile represents a user's public identity on the Mercury marketplace.
// Profiles are gossiped via Whisper StatefulMerge and stored locally.
type UserProfile struct {
	PubKey    ed25519.PublicKey `json:"pubKey"`
	Name      string           `json:"name"`
	Bio       string           `json:"bio"`
	AvatarCID string           `json:"avatarCid"` // content-addressed avatar image
	Location  string           `json:"location"`  // general area, not exact (ISO 3166-1 alpha-2)
	JoinedAt  time.Time        `json:"joinedAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
	Signature []byte           `json:"signature"`
}

// profileDigest computes the SHA-256 hash of all UserProfile fields except Signature.
func profileDigest(p *UserProfile) []byte {
	h := sha256.New()
	h.Write(p.PubKey)
	h.Write([]byte(p.Name))
	h.Write([]byte(p.Bio))
	h.Write([]byte(p.AvatarCID))
	h.Write([]byte(p.Location))
	h.Write([]byte(p.JoinedAt.Format(time.RFC3339Nano)))
	h.Write([]byte(p.UpdatedAt.Format(time.RFC3339Nano)))
	return h.Sum(nil)
}

// Sign signs the profile with the owner's Ed25519 private key.
func (p *UserProfile) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("profile: invalid private key size %d", len(privateKey))
	}
	digest := profileDigest(p)
	p.Signature = ed25519.Sign(privateKey, digest)
	return nil
}

// Verify checks that the profile signature is valid.
func (p *UserProfile) Verify() bool {
	if len(p.PubKey) != ed25519.PublicKeySize {
		return false
	}
	if len(p.Signature) != ed25519.SignatureSize {
		return false
	}
	digest := profileDigest(p)
	return ed25519.Verify(p.PubKey, digest, p.Signature)
}

// profileStateStore implements whisper.StateStore for profile gossip.
type profileStateStore struct {
	service *ProfileService
}

func (s *profileStateStore) Fingerprint() uint64 {
	s.service.mu.RLock()
	defer s.service.mu.RUnlock()
	// Simple fingerprint: hash of profile count + latest update
	var fp uint64
	for _, p := range s.service.cache {
		fp ^= uint64(p.UpdatedAt.UnixNano())
	}
	return fp
}

// Snapshot returns every cached profile; a profile that fails to marshal
// is left out so it does not block the rest.
func (s *profileStateStore) Snapshot() [][]byte {
	return s.Delta(time.Time{})
}

// Delta returns the profiles updated after since; the zero time admits
// every profile, which is the snapshot.
func (s *profileStateStore) Delta(since time.Time) [][]byte {
	s.service.mu.RLock()
	defer s.service.mu.RUnlock()

	var records [][]byte
	for _, p := range s.service.cache {
		if since.IsZero() || p.UpdatedAt.After(since) {
			data, err := json.Marshal(p)
			if err != nil {
				continue
			}
			records = append(records, data)
		}
	}
	return records
}

func (s *profileStateStore) Apply(data []byte) error {
	var p UserProfile
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("profile: unmarshal: %w", err)
	}
	if !p.Verify() {
		return fmt.Errorf("profile: invalid signature")
	}
	s.service.mergeProfile(&p)
	return nil
}

// ProfileService manages user profiles with gossip-based replication.
type ProfileService struct {
	whisper   *whisper.Engine
	cache     map[string]*UserProfile   // hex(pubkey) -> profile
	following map[string]bool           // hex(pubkey) -> true if following
	mu        sync.RWMutex
}

// NewProfileService creates a ProfileService wired to the given Whisper engine.
// It registers the mercury.profiles StatefulMerge topic for profile gossip.
func NewProfileService(engine *whisper.Engine) *ProfileService {
	ps := &ProfileService{
		whisper:   engine,
		cache:     make(map[string]*UserProfile),
		following: make(map[string]bool),
	}

	store := &profileStateStore{service: ps}

	// Register the StatefulMerge topic for profiles
	_ = engine.RegisterTopic(topicProfiles, whisper.TopicConfig{
		Mode:  whisper.StatefulMerge,
		Store: store,
		Rumor: true,
	})

	// Subscribe for broadcast notifications
	engine.Subscribe(topicProfiles, &profileSubscriber{service: ps})

	return ps
}

// Set publishes a user profile. The profile must be signed and valid.
func (ps *ProfileService) Set(profile UserProfile) error {
	if err := validateProfile(&profile); err != nil {
		return err
	}

	// Apply locally
	ps.mergeProfile(&profile)

	// Publish to gossip — mergeProfile already applied local state above,
	// so we skip cfg.Store.Apply (that path is for the gossip receive side).
	data, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("profile: marshal: %w", err)
	}

	// Broadcast to peers via Whisper.
	if err := ps.whisper.Publish(topicProfiles, data); err != nil {
		return fmt.Errorf("profile: publish: %w", err)
	}

	return nil
}

// Get retrieves a user profile by public key. Returns nil if not found.
func (ps *ProfileService) Get(pubkey ed25519.PublicKey) (*UserProfile, error) {
	if len(pubkey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("profile: invalid public key size %d", len(pubkey))
	}

	key := hex.EncodeToString(pubkey)

	ps.mu.RLock()
	defer ps.mu.RUnlock()

	p, ok := ps.cache[key]
	if !ok {
		return nil, nil
	}
	cp := *p
	return &cp, nil
}

// Follow adds a pubkey to the local following list.
func (ps *ProfileService) Follow(pubkey ed25519.PublicKey) error {
	if len(pubkey) != ed25519.PublicKeySize {
		return fmt.Errorf("profile: invalid public key size %d", len(pubkey))
	}

	key := hex.EncodeToString(pubkey)

	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.following[key] = true
	return nil
}

// Unfollow removes a pubkey from the local following list.
func (ps *ProfileService) Unfollow(pubkey ed25519.PublicKey) error {
	if len(pubkey) != ed25519.PublicKeySize {
		return fmt.Errorf("profile: invalid public key size %d", len(pubkey))
	}

	key := hex.EncodeToString(pubkey)

	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.following, key)
	return nil
}

// Following returns a list of all pubkeys the user is following.
func (ps *ProfileService) Following() []ed25519.PublicKey {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	result := make([]ed25519.PublicKey, 0, len(ps.following))
	for hexKey := range ps.following {
		pub, err := hex.DecodeString(hexKey)
		if err != nil {
			continue
		}
		result = append(result, ed25519.PublicKey(pub))
	}
	return result
}

// IsFollowing returns true if the given pubkey is in the following list.
func (ps *ProfileService) IsFollowing(pubkey ed25519.PublicKey) bool {
	key := hex.EncodeToString(pubkey)

	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.following[key]
}

// CachedProfiles returns the count of locally cached profiles.
func (ps *ProfileService) CachedProfiles() int {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return len(ps.cache)
}

// mergeProfile applies a profile update using LWW semantics (latest UpdatedAt wins).
func (ps *ProfileService) mergeProfile(p *UserProfile) {
	key := hex.EncodeToString(p.PubKey)

	ps.mu.Lock()
	defer ps.mu.Unlock()

	existing, ok := ps.cache[key]
	if !ok || p.UpdatedAt.After(existing.UpdatedAt) {
		cp := *p
		ps.cache[key] = &cp
	}
}

// validateProfile performs comprehensive validation on a profile.
func validateProfile(p *UserProfile) error {
	if len(p.PubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("profile: invalid public key size %d", len(p.PubKey))
	}
	if p.Name == "" {
		return fmt.Errorf("profile: name is required")
	}
	if len(p.Name) > maxNameLength {
		return fmt.Errorf("profile: name length %d exceeds limit %d", len(p.Name), maxNameLength)
	}
	if len(p.Bio) > maxBioLength {
		return fmt.Errorf("profile: bio length %d exceeds limit %d", len(p.Bio), maxBioLength)
	}
	if p.JoinedAt.IsZero() {
		return fmt.Errorf("profile: joined_at is required")
	}
	if p.UpdatedAt.IsZero() {
		return fmt.Errorf("profile: updated_at is required")
	}
	if !p.Verify() {
		return fmt.Errorf("profile: invalid signature")
	}
	return nil
}

// profileSubscriber implements whisper.Subscriber for profile gossip.
type profileSubscriber struct {
	service *ProfileService
}

// OnBroadcast implements whisper.Subscriber.
func (s *profileSubscriber) OnBroadcast(topic string, from string, payload []byte) {
	var p UserProfile
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}
	if !p.Verify() {
		return
	}
	s.service.mergeProfile(&p)
}
