package dispatch

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// PoolProvider returns the live proxy-pool outbound. It is consulted per
// request so the dispatcher stays correct across box reloads (which swap the
// underlying outbound instance).
type PoolProvider interface {
	PoolOutbound() (adapter.Outbound, bool)
}

// Logger is the minimal logging surface used by the dispatcher.
type Logger interface {
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
}

// Config controls the dispatcher entry server.
type Config struct {
	Listen          string        // host:port to listen on
	Username        string        // optional proxy auth username
	Password        string        // optional proxy auth password
	DefaultStrategy pool.Strategy // strategy when none specified (default entry)
	DialTimeout     time.Duration // per-connection dial timeout (default 30s)
	// BoundTokens, when non-empty, is a "+"-separated token string (same syntax
	// as the path-prefix entry, e.g. "stable+us+nosplit") applied as a fixed
	// overlay to every request on this listener — the "dedicated port = fixed
	// policy" entry style. It is merged at the lowest priority so headers/path
	// can still refine it.
	BoundTokens string
}

// Server is a smart proxy entry (HTTP/HTTPS CONNECT) that performs traffic
// splitting and strategy-based node selection in front of the pool.
type Server struct {
	cfg       Config
	provider  PoolProvider
	engine    *routerule.Engine
	logger    Logger
	directDLR *net.Dialer
	bound     directiveOverlay // parsed from cfg.BoundTokens (zero value = none)

	lifecycleMu     sync.Mutex
	mu              sync.RWMutex
	started         bool
	generation      uint64
	ln              net.Listener
	baseCtx         context.Context
	cancel          context.CancelFunc
	defaultStrategy pool.Strategy // hot-updatable default selection strategy

	connMu      sync.Mutex
	accepting   bool
	connections map[net.Conn]struct{}
	upstreams   map[net.Conn]struct{}
	handlers    sync.WaitGroup
}

// NewServer constructs a dispatcher server. engine may be nil (then every
// request is proxied). provider must be non-nil.
func NewServer(cfg Config, provider PoolProvider, engine *routerule.Engine, logger Logger) *Server {
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 30 * time.Second
	}
	if cfg.DefaultStrategy == "" {
		cfg.DefaultStrategy = pool.StrategyStable
	}
	s := &Server{
		cfg:             cfg,
		provider:        provider,
		engine:          engine,
		logger:          logger,
		directDLR:       &net.Dialer{Timeout: cfg.DialTimeout},
		defaultStrategy: cfg.DefaultStrategy,
		connections:     make(map[net.Conn]struct{}),
		upstreams:       make(map[net.Conn]struct{}),
	}
	if strings.TrimSpace(cfg.BoundTokens) != "" {
		if overlay, ok := parseTokens(cfg.BoundTokens); ok {
			s.bound = overlay
		}
	}
	return s
}

// SetEngine swaps the routing engine (live reload of rules).
func (s *Server) SetEngine(engine *routerule.Engine) {
	s.mu.Lock()
	s.engine = engine
	s.mu.Unlock()
}

func (s *Server) currentEngine() *routerule.Engine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.engine
}

// Listen returns the configured listen address.
func (s *Server) Listen() string { return s.cfg.Listen }

// DefaultStrategy returns the entry's default selection strategy.
func (s *Server) DefaultStrategy() pool.Strategy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defaultStrategy
}

// SetDefaultStrategy hot-swaps the default selection strategy used when a
// request does not specify one.
func (s *Server) SetDefaultStrategy(strategy pool.Strategy) {
	if strategy == "" {
		strategy = pool.StrategyStable
	}
	s.mu.Lock()
	s.defaultStrategy = strategy
	s.mu.Unlock()
}

// RuleCount returns the number of active (non-FINAL) routing rules, or 0 when
// no engine is bound.
func (s *Server) RuleCount() int {
	e := s.currentEngine()
	if e == nil {
		return 0
	}
	return e.RuleCount()
}

