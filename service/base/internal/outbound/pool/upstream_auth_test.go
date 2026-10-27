package pool

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	sHTTP "github.com/sagernet/sing/protocol/http"
)

type authHTTPOutbound struct{ failingOutbound }

func (authHTTPOutbound) Type() string { return "http" }

type proxyHTTPTestOutbound struct {
	authHTTPOutbound
	client *sHTTP.Client
}

func (o proxyHTTPTestOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return o.client.DialContext(ctx, network, destination)
}

func TestUpstreamProxyErrorClassificationIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"destination-401", errors.New("unexpected status: 401 Unauthorized")},
		{"destination-403", errors.New("unexpected status: 403 Forbidden")},
		{"unrelated-auth-message", errors.New("website authentication required")},
		{"timeout", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamProxyError(authHTTPOutbound{}, tc.err); got != tc.err {
				t.Fatalf("unrelated error was reclassified: %v", got)
			}
		})
	}
	err := errors.New("authentication required")
	if got := upstreamProxyError(failingOutbound{}, err); got != err {
		t.Fatal("non-HTTP error was classified as HTTP CONNECT authentication failure")
	}
}

func TestMemberProbeQuarantinesRealHTTPConnect407(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodConnect {
			t.Errorf("proxy request method = %s, want CONNECT", r.Method)
		}
		w.Header().Set("Proxy-Authenticate", "Basic realm=upstream")
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer server.Close()
	mgr, err := monitor.NewManager(monitor.Config{ProbeTargets: []string{"https://connectivitycheck.gstatic.com/generate_204", "https://cp.cloudflare.com/generate_204"}})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()
	entry := mgr.Register(monitor.NodeInfo{Tag: t.Name()})
	member := &memberState{tag: t.Name(), entry: entry, outbound: proxyHTTPTestOutbound{client: sHTTP.NewClient(sHTTP.Options{Server: M.ParseSocksaddr(server.Listener.Addr().String())})}}
	p := &poolOutbound{monitor: mgr, members: []*memberState{member}, logger: log.NewNOPFactory().Logger()}
	entry.SetProbe(p.makeProbeFunc(member))
	if _, err := mgr.Probe(context.Background(), t.Name()); !errors.Is(err, monitor.ErrUpstreamAuthenticationRequired) {
		t.Fatalf("CONNECT 407 error lost its authentication classification: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("proxy authentication failure retried across %d targets", requests.Load())
	}
	if _, err := p.pickMember("tcp", nil, nil); err == nil {
		t.Fatal("probe-authentication-rejected node survived fallback selection")
	}
}

func TestUpstreamHTTPAuthFailureQuarantinesUntilFreshProbe(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()
	entry := mgr.Register(monitor.NodeInfo{Tag: t.Name()})
	entry.MarkInitialCheckDone(true)
	shared := acquireSharedState(t.Name())
	shared.attachEntry(entry)
	member := &memberState{tag: t.Name(), entry: entry, shared: shared, outbound: authHTTPOutbound{}}
	p := &poolOutbound{logger: log.NewNOPFactory().Logger(), members: []*memberState{member}, options: Options{FailureThreshold: 10, BlacklistDuration: time.Minute}}
	p.recordFailure(member, "tcp", errors.New("authentication required"), "www.google.com:443")
	if snap := entry.Snapshot(); snap.Available || snap.EffectiveAvailable {
		t.Errorf("upstream authentication rejection retained stale health: available=%v effective=%v", snap.Available, snap.EffectiveAvailable)
	}
	// An old, already-open connection cannot undo the authentication quarantine.
	entry.RecordSuccess("old-connection.example:443")
	shared.forceRelease()
	if snap := entry.Snapshot(); snap.EffectiveAvailable {
		t.Error("old connection or blacklist release revived authentication-rejected node")
	}
	if _, err := p.pickMember("tcp", nil, nil); err == nil {
		t.Error("fallback selection reused authentication-rejected node")
	}
	entry.SetProbe(func(context.Context) (time.Duration, error) { return time.Millisecond, nil })
	if _, err := mgr.Probe(context.Background(), t.Name()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pickMember("tcp", nil, nil); err != nil {
		t.Fatalf("fresh successful probe did not recover node: %v", err)
	}
}

func TestOrdinaryHTTPFailurePreservesProbeHealth(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()
	entry := mgr.Register(monitor.NodeInfo{Tag: t.Name()})
	entry.MarkInitialCheckDone(true)
	shared := acquireSharedState(t.Name())
	shared.attachEntry(entry)
	member := &memberState{tag: t.Name(), entry: entry, shared: shared, outbound: authHTTPOutbound{}}
	p := &poolOutbound{logger: log.NewNOPFactory().Logger(), options: Options{FailureThreshold: 10}}
	p.recordFailure(member, "tcp", context.DeadlineExceeded, "slow.example:443")
	if !entry.Snapshot().EffectiveAvailable {
		t.Fatal("ordinary destination timeout invalidated probe health")
	}
}

func TestHTTPProbeRejectsProxyAuthenticationRequired(t *testing.T) {
	for _, tlsEnabled := range []bool{false, true} {
		name := "http"
		if tlsEnabled {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Proxy-Authenticate", "Basic realm=upstream")
				w.WriteHeader(http.StatusProxyAuthRequired)
			})
			server := httptest.NewUnstartedServer(handler)
			if tlsEnabled {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			conn, err := dialContextTCP(context.Background(), server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			target := monitor.ProbeTargetSpec{Scheme: name, Host: "connectivitycheck.gstatic.com", Path: "/generate_204"}
			if _, err := httpProbeTarget(conn, target, true); err == nil {
				t.Fatal("HTTP 407 was accepted as a healthy probe")
			}
		})
	}
}

func TestHTTPProbeDoesNotHideFirstAuthenticationFailureWithWarmSuccess(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusProxyAuthRequired)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	conn, err := dialContextTCP(context.Background(), server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := httpProbeTarget(conn, monitor.ProbeTargetSpec{Scheme: "http", Host: "example.com"}); err == nil {
		t.Fatal("warm success hid first authentication rejection")
	}
	if requests.Load() != 1 {
		t.Fatal("probe continued after authentication rejection")
	}
}
