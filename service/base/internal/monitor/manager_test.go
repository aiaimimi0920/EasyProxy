package monitor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUpdateProbeTargetPreservesHTTPSDefaultPort(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if err := manager.UpdateProbeTarget("https://example.com/generate_204"); err != nil {
		t.Fatalf("UpdateProbeTarget() error = %v", err)
	}

	destination, ok := manager.DestinationForProbe()
	if !ok {
		t.Fatal("expected probe destination to be ready")
	}
	if destination.Fqdn != "example.com" {
		t.Fatalf("unexpected probe host: %s", destination.Fqdn)
	}
	if destination.Port != 443 {
		t.Fatalf("unexpected probe port: %d", destination.Port)
	}
}

func TestUpdateProbeTargetPreservesTCPDefaultPort(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if err := manager.UpdateProbeTarget("tcp://example.com"); err != nil {
		t.Fatalf("UpdateProbeTarget() error = %v", err)
	}

	destination, ok := manager.DestinationForProbe()
	if !ok {
		t.Fatal("expected probe destination to be ready")
	}
	if destination.Fqdn != "example.com" {
		t.Fatalf("unexpected probe host: %s", destination.Fqdn)
	}
	if destination.Port != 443 {
		t.Fatalf("unexpected tcp probe port: %d", destination.Port)
	}
}

func TestUpdateProbeTargetsPreservesMultipleFullURLs(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	targets := []string{
		"https://platform.openai.com/login",
		"https://auth.openai.com/",
	}
	if err := manager.UpdateProbeTargets(targets, ""); err != nil {
		t.Fatalf("UpdateProbeTargets() error = %v", err)
	}

	specs, ok := manager.ProbeTargets()
	if !ok {
		t.Fatal("expected probe targets to be ready")
	}
	if len(specs) != 2 {
		t.Fatalf("unexpected probe target count: %d", len(specs))
	}
	if specs[0].Host != "platform.openai.com" || specs[0].Path != "/login" {
		t.Fatalf("unexpected first probe target: %+v", specs[0])
	}
	if specs[1].Host != "auth.openai.com" || specs[1].Path != "/" {
		t.Fatalf("unexpected second probe target: %+v", specs[1])
	}
}

func TestSourceSelectionStatesExcludeStructurallyBadSource(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	badA := manager.Register(NodeInfo{
		Tag:        "bad-a",
		Name:       "Bad A",
		SourceRef:  "local:subscription-2",
		SourceName: "subscription-2",
	})
	badA.MarkInitialCheckDone(false)
	badA.RecordFailure(errors.New("tls handshake: EOF"), "www.google.com:443")

	badB := manager.Register(NodeInfo{
		Tag:        "bad-b",
		Name:       "Bad B",
		SourceRef:  "local:subscription-2",
		SourceName: "subscription-2",
	})
	badB.MarkInitialCheckDone(false)
	badB.RecordFailure(errors.New("authentication failed, status code: 200"), "www.google.com:443")

	good := manager.Register(NodeInfo{
		Tag:        "good-a",
		Name:       "Good A",
		SourceRef:  "local:subscription-1",
		SourceName: "subscription-1",
	})
	good.MarkInitialCheckDone(true)

	states := manager.SourceSelectionStates()
	badState, ok := states["local:subscription-2"]
	if !ok {
		t.Fatal("expected source state for subscription-2")
	}
	if !badState.Excluded {
		t.Fatalf("expected structurally bad source to be excluded, got %+v", badState)
	}
	if badState.Penalty < 80 {
		t.Fatalf("expected excluded source penalty to be high, got %+v", badState)
	}

	goodState, ok := states["local:subscription-1"]
	if !ok {
		t.Fatal("expected source state for subscription-1")
	}
	if goodState.Excluded || goodState.Penalty != 0 {
		t.Fatalf("expected healthy source to remain unpenalized, got %+v", goodState)
	}
}

func TestSecondarySelectionStatesIsolateBadClustersInsideHealthySource(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	good := manager.Register(NodeInfo{
		Tag:            "good-a",
		Name:           "Good A",
		SourceRef:      "manifest:agg",
		SourceName:     "Aggregator Stable",
		ProtocolFamily: "vless",
		NodeMode:       "tls/ws",
		DomainFamily:   "good.example.com",
	})
	good.MarkInitialCheckDone(true)

	badA := manager.Register(NodeInfo{
		Tag:            "bad-a",
		Name:           "Bad A",
		SourceRef:      "manifest:agg",
		SourceName:     "Aggregator Stable",
		ProtocolFamily: "vless",
		NodeMode:       "reality/tcp",
		DomainFamily:   "badcluster.example.com",
	})
	badA.MarkInitialCheckDone(false)
	badA.RecordFailure(errors.New("reality verification failed"), "www.google.com:443")

	badB := manager.Register(NodeInfo{
		Tag:            "bad-b",
		Name:           "Bad B",
		SourceRef:      "manifest:agg",
		SourceName:     "Aggregator Stable",
		ProtocolFamily: "vless",
		NodeMode:       "reality/tcp",
		DomainFamily:   "badcluster.example.com",
	})
	badB.MarkInitialCheckDone(false)
	badB.RecordFailure(errors.New("reality verification failed"), "www.google.com:443")

	states := manager.SecondarySelectionStates()

	modeState, ok := states[SecondarySelectionStateKey("manifest:agg", SelectionDimensionNodeMode, "reality/tcp")]
	if !ok {
		t.Fatal("expected node_mode secondary state for reality/tcp")
	}
	if !modeState.Excluded {
		t.Fatalf("expected reality/tcp cluster to be excluded, got %+v", modeState)
	}

	domainState, ok := states[SecondarySelectionStateKey("manifest:agg", SelectionDimensionDomainFamily, "badcluster.example.com")]
	if !ok {
		t.Fatal("expected domain_family secondary state for badcluster.example.com")
	}
	if !domainState.Excluded {
		t.Fatalf("expected badcluster.example.com cluster to be excluded, got %+v", domainState)
	}

	protocolState, ok := states[SecondarySelectionStateKey("manifest:agg", SelectionDimensionProtocolFamily, "vless")]
	if !ok {
		t.Fatal("expected protocol_family secondary state for vless")
	}
	if protocolState.Excluded {
		t.Fatalf("expected protocol_family to stay eligible when healthy peers exist, got %+v", protocolState)
	}
	if protocolState.Penalty == 0 {
		t.Fatalf("expected protocol_family to receive a soft penalty, got %+v", protocolState)
	}
}

