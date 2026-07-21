package dispatch

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

const dispatchTestTimeout = 5 * time.Second

type dispatchTestDial struct {
	destination string
	directive   pool.SelectionDirective
}

type dispatchTestOutbound struct {
	tag string

	mu    sync.Mutex
	dials []dispatchTestDial
}

var _ adapter.Outbound = (*dispatchTestOutbound)(nil)

func (o *dispatchTestOutbound) Type() string { return "test" }
func (o *dispatchTestOutbound) Tag() string  { return o.tag }
func (o *dispatchTestOutbound) Network() []string {
	return []string{N.NetworkTCP}
}
func (o *dispatchTestOutbound) Dependencies() []string { return nil }

func (o *dispatchTestOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	call := dispatchTestDial{destination: destination.String()}
	if directive := pool.DirectiveFrom(ctx); directive != nil {
		call.directive = cloneDispatchTestDirective(*directive)
	}
	o.mu.Lock()
	o.dials = append(o.dials, call)
	o.mu.Unlock()

	dialer := &net.Dialer{Timeout: dispatchTestTimeout}
	return dialer.DialContext(ctx, network, destination.String())
}

func (o *dispatchTestOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("packet dialing is not supported by the dispatch test outbound")
}

func (o *dispatchTestOutbound) snapshotDials() []dispatchTestDial {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]dispatchTestDial(nil), o.dials...)
}

type dispatchTestPoolProvider struct {
	mu       sync.RWMutex
	outbound adapter.Outbound
	reads    int
}

type blockingDispatchTestOutbound struct {
	dialed  chan struct{}
	remote  net.Conn
	closeMu sync.Mutex
}

var _ adapter.Outbound = (*blockingDispatchTestOutbound)(nil)

func (o *blockingDispatchTestOutbound) Type() string { return "blocking-test" }
func (o *blockingDispatchTestOutbound) Tag() string  { return "blocking-test" }
func (o *blockingDispatchTestOutbound) Network() []string {
	return []string{N.NetworkTCP}
}
func (o *blockingDispatchTestOutbound) Dependencies() []string { return nil }
func (o *blockingDispatchTestOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	local, remote := net.Pipe()
	o.closeMu.Lock()
	o.remote = remote
	o.closeMu.Unlock()
	select {
	case <-o.dialed:
	default:
		close(o.dialed)
	}
	return local, nil
}
func (o *blockingDispatchTestOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("packet dialing is not supported by the blocking dispatch test outbound")
}
func (o *blockingDispatchTestOutbound) closeRemote() {
	o.closeMu.Lock()
	remote := o.remote
	o.remote = nil
	o.closeMu.Unlock()
	if remote != nil {
		_ = remote.Close()
	}
}

var _ PoolProvider = (*dispatchTestPoolProvider)(nil)

func (p *dispatchTestPoolProvider) PoolOutbound() (adapter.Outbound, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	return p.outbound, p.outbound != nil
}

func (p *dispatchTestPoolProvider) setOutbound(outbound adapter.Outbound) {
	p.mu.Lock()
	p.outbound = outbound
	p.mu.Unlock()
}

func (p *dispatchTestPoolProvider) readCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.reads
}

func cloneDispatchTestDirective(directive pool.SelectionDirective) pool.SelectionDirective {
	clone := directive
	clone.Filter.Countries = append([]string(nil), directive.Filter.Countries...)
	clone.Filter.Regions = append([]string(nil), directive.Filter.Regions...)
	if directive.Filter.LongLived != nil {
		longLived := *directive.Filter.LongLived
		clone.Filter.LongLived = &longLived
	}
	return clone
}

func TestConnectRequestOverlayHeadersOverridePath(t *testing.T) {
	pathOverlay, ok := parseTokens("stable+us")
	if !ok {
		t.Fatal("expected CONNECT path tokens to parse")
	}
	req := &http.Request{Header: http.Header{}}
	req.Header.Set(headerStrategy, "auto")
	req.Header.Set(headerCountry, "JP")

	merged := connectRequestOverlay(req, pathOverlay)
	resolved := merged.resolve(pool.StrategyStable, "203.0.113.10")

	if resolved.directive.Strategy != pool.StrategyAuto {
		t.Fatalf("header strategy should override path strategy: got %s", resolved.directive.Strategy)
	}
	if len(resolved.directive.Filter.Countries) != 1 || resolved.directive.Filter.Countries[0] != "JP" {
		t.Fatalf("header country should override path filter: got %v", resolved.directive.Filter.Countries)
	}
}

