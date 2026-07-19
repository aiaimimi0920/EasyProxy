package monitor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

func TestSourceHealthStatesExposeAvailabilityBreakdown(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	healthy := manager.Register(NodeInfo{
		Tag:        "zen-good",
		Name:       "Zen Good",
		SourceRef:  "manifest:conn_zenproxy_primary",
		SourceName: "ZenProxy Primary",
		SourceKind: "connector",
	})
	healthy.MarkInitialCheckDone(true)

	trafficProven := manager.Register(NodeInfo{
		Tag:        "zen-traffic",
		Name:       "Zen Traffic",
		SourceRef:  "manifest:conn_zenproxy_primary",
		SourceName: "ZenProxy Primary",
		SourceKind: "connector",
	})
	trafficProven.MarkInitialCheckDone(false)
	trafficProven.RecordFailure(errors.New("tls handshake: EOF"), "www.google.com:443")
	trafficProven.RecordSuccess("api.openai.com:443")

	blacklisted := manager.Register(NodeInfo{
		Tag:        "zen-blacklisted",
		Name:       "Zen Blacklisted",
		SourceRef:  "manifest:conn_zenproxy_primary",
		SourceName: "ZenProxy Primary",
		SourceKind: "connector",
	})
	blacklisted.MarkInitialCheckDone(true)
	blacklisted.Blacklist(time.Now().Add(time.Minute))

	manager.Register(NodeInfo{
		Tag:        "zen-pending",
		Name:       "Zen Pending",
		SourceRef:  "manifest:conn_zenproxy_primary",
		SourceName: "ZenProxy Primary",
		SourceKind: "connector",
	})

	unhealthy := manager.Register(NodeInfo{
		Tag:        "zen-bad",
		Name:       "Zen Bad",
		SourceRef:  "manifest:conn_zenproxy_primary",
		SourceName: "ZenProxy Primary",
		SourceKind: "connector",
	})
	unhealthy.MarkInitialCheckDone(false)
	unhealthy.RecordFailure(errors.New("tls handshake: EOF"), "www.google.com:443")

	states := manager.SourceHealthStates()
	state, ok := states["manifest:conn_zenproxy_primary"]
	if !ok {
		t.Fatal("expected source health state for zenproxy connector")
	}
	if state.Name != "ZenProxy Primary" || state.Kind != "connector" {
		t.Fatalf("unexpected source identity: %+v", state)
	}
	if state.TotalNodes != 5 {
		t.Fatalf("expected 5 total nodes, got %+v", state)
	}
	if state.EffectiveAvailableNodes != 2 {
		t.Fatalf("expected 2 effective nodes, got %+v", state)
	}
	if state.ProbeAvailableNodes != 1 {
		t.Fatalf("expected 1 probe-available node, got %+v", state)
	}
	if state.TrafficProvenNodes != 1 {
		t.Fatalf("expected 1 traffic-proven node, got %+v", state)
	}
	if state.BlacklistedNodes != 1 {
		t.Fatalf("expected 1 blacklisted node, got %+v", state)
	}
	if state.PendingNodes != 1 {
		t.Fatalf("expected 1 pending node, got %+v", state)
	}
	if state.UnavailableNodes != 1 {
		t.Fatalf("expected 1 unavailable node, got %+v", state)
	}
	if state.StructuralFailures != 1 {
		t.Fatalf("expected 1 structural failure, got %+v", state)
	}
	if state.SelectionExcluded {
		t.Fatalf("expected source to stay eligible with healthy peers, got %+v", state)
	}
	if state.SelectionPenalty != 20 {
		t.Fatalf("expected soft selection penalty, got %+v", state)
	}
	if state.SelectionReason != "tls_handshake_eof" {
		t.Fatalf("unexpected selection reason: %+v", state)
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

func TestProbeAllNodesCompletesWhenProbeIgnoresContext(t *testing.T) {
	manager, err := NewManager(Config{
		ProbeTargets: []string{"https://platform.openai.com/login"},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	handle := manager.Register(NodeInfo{
		Tag:           "ignores-context",
		Name:          "Ignores Context",
		ListenAddress: "127.0.0.1",
		Port:          32003,
	})
	handle.SetProbe(func(ctx context.Context) (time.Duration, error) {
		close(started)
		<-release
		return 0, errors.New("released after timeout")
	})

	done := make(chan struct{})
	go func() {
		manager.probeAllNodes(25 * time.Millisecond)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("expected probe to start")
	}

	timedOut := false
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		timedOut = true
	}
	close(release)
	if timedOut {
		t.Fatal("expected full probe round to complete after timeout even when a probe ignores context")
	}

	snap := handle.Snapshot()
	if !snap.InitialCheckDone || snap.Available {
		t.Fatalf("expected timed-out probe to mark node checked but unavailable, got %+v", snap)
	}
}

func TestProbeAllNodesAvoidsBurstConcurrencyThatOverwhelmsResolvers(t *testing.T) {
	manager, err := NewManager(Config{
		ProbeTargets: []string{"https://platform.openai.com/login"},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	const (
		totalNodes         = 40
		maxSafeConcurrency = 16
	)

	var inFlight atomic.Int32
	var maxSeen atomic.Int32

	for idx := 0; idx < totalNodes; idx++ {
		handle := manager.Register(NodeInfo{
			Tag:           fmt.Sprintf("burst-%02d", idx),
			Name:          fmt.Sprintf("Burst %02d", idx),
			ListenAddress: "127.0.0.1",
			Port:          uint16(32100 + idx),
		})
		handle.SetProbe(func(ctx context.Context) (time.Duration, error) {
			current := inFlight.Add(1)
			for {
				prev := maxSeen.Load()
				if current <= prev || maxSeen.CompareAndSwap(prev, current) {
					break
				}
			}
			defer inFlight.Add(-1)

			select {
			case <-time.After(30 * time.Millisecond):
			case <-ctx.Done():
				return 0, ctx.Err()
			}

			if current > maxSafeConcurrency {
				return 0, fmt.Errorf("simulated resolver overload at concurrency=%d", current)
			}
			return 10 * time.Millisecond, nil
		})
	}

	manager.probeAllNodes(500 * time.Millisecond)

	snapshots := manager.Snapshot()
	available := 0
	for _, snap := range snapshots {
		if snap.Available {
			available++
		}
	}

	if available != totalNodes {
		t.Fatalf(
			"expected all probes to stay below resolver burst limit and succeed, got available=%d/%d max_concurrency=%d",
			available,
			totalNodes,
			maxSeen.Load(),
		)
	}
	if maxSeen.Load() > maxSafeConcurrency {
		t.Fatalf("expected probe concurrency <= %d, got %d", maxSafeConcurrency, maxSeen.Load())
	}
}

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

func TestLateProbeCompletionCannotMutateNewGeneration(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	mgr.probeReady = true
	started := make(chan struct{})
	release := make(chan struct{})
	generation := mgr.BeginReload()
	handle := mgr.Register(NodeInfo{Tag: "late-probe"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		close(started)
		<-release
		return time.Millisecond, nil
	})
	probeDone := make(chan struct{})
	go func() {
		_, _ = mgr.ProbeGeneration(context.Background(), generation, time.Second)
		close(probeDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	mgr.BeginReload()
	mgr.Register(NodeInfo{Tag: "late-probe"})
	close(release)
	select {
	case <-probeDone:
	case <-time.After(time.Second):
		t.Fatal("probe did not finish")
	}
	snap := handle.Snapshot()
	if snap.Available || snap.InitialCheckDone || snap.LastProbeSuccessAt.IsZero() == false {
		t.Fatalf("stale probe mutated the new generation: %+v", snap)
	}
}

func TestBeginReloadInvalidatesPeriodicRoundAndAllowsNewGenerationRequest(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	oldStarted := make(chan struct{})
	oldRelease := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(oldRelease) }) })
	oldHandle := mgr.Register(NodeInfo{Tag: "periodic-generation"})
	oldHandle.SetProbe(func(context.Context) (time.Duration, error) {
		close(oldStarted)
		<-oldRelease // Deliberately ignore cancellation like a stuck network call.
		return time.Millisecond, nil
	})

	mgr.RequestProbeAllOnce(5 * time.Second)
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old periodic probe did not start")
	}

	mgr.BeginReload()
	candidateCalled := make(chan struct{})
	candidateHandle := mgr.Register(NodeInfo{Tag: "periodic-generation"})
	candidateHandle.SetProbe(func(context.Context) (time.Duration, error) {
		close(candidateCalled)
		return 2 * time.Millisecond, nil
	})
	mgr.RequestProbeAllOnce(time.Second)

	select {
	case <-candidateCalled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("new-generation periodic request was swallowed by the old in-flight round")
	}
	releaseOnce.Do(func() { close(oldRelease) })
}

func TestRegisterNewGenerationDoesNotReusePreviousProbeBeforeReplacement(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	var oldProbeCalls atomic.Int32
	oldHandle := mgr.Register(NodeInfo{Tag: "probe-replacement-window"})
	oldHandle.SetProbe(func(context.Context) (time.Duration, error) {
		oldProbeCalls.Add(1)
		return time.Millisecond, nil
	})

	generation := mgr.BeginReload()
	candidateHandle := mgr.Register(NodeInfo{Tag: "probe-replacement-window"})
	summary, err := mgr.ProbeGeneration(context.Background(), generation, time.Second)
	if err != nil {
		t.Fatalf("ProbeGeneration() error = %v", err)
	}
	if got := oldProbeCalls.Load(); got != 0 {
		t.Fatalf("new generation reused the previous probe closure %d time(s)", got)
	}
	if summary.Total != 1 || summary.Completed != 1 || summary.Available != 0 || summary.Failed != 1 {
		t.Fatalf("candidate summary before probe replacement = %+v, want one unavailable candidate", summary)
	}
	if snap := candidateHandle.Snapshot(); snap.Available || !snap.InitialCheckDone {
		t.Fatalf("candidate health before probe replacement = %+v", snap)
	}
}

func TestReplacingProbeInvalidatesInFlightResultWithinGeneration(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	generation := mgr.BeginReload()
	handle := mgr.Register(NodeInfo{Tag: "same-generation-probe-replacement"})
	oldStarted := make(chan struct{})
	oldRelease := make(chan struct{})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		close(oldStarted)
		<-oldRelease
		return time.Millisecond, nil
	})

	type probeResult struct {
		summary ProbeSummary
		err     error
	}
	resultCh := make(chan probeResult, 1)
	go func() {
		summary, probeErr := mgr.ProbeGeneration(context.Background(), generation, time.Second)
		resultCh <- probeResult{summary: summary, err: probeErr}
	}()
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old probe did not start")
	}

	var replacementCalls atomic.Int32
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		replacementCalls.Add(1)
		return 2 * time.Millisecond, nil
	})
	close(oldRelease)
	result := <-resultCh
	if result.err != nil {
		t.Fatalf("ProbeGeneration(old probe) error = %v", result.err)
	}
	if result.summary.Available != 0 || result.summary.Completed != 0 {
		t.Fatalf("replaced probe result was committed: %+v", result.summary)
	}
	if snap := handle.Snapshot(); snap.Available || snap.InitialCheckDone {
		t.Fatalf("replaced probe mutated current health: %+v", snap)
	}

	replacementSummary, err := mgr.ProbeGeneration(context.Background(), generation, time.Second)
	if err != nil {
		t.Fatalf("ProbeGeneration(replacement) error = %v", err)
	}
	if replacementCalls.Load() != 1 || replacementSummary.Available != 1 {
		t.Fatalf("replacement probe did not become authoritative: calls=%d summary=%+v", replacementCalls.Load(), replacementSummary)
	}
}

