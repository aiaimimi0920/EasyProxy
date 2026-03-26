package pool

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

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
	if _, err := httpProbe(conn, destination); err != nil {
		t.Fatalf("httpProbe() error = %v", err)
	}
}
