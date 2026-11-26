/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package store

import (
	"strings"
	"testing"
)

// A listing field whose stored value does not decode is refused: GetListing
// never serves the listing with that field at its zero value (a price of 0).
// The map's values arrive by gossip, so a peer can write any bytes there.
func TestGetListingRefusesAFieldThatDoesNotDecode(t *testing.T) {
	cases := map[string][]byte{
		"title":     []byte(`{"v":5}`),
		"price":     []byte(`{"v":"twelve"}`),
		"images":    []byte(`{"v":{"not":"a list"}}`),
		"createdAt": []byte(`{"v":"yesterday"}`),
		"currency":  []byte(`not a field envelope`),
	}
	for field, bad := range cases {
		sm := testStoreManager(t)
		if err := sm.AddListing(testListing("l1")); err != nil {
			t.Fatalf("%s: AddListing: %v", field, err)
		}
		sm.lwwMap.Set(fieldKey("l1", field), bad)
		l, err := sm.GetListing("l1")
		if err == nil || l != nil || !strings.Contains(err.Error(), "field "+field) {
			t.Errorf("%s: GetListing = (%+v, %v), want no listing and an error naming the field", field, l, err)
		}
	}
}
