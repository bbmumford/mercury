/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package discovery

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bbmumford/mercury/store"
	"github.com/bbmumford/whisper"
)

// topicPrefix is the gossip topic prefix for category channels.
const topicPrefix = "mercury.cat."

// maxCategoryDepth is the maximum hierarchy depth (dot-separated segments).
const maxCategoryDepth = 4

// announcementTTL is how long category announcements remain valid.
const announcementTTL = 24 * time.Hour

// CategoryChannel represents a subscribed category gossip channel.
type CategoryChannel struct {
	Name     string `json:"name"`
	TopicID  string `json:"topicId"`
	Listings int    `json:"listings"`
}

// CategoryAnnouncement is the lightweight payload published to category topics.
// Kept under 256 bytes to match spec.
type CategoryAnnouncement struct {
	StoreID     string `json:"s"`
	ListingID   string `json:"l"`
	Title       string `json:"t"`
	Price       uint64 `json:"p"`
	Currency    string `json:"c"`
	Category    string `json:"cat"`
	SellerID    string `json:"sel"`
	PublishedAt int64  `json:"ts"`
}

// CategoryManager manages category-scoped gossip channels for listing discovery.
// Each category maps to a Whisper BroadcastOnly topic: mercury.cat.{category}.
type CategoryManager struct {
	engine   *whisper.Engine
	mu       sync.RWMutex
	channels map[string]*CategoryChannel
	listings map[string]map[string]store.Listing // category -> listingID -> listing
	receiver *categoryReceiver
}

// categoryReceiver implements whisper.Subscriber to receive broadcast messages.
type categoryReceiver struct {
	mgr *CategoryManager
}

// OnBroadcast handles incoming category announcements from gossip.
func (r *categoryReceiver) OnBroadcast(topic string, from string, payload []byte) {
	category := strings.TrimPrefix(topic, topicPrefix)

	var ann CategoryAnnouncement
	if err := json.Unmarshal(payload, &ann); err != nil {
		return
	}

	// Check TTL
	if time.Since(time.Unix(ann.PublishedAt, 0)) > announcementTTL {
		return
	}

	r.mgr.mu.Lock()
	defer r.mgr.mu.Unlock()

	// Store the listing in all matching categories (exact + parent subscriptions)
	for cat, ch := range r.mgr.channels {
		if category == cat || strings.HasPrefix(category, cat+".") {
			listing := store.Listing{
				ID:        ann.ListingID,
				StoreID:   ann.StoreID,
				Title:     ann.Title,
				Price:     ann.Price,
				Currency:  ann.Currency,
				Category:  ann.Category,
				SellerID:  ann.SellerID,
				Active:    true,
				UpdatedAt: time.Unix(ann.PublishedAt, 0),
			}
			if r.mgr.listings[cat] == nil {
				r.mgr.listings[cat] = make(map[string]store.Listing)
			}
			r.mgr.listings[cat][ann.ListingID] = listing
			ch.Listings = len(r.mgr.listings[cat])
		}
	}
}

// NewCategoryManager creates a CategoryManager wired to the given Whisper engine.
func NewCategoryManager(engine *whisper.Engine) *CategoryManager {
	cm := &CategoryManager{
		engine:   engine,
		channels: make(map[string]*CategoryChannel),
		listings: make(map[string]map[string]store.Listing),
	}
	cm.receiver = &categoryReceiver{mgr: cm}
	return cm
}

