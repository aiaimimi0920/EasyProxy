package monitor

import (
	"testing"
	"time"
)

func TestRestorePersistedStateHydratesTrafficProvenNode(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	handle := manager.Register(NodeInfo{
		Tag:  "restored-node",
		Name: "Restored Node",
		URI:  "vmess://restored-node",
	})

	now := time.Now()
	ok := manager.RestorePersistedState("vmess://restored-node", "", PersistedState{
		FailureCount:         3,
		SuccessCount:         8,
		TrafficSuccessCount:  2,
		LastError:            "tls handshake: EOF",
		LastFailureAt:        now.Add(-10 * time.Minute),
		LastSuccessAt:        now.Add(-5 * time.Minute),
		LastTrafficSuccessAt: now.Add(-2 * time.Minute),
		LastProbeAt:          now.Add(-15 * time.Minute),
		LastProbeSuccessAt:   now.Add(-20 * time.Minute),
		LastLatencyMs:        245,
		Available:            false,
		InitialCheckDone:     true,
		TotalUpload:          1234,
		TotalDownload:        5678,
	})
	if !ok {
		t.Fatal("expected persisted state to match registered node")
	}

	snap := handle.Snapshot()
	if snap.FailureCount != 3 || snap.SuccessCount != 8 || snap.TrafficSuccessCount != 2 {
		t.Fatalf("unexpected restored counters: %+v", snap)
	}
	if snap.LastLatencyMs != 245 {
		t.Fatalf("expected restored probe latency, got %+v", snap)
	}
	if snap.TotalUpload != 1234 || snap.TotalDownload != 5678 {
		t.Fatalf("unexpected restored traffic totals: %+v", snap)
	}
	if !snap.InitialCheckDone || snap.Available {
		t.Fatalf("expected probe status to remain unavailable after restore, got %+v", snap)
	}
	if !snap.EffectiveAvailable || !snap.TrafficProvenUsable || snap.AvailabilitySource != "recent_traffic" {
		t.Fatalf("expected recent traffic to restore effective availability, got %+v", snap)
	}
}