func TestConnectHandlerHeadersOverridePathTokens(t *testing.T) {
	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetListener.Close()

	targetAccepted := make(chan struct{})
	go func() {
		conn, acceptErr := targetListener.Accept()
		if acceptErr == nil {
			close(targetAccepted)
			_ = conn.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outbound := &dispatchTestOutbound{tag: "connect-precedence"}
	provider := &dispatchTestPoolProvider{outbound: outbound}
	engine := routerule.New(nil, routerule.PolicyProxy, nil)
	server := NewServer(Config{Listen: "127.0.0.1:0"}, provider, engine, nil)
	if err := server.Start(ctx); err != nil {
		t.Fatalf("start dispatch server: %v", err)
	}
	defer server.Stop()

	server.mu.RLock()
	dispatchAddr := server.ln.Addr().String()
	server.mu.RUnlock()
	client, err := net.DialTimeout("tcp", dispatchAddr, time.Second)
	if err != nil {
		t.Fatalf("dial dispatch server: %v", err)
	}
	defer client.Close()

	targetAddr := targetListener.Addr().String()
	request := fmt.Sprintf(
		"CONNECT auto/%s HTTP/1.1\r\nHost: auto/%s\r\n%s: session\r\n%s: header-session\r\n\r\n",
		targetAddr,
		targetAddr,
		headerStrategy,
		headerSession,
	)
	if _, err := client.Write([]byte(request)); err != nil {
		t.Fatalf("write CONNECT request: %v", err)
	}

	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", response.StatusCode)
	}

	select {
	case <-targetAccepted:
	case <-time.After(time.Second):
		t.Fatal("proxy target was not reached")
	}

	dials := outbound.snapshotDials()
	if len(dials) != 1 {
		t.Fatalf("CONNECT outbound dials = %d, want 1", len(dials))
	}
	if dials[0].directive.Strategy != pool.StrategySession {
		t.Fatalf("CONNECT directive strategy = %s, want header session override", dials[0].directive.Strategy)
	}
	if dials[0].directive.SessionKey != "header-session" {
		t.Fatalf("CONNECT directive session key = %q, want header-session", dials[0].directive.SessionKey)
	}
}

func TestServerStopClosesIdleKeepAliveConnection(t *testing.T) {
	origin := newDispatchTestOrigin(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(
		Config{Listen: "127.0.0.1:0", DialTimeout: dispatchTestTimeout},
		nil,
		routerule.New(nil, routerule.PolicyDirect, nil),
		nil,
	)
	if err := server.Start(ctx); err != nil {
		t.Fatalf("start dispatch server: %v", err)
	}
	defer server.Stop()

	server.mu.RLock()
	address := server.ln.Addr().String()
	server.mu.RUnlock()
	conn := dialDispatchTestTCP(t, address)
	reader := bufio.NewReader(conn)
	if body := dispatchTestProxyGET(t, conn, reader, origin.URL+"/keep-alive", nil); body != "/keep-alive" {
		t.Fatalf("keep-alive response body = %q", body)
	}

	server.Stop()
	if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("set post-stop deadline: %v", err)
	}
	_, readErr := reader.ReadByte()
	if readErr == nil {
		t.Fatal("client connection remained readable after server stop")
	}
	if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("idle keep-alive connection remained open after server stop: %v", readErr)
	}
}