// Subscribe subscribes to a category gossip channel. The category must not
// exceed maxCategoryDepth levels. Subscribing to a parent category (e.g.
// "electronics") will also receive announcements for child categories
// (e.g. "electronics.phones").
func (cm *CategoryManager) Subscribe(category string) error {
	if err := validateCategory(category); err != nil {
		return err
	}

	topicName := topicPrefix + category

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, exists := cm.channels[category]; exists {
		return nil // already subscribed
	}

	// Register the topic as BroadcastOnly in Whisper
	err := cm.engine.RegisterTopic(topicName, whisper.TopicConfig{
		Mode: whisper.BroadcastOnly,
		TTL:  announcementTTL,
	})
	if err != nil {
		// Topic may already be registered (e.g., parent registered child topic).
		// Only fail if it's a real error.
		if !strings.Contains(err.Error(), "already registered") {
			return fmt.Errorf("discovery: register topic %q: %w", topicName, err)
		}
	}

	cm.engine.Subscribe(topicName, cm.receiver)

	cm.channels[category] = &CategoryChannel{
		Name:    category,
		TopicID: topicName,
	}
	cm.listings[category] = make(map[string]store.Listing)
	return nil
}

// Unsubscribe removes a category subscription. Listings cached for this
// category are discarded.
func (cm *CategoryManager) Unsubscribe(category string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, exists := cm.channels[category]; !exists {
		return fmt.Errorf("discovery: not subscribed to %q", category)
	}

	delete(cm.channels, category)
	delete(cm.listings, category)
	return nil
}

// PublishListing publishes a listing to its category gossip channel.
// The listing's Category field determines the topic.
func (cm *CategoryManager) PublishListing(category string, listing store.Listing) error {
	if err := validateCategory(category); err != nil {
		return err
	}

	topicName := topicPrefix + category

	ann := CategoryAnnouncement{
		StoreID:     listing.StoreID,
		ListingID:   listing.ID,
		Title:       listing.Title,
		Price:       listing.Price,
		Currency:    listing.Currency,
		Category:    category,
		SellerID:    listing.SellerID,
		PublishedAt: time.Now().Unix(),
	}

	data, err := json.Marshal(ann)
	if err != nil {
		return fmt.Errorf("discovery: marshal announcement: %w", err)
	}

	// Register topic if not already registered (seller may not be subscribed)
	_ = cm.engine.RegisterTopic(topicName, whisper.TopicConfig{
		Mode: whisper.BroadcastOnly,
		TTL:  announcementTTL,
	})

	if err := cm.engine.Publish(topicName, data); err != nil {
		return fmt.Errorf("discovery: publish to %q: %w", topicName, err)
	}
	return nil
}

// Browse returns all cached listings for a given category.
func (cm *CategoryManager) Browse(category string) []store.Listing {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	catListings, ok := cm.listings[category]
	if !ok {
		return nil
	}

	result := make([]store.Listing, 0, len(catListings))
	for _, l := range catListings {
		result = append(result, l)
	}
	return result
}

// Channels returns all currently subscribed category channels.
func (cm *CategoryManager) Channels() []*CategoryChannel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	result := make([]*CategoryChannel, 0, len(cm.channels))
	for _, ch := range cm.channels {
		result = append(result, ch)
	}
	return result
}

// validateCategory checks category format: dot-separated, max depth 4,
// non-empty segments, lowercase alphanumeric.
func validateCategory(category string) error {
	if category == "" {
		return fmt.Errorf("discovery: empty category")
	}
	parts := strings.Split(category, ".")
	if len(parts) > maxCategoryDepth {
		return fmt.Errorf("discovery: category %q exceeds max depth %d", category, maxCategoryDepth)
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("discovery: category %q has empty segment", category)
		}
		for _, c := range part {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return fmt.Errorf("discovery: category %q contains invalid character %q (must be lowercase alphanumeric)", category, string(c))
			}
		}
	}
	return nil
}

// DefaultCategories returns the fixed category taxonomy shipped with the client.
func DefaultCategories() []string {
	return []string{
		"electronics",
		"electronics.phones",
		"electronics.phones.iphone",
		"electronics.phones.android",
		"electronics.computers",
		"electronics.computers.laptops",
		"electronics.computers.desktops",
		"clothing",
		"clothing.mens",
		"clothing.womens",
		"home",
		"home.furniture",
		"home.appliances",
		"services",
		"services.digital",
		"services.local",
		"collectibles",
		"collectibles.art",
		"collectibles.coins",
	}
}
