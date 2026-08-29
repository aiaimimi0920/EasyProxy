package monitor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestSetLongLivedThresholdsUpdatesExistingEntries(t *testing.T) {
	manager, err := NewManager(Config{
		LongLivedMinUptime:      4 * time.Hour,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	handle := manager.Register(NodeInfo{Tag: "threshold-node", Name: "Threshold Node"})
	handle.MarkInitialCheckDone(true)
	handle.ApplyUsageReportSuccess()
	handle.ApplyUsageReportFailure(0, true)

	manager.mu.RLock()
	entry := manager.nodes["threshold-node"]
	manager.mu.RUnlock()
	if entry == nil {
		t.Fatal("expected registered entry")
	}
	entry.mu.Lock()
	entry.firstSeenAt = time.Now().Add(-2 * time.Hour)
	entry.mu.Unlock()

	if snap := handle.Snapshot(); snap.LongLived {
		t.Fatalf("entry should not meet the original uptime/rate thresholds: %+v", snap)
	}

	manager.SetLongLivedThresholds(time.Hour, 0.4)

	snap := handle.Snapshot()
	if !snap.LongLived {
		t.Fatalf("updated thresholds should make the existing entry long-lived: %+v", snap)
	}
	if manager.cfg.LongLivedMinUptime != time.Hour || manager.cfg.LongLivedMinSuccessRate != 0.4 {
		t.Fatalf("manager thresholds not updated: %+v", manager.cfg)
	}
}

func TestSetLongLivedThresholdsNormalizesInvalidValues(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.SetLongLivedThresholds(0, 1.5)

	if manager.cfg.LongLivedMinUptime != defaultLongLivedMinUptime {
		t.Fatalf("zero uptime should use default %s, got %s", defaultLongLivedMinUptime, manager.cfg.LongLivedMinUptime)
	}
	if manager.cfg.LongLivedMinSuccessRate != defaultLongLivedMinSuccessRate {
		t.Fatalf("invalid rate should use default %.2f, got %.2f", defaultLongLivedMinSuccessRate, manager.cfg.LongLivedMinSuccessRate)
	}
}

func TestLongLivedPolicyUsesRawSnapshot(t *testing.T) {
	snapshot := Snapshot{
		EffectiveAvailable:   true,
		Uptime:               1500 * time.Millisecond,
		UptimeSeconds:        1,
		ReportedSuccessCount: 9,
		ReportedFailureCount: 1,
	}
	if !MeetsLongLivedPolicy(snapshot, 1200*time.Millisecond, 0.8) {
		t.Fatal("snapshot should meet relaxed policy")
	}
	if MeetsLongLivedPolicy(snapshot, 2*time.Second, 0.8) {
		t.Fatal("snapshot should not meet strict uptime policy")
	}
}

func TestRecordSuccessWithLatencyClearsLastError(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	handle := manager.Register(NodeInfo{
		Tag:           "success-clears-error",
		Name:          "Success Clears Error",
		ListenAddress: "127.0.0.1",
		Port:          32002,
	})
	handle.RecordFailure(errors.New("tls handshake: EOF"), "www.google.com:443")
	handle.RecordSuccessWithLatency(25 * time.Millisecond)

	snap := handle.Snapshot()
	if snap.LastError != "" {
		t.Fatalf("expected last error to be cleared after probe success, got %+v", snap)
	}
	if !snap.Available || !snap.InitialCheckDone {
		t.Fatalf("expected successful probe to mark node available, got %+v", snap)
	}
}

func TestProbeGenerationRejectsStaleGeneration(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	mgr.probeReady = true
	called := atomic.Bool{}
	handle := mgr.Register(NodeInfo{Tag: "stale-generation"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		called.Store(true)
		return time.Millisecond, nil
	})
	oldGeneration := mgr.BeginReload()
	mgr.BeginReload()
	if _, err := mgr.ProbeGeneration(context.Background(), oldGeneration, time.Second); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("ProbeGeneration() error = %v, want ErrStaleGeneration", err)
	}
	if called.Load() {
		t.Fatal("stale generation started a probe")
	}
}

func TestProbeGenerationDoesNotReusePreviousAvailability(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	mgr.probeReady = true
	handle := mgr.Register(NodeInfo{Tag: "candidate-health"})
	handle.MarkInitialCheckDone(true)
	generation := mgr.BeginReload()
	// Re-registering the same tag keeps historical stats but must invalidate the
	// previous generation's availability for the candidate health gate.
	candidate := mgr.Register(NodeInfo{Tag: "candidate-health"})
	candidate.SetProbe(func(context.Context) (time.Duration, error) {
		return 0, errors.New("candidate probe failed")
	})
	summary, err := mgr.ProbeGeneration(context.Background(), generation, time.Second)
	if err != nil {
		t.Fatalf("ProbeGeneration() error = %v", err)
	}
	if summary.Available != 0 || summary.Total != 1 {
		t.Fatalf("candidate summary = %+v, want one failed candidate", summary)
	}
	if snap := candidate.Snapshot(); snap.Available || snap.EffectiveAvailable {
		t.Fatalf("previous availability leaked into candidate generation: %+v", snap)
	}
}
