package builder

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"

	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/gatewayroute"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

const (
	tunInboundTag     = "easyproxy-tun"
	directOutboundTag = "direct"
)

func tunGatewayEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.Gateway.Enabled && strings.EqualFold(cfg.Gateway.Mode, "tun")
}

func tunDirectFallbackEnabled(cfg *config.Config) bool {
	return tunGatewayEnabled(cfg) &&
		routerule.NormalizePolicy(cfg.Gateway.Routing.NoAvailableProxyPolicy) == routerule.PolicyDirect
}

func buildTunInbound(cfg *config.Config) (option.Inbound, error) {
	addresses := make(badoption.Listable[netip.Prefix], 0, len(cfg.Gateway.Tun.Addresses))
	for _, rawPrefix := range cfg.Gateway.Tun.Addresses {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(rawPrefix))
		if err != nil {
			return option.Inbound{}, fmt.Errorf("parse TUN address %q: %w", rawPrefix, err)
		}
		if (prefix.Addr().Is4() && !cfg.Gateway.Tun.IPv4) || (prefix.Addr().Is6() && !cfg.Gateway.Tun.IPv6) {
			continue
		}
		addresses = append(addresses, prefix)
	}
	if len(addresses) == 0 {
		return option.Inbound{}, fmt.Errorf("native TUN requires an address for an enabled IP family")
	}
	return option.Inbound{
		Type: C.TypeTun,
		Tag:  tunInboundTag,
		Options: &option.TunInboundOptions{
			InterfaceName: cfg.Gateway.Tun.InterfaceName,
			Address:       addresses,
			AutoRoute:     false,
			StrictRoute:   cfg.Gateway.Tun.StrictRoute,
			Stack:         cfg.Gateway.Tun.Stack,
			MTU:           cfg.Gateway.Tun.MTU,
			InboundOptions: option.InboundOptions{
				SniffEnabled:             true,
				SniffOverrideDestination: true,
			},
		},
	}, nil
}

func buildGatewayRouteOptions(cfg *config.Config, hasPool bool) (gatewayroute.Options, error) {
	localRules, err := routerule.LoadLocalRuleFiles(cfg.Routing.RuleFiles)
	if err != nil {
		return gatewayroute.Options{}, err
	}
	rules := make([]string, 0, len(cfg.Routing.Rules)+len(localRules)+64)
	rules = append(rules, cfg.Routing.Rules...)
	rules = append(rules, localRules...)
	if cfg.RoutingUseDefaultRules() {
		rules = append(rules, routerule.DefaultRules()...)
	}
	poolTag := ""
	if hasPool {
		poolTag = pool.Tag
	}
	return gatewayroute.Options{
		Rules:                  rules,
		FinalPolicy:            cfg.Gateway.Routing.FinalPolicy,
		NoAvailableProxyPolicy: cfg.Gateway.Routing.NoAvailableProxyPolicy,
		DefaultStrategy:        pool.NormalizeStrategy(cfg.Routing.DefaultStrategy),
		PoolTag:                poolTag,
		DirectTag:              directOutboundTag,
	}, nil
}

func configureTunGateway(
	cfg *config.Config,
	route *option.RouteOptions,
	dnsOptions *option.DNSOptions,
	hasPool bool,
) (option.Inbound, []option.Outbound, *option.ExperimentalOptions, error) {
	inbound, err := buildTunInbound(cfg)
	if err != nil {
		return option.Inbound{}, nil, nil, err
	}
	routeOptions, err := buildGatewayRouteOptions(cfg, hasPool)
	if err != nil {
		return option.Inbound{}, nil, nil, err
	}
	outbounds := []option.Outbound{
		{Type: C.TypeDirect, Tag: directOutboundTag, Options: &option.DirectOutboundOptions{}},
		{Type: gatewayroute.Type, Tag: gatewayroute.Tag, Options: &routeOptions},
	}

	tunRules := make([]option.Rule, 0, 2)
	if cfg.Gateway.Tun.DNSHijack {
		tunRules = append(tunRules, defaultRouteRule(
			badoption.Listable[string]{tunInboundTag},
			badoption.Listable[string]{"tcp", "udp"},
			badoption.Listable[uint16]{53},
			option.RuleAction{Action: C.RuleActionTypeHijackDNS},
		))
	}
	tunRules = append(tunRules, defaultRouteRule(
		badoption.Listable[string]{tunInboundTag}, nil, nil,
		option.RuleAction{
			Action:       C.RuleActionTypeRoute,
			RouteOptions: option.RouteActionOptions{Outbound: gatewayroute.Tag},
		},
	))
	route.Rules = append(tunRules, route.Rules...)
	if route.Final == "" || !hasPool {
		route.Final = gatewayroute.Tag
	}

	experimental, err := configureTunDNS(cfg, dnsOptions)
	return inbound, outbounds, experimental, err
}

