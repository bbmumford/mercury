/*
 * Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
 * Queries: licensing@hstles.com
 */
package discovery

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// SearchEntry is an indexed listing for full-text search.
type SearchEntry struct {
	ListingID   string   `json:"listingId"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Price       uint64   `json:"price"`
	SellerID    string   `json:"sellerId"`
	Keywords    []string `json:"keywords,omitempty"`
	Score       float64  `json:"score"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Reputation  float64  `json:"reputation"` // 0.0 - 1.0 normalised seller reputation
}

// SearchIndex is an in-memory inverted index for listing search.
// It supports substring matching, keyword matching, category filtering,
// and price range queries with relevance-weighted ranking.
type SearchIndex struct {
	mu      sync.RWMutex
	entries map[string]*SearchEntry       // listingID -> entry
	tokens  map[string]map[string]bool    // token -> set of listingIDs
}

// NewSearchIndex creates an empty search index.
func NewSearchIndex() *SearchIndex {
	return &SearchIndex{
		entries: make(map[string]*SearchEntry),
		tokens:  make(map[string]map[string]bool),
	}
}

// Add indexes a SearchEntry. If an entry with the same ListingID exists,
// it is replaced.
func (idx *SearchIndex) Add(entry SearchEntry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Remove old tokens if replacing
	if old, exists := idx.entries[entry.ListingID]; exists {
		idx.removeTokensLocked(old)
	}

	cp := entry
	idx.entries[entry.ListingID] = &cp

	// Tokenise and index
	allTokens := tokenise(entry.Title)
	allTokens = append(allTokens, tokenise(entry.Description)...)
	for _, kw := range entry.Keywords {
		allTokens = append(allTokens, tokenise(kw)...)
	}
	allTokens = append(allTokens, tokenise(entry.Category)...)

	for _, tok := range allTokens {
		if idx.tokens[tok] == nil {
			idx.tokens[tok] = make(map[string]bool)
		}
		idx.tokens[tok][entry.ListingID] = true
	}
}

// Remove deletes a listing from the index.
func (idx *SearchIndex) Remove(listingID string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	entry, exists := idx.entries[listingID]
	if !exists {
		return
	}
	idx.removeTokensLocked(entry)
	delete(idx.entries, listingID)
}

// removeTokensLocked removes all token associations for an entry.
// Caller must hold idx.mu write lock.
func (idx *SearchIndex) removeTokensLocked(entry *SearchEntry) {
	allTokens := tokenise(entry.Title)
	allTokens = append(allTokens, tokenise(entry.Description)...)
	for _, kw := range entry.Keywords {
		allTokens = append(allTokens, tokenise(kw)...)
	}
	allTokens = append(allTokens, tokenise(entry.Category)...)

	for _, tok := range allTokens {
		if ids, ok := idx.tokens[tok]; ok {
			delete(ids, entry.ListingID)
			if len(ids) == 0 {
				delete(idx.tokens, tok)
			}
		}
	}
}

// Search performs a combined substring match and keyword match across all
// indexed entries. Results are ranked by a composite score and limited to
// `limit` results. If query is empty, returns all entries sorted by reputation.
func (idx *SearchIndex) Search(query string, limit int) []SearchEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	// Empty query: return all, sorted by reputation (browse mode)
	if strings.TrimSpace(query) == "" {
		return idx.allSortedByReputation(limit)
	}

	queryTokens := tokenise(query)
	queryLower := strings.ToLower(strings.TrimSpace(query))

	// Score each entry
	type scored struct {
		entry *SearchEntry
		score float64
	}
	var results []scored

	// Token match: find candidate entries via inverted index
	candidates := make(map[string]int) // listingID -> matched token count
	for _, qt := range queryTokens {
		// Exact token match
		if ids, ok := idx.tokens[qt]; ok {
			for id := range ids {
				candidates[id]++
			}
		}
		// Substring match across all tokens (for partial matches)
		for tok, ids := range idx.tokens {
			if strings.Contains(tok, qt) && tok != qt {
				for id := range ids {
					candidates[id]++
				}
			}
		}
	}

	for id, matchCount := range candidates {
		entry := idx.entries[id]
		if entry == nil {
			continue
		}

		score := idx.computeScore(entry, queryLower, queryTokens, matchCount)
		results = append(results, scored{entry: entry, score: score})
	}

	// Also check substring match on title/description for entries not found via tokens
	for id, entry := range idx.entries {
		if _, found := candidates[id]; found {
			continue
		}
		titleLower := strings.ToLower(entry.Title)
		descLower := strings.ToLower(entry.Description)
		if strings.Contains(titleLower, queryLower) || strings.Contains(descLower, queryLower) {
			score := idx.computeScore(entry, queryLower, queryTokens, 1)
			results = append(results, scored{entry: entry, score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > limit {
		results = results[:limit]
	}

	out := make([]SearchEntry, len(results))
	for i, r := range results {
		cp := *r.entry
		cp.Score = r.score
		out[i] = cp
	}
	return out
}

// ByCategory returns all entries in the given category, sorted by reputation.
func (idx *SearchIndex) ByCategory(category string) []SearchEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var results []SearchEntry
	for _, entry := range idx.entries {
		if entry.Category == category || strings.HasPrefix(entry.Category, category+".") {
			cp := *entry
			results = append(results, cp)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Reputation > results[j].Reputation
	})
	return results
}

// ByPriceRange returns all entries within [min, max] price range, sorted by price ascending.
func (idx *SearchIndex) ByPriceRange(min, max uint64) []SearchEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var results []SearchEntry
	for _, entry := range idx.entries {
		if entry.Price >= min && entry.Price <= max {
			cp := *entry
			results = append(results, cp)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Price < results[j].Price
	})
	return results
}

// Count returns the number of indexed entries.
func (idx *SearchIndex) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.entries)
}

