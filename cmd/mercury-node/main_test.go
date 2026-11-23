/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package main

import (
	"runtime"
	"testing"
)

func TestLoadMaintainerWallets_PosixPath(t *testing.T) {
	got, err := loadMaintainerWallets([]string{
		"bitcoin|bc1qabc|/var/mercury/btc.key|BTC_PASS",
	})
	if err != nil {
		t.Fatalf("loadMaintainerWallets: %v", err)
	}
	w, ok := got["bitcoin"]
	if !ok {
		t.Fatalf("missing bitcoin wallet; got %+v", got)
	}
	if w.Address != "bc1qabc" {
		t.Errorf("Address=%q, want bc1qabc", w.Address)
	}
	if w.KeyPath != "/var/mercury/btc.key" {
		t.Errorf("KeyPath=%q, want /var/mercury/btc.key", w.KeyPath)
	}
	if w.PassphraseEnv != "BTC_PASS" {
		t.Errorf("PassphraseEnv=%q, want BTC_PASS", w.PassphraseEnv)
	}
}

func TestLoadMaintainerWallets_WindowsPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		// The regression this guards exists everywhere, but we exercise the
		// real Windows drive-letter shape only on Windows so the path
		// separators on POSIX builders don't mislead future operators.
		t.Skip("windows-only drive-letter regression")
	}
	got, err := loadMaintainerWallets([]string{
		`ethereum|0xabc|C:\Users\op\eth.key|ETH_PASS`,
	})
	if err != nil {
		t.Fatalf("loadMaintainerWallets: %v", err)
	}
	w, ok := got["ethereum"]
	if !ok {
		t.Fatalf("missing ethereum wallet; got %+v", got)
	}
	want := `C:\Users\op\eth.key`
	if w.KeyPath != want {
		t.Errorf("KeyPath=%q, want %q — drive-letter ':' must survive the '|'-delimited parse", w.KeyPath, want)
	}
	if w.PassphraseEnv != "ETH_PASS" {
		t.Errorf("PassphraseEnv=%q, want ETH_PASS", w.PassphraseEnv)
	}
}

// Also run the Windows-shape string parse on every platform so we never
// regress the delimiter choice even on POSIX CI. It's a pure string
// operation — the path need not exist.
func TestLoadMaintainerWallets_WindowsStylePathOnAnyOS(t *testing.T) {
	got, err := loadMaintainerWallets([]string{
		`ethereum|0xabc|C:\Users\op\eth.key|ETH_PASS`,
	})
	if err != nil {
		t.Fatalf("loadMaintainerWallets: %v", err)
	}
	w, ok := got["ethereum"]
	if !ok {
		t.Fatalf("missing ethereum wallet; got %+v", got)
	}
	want := `C:\Users\op\eth.key`
	if w.KeyPath != want {
		t.Errorf("KeyPath=%q, want %q — drive-letter ':' must survive the '|'-delimited parse", w.KeyPath, want)
	}
}

func TestLoadMaintainerWallets_OmitPassphraseEnv(t *testing.T) {
	got, err := loadMaintainerWallets([]string{
		"bitcoin|bc1qabc|/var/mercury/btc.key",
	})
	if err != nil {
		t.Fatalf("loadMaintainerWallets: %v", err)
	}
	w := got["bitcoin"]
	if w.PassphraseEnv != "" {
		t.Errorf("PassphraseEnv=%q, want empty", w.PassphraseEnv)
	}
}

func TestLoadMaintainerWallets_RejectsTooFewFields(t *testing.T) {
	if _, err := loadMaintainerWallets([]string{"bitcoin|bc1qabc"}); err == nil {
		t.Fatalf("expected error for spec with missing keypath")
	}
}

func TestLoadMaintainerWallets_RejectsEmptyChainID(t *testing.T) {
	if _, err := loadMaintainerWallets([]string{"|bc1qabc|/var/mercury/btc.key"}); err == nil {
		t.Fatalf("expected error for spec with empty chain ID")
	}
}
