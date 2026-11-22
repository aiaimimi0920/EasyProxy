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
			if !p.DefaultRegionExclusions || !p.SharedSelection || len(p.SelectionTiers) != 1 || p.SelectionTiers[0].Country != "US" {
				t.Fatalf("lost shared tiers: %+v", p)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no pool")
	}
}

func TestDefaultRegionPolicyOnlyOnGeneralPools(t *testing.T) {
	for _, mode := range []string{"pool", "hybrid", "multi-port"} {
		cfg := &config.Config{Mode: mode, Nodes: []config.NodeConfig{{Name: "n", URI: "socks5://127.0.0.1:1080", Port: 19201}}}
		cfg.Listener.Protocol = config.InboundProtocolMixed
		cfg.Listener.Address = "127.0.0.1"
		cfg.Listener.Port = 19199
		cfg.MultiPort.Protocol = config.InboundProtocolMixed
		cfg.MultiPort.Address = "127.0.0.1"
		cfg.MultiPort.BasePort = 19201
		cfg.Routing.Enabled = true
		cfg.Routing.Listen = "127.0.0.1:19202"
		opts, err := Build(cfg)
		if err != nil {
			t.Fatal(err)
		}
		general, dedicated := 0, 0
		for _, out := range opts.Outbounds {
			p, ok := out.Options.(*pool.Options)
			if !ok {
				continue
			}
			if p.SharedSelection {
				general++
				if !p.DefaultRegionExclusions {
					t.Fatalf("%s general pool missing policy", mode)
				}
			} else {
				dedicated++
				if p.DefaultRegionExclusions {
					t.Fatalf("%s dedicated pool inherited policy", mode)
				}
			}
		}
		if general == 0 || (mode != "pool" && dedicated == 0) {
			t.Fatalf("%s missing tested pools", mode)
		}
	}
}
