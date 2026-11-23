// Copyright (c) 2026 Brandon Miller-Mumford.
// Licensed under the MIT License; see LICENSE.

package mesh

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	plutuswallet "github.com/bbmumford/plutus/wallet"
)

// minimalConfig returns a Config that should pass Validate and Start
// without requiring external infrastructure. Witness roles are disabled
// here so Neutrino is not constructed (which would attempt P2P DNS
// seeding).
func minimalConfig(dir string) Config {
	return Config{
		EnableReplication:        true,
		EnableBlobProvider:       true,
		EnableMessageRelay:       true,
		EnableBalanceWitness:     false,
		EnableTxWitness:          false,
		EnableArbitration:        false,
		EnableMarketplace:        false,
		StorageMaxPct:            60,
		StorageTargetHeadroomPct: 50,
		StorageMinReserveGB:      10,
		StorageHardMinGB:         2,
		NetworkBudgetMbps:        10,
		AppID:                    "mesh-runtime-test",
		DataDir:                  dir,
		NeutrinoBirthHeight:      defaultNeutrinoBirthHeight,
	}
}

func TestNewRuntime_ConstructStartStop(t *testing.T) {
	t.Parallel()
	cfg := minimalConfig(t.TempDir())

	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	startedStop := time.Now()
	if err := rt.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(startedStop); elapsed > shutdownTimeout+time.Second {
		t.Errorf("Stop took %v, want < %v", elapsed, shutdownTimeout)
	}
}

func TestNewRuntime_InvalidStorageBounds_Rejected(t *testing.T) {
	t.Parallel()
	cfg := minimalConfig(t.TempDir())
	cfg.StorageHardMinGB = 50
	cfg.StorageMinReserveGB = 10

	if _, err := NewRuntime(cfg); err == nil {
		t.Fatal("expected Validate to reject StorageHardMinGB > StorageMinReserveGB")
	}
}

func TestNewRuntime_MissingDataDir_Rejected(t *testing.T) {
	t.Parallel()
	cfg := minimalConfig("")
	if _, err := NewRuntime(cfg); err == nil {
		t.Fatal("expected Validate to reject empty DataDir")
	}
}

