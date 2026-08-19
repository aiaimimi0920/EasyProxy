package pool

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

func TestHTTPProbeSupportsPlainHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/generate_204" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial server: %v", err)
	}
	defer conn.Close()

	destination := M.ParseSocksaddrHostPort("example.com", 80)
	if _, err := httpProbe(conn, destination); err != nil {
		t.Fatalf("httpProbe() error = %v", err)
	}
}

func TestHTTPProbeSupportsTLSOn443(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/generate_204" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial tls server: %v", err)
	}
	defer conn.Close()

	destination := M.ParseSocksaddrHostPort("example.com", 443)
	if _, err := httpProbe(conn, destination, true); err != nil {
		t.Fatalf("httpProbe() error = %v", err)
	}
}

func TestHTTPProbeTargetUsesFullPathAndAcceptsRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Location", "/next")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial tls server: %v", err)
	}
	defer conn.Close()

	target := monitor.ProbeTargetSpec{
		Original: "https://platform.openai.com/login",
		Scheme:   "https",
		Host:     "platform.openai.com",
		Port:     443,
		Path:     "/login",
		HostHdr:  "platform.openai.com",
		Dst:      M.ParseSocksaddrHostPort("platform.openai.com", 443),
	}
	if _, err := httpProbeTarget(conn, target, true); err != nil {
		t.Fatalf("httpProbeTarget() error = %v", err)
	}
}

func TestHTTPProbeTargetRejectsOpenAIAuthChallenge403(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/log-in-or-create-account" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial tls server: %v", err)
	}
	defer conn.Close()

	target := monitor.ProbeTargetSpec{
		Original: "https://auth.openai.com/log-in-or-create-account",
		Scheme:   "https",
		Host:     "auth.openai.com",
		Port:     443,
		Path:     "/log-in-or-create-account",
		HostHdr:  "auth.openai.com",
		Dst:      M.ParseSocksaddrHostPort("auth.openai.com", 443),
	}
	if _, err := httpProbeTarget(conn, target, true); err == nil {
		t.Fatal("expected openai auth 403 probe to fail")
	}
}

type failingOutbound struct{}

func (failingOutbound) Type() string { return "test" }
func (failingOutbound) Tag() string  { return "test-outbound" }
func (failingOutbound) Network() []string {
	return []string{"tcp"}
}
func (failingOutbound) Dependencies() []string { return nil }
func (failingOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, E.New("raw outbound intentionally unavailable")
}
func (failingOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, E.New("raw outbound intentionally unavailable")
}

type directProbeOutbound struct{}

