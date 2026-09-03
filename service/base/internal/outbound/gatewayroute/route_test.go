package gatewayroute

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"

	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"

	"github.com/sagernet/sing-box/adapter"
	sboutbound "github.com/sagernet/sing-box/adapter/outbound"
	C "github.com/sagernet/sing-box/constant"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type recordingOutbound struct {
	tag                   string
	dials                 atomic.Int32
	packetDials           atomic.Int32
	dialErr               error
	lastDirective         *pool.SelectionDirective
	lastPacketDestination M.Socksaddr
}

func (*recordingOutbound) Type() string           { return "test" }
func (o *recordingOutbound) Tag() string          { return o.tag }
func (*recordingOutbound) Network() []string      { return []string{N.NetworkTCP, N.NetworkUDP} }
func (*recordingOutbound) Dependencies() []string { return nil }

func (o *recordingOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	o.dials.Add(1)
	o.lastDirective = pool.DirectiveFrom(ctx)
	if o.dialErr != nil {
		return nil, o.dialErr
	}
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func (o *recordingOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	o.packetDials.Add(1)
	o.lastDirective = pool.DirectiveFrom(ctx)
	o.lastPacketDestination = destination
	if o.dialErr != nil {
		return nil, o.dialErr
	}
	return net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
}

func newTestRoute(rules []string, final, fallback string, direct, proxy adapter.Outbound) *routeOutbound {
	return &routeOutbound{
		Adapter: sboutbound.NewAdapter(Type, Tag, []string{N.NetworkTCP, N.NetworkUDP}, nil),
		options: Options{
			FinalPolicy:            final,
			NoAvailableProxyPolicy: fallback,
			DefaultStrategy:        pool.StrategyStable,
		},
		engine: routerule.New(rules, routerule.NormalizePolicy(final), nil),
		direct: direct,
		pool:   proxy,
	}
}

func TestRouteDialUsesRequestDomainAndDestinationPort(t *testing.T) {
	direct := &recordingOutbound{tag: "direct"}
	proxy := &recordingOutbound{tag: "pool"}
	route := newTestRoute([]string{
		"DOMAIN-SUFFIX,example.cn,DIRECT",
		"DST-PORT,53,DIRECT",
	}, "PROXY", "DIRECT", direct, proxy)

	metadata := &adapter.InboundContext{Domain: "api.example.cn"}
	ctx := adapter.WithContext(context.Background(), metadata)
	conn, err := route.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr("203.0.113.8:443"))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if direct.dials.Load() != 1 || proxy.dials.Load() != 0 {
		t.Fatalf("domain route direct/proxy dials = %d/%d, want 1/0", direct.dials.Load(), proxy.dials.Load())
	}

	packetConn, err := route.ListenPacket(context.Background(), M.ParseSocksaddr("[2001:4860:4860::8888]:53"))
	if err != nil {
		t.Fatal(err)
	}
	_ = packetConn.Close()
	stats := route.Snapshot()
	if stats.Direct != 2 || stats.UDP != 1 || stats.IPv6 != 1 {
		t.Fatalf("unexpected route stats: %+v", stats)
	}
}

func TestRouteFallsBackDirectWhenProxyUnavailable(t *testing.T) {
	direct := &recordingOutbound{tag: "direct"}
	route := newTestRoute(nil, "PROXY", "DIRECT", direct, nil)

	conn, err := route.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("8.8.8.8:443"))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	stats := route.Snapshot()
	if direct.dials.Load() != 1 || stats.NoNodeFallback != 1 || stats.Direct != 1 {
		t.Fatalf("unexpected direct fallback state: dials=%d stats=%+v", direct.dials.Load(), stats)
	}
}

func TestRouteUDPUsesIndependentNativeProtocolPreference(t *testing.T) {
	direct := &recordingOutbound{tag: "direct"}
	proxy := &recordingOutbound{tag: "pool"}
	route := newTestRoute(nil, "PROXY", "DIRECT", direct, proxy)
	packetConn, err := route.ListenPacket(context.Background(), M.ParseSocksaddr("1.1.1.1:443"))
	if err != nil {
		t.Fatal(err)
	}
	_ = packetConn.Close()
	directive := proxy.lastDirective
	if directive == nil || directive.ProfileID != udpProfileID {
		t.Fatalf("UDP directive = %+v", directive)
	}
	if directive.Strategy != pool.StrategyAuto {
		t.Fatalf("UDP strategy = %q, want auto", directive.Strategy)
	}
	if len(directive.PreferredProtocolFamilies) != 2 || directive.PreferredProtocolFamilies[0] != "hysteria2" {
		t.Fatalf("UDP protocol preference = %v", directive.PreferredProtocolFamilies)
	}
	if !directive.RequireAvailablePreferred {
		t.Fatal("UDP directive did not require an available native protocol")
	}
}