// FinalPolicy returns the engine's fallback policy as a string, or "" when no
// engine is bound.
func (s *Server) FinalPolicy() string {
	e := s.currentEngine()
	if e == nil {
		return ""
	}
	return string(e.Final())
}

// Start begins serving in a background goroutine. It returns once the listener
// is bound (or immediately on bind failure reported through the error log).
func (s *Server) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("dispatch listen on %s: %w", s.cfg.Listen, err)
	}
	// A single listener serves both protocols: each accepted connection is
	// sniffed (first byte 0x05 = SOCKS5, else HTTP) and parsed directly, so the
	// dispatcher fully owns the connection lifecycle (no shared http.Server).
	serveCtx, cancel := context.WithCancel(ctx)
	s.generation++
	generation := s.generation
	s.ln = ln
	s.baseCtx = serveCtx
	s.cancel = cancel
	s.started = true
	s.mu.Unlock()
	s.connMu.Lock()
	s.accepting = true
	s.connMu.Unlock()

	go func() {
		s.logf("🧭 smart dispatch entry listening on %s (http+socks5, default strategy: %s)", s.cfg.Listen, s.cfg.DefaultStrategy)
		s.acceptLoop(serveCtx, ln, generation)
	}()
	go func() {
		<-serveCtx.Done()
		s.stopGeneration(generation)
	}()
	return nil
}

// acceptLoop accepts connections and dispatches each to the HTTP or SOCKS5
// handler based on a one-byte protocol sniff.
func (s *Server) acceptLoop(ctx context.Context, ln net.Listener, generation uint64) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			// Transient accept errors: keep serving.
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			s.warnf("dispatch accept loop exiting: %v", err)
			return
		}
		if !s.trackConnection(conn, generation) {
			_ = conn.Close()
			continue
		}
		go func() {
			defer s.untrackConnection(conn)
			s.handleConn(conn)
		}()
	}
}

// handleConn peeks the first byte to route SOCKS5 (0x05) vs HTTP, then parses
// HTTP requests directly off the connection. We deliberately do NOT hand the
// connection to a shared http.Server: driving a shared http.Server one
// connection at a time (via a per-conn listener) proved fragile — after the
// first hijacked CONNECT the shared server's state made subsequent connections
// fail. Parsing here keeps full control of the connection lifecycle and
// supports HTTP keep-alive for plain requests.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	defer func() {
		if r := recover(); r != nil {
			s.warnf("dispatch connection handler panic: %v", r)
		}
	}()
	br := bufio.NewReader(conn)

	// Bound the initial sniff so idle/half-open probe connections cannot pin a
	// handler goroutine forever; cleared once we have the first byte.
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	if first[0] == socks5Version {
		s.handleSOCKS5(&peekedConn{Conn: conn, r: br})
		return
	}
	s.serveHTTP(conn, br)
}

// serveHTTP parses HTTP proxy requests off a single connection. CONNECT
// tunnels take over the connection and return when the tunnel closes. Plain
// requests are proxied and, unless the client/target asked to close, the loop
// continues to honor keep-alive.
func (s *Server) serveHTTP(conn net.Conn, br *bufio.Reader) {
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		if !s.checkAuthConn(conn, req) {
			return
		}
		if req.Method == http.MethodConnect {
			// CONNECT consumes the connection for the tunnel's lifetime.
			s.handleConnectConn(conn, req)
			return
		}
		keepAlive := s.handleHTTPConn(conn, br, req)
		if !keepAlive {
			return
		}
	}
}

// Stop gracefully shuts the server down.
func (s *Server) Stop() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.stopLocked()
}

func (s *Server) stopGeneration(generation uint64) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.RLock()
	current := s.started && s.generation == generation
	s.mu.RUnlock()
	if !current {
		return
	}
	s.stopLocked()
}

