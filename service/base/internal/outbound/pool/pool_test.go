package pool

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"easy_proxies/internal/monitor"

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
