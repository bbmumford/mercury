// Copyright (c) 2026 HSTLES / ORBTR Pty Ltd. All Rights Reserved.
// Queries: licensing@hstles.com

package mesh

import (
	"sync"
	"time"
)

// NeutrinoStatus is the observable state of the embedded BTC SPV
// client.
type NeutrinoStatus struct {
	Synced      bool  `json:"synced"`
	BlockHeight int64 `json:"blockHeight"`
	FilterCount int64 `json:"filterCount"`
}

// AttestationCounts reports witness attestation volume over sliding
// windows.
type AttestationCounts struct {
	Last24h int64 `json:"last24h"`
	Last7d  int64 `json:"last7d"`
}

// InactiveRole explains why a configured-but-not-active role is
// disabled.
type InactiveRole struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Status is the serializable observability snapshot callers' reporting
// pipelines consume. Safe to JSON-encode directly.
type Status struct {
	Running             bool              `json:"running"`
	StartedAt           time.Time         `json:"startedAt,omitempty"`
	UptimeSeconds       int64             `json:"uptimeSeconds"`
	EnabledRoles        []string          `json:"enabledRoles"`
	InactiveRoles       []InactiveRole    `json:"inactiveRoles"`
	Neutrino            NeutrinoStatus    `json:"neutrino"`
	UntrustworthyChains map[string]string `json:"untrustworthyChains,omitempty"`
	StorageUsedBytes    int64             `json:"storageUsedBytes"`
	StorageQuotaBytes   int64             `json:"storageQuotaBytes"`
	StoragePressurePct  int               `json:"storagePressurePct"`
	NetworkBudgetMbps   int               `json:"networkBudgetMbps"`
	Attestations        AttestationCounts `json:"attestations"`
	RelayBufferSize     int               `json:"relayBufferSize"`
	LastError           string            `json:"lastError,omitempty"`
	LastErrorAt         time.Time         `json:"lastErrorAt,omitempty"`
}

// statusTracker is the mutable counterpart of Status, held on the
// Runtime. All fields protected by mu.
type statusTracker struct {
	mu sync.RWMutex

	enabledRoles       []string
	inactiveRoles      map[string]string
	running            bool
	startedAt          time.Time
	neutrino           NeutrinoStatus
	untrustworthy      map[string]string
	attestCapabilities map[string]bool
	attestations24h    int64
	attestations7d     int64
	storageUsedBytes   int64
	storageQuotaBytes  int64
	storagePressurePct int
	networkBudgetMbps  int
	relayBufferSize    int
	lastError          string
	lastErrorAt        time.Time
}

func newStatusTracker() *statusTracker {
	return &statusTracker{
		inactiveRoles:      map[string]string{},
		untrustworthy:      map[string]string{},
		attestCapabilities: map[string]bool{},
	}
}

func (t *statusTracker) setEnabledRoles(roles []string) {
	t.mu.Lock()
	t.enabledRoles = append([]string(nil), roles...)
	t.mu.Unlock()
}

func (t *statusTracker) markStarted() {
	t.mu.Lock()
	t.running = true
	t.startedAt = time.Now()
	t.mu.Unlock()
}

func (t *statusTracker) markStopped() {
	t.mu.Lock()
	t.running = false
	t.mu.Unlock()
}

func (t *statusTracker) recordError(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	t.lastError = err.Error()
	t.lastErrorAt = time.Now()
	t.mu.Unlock()
}

func (t *statusTracker) markRoleInactive(role, reason string) {
	t.mu.Lock()
	t.inactiveRoles[role] = reason
	t.mu.Unlock()
}

func (t *statusTracker) recordAttestationCapability(role string) {
	t.mu.Lock()
	t.attestCapabilities[role] = true
	t.mu.Unlock()
}

func (t *statusTracker) markChainUntrustworthy(chainID, reason string) {
	t.mu.Lock()
	t.untrustworthy[chainID] = reason
	t.mu.Unlock()
}

func (t *statusTracker) clearChainUntrustworthy(chainID string) {
	t.mu.Lock()
	delete(t.untrustworthy, chainID)
	t.mu.Unlock()
}

func (t *statusTracker) chainUntrustworthyReason(chainID string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.untrustworthy[chainID]
}

func (t *statusTracker) setRelayBufferSize(n int) {
	t.mu.Lock()
	t.relayBufferSize = n
	t.mu.Unlock()
}

func (t *statusTracker) setStorageBudget(bytes int64) {
	t.mu.Lock()
	t.storageQuotaBytes = bytes
	t.mu.Unlock()
}

func (t *statusTracker) setStoragePressure(pct int) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	t.mu.Lock()
	t.storagePressurePct = pct
	t.mu.Unlock()
}

func (t *statusTracker) snapshot() Status {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var uptime int64
	if t.running && !t.startedAt.IsZero() {
		uptime = int64(time.Since(t.startedAt).Seconds())
	}
	inactive := make([]InactiveRole, 0, len(t.inactiveRoles))
	for name, reason := range t.inactiveRoles {
		inactive = append(inactive, InactiveRole{Name: name, Reason: reason})
	}
	var untrust map[string]string
	if len(t.untrustworthy) > 0 {
		untrust = make(map[string]string, len(t.untrustworthy))
		for k, v := range t.untrustworthy {
			untrust[k] = v
		}
	}
	return Status{
		Running:             t.running,
		StartedAt:           t.startedAt,
		UptimeSeconds:       uptime,
		EnabledRoles:        append([]string(nil), t.enabledRoles...),
		InactiveRoles:       inactive,
		Neutrino:            t.neutrino,
		UntrustworthyChains: untrust,
		StorageUsedBytes:    t.storageUsedBytes,
		StorageQuotaBytes:   t.storageQuotaBytes,
		StoragePressurePct:  t.storagePressurePct,
		NetworkBudgetMbps:   t.networkBudgetMbps,
		Attestations: AttestationCounts{
			Last24h: t.attestations24h,
			Last7d:  t.attestations7d,
		},
		RelayBufferSize: t.relayBufferSize,
		LastError:       t.lastError,
		LastErrorAt:     t.lastErrorAt,
	}
}
