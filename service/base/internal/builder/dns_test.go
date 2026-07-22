package builder

import (
	"testing"

	"easy_proxies/internal/config"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

func TestBuildDNSOptionsUsesProxyDetourAndLocalNodeResolution(t *testing.T) {
	cfg := config.DNSConfig{
		Enabled:       boolPtr(true),
		RemoteServers: []string{"https://cloudflare-dns.com/dns-query"},
		Detour:        "proxy-pool",
		Strategy:      "prefer_ipv4",
	}

	options, err := buildDNSOptions(cfg, []string{"node.example.com", "198.51.100.7"}, true)
	if err != nil {
		t.Fatalf("build DNS options: %v", err)
	}
	if options == nil {
		t.Fatal("DNS options must be enabled")
	}
	if options.Final != "remote-0" {
		t.Fatalf("final DNS server = %q, want remote-0", options.Final)
	}
	if options.Strategy != option.DomainStrategy(C.DomainStrategyPreferIPv4) {
		t.Fatalf("DNS strategy = %v, want prefer IPv4", options.Strategy)
	}
	if len(options.Servers) != 2 {
		t.Fatalf("DNS servers = %d, want remote + local", len(options.Servers))
	}
	remote, ok := options.Servers[0].Options.(*option.RemoteHTTPSDNSServerOptions)
	if !ok {
		t.Fatalf("remote DNS options type = %T", options.Servers[0].Options)
	}
	if remote.Detour != "proxy-pool" {
		t.Fatalf("remote DNS detour = %q, want proxy-pool", remote.Detour)
	}
	if remote.DomainResolver == nil || remote.DomainResolver.Server != "local" {
		t.Fatalf("remote DNS domain resolver = %#v, want local", remote.DomainResolver)
	}
	if len(options.Rules) != 1 {
		t.Fatalf("DNS rules = %d, want one local node rule", len(options.Rules))
	}
	if options.Rules[0].DefaultOptions.Domain[0] != "node.example.com" {
		t.Fatalf("node domain rule = %v, want node.example.com", options.Rules[0].DefaultOptions.Domain)
	}
	if options.Rules[0].DefaultOptions.RouteOptions.Server != "local" {
		t.Fatalf("node domain DNS server = %q, want local", options.Rules[0].DefaultOptions.RouteOptions.Server)
	}
}

func TestBuildDNSOptionsDisablesProxyDetourWithoutNodes(t *testing.T) {
	cfg := config.DNSConfig{
		Enabled:       boolPtr(true),
		RemoteServers: []string{"https://cloudflare-dns.com/dns-query"},
		Detour:        "proxy-pool",
		Strategy:      "prefer_ipv4",
	}

	options, err := buildDNSOptions(cfg, nil, false)
	if err != nil {
		t.Fatalf("build DNS options: %v", err)
	}
	if options == nil {
		t.Fatal("DNS options must be enabled")
	}
	remote, ok := options.Servers[0].Options.(*option.RemoteHTTPSDNSServerOptions)
	if !ok {
		t.Fatalf("remote DNS options type = %T", options.Servers[0].Options)
	}
	if remote.Detour != "" {
		t.Fatalf("remote DNS detour = %q, want direct fallback without nodes", remote.Detour)
	}
}

func boolPtr(value bool) *bool { return &value }
