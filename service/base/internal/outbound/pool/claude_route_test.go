package pool

import (
	"context"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"errors"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	"net"
	"testing"
)

func TestClaudeRouteOverridesOrdinarySelection(t *testing.T) {
	p := &poolOutbound{options: Options{ClaudeRoute: config.ClaudeRoute{Enabled: true, NodeTag: "fixed", NodeFingerprint: config.NodeFingerprint("test-uri")}}}
	ctx := WithDirective(context.Background(), &SelectionDirective{RequiredTag: "other"})
	next, e := p.claudeSelectionContext(ctx, M.ParseSocksaddr("api.anthropic.com:443"))
	if e != nil {
		t.Fatal(e)
	}
	if DirectiveFrom(next).RequiredTag != "fixed" {
		t.Fatal("override escaped")
	}
	next, e = p.claudeSelectionContext(ctx, M.ParseSocksaddr("example.org:443"))
	if e != nil || DirectiveFrom(next).RequiredTag != "other" {
		t.Fatal("ordinary directive changed")
	}
	p.options.ClaudeRoute.NodeTag = ""
	if _, e = p.claudeSelectionContext(ctx, M.ParseSocksaddr("claude.ai:443")); e == nil {
		t.Fatal("empty pin accepted")
	}
}

type claudeDialOutbound struct {
	failingOutbound
	tag   string
	calls *[]string
	fail  bool
}

func (o claudeDialOutbound) Network() []string { return []string{"tcp", "udp"} }
func (o claudeDialOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	*o.calls = append(*o.calls, o.tag)
	if o.fail {
		return nil, errors.New("fixed node failed")
	}
	a, b := net.Pipe()
	b.Close()
	return a, nil
}
func (o claudeDialOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	*o.calls = append(*o.calls, o.tag)
	if o.fail {
		return nil, errors.New("fixed node failed")
	}
	return net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
}
func TestClaudeRouteActualPoolNeverEscapes(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		for _, state := range []string{"failed", "missing", "replaced", "auth", "success"} {
			t.Run(network+"/"+state, func(t *testing.T) {
				p := tierTestPool(t)
				p.logger = log.NewNOPFactory().Logger()
				var calls []string
				for _, m := range p.members {
					m.outbound = claudeDialOutbound{tag: m.tag, calls: &calls, fail: m.tag == "jp-a" && state == "failed"}
					meta := p.options.Metadata[m.tag]
					meta.URI = "uri-" + m.tag
					p.options.Metadata[m.tag] = meta
				}
				p.options.ClaudeRoute = config.ClaudeRoute{Enabled: true, NodeTag: "jp-a", NodeFingerprint: config.NodeFingerprint("uri-jp-a")}
				switch state {
				case "missing":
					p.options.ClaudeRoute.NodeTag = "missing"
				case "replaced":
					meta := p.options.Metadata["jp-a"]
					meta.URI = "replaced"
					p.options.Metadata["jp-a"] = meta
				case "auth":
					p.members[0].entry.RecordFailure(monitor.ErrUpstreamAuthenticationRequired, "test")
				}
				var e error
				dst := M.ParseSocksaddr("api.anthropic.com:443")
				if network == "tcp" {
					var c net.Conn
					c, e = p.DialContext(context.Background(), network, dst)
					if c != nil {
						c.Close()
					}
				} else {
					var c net.PacketConn
					c, e = p.ListenPacket(context.Background(), dst)
					if c != nil {
						c.Close()
					}
				}
				if state == "success" {
					if e != nil || len(calls) != 1 {
						t.Fatalf("success %v %v", calls, e)
					}
				} else if e == nil {
					t.Fatal("accepted invalid route")
				}
				for _, tag := range calls {
					if tag != "jp-a" {
						t.Fatalf("escaped to %s", tag)
					}
				}
				if (state == "missing" || state == "replaced" || state == "auth") && len(calls) != 0 {
					t.Fatalf("invalid binding dialed %v", calls)
				}
			})
		}
	}
}
