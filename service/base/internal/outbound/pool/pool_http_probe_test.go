package pool

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"easy_proxies/internal/monitor"

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

func TestHTTPProbeTargetUsesWarmSecondRequestDelay(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			time.Sleep(80 * time.Millisecond)
		} else {
			time.Sleep(5 * time.Millisecond)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial tls server: %v", err)
	}
	defer conn.Close()
	target := monitor.ProbeTargetSpec{
		Original: "https://www.gstatic.com/generate_204",
		Scheme:   "https",
		Host:     "www.gstatic.com",
		Port:     443,
		Path:     "/generate_204",
		HostHdr:  "www.gstatic.com",
		Dst:      M.ParseSocksaddrHostPort("www.gstatic.com", 443),
	}
	duration, err := httpProbeTarget(conn, target, true)
	if err != nil {
		t.Fatalf("httpProbeTarget() error = %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("probe request count = %d, want 2", got)
	}
	if duration >= 50*time.Millisecond {
		t.Fatalf("unified delay = %v, want warm second-request latency", duration)
	}
}
