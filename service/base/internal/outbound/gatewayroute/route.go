package gatewayroute

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"

	"easy_proxies/internal/config"
	"easy_proxies/internal/geoip"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/outbound/profileselect"
	"easy_proxies/internal/routerule"

	"github.com/sagernet/sing-box/adapter"
	sboutbound "github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common/bufio"
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
	ClaudeRoute            config.ClaudeRoute
	DeviceProfiles         bool
	SelectionTiers         []config.SelectionTier
	Rules                  []string
	UseDefaultRules        bool
	RuleProviders          []config.RuleProvider
	GeoIP                  config.GeoIPConfig
	FinalPolicy            string
	NoAvailableProxyPolicy string
	DefaultStrategy        pool.Strategy
	PoolTag                string
	DirectTag              string
}

// Stats is a point-in-time route decision snapshot.
type Stats struct {
	Active         int64
	Direct         uint64
	Proxy          uint64
	NoNodeFallback uint64
	TCP            uint64
	UDP            uint64
	IPv4           uint64
	IPv6           uint64
}

type routeOutbound struct {
	admission *profileselect.Admission
	sboutbound.Adapter
	sharedSelection pool.SharedSelectionSource
	profiles        profileselect.Resolver
	manager         adapter.OutboundManager
	options         Options
	engine          *routerule.Engine
	ctx             context.Context
	geo             *geoip.Lookup
	provider        *routerule.ProviderManager
	direct          adapter.Outbound
	pool            adapter.Outbound
	resolveUDP      func(context.Context, M.Socksaddr) (M.Socksaddr, netip.Addr, error)

	activeCount atomic.Int64
	*Totals
}

type trackedRouteConn struct {
	net.Conn
	active *atomic.Int64
	closed atomic.Bool
}

func (c *trackedRouteConn) Close() error {
	if c.closed.CompareAndSwap(false, true) && c.active != nil {
		c.active.Add(-1)
	}
	return c.Conn.Close()
}

func (c *trackedRouteConn) Upstream() any           { return c.Conn }
func (c *trackedRouteConn) ReaderReplaceable() bool { return true }
func (c *trackedRouteConn) WriterReplaceable() bool { return true }

func (r *routeOutbound) trackConn(network string, conn net.Conn) net.Conn {
	if conn == nil || network == N.NetworkUDP {
		return conn
	}
	r.activeCount.Add(1)
	tracked := &trackedRouteConn{Conn: conn, active: &r.activeCount}
	if half, ok := conn.(N.WriteCloser); ok {
		return struct {
			*trackedRouteConn
			N.WriteCloser
		}{tracked, half}
	}
	return tracked
}

var _ adapter.Lifecycle = (*routeOutbound)(nil)
var _ dialer.PacketDialerWithDestination = (*routeOutbound)(nil)

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
	route := &routeOutbound{
		Totals:          totalsFrom(ctx),
		Adapter:         sboutbound.NewAdapter(Type, tag, []string{N.NetworkTCP, N.NetworkUDP}, dependencies),
		manager:         manager,
		sharedSelection: pool.SharedSelectionFrom(ctx),
		options:         options,
		ctx:             ctx,
	}
	route.engine = routerule.New(nil, routerule.NormalizePolicy(options.FinalPolicy), nil)
	route.applyRules(nil)
	route.admission = profileselect.AdmissionFrom(ctx)
	if options.DeviceProfiles {
		route.profiles = profileselect.FromContext(ctx)
	}
	dnsRouter := service.FromContext[adapter.DNSRouter](ctx)
	dnsTransportManager := service.FromContext[adapter.DNSTransportManager](ctx)
	if dnsRouter != nil && dnsTransportManager != nil && dnsTransportManager.Default() != nil {
		dnsTransport := dnsTransportManager.Default()
		route.resolveUDP = func(ctx context.Context, destination M.Socksaddr) (M.Socksaddr, netip.Addr, error) {
			addresses, err := dnsRouter.Lookup(ctx, destination.Fqdn, adapter.DNSQueryOptions{
				Transport: dnsTransport,
				Strategy:  udpLookupStrategy(ctx),
			})
			if err != nil {
				return destination, netip.Addr{}, err
			}
			if len(addresses) == 0 {
				return destination, netip.Addr{}, E.New("empty UDP destination lookup for ", destination.Fqdn)
			}
			address := addresses[0]
			return M.SocksaddrFrom(address, destination.Port), address, nil
		}
	}
	return route, nil
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
	return r.startRules()
}

func (r *routeOutbound) Close() error {
	if r.provider != nil {
		r.provider.Stop()
	}
	if r.geo != nil {
		r.engine.SetGeoLookup(nil)
		return r.geo.Close()
	}
	return nil
}