func TestProbeGenerationSupersedesInFlightPeriodicRoundWithinSameGeneration(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	generation := mgr.BeginReload()
	handle := mgr.Register(NodeInfo{Tag: "exclusive-generation-probe"})
	periodicStarted := make(chan struct{})
	periodicRelease := make(chan struct{})
	periodicReturned := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(periodicRelease) }) })
	var calls atomic.Int32
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		if calls.Add(1) == 1 {
			close(periodicStarted)
			<-periodicRelease // Ignore cancellation to model a late network return.
			close(periodicReturned)
			return 0, errors.New("late periodic failure")
		}
		return 2 * time.Millisecond, nil
	})

	mgr.RequestProbeAllOnce(5 * time.Second)
	select {
	case <-periodicStarted:
	case <-time.After(time.Second):
		t.Fatal("periodic probe did not start")
	}

	summary, err := mgr.ProbeGeneration(context.Background(), generation, time.Second)
	if err != nil {
		t.Fatalf("ProbeGeneration() error = %v", err)
	}
	if summary.Available != 1 || summary.Completed != 1 {
		t.Fatalf("candidate generation summary = %+v", summary)
	}
	if snap := handle.Snapshot(); !snap.Available {
		t.Fatalf("candidate probe did not publish availability: %+v", snap)
	}

	releaseOnce.Do(func() { close(periodicRelease) })
	select {
	case <-periodicReturned:
	case <-time.After(time.Second):
		t.Fatal("late periodic probe did not return")
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mgr.probeRoundMu.Lock()
		activeRound := mgr.probeRound
		mgr.probeRoundMu.Unlock()
		if activeRound == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if snap := handle.Snapshot(); !snap.Available || snap.LastError != "" || snap.FailureCount != 0 || len(snap.Timeline) != 1 {
		t.Fatalf("late periodic result overwrote the exclusive candidate probe: %+v", snap)
	}
}