func TestRouteUDPFallsBackDirectWhenProxyUnavailable(t *testing.T) {
	direct := &recordingOutbound{tag: "direct"}
	route := newTestRoute(nil, "PROXY", "DIRECT", direct, nil)
	packetConn, err := route.ListenPacket(context.Background(), M.ParseSocksaddr("1.1.1.1:443"))
	if err != nil {
		t.Fatal(err)
	}
	_ = packetConn.Close()
	stats := route.Snapshot()
	if direct.packetDials.Load() != 1 || stats.NoNodeFallback != 1 || stats.UDP != 1 {
		t.Fatalf("unexpected UDP fallback: dials=%d stats=%+v", direct.packetDials.Load(), stats)
	}
}

func TestRouteUDPReportsResolvedDestinationForFakeIPNAT(t *testing.T) {
	direct := &recordingOutbound{tag: "direct"}
	proxy := &recordingOutbound{tag: "pool"}
	route := newTestRoute(nil, "PROXY", "DIRECT", direct, proxy)
	route.resolveUDP = func(_ context.Context, destination M.Socksaddr) (M.Socksaddr, netip.Addr, error) {
		if destination.Fqdn != "stun.cloudflare.com" {
			t.Fatalf("resolved domain = %q", destination.Fqdn)
		}
		address := netip.MustParseAddr("162.159.207.0")
		return M.SocksaddrFrom(address, destination.Port), address, nil
	}

	metadata := &adapter.InboundContext{Domain: "stun.cloudflare.com", IPVersion: 4}
	ctx := adapter.WithContext(context.Background(), metadata)
	destination := M.ParseSocksaddr("stun.cloudflare.com:3478")
	packetConn, destinationAddress, err := route.ListenPacketWithDestination(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	_ = packetConn.Close()
	if destinationAddress != netip.MustParseAddr("162.159.207.0") {
		t.Fatalf("reported destination address = %v", destinationAddress)
	}
	if got := proxy.lastPacketDestination; got != M.ParseSocksaddr("162.159.207.0:3478") {
		t.Fatalf("pool packet destination = %v", got)
	}
}

func TestRouteUDPPreservesDomainWhenResolutionFails(t *testing.T) {
	direct := &recordingOutbound{tag: "direct"}
	proxy := &recordingOutbound{tag: "pool"}
	route := newTestRoute(nil, "PROXY", "DIRECT", direct, proxy)
	route.resolveUDP = func(_ context.Context, destination M.Socksaddr) (M.Socksaddr, netip.Addr, error) {
		return destination, netip.Addr{}, context.DeadlineExceeded
	}

	destination := M.ParseSocksaddr("example.com:443")
	packetConn, destinationAddress, err := route.ListenPacketWithDestination(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	_ = packetConn.Close()
	if destinationAddress.IsValid() {
		t.Fatalf("unexpected destination address = %v", destinationAddress)
	}
	if proxy.lastPacketDestination != destination {
		t.Fatalf("pool packet destination = %v, want %v", proxy.lastPacketDestination, destination)
	}
}

func TestRouteUDPResolvesDirectDestination(t *testing.T) {
	direct := &recordingOutbound{tag: "direct"}
	route := newTestRoute(nil, "DIRECT", "DIRECT", direct, nil)
	route.resolveUDP = func(_ context.Context, destination M.Socksaddr) (M.Socksaddr, netip.Addr, error) {
		address := netip.MustParseAddr("2001:db8::53")
		return M.SocksaddrFrom(address, destination.Port), address, nil
	}

	packetConn, destinationAddress, err := route.ListenPacketWithDestination(
		context.Background(), M.ParseSocksaddr("dns.example:53"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = packetConn.Close()
	if destinationAddress != netip.MustParseAddr("2001:db8::53") {
		t.Fatalf("reported destination address = %v", destinationAddress)
	}
	if got := direct.lastPacketDestination; got != M.ParseSocksaddr("[2001:db8::53]:53") {
		t.Fatalf("direct packet destination = %v", got)
	}
}

func TestUDPLookupStrategyFollowsInboundIPVersion(t *testing.T) {
	for _, test := range []struct {
		name      string
		ipVersion uint8
		want      C.DomainStrategy
	}{
		{name: "unspecified", want: C.DomainStrategyAsIS},
		{name: "ipv4", ipVersion: 4, want: C.DomainStrategyIPv4Only},
		{name: "ipv6", ipVersion: 6, want: C.DomainStrategyIPv6Only},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{IPVersion: test.ipVersion})
			if got := udpLookupStrategy(ctx); got != test.want {
				t.Fatalf("UDP lookup strategy = %v, want %v", got, test.want)
			}
		})
	}
}