func TestServerStopClosesActiveConnectUpstream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outbound := &blockingDispatchTestOutbound{dialed: make(chan struct{})}
	server := NewServer(
		Config{Listen: "127.0.0.1:0", DialTimeout: dispatchTestTimeout},
		&dispatchTestPoolProvider{outbound: outbound},
		routerule.New(nil, routerule.PolicyProxy, nil),
		nil,
	)
	if err := server.Start(ctx); err != nil {
		t.Fatalf("start dispatch server: %v", err)
	}

	server.mu.RLock()
	address := server.ln.Addr().String()
	server.mu.RUnlock()
	client, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("dial dispatch server: %v", err)
	}
	defer client.Close()
	if _, err := fmt.Fprintf(client, "CONNECT silent.example:443 HTTP/1.1\r\nHost: silent.example:443\r\n\r\n"); err != nil {
		t.Fatalf("write CONNECT request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", response.StatusCode)
	}
	_ = response.Body.Close()
	select {
	case <-outbound.dialed:
	case <-time.After(time.Second):
		t.Fatal("blocking upstream was not dialed")
	}

	stopDone := make(chan struct{})
	go func() {
		server.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(300 * time.Millisecond):
		outbound.closeRemote()
		select {
		case <-stopDone:
		case <-time.After(time.Second):
			t.Fatal("Server.Stop() remained blocked on an active CONNECT upstream")
		}
		t.Fatal("Server.Stop() returned only after the test manually closed the upstream")
	}
}

func TestServerRestartSurvivesPreviousStopWatcher(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	server := NewServer(
		Config{Listen: "127.0.0.1:0", DialTimeout: dispatchTestTimeout},
		nil,
		routerule.New(nil, routerule.PolicyDirect, nil),
		nil,
	)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	if err := server.Start(firstCtx); err != nil {
		t.Fatalf("start first dispatch generation: %v", err)
	}
	server.Stop()

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	if err := server.Start(secondCtx); err != nil {
		t.Fatalf("start second dispatch generation: %v", err)
	}
	defer server.Stop()

	for i := 0; i < 20; i++ {
		runtime.Gosched()
	}
	time.Sleep(10 * time.Millisecond)

	server.mu.RLock()
	started := server.started
	listener := server.ln
	server.mu.RUnlock()
	if !started || listener == nil {
		t.Fatal("the previous context watcher stopped the restarted dispatch server")
	}
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("restarted dispatch listener is not reachable: %v", err)
	}
	_ = conn.Close()
}

func TestServerSingleListenerServesHTTPProxyAndSOCKS5(t *testing.T) {
	origin := newDispatchTestOrigin(t)
	dispatchAddr := startDispatchTestServer(
		t,
		nil,
		routerule.New(nil, routerule.PolicyDirect, nil),
	)

	httpConn := dialDispatchTestTCP(t, dispatchAddr)
	httpReader := bufio.NewReader(httpConn)
	if body := dispatchTestProxyGET(t, httpConn, httpReader, origin.URL+"/http", nil); body != "/http" {
		t.Fatalf("HTTP proxy body = %q, want %q", body, "/http")
	}

	socksConn := dialDispatchTestSOCKS5(t, dispatchAddr, origin.Listener.Addr().String())
	socksReader := bufio.NewReader(socksConn)
	if body := dispatchTestOriginGET(t, socksConn, socksReader, origin.URL+"/socks"); body != "/socks" {
		t.Fatalf("SOCKS5 tunnel body = %q, want %q", body, "/socks")
	}
}