func TestUpdateProbeTargetsSupersedesInFlightPeriodicRound(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://old.example/"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	handle := mgr.Register(NodeInfo{Tag: "probe-target-reload"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		close(started)
		<-release // model a network call that ignores cancellation
		return time.Millisecond, nil
	})

	mgr.RequestProbeAllOnce(5 * time.Second)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("periodic probe did not start")
	}

	updateDone := make(chan error, 1)
	go func() {
		updateDone <- mgr.UpdateProbeTarget("http://new.example/")
	}()
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("UpdateProbeTarget() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("UpdateProbeTarget did not return while the old probe ignored cancellation")
	}
	if snap := handle.Snapshot(); snap.Available || snap.InitialCheckDone {
		t.Fatalf("old probe remained authoritative after target update: %+v", snap)
	}

	releaseOnce.Do(func() { close(release) })

	if snap := handle.Snapshot(); snap.Available || snap.InitialCheckDone {
		t.Fatalf("stale probe result mutated health after target update: %+v", snap)
	}
	targets, ok := mgr.ProbeTargets()
	if !ok || len(targets) != 1 || targets[0].Original != "http://new.example/" {
		t.Fatalf("probe targets after update = %+v (ok=%v)", targets, ok)
	}
}

func TestManualProbeCompletionCannotMutateAfterProbeTargetUpdate(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://old.example/"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	started := make(chan struct{})
	release := make(chan struct{})
	handle := mgr.Register(NodeInfo{Tag: "manual-probe-target-reload"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		close(started)
		<-release
		return time.Millisecond, nil
	})

	probeDone := make(chan error, 1)
	go func() {
		_, probeErr := mgr.Probe(context.Background(), "manual-probe-target-reload")
		probeDone <- probeErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("manual probe did not start")
	}
	if err := mgr.UpdateProbeTarget("http://new.example/"); err != nil {
		t.Fatalf("UpdateProbeTarget() error = %v", err)
	}
	close(release)
	select {
	case err := <-probeDone:
		if !errors.Is(err, ErrStaleGeneration) {
			t.Fatalf("manual probe error = %v, want stale-generation rejection", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual probe did not finish after release")
	}
	if snap := handle.Snapshot(); snap.Available || snap.InitialCheckDone {
		t.Fatalf("stale manual probe mutated health: %+v", snap)
	}
}

func TestLateManualProbeCannotOverwriteNewerManualProbe(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(firstRelease) }) })
	var calls atomic.Int32
	handle := mgr.Register(NodeInfo{Tag: "manual-order"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-firstRelease
			return 0, errors.New("late manual failure")
		}
		return 2 * time.Millisecond, nil
	})

	firstDone := make(chan error, 1)
	go func() {
		_, probeErr := mgr.Probe(context.Background(), "manual-order")
		firstDone <- probeErr
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first manual probe did not start")
	}

	if _, err := mgr.Probe(context.Background(), "manual-order"); err != nil {
		t.Fatalf("newer manual probe error = %v", err)
	}
	if snap := handle.Snapshot(); !snap.Available || snap.FailureCount != 0 {
		t.Fatalf("newer manual probe did not establish health: %+v", snap)
	}

	releaseOnce.Do(func() { close(firstRelease) })
	select {
	case err := <-firstDone:
		if !errors.Is(err, ErrStaleGeneration) {
			t.Fatalf("late manual probe error = %v, want stale-result rejection", err)
		}
	case <-time.After(time.Second):
		t.Fatal("late manual probe did not finish")
	}
	if snap := handle.Snapshot(); !snap.Available || snap.FailureCount != 0 || len(snap.Timeline) != 1 {
		t.Fatalf("late manual probe overwrote newer health: %+v", snap)
	}
}