func (directProbeOutbound) Type() string           { return "test" }
func (directProbeOutbound) Tag() string            { return "direct-probe" }
func (directProbeOutbound) Network() []string      { return []string{"tcp"} }
func (directProbeOutbound) Dependencies() []string { return nil }
func (directProbeOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	address := net.JoinHostPort(destination.AddrString(), strconv.Itoa(int(destination.Port)))
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
func (directProbeOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, E.New("packet probe unsupported")
}

type probeOutboundManager struct {
	outbound adapter.Outbound
}

func (m *probeOutboundManager) Start(adapter.StartStage) error { return nil }
func (m *probeOutboundManager) Close() error                   { return nil }
func (m *probeOutboundManager) Outbounds() []adapter.Outbound  { return []adapter.Outbound{m.outbound} }
func (m *probeOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	if tag != "probe-node" || m.outbound == nil {
		return nil, false
	}
	return m.outbound, true
}
func (m *probeOutboundManager) Default() adapter.Outbound { return m.outbound }
func (m *probeOutboundManager) Remove(string) error       { return nil }
func (m *probeOutboundManager) Create(
	context.Context,
	adapter.Router,
	log.ContextLogger,
	string,
	string,
	any,
) error {
	return nil
}

func TestLazyPoolInitializationDoesNotInvalidateFirstProbe(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()

	monitorMgr, err := monitor.NewManager(monitor.Config{ProbeTarget: origin.URL})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()

	p := &poolOutbound{
		ctx:     context.Background(),
		logger:  log.NewNOPFactory().Logger(),
		manager: &probeOutboundManager{outbound: directProbeOutbound{}},
		monitor: monitorMgr,
		options: Options{
			Members: []string{"probe-node"},
			Metadata: map[string]MemberMeta{
				"probe-node": {Name: "probe-node"},
			},
		},
	}

	state := acquireSharedState("probe-node")
	entry := monitorMgr.Register(monitor.NodeInfo{Tag: "probe-node", Name: "probe-node"})
	state.attachEntry(entry)
	entry.SetRelease(releaseSharedState(state))
	entry.SetProbe(p.makeProbeByTagFunc("probe-node"))

	summary, err := monitorMgr.ProbeGeneration(context.Background(), 0, time.Second)
	if err != nil {
		t.Fatalf("ProbeGeneration() error = %v", err)
	}
	if summary.Total != 1 || summary.Completed != 1 || summary.Available != 1 {
		t.Fatalf("first lazy probe summary = %+v, want 1 completed and available", summary)
	}
	if snap := entry.Snapshot(); !snap.InitialCheckDone || !snap.Available {
		t.Fatalf("first lazy probe did not publish health: %+v", snap)
	}
}

func TestLazyProbeClosureSurvivesProbeTargetAddedLater(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()

	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()

	p := &poolOutbound{
		ctx:     context.Background(),
		logger:  log.NewNOPFactory().Logger(),
		manager: &probeOutboundManager{outbound: directProbeOutbound{}},
		monitor: monitorMgr,
		options: Options{
			Members: []string{"probe-node"},
			Metadata: map[string]MemberMeta{
				"probe-node": {Name: "probe-node"},
			},
		},
	}

	state := acquireSharedState("probe-node")
	entry := monitorMgr.Register(monitor.NodeInfo{Tag: "probe-node", Name: "probe-node"})
	state.attachEntry(entry)
	entry.SetProbe(p.makeProbeByTagFunc("probe-node"))
	if err := monitorMgr.UpdateProbeTarget(origin.URL); err != nil {
		t.Fatalf("UpdateProbeTarget() error = %v", err)
	}

	summary, err := monitorMgr.ProbeGeneration(context.Background(), 0, time.Second)
	if err != nil {
		t.Fatalf("ProbeGeneration() error = %v", err)
	}
	if summary.Total != 1 || summary.Completed != 1 || summary.Available != 1 {
		t.Fatalf("late-target probe summary = %+v, want 1 completed and available", summary)
	}
}

func TestPoolProbeCallbackDoesNotMutateMonitorEntryDirectly(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	monitorMgr, err := monitor.NewManager(monitor.Config{ProbeTarget: origin.URL})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()
	entry := monitorMgr.Register(monitor.NodeInfo{Tag: "probe-node", Name: "probe-node"})
	member := &memberState{tag: "probe-node", outbound: directProbeOutbound{}, entry: entry}
	pool := &poolOutbound{ctx: context.Background(), monitor: monitorMgr}
	probe := pool.makeProbeFunc(member)
	if probe == nil {
		t.Fatal("makeProbeFunc() returned nil")
	}
	before := entry.Snapshot()
	if _, err := probe(context.Background()); err != nil {
		t.Fatalf("probe callback error = %v", err)
	}
	after := entry.Snapshot()
	if after.SuccessCount != before.SuccessCount || after.FailureCount != before.FailureCount ||
		after.Available != before.Available || after.InitialCheckDone != before.InitialCheckDone ||
		len(after.Timeline) != len(before.Timeline) {
		t.Fatalf("probe callback mutated monitor state directly: before=%+v after=%+v", before, after)
	}
}

func startConnectProxy(t *testing.T) (string, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Fatalf("unexpected proxy method: %s", r.Method)
		}
		targetConn, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			targetConn.Close()
			t.Fatal("response writer does not support hijacking")
		}
		clientConn, _, err := hijacker.Hijack()
		if err != nil {
			targetConn.Close()
			t.Fatalf("hijack proxy connection: %v", err)
		}
		if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			clientConn.Close()
			targetConn.Close()
			t.Fatalf("write CONNECT response: %v", err)
		}
		go func() {
			defer clientConn.Close()
			defer targetConn.Close()
			_, _ = io.Copy(targetConn, clientConn)
		}()
		go func() {
			defer clientConn.Close()
			defer targetConn.Close()
			_, _ = io.Copy(clientConn, targetConn)
		}()
	}))
	return server.Listener.Addr().String(), server.Close
}

