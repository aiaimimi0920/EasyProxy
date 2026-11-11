package builder

import (
	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/pool"
	"testing"
)

func TestLegacyPoolBuildEnablesLiveSharedSelection(t *testing.T) {
	cfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "n", URI: "socks5://127.0.0.1:1080"}}}
	cfg.Listener.Protocol = config.InboundProtocolMixed
	cfg.Listener.Address = "127.0.0.1"
	cfg.Listener.Port = 19199
	cfg.Routing.SelectionTiers = []config.SelectionTier{{Kind: "country", Country: "US"}}
	opts, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, out := range opts.Outbounds {
		if out.Tag == pool.Tag {
			p := out.Options.(*pool.Options)
			if !p.SharedSelection || len(p.SelectionTiers) != 1 || p.SelectionTiers[0].Country != "US" {
				t.Fatalf("lost shared tiers: %+v", p)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no pool")
	}
}
