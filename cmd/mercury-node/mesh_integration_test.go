/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package main

import (
	"context"
	"testing"
	"time"

	meshruntime "github.com/bbmumford/mercury/runtime/mesh"
)

// TestMeshRuntime_StartStopCycle verifies mercury-node can construct
// the shared mesh runtime with typical CLI-flag defaults, bring it up,
// snapshot its Status, and tear it down cleanly. Witness roles are
// disabled so Neutrino does not attempt P2P DNS seeding on CI.
func TestMeshRuntime_StartStopCycle(t *testing.T) {
	t.Parallel()
	cfg := meshruntime.Config{
		EnableReplication:        true,
		EnableBlobProvider:       true,
		EnableMessageRelay:       true,
		EnableBalanceWitness:     false,
		EnableTxWitness:          false,
		StorageMaxPct:            70,
		StorageTargetHeadroomPct: 50,
		StorageMinReserveGB:      5,
		StorageHardMinGB:         2,
		NetworkBudgetMbps:        50,
		AppID:                    "mercury-node-test",
		DataDir:                  t.TempDir(),
	}
	rt, err := meshruntime.NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	snap := rt.Status()
	if !snap.Running {
		t.Errorf("expected Running=true after Start")
	}
	if len(snap.EnabledRoles) == 0 {
		t.Errorf("expected at least one enabled role, got %+v", snap)
	}
	if err := rt.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