func TestLatePeriodicProbeCannotOverwriteNewerManualProbe(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	periodicStarted := make(chan struct{})
	periodicRelease := make(chan struct{})
	periodicReturned := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(periodicRelease) }) })
	var calls atomic.Int32
	handle := mgr.Register(NodeInfo{Tag: "periodic-manual-order"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		if calls.Add(1) == 1 {
			close(periodicStarted)
			<-periodicRelease
			close(periodicReturned)
			return 0, errors.New("late periodic failure")
		}
		return 2 * time.Millisecond, nil
	})

	mgr.RequestProbeAllOnce(5 * time.Second)
	select {
	case <-periodicStarted:
	case <-time.After(time.Second):
		t.Fatal("periodic probe did not start")
	}
	if _, err := mgr.Probe(context.Background(), "periodic-manual-order"); err != nil {
		t.Fatalf("newer manual probe error = %v", err)
	}
	if snap := handle.Snapshot(); !snap.Available || snap.FailureCount != 0 {
		t.Fatalf("newer manual probe did not establish health: %+v", snap)
	}

	releaseOnce.Do(func() { close(periodicRelease) })
	select {
	case <-periodicReturned:
	case <-time.After(time.Second):
		t.Fatal("periodic probe did not return")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mgr.probeRoundMu.Lock()
		active := mgr.probeRound
		mgr.probeRoundMu.Unlock()
		if active == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if snap := handle.Snapshot(); !snap.Available || snap.FailureCount != 0 || len(snap.Timeline) != 1 {
		t.Fatalf("late periodic probe overwrote newer manual health: %+v", snap)
	}
}