func TestRunProbeTargetsForMemberPrefersLocalHTTPProxy(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/generate_204" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer tlsServer.Close()

	proxyAddress, stopProxy := startConnectProxy(t)
	defer stopProxy()

	host, portText, err := net.SplitHostPort(proxyAddress)
	if err != nil {
		t.Fatalf("split proxy address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse proxy port: %v", err)
	}

	targetHost, targetPortText, err := net.SplitHostPort(tlsServer.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split target address: %v", err)
	}
	targetPort, err := strconv.Atoi(targetPortText)
	if err != nil {
		t.Fatalf("parse target port: %v", err)
	}

	mgr, err := monitor.NewManager(monitor.Config{SkipCertVerify: true})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	p := &poolOutbound{
		monitor: mgr,
		options: Options{
			Metadata: map[string]MemberMeta{
				"commercial-node": {
					Mode:          "multi-port",
					ListenAddress: host,
					Port:          uint16(port),
				},
			},
		},
	}

	member := &memberState{
		tag:      "commercial-node",
		outbound: failingOutbound{},
	}
	targets := []monitor.ProbeTargetSpec{
		{
			Original: "https://commercial-node.example/generate_204",
			Scheme:   "https",
			Host:     targetHost,
			Port:     uint16(targetPort),
			Path:     "/generate_204",
			HostHdr:  targetHost,
			Dst:      M.ParseSocksaddrHostPort(targetHost, uint16(targetPort)),
		},
	}

	duration, err := p.runProbeTargetsForMember(context.Background(), member, targets)
	if err != nil {
		t.Fatalf("runProbeTargetsForMember() error = %v", err)
	}
	if duration <= 0 {
		t.Fatalf("expected positive probe duration, got %v", duration)
	}
}

func TestMemberProbeProxyAddressSkipsSharedPoolListener(t *testing.T) {
	p := &poolOutbound{options: Options{Metadata: map[string]MemberMeta{
		"pool-node": {
			Mode:          "pool",
			ListenAddress: "0.0.0.0",
			Port:          22323,
		},
		"multi-port-node": {
			Mode:          "multi-port",
			ListenAddress: "0.0.0.0",
			Port:          25001,
		},
	}}}

	if got := p.memberProbeProxyAddress(&memberState{tag: "pool-node"}); got != "" {
		t.Fatalf("pool member probe address = %q, want raw outbound", got)
	}
	if got := p.memberProbeProxyAddress(&memberState{tag: "multi-port-node"}); got != "127.0.0.1:25001" {
		t.Fatalf("multi-port member probe address = %q, want dedicated listener", got)
	}
}

func TestShouldSkipProbeTLSVerifyFollowsMonitorConfig(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{SkipCertVerify: true})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	p := &poolOutbound{monitor: mgr}
	if !p.shouldSkipProbeTLSVerify() {
		t.Fatal("expected pool to inherit skip-cert-verify from monitor config")
	}

	mgr.SetSkipCertVerify(false)
	if p.shouldSkipProbeTLSVerify() {
		t.Fatal("expected pool to observe runtime skip-cert-verify updates")
	}
}

func TestSelectMemberAutoPrefersHigherAvailabilityScore(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	healthyEntry := mgr.Register(monitor.NodeInfo{Tag: "healthy", Name: "Healthy"})
	healthyEntry.MarkInitialCheckDone(true)
	healthyState := acquireSharedState("healthy")
	healthyState.attachEntry(healthyEntry)

	penalizedEntry := mgr.Register(monitor.NodeInfo{Tag: "penalized", Name: "Penalized"})
	penalizedEntry.MarkInitialCheckDone(true)
	penalizedEntry.ApplyUsageReportFailure(15, true)
	penalizedState := acquireSharedState("penalized")
	penalizedState.attachEntry(penalizedEntry)

	p := &poolOutbound{mode: modeAuto}
	selected := p.selectMember([]*memberState{
		{tag: "penalized", shared: penalizedState, entry: penalizedEntry},
		{tag: "healthy", shared: healthyState, entry: healthyEntry},
	}, nil, nil)
	if selected == nil {
		t.Fatal("expected a selected member")
	}
	if selected.tag != "healthy" {
		t.Fatalf("expected healthy member to be selected, got %q", selected.tag)
	}
}