// stopLocked stops one server generation. The caller must hold lifecycleMu.
func (s *Server) stopLocked() {

	s.mu.Lock()
	ln := s.ln
	cancel := s.cancel
	generation := s.generation
	s.ln = nil
	s.cancel = nil
	s.started = false
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	s.connMu.Lock()
	s.accepting = false
	connections := make([]net.Conn, 0, len(s.connections))
	for conn := range s.connections {
		connections = append(connections, conn)
	}
	upstreams := make([]net.Conn, 0, len(s.upstreams))
	for conn := range s.upstreams {
		upstreams = append(upstreams, conn)
	}
	s.connMu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
	for _, conn := range upstreams {
		_ = conn.Close()
	}
	s.handlers.Wait()
	s.mu.Lock()
	if !s.started && s.generation == generation {
		s.baseCtx = nil
	}
	s.mu.Unlock()
}

func (s *Server) trackConnection(conn net.Conn, generation uint64) bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.mu.RLock()
	currentGeneration := s.generation
	s.mu.RUnlock()
	if !s.accepting || currentGeneration != generation {
		return false
	}
	s.connections[conn] = struct{}{}
	s.handlers.Add(1)
	return true
}

func (s *Server) untrackConnection(conn net.Conn) {
	s.connMu.Lock()
	delete(s.connections, conn)
	s.connMu.Unlock()
	s.handlers.Done()
}

func (s *Server) trackUpstream(conn net.Conn) (net.Conn, error) {
	if conn == nil {
		return nil, fmt.Errorf("dispatch dial returned a nil connection")
	}
	tracked := &trackedUpstreamConn{Conn: conn, server: s}
	s.connMu.Lock()
	if !s.accepting {
		s.connMu.Unlock()
		_ = conn.Close()
		return nil, fmt.Errorf("dispatch server is stopping")
	}
	if s.upstreams == nil {
		s.upstreams = make(map[net.Conn]struct{})
	}
	s.upstreams[tracked] = struct{}{}
	s.connMu.Unlock()
	return tracked, nil
}

func (s *Server) untrackUpstream(conn net.Conn) {
	s.connMu.Lock()
	delete(s.upstreams, conn)
	s.connMu.Unlock()
}

func (s *Server) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Infof(format, args...)
	}
}

func (s *Server) warnf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Warnf(format, args...)
	}
}

// checkAuthConn validates proxy auth for a connection-based request, writing a
// 407 challenge directly to the connection on failure. Returns true when the
// request may proceed.
func (s *Server) checkAuthConn(conn net.Conn, req *http.Request) bool {
	if s.cfg.Username == "" {
		return true
	}
	user, pass, ok := proxyBasicAuth(req)
	if !ok || user != s.cfg.Username || pass != s.cfg.Password {
		_, _ = conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\n" +
			"Proxy-Authenticate: Basic realm=\"EasyProxy\"\r\n" +
			"Content-Length: 0\r\n\r\n"))
		return false
	}
	return true
}

// resolveDirective is the protocol-agnostic core shared by the HTTP and SOCKS5
// entries: it layers the port-bound overlay (lowest priority) under the
// per-request overlay, resolves defaults, and computes the routing policy for
// the destination host. sessionFallback supplies the session stickiness key
// (typically the client source IP) when the session strategy has no explicit
// key.
func (s *Server) resolveDirective(reqOverlay directiveOverlay, host, sessionFallback string) (resolved, routerule.Policy) {
	overlay := s.bound.merge(reqOverlay)
	res := overlay.resolve(s.DefaultStrategy(), sessionFallback)
	policy := policyForSplit(res.split, s.currentEngine(), host)
	return res, policy
}

