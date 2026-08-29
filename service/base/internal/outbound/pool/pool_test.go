package pool

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

type failingOutbound struct{}

func (failingOutbound) Type() string { return "test" }

func (failingOutbound) Tag() string { return "test-outbound" }

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

func (directProbeOutbound) Type() string { return "test" }

func (directProbeOutbound) Tag() string { return "direct-probe" }

func (directProbeOutbound) Network() []string { return []string{"tcp"} }

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

func (m *probeOutboundManager) Close() error { return nil }

func (m *probeOutboundManager) Outbounds() []adapter.Outbound { return []adapter.Outbound{m.outbound} }

func (m *probeOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	if tag != "probe-node" || m.outbound == nil {
		return nil, false
	}
	return m.outbound, true
}

func (m *probeOutboundManager) Default() adapter.Outbound { return m.outbound }

func (m *probeOutboundManager) Remove(string) error { return nil }

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