func TestSelectMemberStablePrefersLongLivedCandidates(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      time.Nanosecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	longEntry := mgr.Register(monitor.NodeInfo{Tag: "long-lived"})
	longEntry.MarkInitialCheckDone(true)
	normalEntry := mgr.Register(monitor.NodeInfo{Tag: "normal"})
	normalEntry.MarkInitialCheckDone(true)
	normalEntry.ApplyUsageReportFailure(20, true)

	// Give the uptime threshold a chance to elapse while keeping the test
	// independent of wall-clock hours.
	time.Sleep(2 * time.Millisecond)
	if !longEntry.Snapshot().LongLived {
		t.Fatalf("expected long-lived test candidate, got %+v", longEntry.Snapshot())
	}

	p := &poolOutbound{
		mode:   modeSequential,
		sticky: newStickyState(time.Minute),
	}
	directive := &SelectionDirective{Strategy: StrategyStable}
	got := p.selectMemberWithDirective(
		[]*memberState{
			{tag: "normal", entry: normalEntry},
			{tag: "long-lived", entry: longEntry},
		},
		nil,
		nil,
		directive,
	)
	if got == nil || got.tag != "long-lived" {
		t.Fatalf("stable should prefer long-lived candidate, got %+v", got)
	}
}

func TestSelectMemberStableFallsBackWhenNoLongLivedCandidate(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      time.Nanosecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	entry := mgr.Register(monitor.NodeInfo{Tag: "normal"})
	entry.MarkInitialCheckDone(true)

	p := &poolOutbound{
		mode:   modeSequential,
		sticky: newStickyState(time.Minute),
	}
	got := p.selectMemberWithDirective(
		[]*memberState{{tag: "normal", entry: entry}},
		nil,
		nil,
		&SelectionDirective{Strategy: StrategyStable},
	)
	if got == nil || got.tag != "normal" {
		t.Fatalf("stable should fall back to healthy candidate, got %+v", got)
	}
}

func TestSelectMemberStableHonorsExplicitNoLongLivedFilter(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      time.Nanosecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	longEntry := mgr.Register(monitor.NodeInfo{Tag: "long-lived"})
	longEntry.MarkInitialCheckDone(true)
	normalEntry := mgr.Register(monitor.NodeInfo{Tag: "normal"})
	normalEntry.MarkInitialCheckDone(true)
	normalEntry.ApplyUsageReportFailure(20, true)
	time.Sleep(2 * time.Millisecond)

	p := &poolOutbound{
		mode:   modeSequential,
		sticky: newStickyState(time.Minute),
	}
	nolong := false
	got := p.selectMemberWithDirective(
		[]*memberState{
			{tag: "normal", entry: normalEntry},
			{tag: "long-lived", entry: longEntry},
		},
		nil,
		nil,
		&SelectionDirective{Strategy: StrategyStable, Filter: NodeFilter{LongLived: &nolong}},
	)
	if got == nil || got.tag != "normal" {
		t.Fatalf("explicit nolong filter should remain strict, got %+v", got)
	}
}

func TestAvailableMembersAppliesExplicitLongLivedFilterStrictly(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      time.Nanosecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	longEntry := mgr.Register(monitor.NodeInfo{Tag: "long-lived"})
	longEntry.MarkInitialCheckDone(true)
	normalEntry := mgr.Register(monitor.NodeInfo{Tag: "normal"})
	normalEntry.MarkInitialCheckDone(true)
	normalEntry.ApplyUsageReportFailure(20, true)
	time.Sleep(2 * time.Millisecond)
	if !longEntry.Snapshot().LongLived || normalEntry.Snapshot().LongLived {
		t.Fatalf("invalid test setup: long=%+v normal=%+v", longEntry.Snapshot(), normalEntry.Snapshot())
	}

	p := &poolOutbound{
		members: []*memberState{
			{tag: "long-lived", entry: longEntry},
			{tag: "normal", entry: normalEntry},
		},
	}

	for _, tt := range []struct {
		name     string
		wantLong bool
		wantTag  string
	}{
		{name: "long", wantLong: true, wantTag: "long-lived"},
		{name: "nolong", wantLong: false, wantTag: "normal"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := p.availableMembersLocked(
				time.Now(),
				"",
				nil,
				nil,
				nil,
				false,
				false,
				nil,
				&SelectionDirective{Filter: NodeFilter{LongLived: &tt.wantLong}},
			)
			if len(got) != 1 || got[0].tag != tt.wantTag {
				t.Fatalf("explicit %s filter returned %+v, want only %s", tt.name, got, tt.wantTag)
			}
		})
	}
}