// handleConnectConn tunnels an HTTPS CONNECT request directly on the connection.
// The CONNECT authority may carry a token prefix ("stable+us/example.com:443").
func (s *Server) handleConnectConn(conn net.Conn, req *http.Request) {
	// Use the raw request-target (RequestURI) rather than req.Host: when a token
	// prefix is present ("nosplit/host:port"), http.ReadRequest parses req.Host
	// as just "nosplit" and moves the rest into the URL path, losing the target.
	// The RequestURI preserves the full "token/host:port" form we need to split.
	rawTarget := req.RequestURI
	if rawTarget == "" {
		rawTarget = req.Host
	}
	overlay, authority := splitConnectAuthority(rawTarget)
	host, port := splitHostPort(authority, 443)
	if host == "" {
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"))
		return
	}

	res, policy := s.resolveDirective(connectRequestOverlay(req, overlay), host, clientIP(conn.RemoteAddr().String()))

	target, err := s.dial(s.baseContext(), N.NetworkTCP, host, port, res, policy)
	if err != nil {
		s.warnf("dispatch CONNECT %s:%d [%s] dial failed: %v", host, port, policy, err)
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
		return
	}
	defer target.Close()

	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	relay(conn, target)
}

// connectRequestOverlay merges CONNECT path tokens with HTTP headers. Headers
// are the highest-priority per-request source, matching the documented order:
// port-bound < path/username < HTTP headers.
func connectRequestOverlay(req *http.Request, pathOverlay directiveOverlay) directiveOverlay {
	if req == nil {
		return pathOverlay
	}
	return pathOverlay.merge(parseHeaders(req.Header))
}

// handleHTTPConn proxies a plain (non-CONNECT) HTTP request on the connection
// and reports whether the connection may be reused for another request
// (keep-alive). A leading path token segment ("/stable+us/...") is recognized
// as a directive overlay.
func (s *Server) handleHTTPConn(conn net.Conn, br *bufio.Reader, req *http.Request) bool {
	overlay := stripPathOverlay(req)

	host := req.Host
	if host == "" && req.URL != nil {
		host = req.URL.Host
	}
	hostOnly, port := splitHostPort(host, 80)
	if hostOnly == "" {
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"))
		return false
	}

	res, policy := s.resolveDirective(overlay.merge(parseHeaders(req.Header)), hostOnly, clientIP(conn.RemoteAddr().String()))

	target, err := s.dial(s.baseContext(), N.NetworkTCP, hostOnly, port, res, policy)
	if err != nil {
		s.warnf("dispatch HTTP %s:%d [%s] dial failed: %v", hostOnly, port, policy, err)
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
		return false
	}
	defer target.Close()

	// Preserve keep-alive intent: honor the client's request semantics.
	keepAlive := !req.Close && req.ProtoAtLeast(1, 1)

	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authorization")
	// Rewrite to origin-form: strip scheme/host so the target sees a normal
	// request line. req.Write handles this when RequestURI is cleared.
	req.RequestURI = ""
	if req.URL != nil {
		req.URL.Scheme = ""
		req.URL.Host = ""
	}
	if err := req.Write(target); err != nil {
		return false
	}

	// Stream the response back. Copy the response through so we can detect
	// connection-close semantics from the origin.
	resp, err := http.ReadResponse(bufio.NewReader(target), req)
	if err != nil {
		return false
	}
	if err := resp.Write(conn); err != nil {
		resp.Body.Close()
		return false
	}
	resp.Body.Close()
	if resp.Close {
		keepAlive = false
	}
	return keepAlive
}

// dial routes the connection per policy: DIRECT uses a local dialer; PROXY
// hands the destination to the pool outbound with the selection directive
// injected into the context so the pool honors stable/session/filter intent.
func (s *Server) dial(ctx context.Context, network, host string, port uint16, res resolved, policy routerule.Policy) (net.Conn, error) {
	if policy == routerule.PolicyDirect {
		conn, err := s.directDLR.DialContext(ctx, network, net.JoinHostPort(host, strconv.Itoa(int(port))))
		if err != nil {
			return nil, fmt.Errorf("direct: %w", err)
		}
		return s.trackUpstream(conn)
	}

	out, ok := s.provider.PoolOutbound()
	if !ok || out == nil {
		return nil, fmt.Errorf("proxy pool not available")
	}
	dirCtx := pool.WithDirective(ctx, &res.directive)
	dst := M.ParseSocksaddrHostPort(host, port)
	conn, err := out.DialContext(dirCtx, network, dst)
	if err != nil {
		return nil, fmt.Errorf("proxy: %w", err)
	}
	return s.trackUpstream(conn)
}