func TestBeginReloadResetsInitialProbeGateAndRejectsOldRoundCompletion(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	oldStarted := make(chan struct{})
	oldRelease := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(oldRelease) }) })
	handle := mgr.Register(NodeInfo{Tag: "reload-gate"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		close(oldStarted)
		<-oldRelease
		return time.Millisecond, nil
	})

	mgr.RequestProbeAllOnce(5 * time.Second)
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old periodic probe did not start")
	}

	generation := mgr.BeginReload()
	if err := mgr.WaitForInitialProbe(75 * time.Millisecond); err == nil {
		t.Fatal("reload should reset the initial probe gate until the new round completes")
	}

	candidate := mgr.Register(NodeInfo{Tag: "reload-gate"})
	candidate.SetProbe(func(context.Context) (time.Duration, error) {
		return 2 * time.Millisecond, nil
	})
	summary, err := mgr.ProbeGeneration(context.Background(), generation, time.Second)
	if err != nil {
		t.Fatalf("new generation probe error = %v", err)
	}
	if summary.Available != 1 || summary.Completed != 1 {
		t.Fatalf("new generation probe summary = %+v", summary)
	}
	if err := mgr.WaitForInitialProbe(200 * time.Millisecond); err != nil {
		t.Fatalf("matching generation probe should complete the gate: %v", err)
	}

	releaseOnce.Do(func() { close(oldRelease) })
}