func (r *routeOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if err := r.admission.Check(); err != nil {
		return nil, err
	}
	r.recordNetwork(network, destination)
	policy, directive, err := r.selection(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	if policy == routerule.PolicyDirect {
		conn, err := r.direct.DialContext(ctx, network, destination)
		if err != nil {
			return nil, err
		}
		r.directCount.Add(1)
		return r.trackConn(network, conn), nil
	}
	if r.pool != nil {
		conn, err := r.pool.DialContext(pool.WithDirective(ctx, directive), network, proxyDestination(ctx, destination))
		if err == nil {
			r.proxyCount.Add(1)
			return r.trackConn(network, conn), nil
		}
		if !r.mayFallback(directive) {
			return nil, err
		}
	}
	if !r.mayFallback(directive) {
		return nil, E.New("no healthy proxy available")
	}
	conn, err := r.direct.DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	r.noNodeFallbackCount.Add(1)
	r.directCount.Add(1)
	return r.trackConn(network, conn), nil
}

func (r *routeOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	packetConn, destinationAddress, err := r.ListenPacketWithDestination(ctx, destination)
	if err != nil {
		return nil, err
	}
	if destinationAddress.IsValid() {
		resolvedDestination := M.SocksaddrFrom(destinationAddress, destination.Port)
		if destination != resolvedDestination {
			return bufio.NewNATPacketConn(bufio.NewPacketConn(packetConn), resolvedDestination, destination), nil
		}
	}
	return packetConn, nil
}

func udpLookupStrategy(ctx context.Context) C.DomainStrategy {
	if metadata := adapter.ContextFrom(ctx); metadata != nil {
		switch metadata.IPVersion {
		case 4:
			return C.DomainStrategyIPv4Only
		case 6:
			return C.DomainStrategyIPv6Only
		}
	}
	return C.DomainStrategyAsIS
}

func (r *routeOutbound) ListenPacketWithDestination(ctx context.Context, destination M.Socksaddr) (net.PacketConn, netip.Addr, error) {
	if err := r.admission.Check(); err != nil {
		return nil, netip.Addr{}, err
	}
	r.recordNetwork(N.NetworkUDP, destination)
	policy, directive, err := r.selection(ctx, N.NetworkUDP, destination)
	if err != nil {
		return nil, netip.Addr{}, err
	}
	packetDestination := destination
	if policy == routerule.PolicyProxy {
		packetDestination = proxyDestination(ctx, destination)
	}
	resolvedDestination, destinationAddress := r.resolvePacketDestination(ctx, packetDestination)
	if policy == routerule.PolicyDirect {
		packetConn, err := r.direct.ListenPacket(ctx, resolvedDestination)
		if err == nil {
			r.directCount.Add(1)
		}
		return packetConn, destinationAddress, err
	}
	if r.pool != nil {
		packetConn, err := r.pool.ListenPacket(pool.WithDirective(ctx, directive), resolvedDestination)
		if err == nil {
			r.proxyCount.Add(1)
			return packetConn, destinationAddress, nil
		}
		if !r.mayFallback(directive) {
			return nil, netip.Addr{}, err
		}
	}
	if !r.mayFallback(directive) {
		return nil, netip.Addr{}, E.New("no healthy UDP proxy available")
	}
	packetConn, err := r.direct.ListenPacket(ctx, resolvedDestination)
	if err == nil {
		r.noNodeFallbackCount.Add(1)
		r.directCount.Add(1)
	}
	return packetConn, destinationAddress, err
}

func (r *routeOutbound) resolvePacketDestination(ctx context.Context, destination M.Socksaddr) (M.Socksaddr, netip.Addr) {
	if destination.IsIP() {
		return destination, destination.Addr
	}
	if !destination.IsFqdn() || r.resolveUDP == nil {
		return destination, netip.Addr{}
	}
	resolvedDestination, address, err := r.resolveUDP(ctx, destination)
	if err != nil || !address.IsValid() {
		return destination, netip.Addr{}
	}
	return resolvedDestination, address
}

func (r *routeOutbound) policy(ctx context.Context, network string, destination M.Socksaddr) routerule.Policy {
	host := destination.AddrString()
	if metadata := adapter.ContextFrom(ctx); metadata != nil && strings.TrimSpace(metadata.Domain) != "" {
		host = metadata.Domain
	}
	return r.engine.MatchRequest(routerule.Request{Host: host, Port: destination.Port, Network: network, Address: destination.Addr})
}

// Preserve the pre-existing sniffed-domain proxy behavior without discarding
// the original IP before routing (or re-resolving a DIRECT destination).
func proxyDestination(ctx context.Context, destination M.Socksaddr) M.Socksaddr {
	if metadata := adapter.ContextFrom(ctx); metadata != nil && M.IsDomainName(metadata.Domain) {
		return M.ParseSocksaddrHostPort(metadata.Domain, destination.Port)
	}
	return destination
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
	s := r.Totals.Snapshot()
	s.Active = r.activeCount.Load()
	return s
}

// Exact pins must never inherit a shared fail-open setting.
func (r *routeOutbound) mayFallback(directive *pool.SelectionDirective) bool {
	return r.failOpen() && (directive == nil || directive.RequiredTag == "")
}
