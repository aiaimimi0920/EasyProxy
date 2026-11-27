package boxmgr

import (
	"context"
	"net"
	"testing"

	"easy_proxies/internal/outbound/gatewayroute"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

type statsTestRegistry struct {
	adapter.OutboundManager
	direct, route adapter.Outbound
}

func (r *statsTestRegistry) Outbound(tag string) (adapter.Outbound, bool) {
	if tag == gatewayroute.Tag {
		return r.route, r.route != nil
	}
	return r.direct, tag == "direct"
}

type statsTestDirect struct{ adapter.Outbound }

func (*statsTestDirect) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	c, p := net.Pipe()
	_ = p.Close()
	return c, nil
}

func TestGatewayTotalsAcrossIdleRollbackAndLateCompletion(t *testing.T) {
	m := new(Manager)
	build := func() adapter.Outbound {
		t.Helper()
		r := &statsTestRegistry{direct: new(statsTestDirect)}
		ctx := service.ContextWith[adapter.OutboundManager](context.Background(), r)
		reg := outbound.NewRegistry()
		gatewayroute.Register(reg)
		out, err := reg.CreateOutbound(gatewayroute.WithTotals(ctx, &m.gatewayTotals), nil, nil, gatewayroute.Tag, gatewayroute.Type, &gatewayroute.Options{DirectTag: "direct", FinalPolicy: "DIRECT"})
		if err != nil {
			t.Fatal(err)
		}
		if err = out.(adapter.Lifecycle).Start(adapter.StartStateStart); err != nil {
			t.Fatal(err)
		}
		r.route = out
		m.currentBox = &fakeManagedBox{outbound: r}
		return out
	}
	dial := func(out adapter.Outbound) net.Conn {
		t.Helper()
		c, err := out.DialContext(context.Background(), "tcp", M.ParseSocksaddr("192.0.2.1:80"))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	check := func(direct uint64, active int64) {
		t.Helper()
		s, ok := m.GatewayRouteStats()
		if !ok || s.Direct != direct || s.Active != active {
			t.Fatalf("stats=%+v ok=%t want %d/%d", s, ok, direct, active)
		}
	}
	old := build()
	first := dial(old)
	defer first.Close()
	check(1, 1)
	m.currentBox = nil
	check(1, 0)
	candidate := build()
	second := dial(candidate)
	defer second.Close()
	check(2, 1)
	_ = dial(old).Close() // late work against the retired instance
	check(3, 1)
	_ = build() // rollback publishes a new box, not the old pointer
	check(3, 0)
}
