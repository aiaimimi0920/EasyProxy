package gatewayroute

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"

	"github.com/sagernet/sing-box/adapter"
	sboutbound "github.com/sagernet/sing-box/adapter/outbound"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type recordingOutbound struct {
	tag           string
	dials         atomic.Int32
	packetDials   atomic.Int32
	dialErr       error
	lastDirective *pool.SelectionDirective
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

func (o *recordingOutbound) ListenPacket(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	o.packetDials.Add(1)
	o.lastDirective = pool.DirectiveFrom(ctx)
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
	if len(directive.PreferredProtocolFamilies) != 2 || directive.PreferredProtocolFamilies[0] != "hysteria2" {
		t.Fatalf("UDP protocol preference = %v", directive.PreferredProtocolFamilies)
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
