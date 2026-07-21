package dispatch

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// TransparentRouterConfig controls the provider-neutral TCP data plane.
type TransparentRouterConfig struct {
	DialTimeout            time.Duration
	NoAvailableProxyPolicy routerule.Policy
}

// TransparentRouter consumes a connection accepted by a Linux TPROXY
// listener. The accepted socket's local address is the original destination;
// its remote address remains the client identity used for session routing.
type TransparentRouter struct {
	cfg               TransparentRouterConfig
	provider          PoolProvider
	engine            *routerule.Engine
	logger            Logger
	direct            *net.Dialer
	directConnections atomic.Uint64
	proxyConnections  atomic.Uint64
	directFallbacks   atomic.Uint64
}

type TransparentStats struct {
	DirectConnections uint64
	ProxyConnections  uint64
	DirectFallbacks   uint64
}

func NewTransparentRouter(cfg TransparentRouterConfig, provider PoolProvider, engine *routerule.Engine, logger Logger) *TransparentRouter {
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 30 * time.Second
	}
	if cfg.NoAvailableProxyPolicy == "" {
		cfg.NoAvailableProxyPolicy = routerule.PolicyDirect
	}
	return &TransparentRouter{
		cfg:      cfg,
		provider: provider,
		engine:   engine,
		logger:   logger,
		direct:   &net.Dialer{Timeout: cfg.DialTimeout},
	}
}

// ServeConn routes one transparent TCP connection and relays bytes until one
// side closes. The caller owns listener lifecycle; this method owns conn.
func (r *TransparentRouter) ServeConn(ctx context.Context, conn net.Conn) {
	if conn == nil {
		return
	}
	defer conn.Close()

	host, port, err := transparentDestination(conn.LocalAddr())
	if err != nil {
		r.warnf("transparent destination rejected: %v", err)
		return
	}
	policy := routerule.PolicyProxy
	if r.engine != nil {
		policy = r.engine.Match(host)
	}

	upstream, dialErr := r.dial(ctx, host, port, policy)
	usedFallback := false
	if dialErr != nil && policy == routerule.PolicyProxy && r.cfg.NoAvailableProxyPolicy == routerule.PolicyDirect {
		r.warnf("transparent %s:%d proxy unavailable; falling back to DIRECT: %v", host, port, dialErr)
		upstream, dialErr = r.dialDirect(ctx, host, port)
		usedFallback = dialErr == nil
	}
	if dialErr != nil {
		r.warnf("transparent %s:%d [%s] dial failed: %v", host, port, policy, dialErr)
		return
	}
	defer upstream.Close()
	if usedFallback {
		r.directConnections.Add(1)
		r.directFallbacks.Add(1)
	} else if policy == routerule.PolicyDirect {
		r.directConnections.Add(1)
	} else {
		r.proxyConnections.Add(1)
	}
	relay(conn, upstream)
}

func (r *TransparentRouter) Stats() TransparentStats {
	if r == nil {
		return TransparentStats{}
	}
	return TransparentStats{
		DirectConnections: r.directConnections.Load(),
		ProxyConnections:  r.proxyConnections.Load(),
		DirectFallbacks:   r.directFallbacks.Load(),
	}
}

func (r *TransparentRouter) dial(ctx context.Context, host string, port uint16, policy routerule.Policy) (net.Conn, error) {
	if policy == routerule.PolicyDirect {
		return r.dialDirect(ctx, host, port)
	}
	if r.provider == nil {
		return nil, fmt.Errorf("proxy pool not available")
	}
	out, ok := r.provider.PoolOutbound()
	if !ok || out == nil {
		return nil, fmt.Errorf("proxy pool not available")
	}
	dst := M.ParseSocksaddrHostPort(host, port)
	conn, err := out.DialContext(pool.WithDirective(ctx, nil), N.NetworkTCP, dst)
	if err != nil {
		return nil, fmt.Errorf("proxy: %w", err)
	}
	return conn, nil
}

func (r *TransparentRouter) dialDirect(ctx context.Context, host string, port uint16) (net.Conn, error) {
	if r == nil || r.direct == nil {
		return nil, fmt.Errorf("transparent direct dialer unavailable")
	}
	conn, err := r.direct.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return nil, fmt.Errorf("direct: %w", err)
	}
	return conn, nil
}

func transparentDestination(addr net.Addr) (string, uint16, error) {
	if addr == nil {
		return "", 0, fmt.Errorf("missing original destination")
	}
	var tcpAddr *net.TCPAddr
	switch value := addr.(type) {
	case *net.TCPAddr:
		tcpAddr = value
	default:
		resolved, err := net.ResolveTCPAddr("tcp", addr.String())
		if err != nil {
			return "", 0, fmt.Errorf("invalid original destination %q: %w", addr.String(), err)
		}
		tcpAddr = resolved
	}
	if tcpAddr == nil || tcpAddr.IP == nil || tcpAddr.Port <= 0 || tcpAddr.Port > 65535 {
		return "", 0, fmt.Errorf("invalid original destination %v", addr)
	}
	return tcpAddr.IP.String(), uint16(tcpAddr.Port), nil
}

func (r *TransparentRouter) warnf(format string, args ...any) {
	if r != nil && r.logger != nil {
		r.logger.Warnf(format, args...)
	}
}