func TestServerRoutesDirectAndProxyDials(t *testing.T) {
	origin := newDispatchTestOrigin(t)
	outbound := &dispatchTestOutbound{tag: "proxy-choice"}
	provider := &dispatchTestPoolProvider{outbound: outbound}
	dispatchAddr := startDispatchTestServer(
		t,
		provider,
		routerule.New(nil, routerule.PolicyDirect, nil),
	)

	directConn := dialDispatchTestTCP(t, dispatchAddr)
	directReader := bufio.NewReader(directConn)
	if body := dispatchTestProxyGET(t, directConn, directReader, origin.URL+"/direct", nil); body != "/direct" {
		t.Fatalf("DIRECT body = %q, want %q", body, "/direct")
	}
	if reads := provider.readCount(); reads != 0 {
		t.Fatalf("DIRECT request read pool provider %d times, want 0", reads)
	}
	if dials := outbound.snapshotDials(); len(dials) != 0 {
		t.Fatalf("DIRECT request used proxy outbound %d times, want 0", len(dials))
	}

	proxyHeaders := http.Header{}
	proxyHeaders.Set(headerSplit, "off")
	proxyHeaders.Set(headerStrategy, string(pool.StrategySession))
	proxyHeaders.Set(headerSession, "route-choice-session")
	proxyConn := dialDispatchTestTCP(t, dispatchAddr)
	proxyReader := bufio.NewReader(proxyConn)
	if body := dispatchTestProxyGET(t, proxyConn, proxyReader, origin.URL+"/proxy", proxyHeaders); body != "/proxy" {
		t.Fatalf("PROXY body = %q, want %q", body, "/proxy")
	}

	if reads := provider.readCount(); reads != 1 {
		t.Fatalf("PROXY request read pool provider %d times, want 1", reads)
	}
	dials := outbound.snapshotDials()
	if len(dials) != 1 {
		t.Fatalf("PROXY outbound dial count = %d, want 1", len(dials))
	}
	if dials[0].destination != origin.Listener.Addr().String() {
		t.Fatalf("PROXY destination = %q, want %q", dials[0].destination, origin.Listener.Addr().String())
	}
	if dials[0].directive.Strategy != pool.StrategySession {
		t.Fatalf("PROXY directive strategy = %q, want %q", dials[0].directive.Strategy, pool.StrategySession)
	}
	if dials[0].directive.SessionKey != "route-choice-session" {
		t.Fatalf("PROXY directive session = %q, want %q", dials[0].directive.SessionKey, "route-choice-session")
	}
}

func TestServerKeepsHTTP11ClientConnectionAlive(t *testing.T) {
	origin := newDispatchTestOrigin(t)
	dispatchAddr := startDispatchTestServer(
		t,
		nil,
		routerule.New(nil, routerule.PolicyDirect, nil),
	)

	conn := dialDispatchTestTCP(t, dispatchAddr)
	reader := bufio.NewReader(conn)
	if body := dispatchTestProxyGET(t, conn, reader, origin.URL+"/first", nil); body != "/first" {
		t.Fatalf("first response body = %q, want %q", body, "/first")
	}
	if body := dispatchTestProxyGET(t, conn, reader, origin.URL+"/second", nil); body != "/second" {
		t.Fatalf("second response body = %q, want %q", body, "/second")
	}
}

func TestServerReadsPoolProviderForEveryRequest(t *testing.T) {
	origin := newDispatchTestOrigin(t)
	firstOutbound := &dispatchTestOutbound{tag: "first-live-pool"}
	secondOutbound := &dispatchTestOutbound{tag: "second-live-pool"}
	provider := &dispatchTestPoolProvider{outbound: firstOutbound}
	dispatchAddr := startDispatchTestServer(
		t,
		provider,
		routerule.New(nil, routerule.PolicyProxy, nil),
	)

	conn := dialDispatchTestTCP(t, dispatchAddr)
	reader := bufio.NewReader(conn)
	if body := dispatchTestProxyGET(t, conn, reader, origin.URL+"/first-pool", nil); body != "/first-pool" {
		t.Fatalf("first pool response body = %q, want %q", body, "/first-pool")
	}

	provider.setOutbound(secondOutbound)
	if body := dispatchTestProxyGET(t, conn, reader, origin.URL+"/second-pool", nil); body != "/second-pool" {
		t.Fatalf("second pool response body = %q, want %q", body, "/second-pool")
	}

	if reads := provider.readCount(); reads != 2 {
		t.Fatalf("pool provider read count = %d, want 2", reads)
	}
	if dials := firstOutbound.snapshotDials(); len(dials) != 1 {
		t.Fatalf("first live outbound dial count = %d, want 1", len(dials))
	}
	if dials := secondOutbound.snapshotDials(); len(dials) != 1 {
		t.Fatalf("second live outbound dial count = %d, want 1", len(dials))
	}
}

func startDispatchTestServer(t *testing.T, provider PoolProvider, engine *routerule.Engine) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	server := NewServer(
		Config{Listen: "127.0.0.1:0", DialTimeout: dispatchTestTimeout},
		provider,
		engine,
		nil,
	)
	if err := server.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start dispatch test server: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		server.Stop()
	})

	server.mu.RLock()
	defer server.mu.RUnlock()
	if server.ln == nil {
		t.Fatal("dispatch test server started without a listener")
	}
	return server.ln.Addr().String()
}