// computeScore calculates a composite relevance score per the plan:
//
//	score = (textRelevance * 0.4) + (reputation * 0.3) + (recency * 0.2) + (categoryMatch * 0.1)
func (idx *SearchIndex) computeScore(entry *SearchEntry, queryLower string, queryTokens []string, matchCount int) float64 {
	// Text relevance: ratio of matched tokens to total query tokens,
	// boosted for title matches.
	textRelevance := 0.0
	if len(queryTokens) > 0 {
		textRelevance = float64(matchCount) / float64(len(queryTokens))
		if textRelevance > 1.0 {
			textRelevance = 1.0
		}
	}
	// Boost for exact substring in title
	titleLower := strings.ToLower(entry.Title)
	if strings.Contains(titleLower, queryLower) {
		textRelevance = math.Min(textRelevance+0.5, 1.0)
	}

	// Reputation (already 0.0-1.0)
	reputation := entry.Reputation

	// Recency: 1.0 / (1.0 + daysSinceUpdate)
	recency := 0.5
	if !entry.UpdatedAt.IsZero() {
		days := time.Since(entry.UpdatedAt).Hours() / 24.0
		if days < 0 {
			days = 0
		}
		recency = 1.0 / (1.0 + days)
	}

	// Category match: 1.0 if query tokens match category, 0.0 otherwise.
	// (Category match is more meaningful when used with explicit category filter;
	// for free-text search we give a small boost if any query token appears in category.)
	categoryMatch := 0.0
	catLower := strings.ToLower(entry.Category)
	for _, qt := range queryTokens {
		if strings.Contains(catLower, qt) {
			categoryMatch = 0.5
			break
		}
	}

	return (textRelevance * 0.4) + (reputation * 0.3) + (recency * 0.2) + (categoryMatch * 0.1)
}

// allSortedByReputation returns all entries sorted by reputation descending.
func (idx *SearchIndex) allSortedByReputation(limit int) []SearchEntry {
	results := make([]SearchEntry, 0, len(idx.entries))
	for _, entry := range idx.entries {
		cp := *entry
		cp.Score = cp.Reputation
		results = append(results, cp)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Reputation > results[j].Reputation
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// tokenise splits text into lowercase tokens, stripping common punctuation.
// Returns deduplicated tokens.
func tokenise(text string) []string {
	text = strings.ToLower(text)

	// Replace common separators with spaces
	replacer := strings.NewReplacer(
		".", " ", ",", " ", ";", " ", ":", " ",
		"(", " ", ")", " ", "[", " ", "]", " ",
		"{", " ", "}", " ", "/", " ", "\\", " ",
		"\"", " ", "'", " ", "-", " ", "_", " ",
		"\t", " ", "\n", " ", "\r", " ",
	)
	text = replacer.Replace(text)

	words := strings.Fields(text)

	// Deduplicate
	seen := make(map[string]bool, len(words))
	var tokens []string
	for _, w := range words {
		if len(w) < 2 {
			continue // skip single chars
		}
		if !seen[w] {
			seen[w] = true
			tokens = append(tokens, w)
		}
	}
	return tokens
}
