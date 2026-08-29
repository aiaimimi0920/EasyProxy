package pool

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
)

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

func TestRunProbeTargetsForMemberDoesNotHideRawOutboundFailureBehindLocalProxy(t *testing.T) {
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

	if _, err := p.runProbeTargetsForMember(context.Background(), member, targets); err == nil || !strings.Contains(err.Error(), "intentionally unavailable") {
		t.Fatalf("runProbeTargetsForMember() error = %v, want raw outbound failure", err)
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
