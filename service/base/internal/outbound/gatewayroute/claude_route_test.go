package gatewayroute

import (
	"context"
	"easy_proxies/internal/config"
	"errors"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"testing"
)

func TestClaudeRouteFailsClosedTCPAndUDP(t *testing.T) {
	for _, missing := range []bool{false, true} {
		for _, tag := range []string{"fixed", ""} {
			direct := &recordingOutbound{tag: "direct"}
			proxy := &recordingOutbound{tag: "pool", dialErr: errors.New("unavailable")}
			r := newTestRoute(nil, "DIRECT", "DIRECT", direct, proxy)
			if missing {
				r.pool = nil
			}
			r.options.ClaudeRoute = config.ClaudeRoute{Enabled: true, NodeTag: tag, NodeFingerprint: config.NodeFingerprint("test-uri")}
			ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Domain: "api.anthropic.com"})
			dst := M.ParseSocksaddr("203.0.113.8:443")
			if c, e := r.DialContext(ctx, "tcp", dst); e == nil {
				c.Close()
				t.Fatal("TCP leaked")
			}
			if c, _, e := r.ListenPacketWithDestination(ctx, dst); e == nil {
				c.Close()
				t.Fatal("UDP leaked")
			}
			if direct.dials.Load() != 0 || direct.packetDials.Load() != 0 {
				t.Fatal("DIRECT fallback")
			}
			if tag != "" && !missing && (proxy.lastDirective == nil || proxy.lastDirective.RequiredTag != "fixed") {
				t.Fatal("lost exact pin")
			}
		}
	}
}
func TestClaudeRouteSuccessAndUnmatched(t *testing.T) {
	direct := &recordingOutbound{tag: "direct"}
	proxy := &recordingOutbound{tag: "pool"}
	r := newTestRoute(nil, "DIRECT", "DIRECT", direct, proxy)
	r.options.ClaudeRoute = config.ClaudeRoute{Enabled: true, NodeTag: "fixed", NodeFingerprint: config.NodeFingerprint("test-uri")}
	dst := M.ParseSocksaddr("claude.ai:443")
	c, e := r.DialContext(context.Background(), "tcp", dst)
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	p, _, e := r.ListenPacketWithDestination(context.Background(), dst)
	if e != nil {
		t.Fatal(e)
	}
	p.Close()
	if proxy.lastDirective.RequiredTag != "fixed" {
		t.Fatal("lost pin")
	}
	c, e = r.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.org:443"))
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	if direct.dials.Load() != 1 {
		t.Fatal("unmatched behavior changed")
	}
	r.engine = nil
	r.options.ClaudeRoute.Enabled = false // disabled control uses normal policy in existing suite
}
