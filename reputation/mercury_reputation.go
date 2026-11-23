/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package reputation

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	plutusRep "github.com/bbmumford/plutus/reputation"
	"github.com/bbmumford/whisper"
)

const (
	// topicReviews is the StatefulMerge topic for Mercury marketplace reviews.
	topicReviews = "mercury.reviews"

	// maxReviewTitleLength is the max review title length.
	maxReviewTitleLength = 128

	// maxReviewBodyLength is the max review body length.
	maxReviewBodyLength = 2048

	// minRating is the minimum star rating.
	minRating = 1

	// maxRating is the maximum star rating.
	maxRating = 5

	// minTradesForDisplay is the minimum completed trades before reputation is displayed.
	minTradesForDisplay = 3
)

// Review represents a marketplace-specific review tied to a completed order.
type Review struct {
	ReviewerPubKey ed25519.PublicKey `json:"reviewerPubKey"`
	SellerPubKey   ed25519.PublicKey `json:"sellerPubKey"`
	OrderID        string            `json:"orderId"`
	Rating         int               `json:"rating"` // 1-5 stars
	Title          string            `json:"title"`
	Body           string            `json:"body"`
	Timestamp      time.Time         `json:"timestamp"`
	Signature      []byte            `json:"signature"`
}

// reviewDigest computes the SHA-256 hash of all Review fields except Signature.
func reviewDigest(r *Review) []byte {
	h := sha256.New()
	h.Write(r.ReviewerPubKey)
	h.Write(r.SellerPubKey)
	h.Write([]byte(r.OrderID))
	h.Write([]byte{byte(r.Rating)})
	h.Write([]byte(r.Title))
	h.Write([]byte(r.Body))
	h.Write([]byte(r.Timestamp.Format(time.RFC3339Nano)))
	return h.Sum(nil)
}

// Sign signs the review with the reviewer's Ed25519 private key.
func (r *Review) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("reputation: invalid private key size %d", len(privateKey))
	}
	digest := reviewDigest(r)
	r.Signature = ed25519.Sign(privateKey, digest)
	return nil
}

// Verify checks that the review signature is valid.
func (r *Review) Verify() bool {
	if len(r.ReviewerPubKey) != ed25519.PublicKeySize {
		return false
	}
	if len(r.Signature) != ed25519.SignatureSize {
		return false
	}
	digest := reviewDigest(r)
	return ed25519.Verify(r.ReviewerPubKey, digest, r.Signature)
}

// reviewStateStore answers whisper.StateStore for the mercury.reviews
// topic. Fingerprint, Snapshot and Delta read the service's record set,
// which holds the encoding of exactly the reviews in the service, so the
// summary whisper's G2 digest compares covers every held review's content,
// and Delta filters on the time a review was stored here rather than its
// author's Timestamp, which whisper's per-peer watermark (the local time it
// last selected records for that peer) cannot be compared with. Apply
// admits through mergeReview, the rule Submit and the gossip subscriber use.
type reviewStateStore struct {
	service *ReviewService
}

// Fingerprint summarises every held review's content; 0 when none is held.
func (s *reviewStateStore) Fingerprint() uint64 { return s.service.records.Fingerprint() }

// Snapshot returns every held review.
func (s *reviewStateStore) Snapshot() [][]byte { return s.service.records.Snapshot() }

// Delta returns the reviews stored here at or after since.
func (s *reviewStateStore) Delta(since time.Time) [][]byte { return s.service.records.Delta(since) }

// BucketDigest lets a fingerprint mismatch narrow to the differing buckets.
func (s *reviewStateStore) BucketDigest(buckets uint16) []uint64 {
	return s.service.records.BucketDigest(buckets)
}

// RecordsInBuckets serves the reviews of the buckets a peer differs on.
func (s *reviewStateStore) RecordsInBuckets(buckets uint16, want []bool) [][]byte {
	return s.service.records.RecordsInBuckets(buckets, want)
}

