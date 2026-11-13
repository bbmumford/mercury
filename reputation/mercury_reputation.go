/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package reputation

import (
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
	OrderID        string           `json:"orderId"`
	Rating         int              `json:"rating"` // 1-5 stars
	Title          string           `json:"title"`
	Body           string           `json:"body"`
	Timestamp      time.Time        `json:"timestamp"`
	Signature      []byte           `json:"signature"`
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

// reviewStateStore implements whisper.StateStore for review gossip.
type reviewStateStore struct {
	service *ReviewService
}

func (s *reviewStateStore) Fingerprint() uint64 {
	s.service.mu.RLock()
	defer s.service.mu.RUnlock()

	var fp uint64
	for _, reviews := range s.service.reviews {
		fp ^= uint64(len(reviews))
		for _, r := range reviews {
			fp ^= uint64(r.Timestamp.UnixNano())
		}
	}
	return fp
}

// Snapshot returns every stored review; a review that fails to marshal is
// left out so it does not block the rest.
func (s *reviewStateStore) Snapshot() [][]byte {
	return s.Delta(time.Time{})
}

// Delta returns the reviews stamped after since; the zero time admits
// every review, which is the snapshot.
func (s *reviewStateStore) Delta(since time.Time) [][]byte {
	s.service.mu.RLock()
	defer s.service.mu.RUnlock()

	var records [][]byte
	for _, reviews := range s.service.reviews {
		for _, r := range reviews {
			if since.IsZero() || r.Timestamp.After(since) {
				data, err := json.Marshal(r)
				if err != nil {
					continue
				}
				records = append(records, data)
			}
		}
	}
	return records
}

func (s *reviewStateStore) Apply(data []byte) error {
	var r Review
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("reputation: unmarshal review: %w", err)
	}
	if !r.Verify() {
		return fmt.Errorf("reputation: invalid review signature")
	}
	s.service.mergeReview(&r)
	return nil
}

// ReviewService manages marketplace-specific reviews. Reviews are gossiped
// via Whisper StatefulMerge on the mercury.reviews topic.
type ReviewService struct {
	whisper *whisper.Engine
	reviews map[string][]Review // hex(sellerPubKey) -> reviews
	mu      sync.RWMutex
}

// NewReviewService creates a ReviewService wired to the given Whisper engine.
func NewReviewService(engine *whisper.Engine) *ReviewService {
	rs := &ReviewService{
		whisper: engine,
		reviews: make(map[string][]Review),
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
	if err := validateReview(&review); err != nil {
		return err
	}

	rs.mergeReview(&review)

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

// mergeReview adds a review, deduplicating by reviewer + order.
func (rs *ReviewService) mergeReview(r *Review) {
	sellerHex := hex.EncodeToString(r.SellerPubKey)
	reviewerHex := hex.EncodeToString(r.ReviewerPubKey)

	rs.mu.Lock()
	defer rs.mu.Unlock()

	// Dedup: one review per reviewer per order
	for _, existing := range rs.reviews[sellerHex] {
		if hex.EncodeToString(existing.ReviewerPubKey) == reviewerHex &&
			existing.OrderID == r.OrderID {
			return
		}
	}

	cp := *r
	rs.reviews[sellerHex] = append(rs.reviews[sellerHex], cp)
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

// OnBroadcast implements whisper.Subscriber.
func (s *reviewSubscriber) OnBroadcast(topic string, from string, payload []byte) {
	var r Review
	if err := json.Unmarshal(payload, &r); err != nil {
		return
	}
	if !r.Verify() {
		return
	}
	s.service.mergeReview(&r)
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