func defaultRouteRule(inbound, network badoption.Listable[string], port badoption.Listable[uint16], action option.RuleAction) option.Rule {
	return option.Rule{
		Type: C.RuleTypeDefault,
		DefaultOptions: option.DefaultRule{
			RawDefaultRule: option.RawDefaultRule{Inbound: inbound, Network: network, Port: port},
			RuleAction:     action,
		},
	}
}

func configureTunDNS(cfg *config.Config, dnsOptions *option.DNSOptions) (*option.ExperimentalOptions, error) {
	if !cfg.Gateway.DNS.Enabled {
		return nil, nil
	}
	if dnsOptions == nil {
		return nil, fmt.Errorf("native TUN DNS requires sing-box DNS options")
	}
	if !cfg.Gateway.Tun.FakeIP {
		return nil, nil
	}
	fakeOptions := &option.FakeIPDNSServerOptions{}
	if cfg.Gateway.Tun.IPv4 {
		prefix, err := netip.ParsePrefix(cfg.Gateway.Tun.FakeIPv4Range)
		if err != nil {
			return nil, fmt.Errorf("parse fake IPv4 range: %w", err)
		}
		value := badoption.Prefix(prefix)
		fakeOptions.Inet4Range = &value
	}
	if cfg.Gateway.Tun.IPv6 {
		prefix, err := netip.ParsePrefix(cfg.Gateway.Tun.FakeIPv6Range)
		if err != nil {
			return nil, fmt.Errorf("parse fake IPv6 range: %w", err)
		}
		value := badoption.Prefix(prefix)
		fakeOptions.Inet6Range = &value
	}
	dnsOptions.Servers = append(dnsOptions.Servers, option.DNSServerOptions{
		Type: C.DNSTypeFakeIP, Tag: "easyproxy-fakeip", Options: fakeOptions,
	})
	dnsOptions.Rules = append(dnsOptions.Rules,
		dnsRouteRule([]string{"local", "lan", "home.arpa"}, "local"),
		dnsInboundRouteRule(tunInboundTag, "easyproxy-fakeip"),
	)
	cacheDir := "data"
	if path := strings.TrimSpace(cfg.DatabasePath); path != "" {
		cacheDir = filepath.Dir(path)
	}
	return &option.ExperimentalOptions{CacheFile: &option.CacheFileOptions{
		Enabled: true, Path: filepath.Join(cacheDir, "tun-cache.db"), CacheID: "easyproxy-tun", StoreFakeIP: true,
	}}, nil
}

func dnsRouteRule(suffixes []string, server string) option.DNSRule {
	return option.DNSRule{
		Type: C.RuleTypeDefault,
		DefaultOptions: option.DefaultDNSRule{
			RawDefaultDNSRule: option.RawDefaultDNSRule{DomainSuffix: badoption.Listable[string](suffixes)},
			DNSRuleAction:     option.DNSRuleAction{Action: C.RuleActionTypeRoute, RouteOptions: option.DNSRouteActionOptions{Server: server}},
		},
	}
}

func dnsInboundRouteRule(inbound, server string) option.DNSRule {
	return option.DNSRule{
		Type: C.RuleTypeDefault,
		DefaultOptions: option.DefaultDNSRule{
			RawDefaultDNSRule: option.RawDefaultDNSRule{Inbound: badoption.Listable[string]{inbound}},
			DNSRuleAction:     option.DNSRuleAction{Action: C.RuleActionTypeRoute, RouteOptions: option.DNSRouteActionOptions{Server: server}},
		},
	}
}
