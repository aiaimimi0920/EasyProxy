package gatewayroute

import (
	"context"
	"net"
	"strings"
	"sync/atomic"

	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"

	"github.com/sagernet/sing-box/adapter"
	sboutbound "github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

const (
	Type         = "easyproxy-gateway-route"
	Tag          = "easyproxy-gateway-route"
	tcpProfileID = Tag + "/tcp"
	udpProfileID = Tag + "/udp"
)

var nativeUDPProtocolFamilies = []string{"hysteria2", "tuic"}

// Options controls native TUN routing through EasyProxy's direct and pool
// outbounds. An empty PoolTag represents a valid direct-only runtime.
type Options struct {
	Rules                  []string
	FinalPolicy            string
	NoAvailableProxyPolicy string
	DefaultStrategy        pool.Strategy
	PoolTag                string
	DirectTag              string
}

// Stats is a point-in-time route decision snapshot.
type Stats struct {
	Direct         uint64
	Proxy          uint64
	NoNodeFallback uint64
	TCP            uint64
	UDP            uint64
	IPv4           uint64
	IPv6           uint64
}

type routeOutbound struct {
	sboutbound.Adapter
	manager adapter.OutboundManager
	options Options
	engine  *routerule.Engine
	direct  adapter.Outbound
	pool    adapter.Outbound

	directCount         atomic.Uint64
	proxyCount          atomic.Uint64
	noNodeFallbackCount atomic.Uint64
	tcpCount            atomic.Uint64
	udpCount            atomic.Uint64
	ipv4Count           atomic.Uint64
	ipv6Count           atomic.Uint64
}

var _ adapter.Lifecycle = (*routeOutbound)(nil)

// Register wires the gateway route outbound into sing-box.
func Register(registry *sboutbound.Registry) {
	sboutbound.Register[Options](registry, Type, newRoute)
}

func newRoute(ctx context.Context, _ adapter.Router, _ log.ContextLogger, tag string, options Options) (adapter.Outbound, error) {
	manager := service.FromContext[adapter.OutboundManager](ctx)
	if manager == nil {
		return nil, E.New("missing outbound manager in context")
	}
	if strings.TrimSpace(options.DirectTag) == "" {
		return nil, E.New("gateway route requires a direct outbound tag")
	}
	dependencies := []string{options.DirectTag}
	if strings.TrimSpace(options.PoolTag) != "" {
		dependencies = append(dependencies, options.PoolTag)
	}
	return &routeOutbound{
		Adapter: sboutbound.NewAdapter(Type, tag, []string{N.NetworkTCP, N.NetworkUDP}, dependencies),
		manager: manager,
		options: options,
		engine:  routerule.New(options.Rules, routerule.NormalizePolicy(options.FinalPolicy), nil),
	}, nil
}

func (r *routeOutbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	direct, ok := r.manager.Outbound(r.options.DirectTag)
	if !ok {
		return E.New("gateway direct outbound not found: ", r.options.DirectTag)
	}
	r.direct = direct
	if strings.TrimSpace(r.options.PoolTag) != "" {
		proxyPool, loaded := r.manager.Outbound(r.options.PoolTag)
		if !loaded {
			return E.New("gateway pool outbound not found: ", r.options.PoolTag)
		}
		r.pool = proxyPool
	}
	return nil
}

func (*routeOutbound) Close() error { return nil }

func (r *routeOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	r.recordNetwork(network, destination)
	if r.policy(ctx, network, destination) == routerule.PolicyDirect {
		r.directCount.Add(1)
		return r.direct.DialContext(ctx, network, destination)
	}
	if r.pool != nil {
		directive := &pool.SelectionDirective{
			ProfileID: tcpProfileID,
			Strategy:  pool.NormalizeStrategy(string(r.options.DefaultStrategy)),
		}
		conn, err := r.pool.DialContext(pool.WithDirective(ctx, directive), network, destination)
		if err == nil {
			r.proxyCount.Add(1)
			return conn, nil
		}
		if !r.failOpen() {
			return nil, err
		}
	}
	if !r.failOpen() {
		return nil, E.New("no healthy proxy available")
	}
	r.noNodeFallbackCount.Add(1)
	r.directCount.Add(1)
	return r.direct.DialContext(ctx, network, destination)
}

func (r *routeOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	r.recordNetwork(N.NetworkUDP, destination)
	if r.policy(ctx, N.NetworkUDP, destination) == routerule.PolicyDirect {
		r.directCount.Add(1)
		return r.direct.ListenPacket(ctx, destination)
	}
	if r.pool != nil {
		directive := &pool.SelectionDirective{
			ProfileID:                 udpProfileID,
			Strategy:                  pool.StrategyAuto,
			PreferredProtocolFamilies: nativeUDPProtocolFamilies,
			RequireAvailablePreferred: true,
		}
		packetConn, err := r.pool.ListenPacket(pool.WithDirective(ctx, directive), destination)
		if err == nil {
			r.proxyCount.Add(1)
			return packetConn, nil
		}
		if !r.failOpen() {
			return nil, err
		}
	}
	if !r.failOpen() {
		return nil, E.New("no healthy UDP proxy available")
	}
	r.noNodeFallbackCount.Add(1)
	r.directCount.Add(1)
	return r.direct.ListenPacket(ctx, destination)
}

func (r *routeOutbound) policy(ctx context.Context, network string, destination M.Socksaddr) routerule.Policy {
	host := destination.AddrString()
	if metadata := adapter.ContextFrom(ctx); metadata != nil && strings.TrimSpace(metadata.Domain) != "" {
		host = metadata.Domain
	}
	return r.engine.MatchRequest(routerule.Request{Host: host, Port: destination.Port, Network: network})
}

func (r *routeOutbound) failOpen() bool {
	return routerule.NormalizePolicy(r.options.NoAvailableProxyPolicy) == routerule.PolicyDirect
}

func (r *routeOutbound) recordNetwork(network string, destination M.Socksaddr) {
	if network == N.NetworkUDP {
		r.udpCount.Add(1)
	} else {
		r.tcpCount.Add(1)
	}
	if destination.IsIPv6() {
		r.ipv6Count.Add(1)
	} else {
		r.ipv4Count.Add(1)
	}
}

func (r *routeOutbound) Snapshot() Stats {
	return Stats{
		Direct:         r.directCount.Load(),
		Proxy:          r.proxyCount.Load(),
		NoNodeFallback: r.noNodeFallbackCount.Load(),
		TCP:            r.tcpCount.Load(),
		UDP:            r.udpCount.Load(),
		IPv4:           r.ipv4Count.Load(),
		IPv6:           r.ipv6Count.Load(),
	}
}
