package builder

import (
	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/pool"
	"testing"
)

func TestClaudeRouteWiringWithoutListenerTakeover(t *testing.T) {
	cfg := &config.Config{Mode: "pool", ClaudeRoute: config.ClaudeRoute{Enabled: true, NodeTag: "fixed", NodeFingerprint: config.NodeFingerprint("test-uri")}, Nodes: []config.NodeConfig{{Name: "n", URI: "socks5://127.0.0.1:1080"}}}
	cfg.Listener.Protocol = config.InboundProtocolMixed
	cfg.Listener.Address = "127.0.0.1"
	cfg.Listener.Port = 19199
	if cfg.DispatchEnabled() || cfg.DispatchOwnsPrimaryInbound() {
		t.Fatal("unexpected listener takeover")
	}
	opts, e := Build(cfg)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, o := range opts.Outbounds {
		if o.Tag == pool.Tag {
			found = true
			if o.Options.(*pool.Options).ClaudeRoute != cfg.ClaudeRoute {
				t.Fatal("pool lost guard")
			}
		}
	}
	if !found {
		t.Fatal("no pool")
	}
	tun, e := buildGatewayRouteOptions(cfg, true)
	if e != nil || tun.ClaudeRoute != cfg.ClaudeRoute {
		t.Fatal("TUN lost guard")
	}
}
