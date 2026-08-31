package builder

import (
	"strings"
	"testing"

	"easy_proxies/internal/config"
	poolout "easy_proxies/internal/outbound/pool"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

const zenTestSourceRef = "manifest:conn_zenproxy_primary"

func TestBuildDetoursConfiguredSourceProtocolsThroughBootstrapPool(t *testing.T) {
	cfg := bootstrapDetourBuildConfig([]config.NodeConfig{
		{Name: "bootstrap", URI: "socks5://127.0.0.1:1080", SourceRef: "manifest:free"},
		{Name: "zen-http", URI: "http://127.0.0.1:8080", SourceRef: zenTestSourceRef},
		{Name: "zen-ss", URI: "ss://YWVzLTEyOC1nY206c2VjcmV0@127.0.0.1:8388", SourceRef: zenTestSourceRef},
		{Name: "zen-trojan", URI: "trojan://secret@127.0.0.1:443?security=tls&sni=example.com", SourceRef: zenTestSourceRef},
		{Name: "zen-vless", URI: "vless://11111111-1111-1111-1111-111111111111@127.0.0.1:443?encryption=none", SourceRef: zenTestSourceRef},
		{Name: "zen-vmess", URI: "vmess://11111111-1111-1111-1111-111111111111@127.0.0.1:443?encryption=auto", SourceRef: zenTestSourceRef},
	})

	opts, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	bootstrap := findOutboundByTag(t, opts.Outbounds, poolout.BootstrapTag)
	bootstrapOptions, ok := bootstrap.Options.(*poolout.Options)
	if !ok {
		t.Fatalf("bootstrap options type = %T, want *pool.Options", bootstrap.Options)
	}
	if got, want := bootstrapOptions.Members, []string{"bootstrap"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("bootstrap members = %#v, want %#v", got, want)
	}

	assertOutboundDetour(t, opts.Outbounds, "bootstrap", "")
	assertOutboundDetour(t, opts.Outbounds, "zen-http", "")
	for _, tag := range []string{"zen-ss", "zen-trojan", "zen-vless", "zen-vmess"} {
		assertOutboundDetour(t, opts.Outbounds, tag, poolout.BootstrapTag)
	}

	global := findOutboundByTag(t, opts.Outbounds, poolout.Tag)
	globalOptions := global.Options.(*poolout.Options)
	if len(globalOptions.Members) != len(cfg.Nodes) {
		t.Fatalf("global members = %d, want %d", len(globalOptions.Members), len(cfg.Nodes))
	}
}

func TestBuildWithoutConfiguredDetourPreservesDirectNodeDialing(t *testing.T) {
	cfg := bootstrapDetourBuildConfig([]config.NodeConfig{
		{Name: "ordinary-vless", URI: "vless://11111111-1111-1111-1111-111111111111@127.0.0.1:443?encryption=none"},
	})
	cfg.Pool.DetourSourceRefs = nil

	opts, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if outboundTagSet(opts.Outbounds)[poolout.BootstrapTag] {
		t.Fatalf("unexpected %q outbound without detour configuration", poolout.BootstrapTag)
	}
	assertOutboundDetour(t, opts.Outbounds, "ordinary-vless", "")
}

func TestBuildRejectsDetourWhenNoIndependentBootstrapMemberExists(t *testing.T) {
	cfg := bootstrapDetourBuildConfig([]config.NodeConfig{
		{Name: "zen-http", URI: "http://127.0.0.1:8080", SourceRef: zenTestSourceRef},
		{Name: "zen-vless", URI: "vless://11111111-1111-1111-1111-111111111111@127.0.0.1:443?encryption=none", SourceRef: zenTestSourceRef},
	})

	_, err := Build(cfg)
	if err == nil || !strings.Contains(err.Error(), "bootstrap pool requires") {
		t.Fatalf("Build error = %v, want missing bootstrap member error", err)
	}
}

func bootstrapDetourBuildConfig(nodes []config.NodeConfig) *config.Config {
	return &config.Config{
		Mode: "pool",
		Listener: config.ListenerConfig{
			Address:  "127.0.0.1",
			Port:     22323,
			Protocol: config.InboundProtocolHTTP,
		},
		Pool: config.PoolConfig{
			Mode:             "auto",
			DetourSourceRefs: []string{zenTestSourceRef},
		},
		Nodes: nodes,
	}
}

func findOutboundByTag(t *testing.T, outbounds []option.Outbound, tag string) option.Outbound {
	t.Helper()
	for _, outbound := range outbounds {
		if outbound.Tag == tag {
			return outbound
		}
	}
	t.Fatalf("outbound %q not found", tag)
	return option.Outbound{}
}

func assertOutboundDetour(t *testing.T, outbounds []option.Outbound, tag, want string) {
	t.Helper()
	outbound := findOutboundByTag(t, outbounds, tag)
	var got string
	switch options := outbound.Options.(type) {
	case *option.HTTPOutboundOptions:
		got = options.Detour
	case *option.SOCKSOutboundOptions:
		got = options.Detour
	case *option.VLESSOutboundOptions:
		got = options.Detour
	case *option.ShadowsocksOutboundOptions:
		got = options.Detour
	case *option.TrojanOutboundOptions:
		got = options.Detour
	case *option.VMessOutboundOptions:
		got = options.Detour
	default:
		t.Fatalf("outbound %q has unexpected options type %T", tag, outbound.Options)
	}
	if got != want {
		t.Fatalf("outbound %q detour = %q, want %q", tag, got, want)
	}
	if outbound.Type == C.TypeHTTP && want != "" {
		t.Fatalf("HTTP outbound %q must remain direct", tag)
	}
}