func newDispatchTestOrigin(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, r.URL.Path)
		}),
	)
	t.Cleanup(server.Close)
	return server
}

func dialDispatchTestTCP(t *testing.T, address string) net.Conn {
	t.Helper()
	dialer := &net.Dialer{Timeout: dispatchTestTimeout}
	conn, err := dialer.DialContext(context.Background(), N.NetworkTCP, address)
	if err != nil {
		t.Fatalf("dial %s: %v", address, err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return conn
}

func dispatchTestProxyGET(t *testing.T, conn net.Conn, reader *bufio.Reader, rawURL string, headers http.Header) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("create proxy GET %s: %v", rawURL, err)
	}
	request.Header = headers.Clone()
	if err := conn.SetDeadline(time.Now().Add(dispatchTestTimeout)); err != nil {
		t.Fatalf("set proxy connection deadline: %v", err)
	}
	if err := request.WriteProxy(conn); err != nil {
		t.Fatalf("write proxy GET %s: %v", rawURL, err)
	}
	return dispatchTestReadResponse(t, reader, request)
}

func dispatchTestOriginGET(t *testing.T, conn net.Conn, reader *bufio.Reader, rawURL string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("create origin GET %s: %v", rawURL, err)
	}
	if err := conn.SetDeadline(time.Now().Add(dispatchTestTimeout)); err != nil {
		t.Fatalf("set tunnel connection deadline: %v", err)
	}
	if err := request.Write(conn); err != nil {
		t.Fatalf("write tunneled GET %s: %v", rawURL, err)
	}
	return dispatchTestReadResponse(t, reader, request)
}

func dispatchTestReadResponse(t *testing.T, reader *bufio.Reader, request *http.Request) string {
	t.Helper()
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		t.Fatalf("read response for %s: %v", request.URL, err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		t.Fatalf("read response body for %s: %v", request.URL, readErr)
	}
	if closeErr != nil {
		t.Fatalf("close response body for %s: %v", request.URL, closeErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response status for %s = %d, want 200; body=%q", request.URL, response.StatusCode, body)
	}
	return string(body)
}

func dialDispatchTestSOCKS5(t *testing.T, dispatchAddr, targetAddr string) net.Conn {
	t.Helper()
	conn := dialDispatchTestTCP(t, dispatchAddr)
	if err := conn.SetDeadline(time.Now().Add(dispatchTestTimeout)); err != nil {
		t.Fatalf("set SOCKS5 deadline: %v", err)
	}
	if _, err := conn.Write([]byte{socks5Version, 0x01, authNoAuth}); err != nil {
		t.Fatalf("write SOCKS5 greeting: %v", err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatalf("read SOCKS5 method reply: %v", err)
	}
	if methodReply[0] != socks5Version || methodReply[1] != authNoAuth {
		t.Fatalf("SOCKS5 method reply = %v, want [%d %d]", methodReply, socks5Version, authNoAuth)
	}

	host, portText, err := net.SplitHostPort(targetAddr)
	if err != nil {
		t.Fatalf("split SOCKS5 target %q: %v", targetAddr, err)
	}
	portValue, err := net.LookupPort(N.NetworkTCP, portText)
	if err != nil {
		t.Fatalf("parse SOCKS5 target port %q: %v", portText, err)
	}
	request := []byte{socks5Version, cmdConnect, 0x00}
	ip := net.ParseIP(host)
	switch {
	case ip != nil && ip.To4() != nil:
		request = append(request, atypIPv4)
		request = append(request, ip.To4()...)
	case ip != nil && ip.To16() != nil:
		request = append(request, atypIPv6)
		request = append(request, ip.To16()...)
	default:
		if len(host) > 255 {
			t.Fatalf("SOCKS5 target host is too long: %q", host)
		}
		request = append(request, atypDomain, byte(len(host)))
		request = append(request, host...)
	}
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, uint16(portValue))
	request = append(request, port...)
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write SOCKS5 CONNECT: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read SOCKS5 CONNECT reply: %v", err)
	}
	if reply[0] != socks5Version || reply[1] != repSuccess {
		t.Fatalf("SOCKS5 CONNECT reply = %v, want success", reply)
	}
	return conn
}
