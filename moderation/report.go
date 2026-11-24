/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package moderation

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
	// topicReports is the BroadcastOnly topic for content reports.
	topicReports = "mercury.reports"

	// maxReasonLength is the maximum report reason/details length.
	maxReasonLength = 500

	// defaultHideThreshold is the default number of reports before content is auto-hidden.
	defaultHideThreshold = 5
)

// Valid content types for reports.
const (
	ContentTypeListing = "listing"
	ContentTypeProfile = "profile"
	ContentTypeMessage = "message"
	ContentTypeStore   = "store"
)

// Valid report reasons.
const (
	ReasonFraud       = "fraud"
	ReasonIllegal     = "illegal"
	ReasonSpam        = "spam"
	ReasonCounterfeit = "counterfeit"
	ReasonOther       = "other"
)

// ContentReport represents a report/flag against content on the marketplace.
// Reports are propagated via gossip (G3 rumor push) and are signals, not enforcement.
type ContentReport struct {
	ReporterPubKey ed25519.PublicKey `json:"reporterPubKey"`
	ContentType    string            `json:"contentType"` // "listing", "profile", "message", "store"
	ContentID      string            `json:"contentId"`
	Reason         string            `json:"reason"` // "fraud", "illegal", "spam", "counterfeit", "other"
	Details        string            `json:"details"`
	Timestamp      time.Time         `json:"timestamp"`
	Signature      []byte            `json:"signature"`
}

// reportDigest computes the SHA-256 hash of all ContentReport fields except Signature.
func reportDigest(r *ContentReport) []byte {
	h := sha256.New()
	h.Write(r.ReporterPubKey)
	h.Write([]byte(r.ContentType))
	h.Write([]byte(r.ContentID))
	h.Write([]byte(r.Reason))
	h.Write([]byte(r.Details))
	h.Write([]byte(r.Timestamp.Format(time.RFC3339Nano)))
	return h.Sum(nil)
}

// Sign signs the report with the reporter's Ed25519 private key.
func (r *ContentReport) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("moderation: invalid private key size %d", len(privateKey))
	}
	digest := reportDigest(r)
	r.Signature = ed25519.Sign(privateKey, digest)
	return nil
}

// Verify checks that the report signature is valid.
func (r *ContentReport) Verify() bool {
	if len(r.ReporterPubKey) != ed25519.PublicKeySize {
		return false
	}
	if len(r.Signature) != ed25519.SignatureSize {
		return false
	}
	digest := reportDigest(r)
	return ed25519.Verify(r.ReporterPubKey, digest, r.Signature)
}

// ModerationService tracks content reports and provides client-side filtering.
// Reports are gossiped via Whisper BroadcastOnly topic and stored locally.
// Clients choose their own threshold for auto-hiding reported content.
type ModerationService struct {
	whisper       *whisper.Engine
	reports       map[string][]ContentReport // contentID -> reports
	hideThreshold int
	mu            sync.RWMutex
}

// NewModerationService creates a ModerationService wired to the given Whisper engine.
func NewModerationService(engine *whisper.Engine) *ModerationService {
	ms := &ModerationService{
		whisper:       engine,
		reports:       make(map[string][]ContentReport),
		hideThreshold: defaultHideThreshold,
	}

	// Register the BroadcastOnly topic for reports
	_ = engine.RegisterTopic(topicReports, whisper.TopicConfig{
		Mode: whisper.BroadcastOnly,
		TTL:  30 * 24 * time.Hour, // reports expire after 30 days
	})

	engine.Subscribe(topicReports, &reportSubscriber{service: ms})

	return ms
}

// SetHideThreshold configures the number of reports needed before content
// is considered hidden. Default is 5.
func (ms *ModerationService) SetHideThreshold(threshold int) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.hideThreshold = threshold
}

// Report submits a content report. The report must be signed and valid.
func (ms *ModerationService) Report(report ContentReport) error {
	if err := validateReport(&report); err != nil {
		return err
	}

	ms.addReport(report)

	// Publish to gossip
	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("moderation: marshal report: %w", err)
	}

	if err := ms.whisper.Publish(topicReports, payload); err != nil {
		return fmt.Errorf("moderation: publish report: %w", err)
	}

	return nil
}

// ReportsFor returns all reports for the given content ID.
func (ms *ModerationService) ReportsFor(contentID string) []ContentReport {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	reports := ms.reports[contentID]
	result := make([]ContentReport, len(reports))
	copy(result, reports)
	return result
}

// ReportCount returns the number of reports for the given content ID.
func (ms *ModerationService) ReportCount(contentID string) int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return len(ms.reports[contentID])
}

// ShouldHide returns true if the content has accumulated enough reports to
// exceed the configured hide threshold.
func (ms *ModerationService) ShouldHide(contentID string) bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return len(ms.reports[contentID]) >= ms.hideThreshold
}

// UniqueReporters returns the number of unique reporters for a content ID.
func (ms *ModerationService) UniqueReporters(contentID string) int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	seen := make(map[string]bool)
	for _, r := range ms.reports[contentID] {
		seen[hex.EncodeToString(r.ReporterPubKey)] = true
	}
	return len(seen)
}

// addReport stores a report locally, deduplicating by reporter + content.
func (ms *ModerationService) addReport(report ContentReport) {
	reporterHex := hex.EncodeToString(report.ReporterPubKey)

	ms.mu.Lock()
	defer ms.mu.Unlock()

	// Dedup: one report per reporter per content
	for _, existing := range ms.reports[report.ContentID] {
		if hex.EncodeToString(existing.ReporterPubKey) == reporterHex {
			return
		}
	}

	ms.reports[report.ContentID] = append(ms.reports[report.ContentID], report)
}

// validateReport performs comprehensive validation on a content report.
func validateReport(r *ContentReport) error {
	if len(r.ReporterPubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("moderation: invalid reporter public key size %d", len(r.ReporterPubKey))
	}
	if r.ContentID == "" {
		return fmt.Errorf("moderation: content ID is required")
	}

	switch r.ContentType {
	case ContentTypeListing, ContentTypeProfile, ContentTypeMessage, ContentTypeStore:
		// valid
	default:
		return fmt.Errorf("moderation: invalid content type %q", r.ContentType)
	}

	switch r.Reason {
	case ReasonFraud, ReasonIllegal, ReasonSpam, ReasonCounterfeit, ReasonOther:
		// valid
	default:
		return fmt.Errorf("moderation: invalid reason %q", r.Reason)
	}

	if len(r.Details) > maxReasonLength {
		return fmt.Errorf("moderation: details length %d exceeds limit %d", len(r.Details), maxReasonLength)
	}
	if r.Timestamp.IsZero() {
		return fmt.Errorf("moderation: timestamp is required")
	}
	if !r.Verify() {
		return fmt.Errorf("moderation: invalid signature")
	}
	return nil
}

// reportSubscriber implements whisper.Subscriber for content reports.
type reportSubscriber struct {
	service *ModerationService
}

// OnBroadcast implements whisper.Subscriber.
func (s *reportSubscriber) OnBroadcast(topic string, from string, payload []byte) {
	var report ContentReport
	if err := json.Unmarshal(payload, &report); err != nil {
		return
	}
	if !report.Verify() {
		return
	}
	s.service.addReport(report)
}