func TestSourceSelectionStatesKeepTrafficProvenSourceEligible(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	proven := manager.Register(NodeInfo{
		Tag:        "traffic-proven",
		Name:       "Traffic Proven",
		SourceRef:  "local:subscription-3",
		SourceName: "subscription-3",
	})
	proven.MarkInitialCheckDone(false)
	proven.RecordFailure(errors.New("tls handshake: EOF"), "www.google.com:443")
	proven.RecordSuccess("api.openai.com:443")

	bad := manager.Register(NodeInfo{
		Tag:        "still-bad",
		Name:       "Still Bad",
		SourceRef:  "local:subscription-3",
		SourceName: "subscription-3",
	})
	bad.MarkInitialCheckDone(false)
	bad.RecordFailure(errors.New("tls handshake: EOF"), "www.google.com:443")

	states := manager.SourceSelectionStates()
	state, ok := states["local:subscription-3"]
	if !ok {
		t.Fatal("expected source state for subscription-3")
	}
	if state.HealthyNodes != 1 {
		t.Fatalf("expected one healthy traffic-proven node, got %+v", state)
	}
	if state.Excluded {
		t.Fatalf("expected source to stay eligible when real traffic proved a node usable, got %+v", state)
	}
}

func TestSelectProxyCompatCandidateSnapshotsTreatsTrafficProvenNodesAsEffective(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	proven := manager.Register(NodeInfo{
		Tag:           "traffic-proven",
		Name:          "Traffic Proven",
		ListenAddress: "127.0.0.1",
		Port:          31001,
	})
	proven.MarkInitialCheckDone(false)
	proven.RecordFailure(errors.New("tls handshake: EOF"), "www.google.com:443")
	proven.RecordSuccess("api.openai.com:443")

	nodes, tier := selectProxyCompatCandidateSnapshots(manager.Snapshot())
	if tier != "effective" {
		t.Fatalf("expected traffic-proven node to stay in effective tier, got %q", tier)
	}
	if len(nodes) != 1 || nodes[0].Tag != "traffic-proven" {
		t.Fatalf("expected traffic-proven node to be selected, got %+v", nodes)
	}
	if !nodes[0].EffectiveAvailable || !nodes[0].TrafficProvenUsable {
		t.Fatalf("expected effective availability flags on snapshot, got %+v", nodes[0])
	}
}

func TestTrafficProvenStatusExpiresAfterLaterFailure(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	handle := manager.Register(NodeInfo{
		Tag:           "traffic-then-fail",
		Name:          "Traffic Then Fail",
		ListenAddress: "127.0.0.1",
		Port:          31002,
	})
	handle.MarkInitialCheckDone(false)
	handle.RecordSuccess("api.openai.com:443")
	handle.RecordFailure(errors.New("unexpected HTTP response status: 403"), "api.ipify.org:443")

	snap := handle.Snapshot()
	if snap.TrafficProvenUsable {
		t.Fatalf("expected later failure to clear traffic-proven usability, got %+v", snap)
	}
	if snap.EffectiveAvailable {
		t.Fatalf("expected effective availability to be false after later failure, got %+v", snap)
	}
}

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

func TestWaitForInitialProbeBlocksUntilFirstProbeRoundCompletes(t *testing.T) {
	manager, err := NewManager(Config{
		ProbeTargets: []string{"https://platform.openai.com/login"},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	handle := manager.Register(NodeInfo{
		Tag:           "blocking-node",
		Name:          "Blocking Node",
		ListenAddress: "127.0.0.1",
		Port:          32001,
	})
	handle.SetProbe(func(ctx context.Context) (time.Duration, error) {
		close(started)
		select {
		case <-release:
			return 25 * time.Millisecond, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})

	done := make(chan struct{})
	go func() {
		manager.StartPeriodicHealthCheck(time.Hour, time.Second)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("expected initial probe to start")
	}

	if err := manager.WaitForInitialProbe(100 * time.Millisecond); err == nil {
		t.Fatal("expected wait to time out while first probe is still blocked")
	}

	close(release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected StartPeriodicHealthCheck to return after initial probe completed")
	}

	if err := manager.WaitForInitialProbe(200 * time.Millisecond); err != nil {
		t.Fatalf("expected initial probe wait to pass after completion, got %v", err)
	}

	manager.Stop()
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