func TestSelectMemberStableHonorsPinnedHealthyNonLongLivedCandidate(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      time.Nanosecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	longEntry := mgr.Register(monitor.NodeInfo{Tag: "long-lived"})
	longEntry.MarkInitialCheckDone(true)
	pinnedEntry := mgr.Register(monitor.NodeInfo{Tag: "pinned"})
	pinnedEntry.MarkInitialCheckDone(true)
	pinnedEntry.ApplyUsageReportFailure(20, true)
	time.Sleep(2 * time.Millisecond)
	if !longEntry.Snapshot().LongLived || pinnedEntry.Snapshot().LongLived {
		t.Fatalf("invalid test setup: long=%+v pinned=%+v", longEntry.Snapshot(), pinnedEntry.Snapshot())
	}

	p := &poolOutbound{
		mode:   modeSequential,
		sticky: newStickyState(time.Minute),
	}
	got := p.selectMemberWithDirective(
		[]*memberState{
			{tag: "pinned", entry: pinnedEntry},
			{tag: "long-lived", entry: longEntry},
		},
		nil,
		nil,
		&SelectionDirective{Strategy: StrategyStable, PinnedTag: "pinned"},
	)
	if got == nil || got.tag != "pinned" {
		t.Fatalf("healthy pinned candidate should win implicit long-lived preference, got %+v", got)
	}
}

func TestSelectMemberStableKeepsExistingBindingWhenLongLivedAppears(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      2 * time.Millisecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	normalEntry := mgr.Register(monitor.NodeInfo{Tag: "normal"})
	normalEntry.MarkInitialCheckDone(true)
	normalEntry.ApplyUsageReportFailure(20, true)
	longEntry := mgr.Register(monitor.NodeInfo{Tag: "long-lived"})
	longEntry.MarkInitialCheckDone(true)

	p := &poolOutbound{mode: modeSequential, sticky: newStickyState(time.Minute)}
	directive := &SelectionDirective{Strategy: StrategyStable}
	candidates := []*memberState{
		{tag: "normal", entry: normalEntry},
		{tag: "long-lived", entry: longEntry},
	}

	first := p.selectMemberWithDirective(candidates, nil, nil, directive)
	if first == nil || first.tag != "normal" {
		t.Fatalf("expected initial fallback to normal node, got %+v", first)
	}
	time.Sleep(5 * time.Millisecond)
	if !longEntry.Snapshot().LongLived {
		t.Fatalf("expected long-lived candidate after threshold, got %+v", longEntry.Snapshot())
	}

	second := p.selectMemberWithDirective(candidates, nil, nil, directive)
	if second == nil || second.tag != "normal" {
		t.Fatalf("existing stable binding should not move when long-lived appears, got %+v", second)
	}
	third := p.selectMemberWithDirective([]*memberState{candidates[1]}, nil, nil, directive)
	if third == nil || third.tag != "long-lived" {
		t.Fatalf("binding should promote after pinned node disappears, got %+v", third)
	}
}

func TestSelectMemberAutoPenalizesBadSources(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	goodEntry := mgr.Register(monitor.NodeInfo{
		Tag:        "good-node",
		Name:       "Good Node",
		SourceRef:  "local-sub-1",
		SourceName: "subscription-1",
	})
	goodEntry.MarkInitialCheckDone(true)
	goodState := acquireSharedState("good-node")
	goodState.attachEntry(goodEntry)

	badEntry := mgr.Register(monitor.NodeInfo{
		Tag:        "bad-node",
		Name:       "Bad Node",
		SourceRef:  "local-sub-2",
		SourceName: "subscription-2",
	})
	badEntry.MarkInitialCheckDone(false)
	badEntry.RecordFailure(E.New("tls handshake: EOF"), "www.google.com:443")
	badState := acquireSharedState("bad-node")
	badState.attachEntry(badEntry)

	p := &poolOutbound{
		mode:    modeAuto,
		monitor: mgr,
		options: Options{
			Metadata: map[string]MemberMeta{
				"good-node": {Name: "Good Node", SourceRef: "local-sub-1", SourceName: "subscription-1"},
				"bad-node":  {Name: "Bad Node", SourceRef: "local-sub-2", SourceName: "subscription-2"},
			},
		},
	}

	selected := p.selectMember([]*memberState{
		{tag: "bad-node", shared: badState, entry: badEntry},
		{tag: "good-node", shared: goodState, entry: goodEntry},
	}, mgr.SourceSelectionStates(), nil)
	if selected == nil {
		t.Fatal("expected a selected member")
	}
	if selected.tag != "good-node" {
		t.Fatalf("expected good source to be preferred, got %q", selected.tag)
	}
}