// relay performs a bidirectional copy between two connections and returns when
// either direction closes.
func relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		closeWrite(a)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		closeWrite(b)
	}()
	wg.Wait()
}

type closeWriter interface{ CloseWrite() error }

func closeWrite(c net.Conn) {
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}

type trackedUpstreamConn struct {
	net.Conn
	server *Server
	once   sync.Once
}

func (c *trackedUpstreamConn) Close() error {
	var closeErr error
	c.once.Do(func() {
		c.server.untrackUpstream(c)
		closeErr = c.Conn.Close()
	})
	return closeErr
}

func (c *trackedUpstreamConn) CloseWrite() error {
	if writer, ok := c.Conn.(closeWriter); ok {
		return writer.CloseWrite()
	}
	return nil
}

// peekedConn wraps a net.Conn whose stream has been buffered for protocol
// sniffing, so the first byte peeked during dispatch is still visible to the
// downstream parser (http.Server or the SOCKS5 handler).
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// splitConnectAuthority separates an optional "token+token/" prefix from the
// CONNECT authority. "stable+us/example.com:443" → (overlay, "example.com:443").
func splitConnectAuthority(authority string) (directiveOverlay, string) {
	authority = strings.TrimSpace(authority)
	idx := strings.IndexByte(authority, '/')
	if idx <= 0 {
		return directiveOverlay{}, authority
	}
	prefix := authority[:idx]
	rest := authority[idx+1:]
	if overlay, ok := parseTokens(prefix); ok {
		return overlay, rest
	}
	return directiveOverlay{}, authority
}

// stripPathOverlay extracts a leading "/token+token/" segment from the request
// URL path, rewriting the path in place, and returns the parsed overlay.
func stripPathOverlay(req *http.Request) directiveOverlay {
	if req.URL == nil {
		return directiveOverlay{}
	}
	path := req.URL.Path
	if len(path) < 2 || path[0] != '/' {
		return directiveOverlay{}
	}
	rest := path[1:]
	slash := strings.IndexByte(rest, '/')
	var seg string
	if slash < 0 {
		seg = rest
	} else {
		seg = rest[:slash]
	}
	overlay, ok := parseTokens(seg)
	if !ok {
		return directiveOverlay{}
	}
	// Rewrite path to drop the directive segment.
	if slash < 0 {
		req.URL.Path = "/"
	} else {
		req.URL.Path = rest[slash:]
	}
	return overlay
}

// splitHostPort parses "host:port" or "host" (using defaultPort). It tolerates
// IPv6 brackets.
func splitHostPort(hostport string, defaultPort uint16) (string, uint16) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return "", 0
	}
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		port := defaultPort
		if pv, perr := strconv.Atoi(p); perr == nil && pv > 0 && pv <= 65535 {
			port = uint16(pv)
		}
		return h, port
	}
	// No port present; strip IPv6 brackets if any.
	h := hostport
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	return h, defaultPort
}

// clientIP returns the bare IP from a RemoteAddr "ip:port" string, used as the
// session stickiness fallback key.
func clientIP(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return h
	}
	return remoteAddr
}

// proxyBasicAuth reads Proxy-Authorization basic credentials (net/http only
// parses the non-proxy Authorization header natively).
func proxyBasicAuth(req *http.Request) (string, string, bool) {
	const prefix = "Basic "
	h := req.Header.Get("Proxy-Authorization")
	if h == "" {
		return "", "", false
	}
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", "", false
	}
	// Reuse the standard library decoder via a synthetic request header.
	r := &http.Request{Header: http.Header{"Authorization": []string{h}}}
	return r.BasicAuth()
}