func TestStatus_ReportsSaneValuesAfterStartup(t *testing.T) {
	t.Parallel()
	cfg := minimalConfig(t.TempDir())

	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	pre := rt.Status()
	if pre.Running {
		t.Errorf("expected Running=false before Start")
	}
	if pre.UptimeSeconds != 0 {
		t.Errorf("expected UptimeSeconds=0 before Start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop() }()

	post := rt.Status()
	if !post.Running {
		t.Errorf("expected Running=true after Start")
	}
	if post.StartedAt.IsZero() {
		t.Errorf("expected StartedAt set after Start")
	}
	hasRole := func(name string) bool {
		for _, r := range post.EnabledRoles {
			if r == name {
				return true
			}
		}
		return false
	}
	if !hasRole(RoleReplicator) {
		t.Errorf("expected %q in EnabledRoles, got %v", RoleReplicator, post.EnabledRoles)
	}
	if !hasRole(RoleMessageRelay) {
		t.Errorf("expected %q in EnabledRoles, got %v", RoleMessageRelay, post.EnabledRoles)
	}
	if !hasRole(RoleBlobProvider) {
		t.Errorf("expected %q in EnabledRoles, got %v", RoleBlobProvider, post.EnabledRoles)
	}
}

func TestStop_WithoutStart_NoOp(t *testing.T) {
	t.Parallel()
	rt, err := NewRuntime(minimalConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if err := rt.Stop(); err != nil {
		t.Errorf("Stop on un-started runtime should be no-op: %v", err)
	}
}

// fakeGovernor satisfies GovernorHook for assertions about the
// throttle-hook wire-up.
type fakeGovernor struct{ t bool }

func (f *fakeGovernor) Throttled() bool { return f.t }

func TestGovernorHook_Threading(t *testing.T) {
	t.Parallel()
	rt, err := NewRuntime(minimalConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if rt.throttled() {
		t.Errorf("no governor configured → throttled() should be false")
	}

	cfg := minimalConfig(t.TempDir())
	g := &fakeGovernor{}
	cfg.Governor = g
	rt2, err := NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime with governor: %v", err)
	}
	if rt2.throttled() {
		t.Errorf("governor off → throttled() should be false")
	}
	g.t = true
	if !rt2.throttled() {
		t.Errorf("governor on → throttled() should be true")
	}
}

// --- Defaults table per DESIGN §19 -------------------------------------------

func TestDefaults_MercuryNode(t *testing.T) {
	t.Parallel()
	d := MercuryNodeDefaults()
	if d.StorageMaxPct != 70 {
		t.Errorf("StorageMaxPct=%d, want 70 per DESIGN §19", d.StorageMaxPct)
	}
	if d.StorageTargetHeadroomPct != 50 {
		t.Errorf("StorageTargetHeadroomPct=%d, want 50", d.StorageTargetHeadroomPct)
	}
	if d.StorageMinReserveGB != 5 {
		t.Errorf("StorageMinReserveGB=%d, want 5", d.StorageMinReserveGB)
	}
	if !d.EnableReplication || !d.EnableBlobProvider || !d.EnableMessageRelay ||
		!d.EnableBalanceWitness || !d.EnableTxWitness {
		t.Errorf("DESIGN §19: mercury-node should enable all four role categories by default, got %+v", d)
	}
}

func TestDefaults_WailsDesktop(t *testing.T) {
	t.Parallel()
	d := WailsDesktopDefaults()
	if d.StorageMaxPct != 50 {
		t.Errorf("StorageMaxPct=%d, want 50 per DESIGN §19", d.StorageMaxPct)
	}
	if d.StorageTargetHeadroomPct != 25 {
		t.Errorf("StorageTargetHeadroomPct=%d, want 25", d.StorageTargetHeadroomPct)
	}
	if d.StorageMinReserveGB != 10 {
		t.Errorf("StorageMinReserveGB=%d, want 10", d.StorageMinReserveGB)
	}
}

func TestDefaults_ORBTRAgent(t *testing.T) {
	t.Parallel()
	d := ORBTRAgentDefaults()
	if d.StorageMaxPct != 60 {
		t.Errorf("StorageMaxPct=%d, want 60 per DESIGN §19", d.StorageMaxPct)
	}
	if d.StorageTargetHeadroomPct != 40 {
		t.Errorf("StorageTargetHeadroomPct=%d, want 40", d.StorageTargetHeadroomPct)
	}
}

// --- Fee distribution loop wiring ----------------------------------------

// writeTestKeyfile seals a 32-byte secp256k1-shaped scalar into an
// encrypted wallet keyfile at path using the real wallet package's
// primitive. Used by the fee-distribution wiring tests.
func writeTestKeyfile(t *testing.T, path string, passphrase []byte) {
	t.Helper()
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatalf("random key: %v", err)
	}
	priv[31] |= 0x01
	if err := plutuswallet.WriteEncryptedKeyfile(path, passphrase, priv); err != nil {
		t.Fatalf("WriteEncryptedKeyfile: %v", err)
	}
}

func TestRunFeeDistributionLoop_EmptyWalletsReturnsImmediately(t *testing.T) {
	t.Parallel()
	cfg := minimalConfig(t.TempDir())
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		rt.runFeeDistributionLoop(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("runFeeDistributionLoop did not return for empty wallets")
	}
}

func TestRunFeeDistributionLoop_BadKeyfilePathLogsAndExits(t *testing.T) {
	t.Parallel()
	cfg := minimalConfig(t.TempDir())
	cfg.MaintainerWallets = map[string]MaintainerWallet{
		"bitcoin": {
			Address: "bc1qtest000000000000000000000000000000000",
			KeyPath: filepath.Join(t.TempDir(), "does-not-exist.key"),
		},
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		rt.runFeeDistributionLoop(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("runFeeDistributionLoop did not exit on bad keyfile path")
	}
}

func TestRunFeeDistributionLoop_SettlementOnlyWallet(t *testing.T) {
	t.Parallel()
	cfg := minimalConfig(t.TempDir())
	cfg.MaintainerWallets = map[string]MaintainerWallet{
		"bitcoin": {
			Address: "bc1qsettlementonly00000000000000000000000",
		},
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		rt.runFeeDistributionLoop(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("runFeeDistributionLoop did not exit when no signing keys were configured")
	}
}

func TestWindowOrDefault(t *testing.T) {
	t.Parallel()
	if got := windowOrDefault(0); got != defaultFeeDistributionWindow {
		t.Errorf("zero should yield default, got %s", got)
	}
	if got := windowOrDefault(-5 * time.Second); got != defaultFeeDistributionWindow {
		t.Errorf("negative should yield default, got %s", got)
	}
	if got := windowOrDefault(2 * time.Hour); got != 2*time.Hour {
		t.Errorf("explicit should pass through, got %s", got)
	}
}

// TestMaintainerWallet_TempKeyfileRoundTrip exercises the keyfile
// write helper so a subsequent construction path would find a real
// file.
func TestMaintainerWallet_TempKeyfileRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wallet.key")
	writeTestKeyfile(t, path, []byte("pass"))
	out, err := plutuswallet.ReadEncryptedKeyfile(path, []byte("pass"))
	if err != nil {
		t.Fatalf("ReadEncryptedKeyfile: %v", err)
	}
	if len(out) != 32 {
		t.Errorf("want 32-byte scalar, got %d", len(out))
	}
}
