package builder

import (
	"testing"

	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/gatewayroute"
	poolout "easy_proxies/internal/outbound/pool"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

func TestBuildTunGatewayCreatesDualStackUDPAndDNSRuntime(t *testing.T) {
	cfg := tunBuildConfig(true)
	opts, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var tunOptions *option.TunInboundOptions
	for _, inbound := range opts.Inbounds {
		if inbound.Tag == tunInboundTag {
			tunOptions, _ = inbound.Options.(*option.TunInboundOptions)
		}
	}
	if tunOptions == nil {
		t.Fatal("native TUN inbound is missing")
	}
	if tunOptions.InterfaceName != "easyproxy0" || tunOptions.AutoRoute || !tunOptions.StrictRoute {
		t.Fatalf("unexpected TUN options: %+v", tunOptions)
	}
	if len(tunOptions.Address) != 2 || !tunOptions.SniffEnabled || tunOptions.SniffOverrideDestination {
		t.Fatalf("dual-stack/sniff TUN options incomplete: %+v", tunOptions)
	}

	tags := outboundTagSet(opts.Outbounds)
	for _, tag := range []string{directOutboundTag, gatewayroute.Tag, poolout.Tag} {
		if !tags[tag] {
			t.Fatalf("outbounds = %#v, missing %q", tags, tag)
		}
	}
	if opts.Route == nil || len(opts.Route.Rules) < 2 {
		t.Fatalf("TUN route rules missing: %+v", opts.Route)
	}
	if got := opts.Route.Rules[0].DefaultOptions.Action; got != C.RuleActionTypeHijackDNS {
		t.Fatalf("first TUN rule action = %q, want hijack-dns", got)
	}
	if got := opts.Route.Rules[1].DefaultOptions.RouteOptions.Outbound; got != gatewayroute.Tag {
		t.Fatalf("TUN route outbound = %q, want %q", got, gatewayroute.Tag)
	}
	if opts.DNS == nil || opts.Experimental == nil || opts.Experimental.CacheFile == nil || !opts.Experimental.CacheFile.StoreFakeIP {
		t.Fatalf("fake-IP DNS persistence is incomplete: dns=%v experimental=%+v", opts.DNS != nil, opts.Experimental)
	}
	foundFakeIP := false
	for _, server := range opts.DNS.Servers {
		foundFakeIP = foundFakeIP || server.Type == C.DNSTypeFakeIP
	}
	if !foundFakeIP {
		t.Fatal("fake-IP DNS server is missing")
	}
}

func TestBuildTunGatewayWithoutNodesProducesDirectOnlyRuntime(t *testing.T) {
	cfg := tunBuildConfig(false)
	opts, err := Build(cfg)
	if err != nil {
		t.Fatalf("direct-only TUN Build returned error: %v", err)
	}
	tags := outboundTagSet(opts.Outbounds)
	if tags[poolout.Tag] {
		t.Fatalf("direct-only TUN runtime unexpectedly contains %q", poolout.Tag)
	}
	if !tags[directOutboundTag] || !tags[gatewayroute.Tag] {
		t.Fatalf("direct-only TUN outbounds incomplete: %#v", tags)
	}
	if opts.Route == nil || opts.Route.Final != gatewayroute.Tag {
		t.Fatalf("direct-only TUN final = %v, want %q", opts.Route, gatewayroute.Tag)
	}
	for _, outbound := range opts.Outbounds {
		if outbound.Tag != gatewayroute.Tag {
			continue
		}
		routeOptions, ok := outbound.Options.(*gatewayroute.Options)
		if !ok {
			t.Fatalf("gateway route options type = %T", outbound.Options)
		}
		if routeOptions.PoolTag != "" {
			t.Fatalf("direct-only gateway route pool tag = %q", routeOptions.PoolTag)
		}
		return
	}
	t.Fatal("gateway route outbound is missing")
}

func TestGatewayRuleInputsDoNotEnableExplicitDispatcher(t *testing.T) {
	cfg := tunBuildConfig(true)
	cfg.GeoIP = config.GeoIPConfig{Enabled: true, DatabasePath: "country.mmdb"}
	cfg.Routing.Rules = []string{"DOMAIN,allowed.example,DIRECT"}
	cfg.Routing.RuleProviders = []config.RuleProvider{{URL: "https://rules.example/list", Policy: "PROXY"}}
	cfg.Gateway.Routing.FinalPolicy = "DIRECT"
	options, err := buildGatewayRouteOptions(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if options.GeoIP != cfg.GeoIP || len(options.RuleProviders) != 1 || !options.UseDefaultRules {
		t.Fatalf("missing gateway rule inputs: %+v", options)
	}
	if options.FinalPolicy != "DIRECT" || len(options.Rules) != 1 {
		t.Fatalf("gateway rule ordering/fallback changed: %+v", options)
	}
	if cfg.Routing.Enabled || options.DeviceProfiles {
		t.Fatal("default gateway rules must not enable explicit dispatch or device profiles")
	}
}

func TestTunFakeIPDNSStrategyMatchesEnabledFamilies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		v4, v6   bool
		strategy option.DomainStrategy
	}{
		{"IPv4 only", true, false, option.DomainStrategy(C.DomainStrategyIPv4Only)},
		{"IPv6 only", false, true, option.DomainStrategy(C.DomainStrategyIPv6Only)},
		{"dual stack", true, true, option.DomainStrategy(C.DomainStrategyAsIS)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tunBuildConfig(true)
			cfg.Gateway.Tun.IPv4, cfg.Gateway.Tun.IPv6 = tc.v4, tc.v6
			dns := &option.DNSOptions{}
			if _, err := configureTunDNS(cfg, dns); err != nil {
				t.Fatal(err)
			}
			if len(dns.Rules) != 2 {
				t.Fatalf("rules = %d", len(dns.Rules))
			}
			if dns.Rules[0].DefaultOptions.RouteOptions.Server != "local" {
				t.Fatal("local DNS precedence changed")
			}
			rule := dns.Rules[1].DefaultOptions
			if rule.RouteOptions.Server != "easyproxy-fakeip" || rule.RouteOptions.Strategy != tc.strategy {
				t.Fatalf("unexpected fake-IP route: %+v", rule.RouteOptions)
			}
		})
	}
}