// Apply admits a review a peer sent through state sync, refusing what
// mergeReview refuses.
func (s *reviewStateStore) Apply(data []byte) error {
	var r Review
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("reputation: unmarshal review: %w", err)
	}
	return s.service.mergeReview(&r)
}

// ReviewService manages marketplace-specific reviews. Reviews are gossiped
// via Whisper StatefulMerge on the mercury.reviews topic. records holds the
// encoding of each review in reviews under reviewKey, written only by
// mergeReview under mu, so the two never disagree.
type ReviewService struct {
	whisper *whisper.Engine
	reviews map[string][]Review // hex(sellerPubKey) -> reviews
	records *whisper.RecordSet  // reviewKey -> encoded review
	mu      sync.RWMutex
}

// NewReviewService creates a ReviewService wired to the given Whisper engine.
func NewReviewService(engine *whisper.Engine) *ReviewService {
	rs := &ReviewService{
		whisper: engine,
		reviews: make(map[string][]Review),
		records: whisper.NewRecordSet(),
	}

	store := &reviewStateStore{service: rs}

	_ = engine.RegisterTopic(topicReviews, whisper.TopicConfig{
		Mode:  whisper.StatefulMerge,
		Store: store,
		Rumor: true,
	})

	engine.Subscribe(topicReviews, &reviewSubscriber{service: rs})

	return rs
}

// Submit publishes a review. The review must be signed and valid.
// Reviews require a valid OrderID (Sybil resistance -- reviews require economic activity).
func (rs *ReviewService) Submit(review Review) error {
	// Apply locally, under the rule a peer's copy is admitted by.
	if err := rs.mergeReview(&review); err != nil {
		return err
	}

	// Publish to gossip — mergeReview already applied local state above,
	// so we skip cfg.Store.Apply (that path is for the gossip receive side).
	data, err := json.Marshal(review)
	if err != nil {
		return fmt.Errorf("reputation: marshal review: %w", err)
	}

	// Broadcast to peers via Whisper.
	if err := rs.whisper.Publish(topicReviews, data); err != nil {
		return fmt.Errorf("reputation: publish review: %w", err)
	}

	return nil
}

