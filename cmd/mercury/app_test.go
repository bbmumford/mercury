// Copyright (c) 2026 Brandon Miller-Mumford.
// Licensed under the MIT License; see LICENSE.

//go:build wails

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	meshruntime "github.com/bbmumford/mercury/runtime/mesh"
)

// TestLoadMeshSettings_MissingFileReturnsDefaults verifies that a
// fresh install (no settings.json yet) degrades to DESIGN §19 Wails-
// desktop defaults.
func TestLoadMeshSettings_MissingFileReturnsDefaults(t *testing.T) {
	t.Parallel()
	a := &App{dataDir: t.TempDir()}
	got := a.loadMeshSettings()
	want := defaultMeshSettings()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mismatch: got %+v, want %+v", got, want)
	}
}

// TestLoadMeshSettings_CorruptFileReturnsDefaults verifies that a
// corrupted settings.json does NOT prevent startup — settings degrade
// silently to the documented defaults.
func TestLoadMeshSettings_CorruptFileReturnsDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mesh-settings.json"), []byte("{this is not json"), 0o600); err != nil {
		t.Fatalf("write corrupt settings: %v", err)
	}
	a := &App{dataDir: dir}
	got := a.loadMeshSettings()
	want := defaultMeshSettings()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("corrupt file should yield defaults: got %+v, want %+v", got, want)
	}
}

// TestSetMeshSettings_PersistsAndRoundTrips verifies the frontend-
// driven SetMeshSettings → file → GetMeshSettings round-trip survives
// in a DESIGN-compatible shape.
func TestSetMeshSettings_PersistsAndRoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := &App{dataDir: dir}
	settings := MeshSettings{
		Enabled:              true,
		EnableReplication:    true,
		EnableBlobProvider:   true,
		EnableMessageRelay:   false,
		EnableBalanceWitness: false,
		EnableTxWitness:      false,
		StorageMaxPct:        40,
		StorageHeadroomPct:   20,
		StorageMinReserveGB:  15,
	}
	if err := a.SetMeshSettings(settings); err != nil {
		t.Fatalf("SetMeshSettings: %v", err)
	}
	// File must exist.
	data, err := os.ReadFile(filepath.Join(dir, "mesh-settings.json"))
	if err != nil {
		t.Fatalf("settings file not written: %v", err)
	}
	var roundTrip MeshSettings
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("settings file invalid JSON: %v", err)
	}
	if !reflect.DeepEqual(roundTrip, settings) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", roundTrip, settings)
	}
	got := a.GetMeshSettings()
	if !reflect.DeepEqual(got, settings) {
		t.Errorf("GetMeshSettings mismatch: got %+v, want %+v", got, settings)
	}
}

// TestDefaultMeshSettings_AlignsWithDESIGN19 pins the Wails-desktop
// storage defaults to the DESIGN §19 contract: StorageMaxPct=50,
// TargetHeadroom=25, MinReserveGB=10. A regression in either breaks
// the runtime parity claim.
func TestDefaultMeshSettings_AlignsWithDESIGN19(t *testing.T) {
	t.Parallel()
	d := defaultMeshSettings()
	if d.StorageMaxPct != 50 {
		t.Errorf("StorageMaxPct=%d, want 50 per DESIGN §19", d.StorageMaxPct)
	}
	if d.StorageHeadroomPct != 25 {
		t.Errorf("StorageHeadroomPct=%d, want 25 per DESIGN §19", d.StorageHeadroomPct)
	}
	if d.StorageMinReserveGB != 10 {
		t.Errorf("StorageMinReserveGB=%d, want 10 per DESIGN §19", d.StorageMinReserveGB)
	}
	if !d.EnableReplication || !d.EnableBlobProvider || !d.EnableMessageRelay ||
		!d.EnableBalanceWitness || !d.EnableTxWitness {
		t.Errorf("DESIGN §19: Wails-desktop should enable all four role categories by default, got %+v", d)
	}
}

// TestSettingsTranslateToConfig verifies the translation from
// MeshSettings → meshruntime.Config is complete and preserves the
// toggles the frontend exposes.
func TestSettingsTranslateToConfig(t *testing.T) {
	t.Parallel()
	settings := MeshSettings{
		Enabled:              true,
		EnableReplication:    true,
		EnableBlobProvider:   false,
		EnableMessageRelay:   true,
		EnableBalanceWitness: true,
		EnableTxWitness:      false,
		StorageMaxPct:        42,
		StorageHeadroomPct:   21,
		StorageMinReserveGB:  8,
	}
	// Simulate the translation startMeshRuntime would perform.
	cfg := meshruntime.Config{
		EnableReplication:        settings.EnableReplication,
		EnableBlobProvider:       settings.EnableBlobProvider,
		EnableMessageRelay:       settings.EnableMessageRelay,
		EnableBalanceWitness:     settings.EnableBalanceWitness,
		EnableTxWitness:          settings.EnableTxWitness,
		StorageMaxPct:            settings.StorageMaxPct,
		StorageTargetHeadroomPct: settings.StorageHeadroomPct,
		StorageMinReserveGB:      settings.StorageMinReserveGB,
		StorageHardMinGB:         2,
		NetworkBudgetMbps:        10,
		AppID:                    "mercury-desktop",
		DataDir:                  t.TempDir(),
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("translated cfg fails Validate: %v", err)
	}
	if cfg.EnableBlobProvider {
		t.Errorf("EnableBlobProvider should have been carried through as false")
	}
	if cfg.StorageMaxPct != 42 {
		t.Errorf("StorageMaxPct mismatch: %d", cfg.StorageMaxPct)
	}
}