func tunBuildConfig(withNode bool) *config.Config {
	cfg := &config.Config{
		Mode: "pool",
		Listener: config.ListenerConfig{
			Address: "127.0.0.1", Port: 22323, Protocol: config.InboundProtocolMixed,
		},
		Pool:    config.PoolConfig{Mode: "auto"},
		Routing: config.RoutingConfig{DefaultStrategy: "stable"},
		Gateway: config.GatewayConfig{
			Enabled: true,
			Mode:    "tun",
			Routing: config.GatewayRoutingConfig{FinalPolicy: "PROXY", NoAvailableProxyPolicy: "DIRECT"},
			DNS:     config.GatewayDNSConfig{Enabled: true, Listen: "0.0.0.0:53"},
			Tun: config.GatewayTunConfig{
				InterfaceName: "easyproxy0",
				Addresses:     []string{"172.31.255.1/30", "fd31:255::1/126"},
				Stack:         "mixed",
				MTU:           1500,
				IPv4:          true,
				IPv6:          true,
				UDP:           true,
				StrictRoute:   true,
				DNSHijack:     true,
				FakeIP:        true,
				FakeIPv4Range: "198.18.0.0/16",
				FakeIPv6Range: "fc00::/18",
			},
		},
		DatabasePath: "data/data.db",
	}
	if withNode {
		cfg.Nodes = []config.NodeConfig{{Name: "node-one", URI: "socks5://127.0.0.1:1080"}}
	}
	return cfg
}