// ForSeller returns all reviews for a given seller, ordered by timestamp descending.
func (rs *ReviewService) ForSeller(pubkey ed25519.PublicKey) []Review {
	key := hex.EncodeToString(pubkey)

	rs.mu.RLock()
	defer rs.mu.RUnlock()

	reviews := rs.reviews[key]
	result := make([]Review, len(reviews))
	copy(result, reviews)

	// Reverse for descending order (newest first)
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// AverageRating computes the average star rating for a seller.
// Returns 0.0 if the seller has no reviews.
func (rs *ReviewService) AverageRating(pubkey ed25519.PublicKey) float64 {
	key := hex.EncodeToString(pubkey)

	rs.mu.RLock()
	defer rs.mu.RUnlock()

	reviews := rs.reviews[key]
	if len(reviews) == 0 {
		return 0.0
	}

	var total int
	for _, r := range reviews {
		total += r.Rating
	}
	return float64(total) / float64(len(reviews))
}

// ReviewCount returns the number of reviews for a seller.
func (rs *ReviewService) ReviewCount(pubkey ed25519.PublicKey) int {
	key := hex.EncodeToString(pubkey)

	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return len(rs.reviews[key])
}

// mergeReview admits one review, from Submit, from gossip
// (reviewSubscriber.OnBroadcast) or from state sync (reviewStateStore.Apply),
// under one rule, so a peer cannot place on one path a review another
// refuses: validateReview's checks, then one review per reviewer per order.
// When two versions of the same reviewer's review of an order meet, the
// earlier Timestamp is kept, and an equal Timestamp is settled by the
// smaller signature, so every node keeps the same one whatever order they
// arrived in and the content fingerprints whisper compares can agree. A
// review that does not replace the held one is not an error.
func (rs *ReviewService) mergeReview(r *Review) error {
	if err := validateReview(r); err != nil {
		return err
	}
	sellerHex := hex.EncodeToString(r.SellerPubKey)
	reviewerHex := hex.EncodeToString(r.ReviewerPubKey)

	rs.mu.Lock()
	defer rs.mu.Unlock()

	held := -1
	for i, existing := range rs.reviews[sellerHex] {
		if hex.EncodeToString(existing.ReviewerPubKey) == reviewerHex && existing.OrderID == r.OrderID {
			held = i
			break
		}
	}
	if held >= 0 && !reviewSupersedes(r, &rs.reviews[sellerHex][held]) {
		return nil
	}

	cp := *r
	data, err := json.Marshal(&cp)
	if err != nil {
		return fmt.Errorf("reputation: marshal review: %w", err)
	}
	if held >= 0 {
		rs.reviews[sellerHex][held] = cp
	} else {
		rs.reviews[sellerHex] = append(rs.reviews[sellerHex], cp)
	}
	rs.records.Put(reviewKey(sellerHex, reviewerHex, r.OrderID), data)
	return nil
}

// reviewSupersedes reports whether incoming replaces held for the same
// reviewer and order: an earlier Timestamp, or the same Timestamp and a
// smaller signature.
func reviewSupersedes(incoming, held *Review) bool {
	if !incoming.Timestamp.Equal(held.Timestamp) {
		return incoming.Timestamp.Before(held.Timestamp)
	}
	return bytes.Compare(incoming.Signature, held.Signature) < 0
}

// reviewKey is a review's identity in the record set: the seller, the
// reviewer and the order, the three fields mergeReview deduplicates on.
func reviewKey(sellerHex, reviewerHex, orderID string) string {
	return sellerHex + "/" + reviewerHex + "/" + orderID
}

// validateReview performs comprehensive validation on a review.
func validateReview(r *Review) error {
	if len(r.ReviewerPubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("reputation: invalid reviewer public key size %d", len(r.ReviewerPubKey))
	}
	if len(r.SellerPubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("reputation: invalid seller public key size %d", len(r.SellerPubKey))
	}
	if r.OrderID == "" {
		return fmt.Errorf("reputation: order ID is required (reviews require a completed order)")
	}
	if r.Rating < minRating || r.Rating > maxRating {
		return fmt.Errorf("reputation: rating must be %d-%d, got %d", minRating, maxRating, r.Rating)
	}
	if len(r.Title) > maxReviewTitleLength {
		return fmt.Errorf("reputation: title length %d exceeds limit %d", len(r.Title), maxReviewTitleLength)
	}
	if len(r.Body) > maxReviewBodyLength {
		return fmt.Errorf("reputation: body length %d exceeds limit %d", len(r.Body), maxReviewBodyLength)
	}
	if r.Timestamp.IsZero() {
		return fmt.Errorf("reputation: timestamp is required")
	}
	if !r.Verify() {
		return fmt.Errorf("reputation: invalid signature")
	}
	return nil
}

// reviewSubscriber implements whisper.Subscriber for review gossip.
type reviewSubscriber struct {
	service *ReviewService
}

// OnBroadcast implements whisper.Subscriber. A refused review is dropped:
// gossip is best-effort.
func (s *reviewSubscriber) OnBroadcast(topic string, from string, payload []byte) {
	var r Review
	if err := json.Unmarshal(payload, &r); err != nil {
		return
	}
	_ = s.service.mergeReview(&r)
}

// MercuryReputation combines Plutus universal reputation with Mercury-specific reviews.
type MercuryReputation struct {
	plutusRep *plutusRep.ReputationService
	reviews   *ReviewService
}

// NewMercuryReputation creates a combined reputation service.
func NewMercuryReputation(plutus *plutusRep.ReputationService, reviews *ReviewService) *MercuryReputation {
	return &MercuryReputation{
		plutusRep: plutus,
		reviews:   reviews,
	}
}

// CompositeScore computes a weighted composite reputation score for a seller.
// The score incorporates Plutus trade history and Mercury marketplace reviews.
//
// Components:
//   - tradeScore (40%): log-scaled completed trades, capped at 1.0 at 1000 trades
//   - disputeScore (30%): dispute win rate (wins / total), default 0.5
//   - reviewScore (20%): normalised average rating (1-5 stars -> 0.0-1.0)
//   - ageScore (10%): account age, capped at 1.0 after 365 days
func (mr *MercuryReputation) CompositeScore(pubkey ed25519.PublicKey) float64 {
	plutusScore := mr.plutusRep.Score(pubkey)

	// Trade score: log scale, cap at 1.0
	trades := float64(plutusScore.TotalTrades)
	tradeScore := math.Min(1.0, math.Log10(trades+1)/3.0)

	// Dispute score: use Plutus dispute rate, invert (lower dispute = better)
	disputeScore := 0.5
	if plutusScore.TotalTrades > 0 {
		disputeScore = 1.0 - plutusScore.DisputeRate
	}

	// Review score: Mercury-specific 1-5 star reviews normalised to 0-1
	avgRating := mr.reviews.AverageRating(pubkey)
	reviewScore := 0.5
	if mr.reviews.ReviewCount(pubkey) > 0 {
		reviewScore = (avgRating - 1.0) / 4.0
	}

	// Age score: capped at 1.0 after 365 days
	ageScore := 0.0
	if !plutusScore.FirstTradeAt.IsZero() {
		ageDays := time.Since(plutusScore.FirstTradeAt).Hours() / 24.0
		ageScore = math.Min(1.0, ageDays/365.0)
	}

	return (tradeScore * 0.4) + (disputeScore * 0.3) + (reviewScore * 0.2) + (ageScore * 0.1)
}

// IsNewSeller returns true if the seller has fewer than minTradesForDisplay
// completed trades. New sellers show "New Seller" instead of a score.
func (mr *MercuryReputation) IsNewSeller(pubkey ed25519.PublicKey) bool {
	return mr.plutusRep.TotalTrades(pubkey) < minTradesForDisplay
}

// SellerSummary returns a human-readable summary of a seller's reputation.
type SellerSummary struct {
	IsNew         bool
	TotalTrades   int
	PositiveRate  float64
	AverageRating float64
	ReviewCount   int
	Score         float64
}

// Summary computes a complete SellerSummary for the given seller.
func (mr *MercuryReputation) Summary(pubkey ed25519.PublicKey) SellerSummary {
	plutusScore := mr.plutusRep.Score(pubkey)

	return SellerSummary{
		IsNew:         mr.IsNewSeller(pubkey),
		TotalTrades:   plutusScore.TotalTrades,
		PositiveRate:  plutusScore.PositiveRate,
		AverageRating: mr.reviews.AverageRating(pubkey),
		ReviewCount:   mr.reviews.ReviewCount(pubkey),
		Score:         mr.CompositeScore(pubkey),
	}
}

// DecayWeight returns the time-decay weight for a given event age.
// Recent activity is weighted higher than old activity.
//
//   - Last 30 days: 1.0x
//   - 30-90 days: 0.8x
//   - 90-180 days: 0.5x
//   - 180-365 days: 0.3x
//   - Over 365 days: 0.1x
func DecayWeight(eventTime time.Time) float64 {
	days := time.Since(eventTime).Hours() / 24.0

	switch {
	case days <= 30:
		return 1.0
	case days <= 90:
		return 0.8
	case days <= 180:
		return 0.5
	case days <= 365:
		return 0.3
	default:
		return 0.1
	}
}

// DecayedAverageRating computes a time-weighted average rating for a seller,
// giving more weight to recent reviews.
func (mr *MercuryReputation) DecayedAverageRating(pubkey ed25519.PublicKey) float64 {
	reviews := mr.reviews.ForSeller(pubkey)
	if len(reviews) == 0 {
		return 0.0
	}

	var weightedSum, weightSum float64
	for _, r := range reviews {
		w := DecayWeight(r.Timestamp)
		weightedSum += float64(r.Rating) * w
		weightSum += w
	}

	if weightSum == 0 {
		return 0.0
	}
	return weightedSum / weightSum
}