func TestUpdateProbeTargetResetsInitialProbeGateUntilDirectGenerationProbe(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://old.example/"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	handle := mgr.Register(NodeInfo{Tag: "target-gate"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		return 2 * time.Millisecond, nil
	})
	mgr.probeAllNodes(time.Second)
	if err := mgr.WaitForInitialProbe(100 * time.Millisecond); err != nil {
		t.Fatalf("initial probe should complete the initial gate: %v", err)
	}

	if err := mgr.UpdateProbeTarget("http://new.example/"); err != nil {
		t.Fatalf("UpdateProbeTarget() error = %v", err)
	}
	if err := mgr.WaitForInitialProbe(75 * time.Millisecond); err == nil {
		t.Fatal("probe target update should reset the initial probe gate")
	}

	mgr.mu.RLock()
	generation := mgr.reloadGen
	mgr.mu.RUnlock()
	if _, err := mgr.ProbeGeneration(context.Background(), generation, time.Second); err != nil {
		t.Fatalf("direct generation probe error = %v", err)
	}
	if err := mgr.WaitForInitialProbe(200 * time.Millisecond); err != nil {
		t.Fatalf("direct matching generation probe should complete the gate: %v", err)
	}
}

func TestPeriodicHealthCheckStartsAfterLateProbeTarget(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	probed := make(chan struct{}, 1)
	handle := mgr.Register(NodeInfo{Tag: "late-periodic-target"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		select {
		case probed <- struct{}{}:
		default:
		}
		return time.Millisecond, nil
	})

	mgr.StartPeriodicHealthCheck(20*time.Millisecond, 100*time.Millisecond)
	select {
	case <-probed:
		t.Fatal("periodic probe ran before a target was configured")
	case <-time.After(40 * time.Millisecond):
	}

	if err := mgr.UpdateProbeTarget("http://late.example/"); err != nil {
		t.Fatalf("UpdateProbeTarget() error = %v", err)
	}
	select {
	case <-probed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("periodic health check did not resume after adding a probe target")
	}
	if err := mgr.WaitForInitialProbe(200 * time.Millisecond); err != nil {
		t.Fatalf("late periodic probe did not complete the initial gate: %v", err)
	}
}

func TestInitialProbeWaiterFollowsGateResetToNewGeneration(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://old.example/"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	waitDone := make(chan error, 1)
	mgr.initialProbeMu.Lock()
	go func() {
		waitDone <- mgr.WaitForInitialProbe(250 * time.Millisecond)
	}()
	mgr.initialProbeMu.Unlock()
	// Give the waiter time to capture the old gate before the reload replaces it.
	time.Sleep(25 * time.Millisecond)

	generation := mgr.BeginReload()
	select {
	case err := <-waitDone:
		t.Fatalf("waiter returned before the replacement probe: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	handle := mgr.Register(NodeInfo{Tag: "waiter-gate"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		return time.Millisecond, nil
	})
	if _, err := mgr.ProbeGeneration(context.Background(), generation, time.Second); err != nil {
		t.Fatalf("replacement generation probe error = %v", err)
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("waiter did not follow the replacement gate: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not finish after replacement probe")
	}
}

func TestUpdateProbeTargetsCanBeCleared(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if err := manager.UpdateProbeTargets([]string{"https://platform.openai.com/login"}, ""); err != nil {
		t.Fatalf("UpdateProbeTargets() error = %v", err)
	}
	if _, ok := manager.ProbeTargets(); !ok {
		t.Fatal("expected probe targets to be ready before clear")
	}

	if err := manager.UpdateProbeTargets(nil, ""); err != nil {
		t.Fatalf("UpdateProbeTargets(clear) error = %v", err)
	}
	if _, ok := manager.ProbeTargets(); ok {
		t.Fatal("expected probe targets to be cleared")
	}
	if destination, ok := manager.DestinationForProbe(); ok || destination.Fqdn != "" {
		t.Fatalf("expected probe destination to be cleared, got %+v", destination)
	}
}