func TestAvailableMembersLockedExcludesBadSecondaryClusters(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	goodEntry := mgr.Register(monitor.NodeInfo{
		Tag:            "good-node",
		Name:           "Good Node",
		SourceRef:      "manifest:agg",
		SourceName:     "Aggregator Stable",
		ProtocolFamily: "vless",
		NodeMode:       "tls/ws",
		DomainFamily:   "good.example.com",
	})
	goodEntry.MarkInitialCheckDone(true)
	goodState := acquireSharedState("good-node")
	goodState.attachEntry(goodEntry)

	badEntry := mgr.Register(monitor.NodeInfo{
		Tag:            "bad-node",
		Name:           "Bad Node",
		SourceRef:      "manifest:agg",
		SourceName:     "Aggregator Stable",
		ProtocolFamily: "vless",
		NodeMode:       "reality/tcp",
		DomainFamily:   "badcluster.example.com",
	})
	badEntry.MarkInitialCheckDone(false)
	badEntry.RecordFailure(E.New("reality verification failed"), "www.google.com:443")
	badState := acquireSharedState("bad-node")
	badState.attachEntry(badEntry)

	p := &poolOutbound{
		mode:    modeAuto,
		monitor: mgr,
		options: Options{
			Metadata: map[string]MemberMeta{
				"good-node": {
					Name:           "Good Node",
					SourceRef:      "manifest:agg",
					SourceName:     "Aggregator Stable",
					ProtocolFamily: "vless",
					NodeMode:       "tls/ws",
					DomainFamily:   "good.example.com",
				},
				"bad-node": {
					Name:           "Bad Node",
					SourceRef:      "manifest:agg",
					SourceName:     "Aggregator Stable",
					ProtocolFamily: "vless",
					NodeMode:       "reality/tcp",
					DomainFamily:   "badcluster.example.com",
				},
			},
		},
		members: []*memberState{
			{tag: "good-node", shared: goodState, entry: goodEntry},
			{tag: "bad-node", shared: badState, entry: badEntry},
		},
	}

	candidates := p.availableMembersLocked(
		time.Now(),
		"",
		nil,
		mgr.SourceSelectionStates(),
		mgr.SecondarySelectionStates(),
		true,
		true,
		nil,
		nil,
	)
	if len(candidates) != 1 || candidates[0].tag != "good-node" {
		t.Fatalf("expected only good-node to remain after secondary exclusion, got %+v", candidates)
	}
}

func TestTrackedConnRecordsTrafficSuccessOnlyAfterDownload(t *testing.T) {
	ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	entry := mgr.Register(monitor.NodeInfo{Tag: "traffic-node", Name: "Traffic Node"})
	shared := acquireSharedState("traffic-node")
	shared.attachEntry(entry)

	server, client := net.Pipe()
	defer server.Close()

	conn := &trackedConn{
		Conn:    client,
		release: func() {},
		onConfirmedSuccess: func() {
			shared.recordSuccess("example.com:443")
		},
	}
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = server.Write([]byte("hello"))
	}()

	buf := make([]byte, 5)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("trackedConn.Read() error = %v", err)
	}
	<-done

	snaps := mgr.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].TrafficSuccessCount != 1 {
		t.Fatalf("expected traffic success count to be 1 after download, got %d", snaps[0].TrafficSuccessCount)
	}
	if snaps[0].LastTrafficSuccessAt.IsZero() {
		t.Fatal("expected last traffic success timestamp to be set")
	}
}

func TestTrackedConnWriteOnlyDoesNotRecordTrafficSuccess(t *testing.T) {
	ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	entry := mgr.Register(monitor.NodeInfo{Tag: "write-only-node", Name: "Write Only Node"})
	shared := acquireSharedState("write-only-node")
	shared.attachEntry(entry)

	server, client := net.Pipe()
	defer server.Close()

	conn := &trackedConn{
		Conn:    client,
		release: func() {},
		onConfirmedSuccess: func() {
			shared.recordSuccess("example.com:443")
		},
	}
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4)
		_, _ = server.Read(buf)
	}()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("trackedConn.Write() error = %v", err)
	}
	<-done

	snaps := mgr.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].TrafficSuccessCount != 0 {
		t.Fatalf("expected traffic success count to remain 0 without download, got %d", snaps[0].TrafficSuccessCount)
	}
	if !snaps[0].LastTrafficSuccessAt.IsZero() {
		t.Fatal("expected last traffic success timestamp to remain unset without download")
	}
}
