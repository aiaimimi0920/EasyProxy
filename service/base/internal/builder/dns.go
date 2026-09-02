package builder

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"easy_proxies/internal/config"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func buildDNSOptions(cfg config.DNSConfig, nodeDomains []string, hasProxy bool) (*option.DNSOptions, error) {
	if cfg.Enabled != nil && !*cfg.Enabled {
		return nil, nil
	}
	servers := append([]string(nil), cfg.RemoteServers...)
	if len(servers) == 0 {
		servers = []string{"https://cloudflare-dns.com/dns-query"}
	}
	detour := strings.TrimSpace(cfg.Detour)
	if detour != "" && !hasProxy {
		detour = ""
	}
	strategy := C.DomainStrategyPreferIPv4
	switch strings.ToLower(strings.TrimSpace(cfg.Strategy)) {
	case "", "prefer_ipv4", "prefer-ipv4":
		strategy = C.DomainStrategyPreferIPv4
	case "as_is", "as-is":
		strategy = C.DomainStrategyAsIS
	case "prefer_ipv6", "prefer-ipv6":
		strategy = C.DomainStrategyPreferIPv6
	case "ipv4_only", "ipv4-only":
		strategy = C.DomainStrategyIPv4Only
	case "ipv6_only", "ipv6-only":
		strategy = C.DomainStrategyIPv6Only
	default:
		return nil, fmt.Errorf("unsupported DNS strategy %q", cfg.Strategy)
	}

	options := &option.DNSOptions{
		RawDNSOptions: option.RawDNSOptions{
			Final:            "remote-0",
			DNSClientOptions: option.DNSClientOptions{Strategy: option.DomainStrategy(strategy)},
		},
	}
	for idx, rawServer := range servers {
		parsed, err := url.Parse(strings.TrimSpace(rawServer))
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
			return nil, fmt.Errorf("DNS remote server must be an https URL, got %q", rawServer)
		}
		port := uint16(443)
		if parsed.Port() != "" {
			parsedPort, err := strconv.ParseUint(parsed.Port(), 10, 16)
			if err != nil || parsedPort == 0 {
				return nil, fmt.Errorf("invalid DNS remote server port in %q", rawServer)
			}
			port = uint16(parsedPort)
		}
		path := parsed.EscapedPath()
		if path == "" {
			path = "/dns-query"
		}
		serverName := strings.TrimSpace(parsed.Query().Get("server_name"))
		if serverName == "" {
			serverName = parsed.Hostname()
		}
		remote := &option.RemoteHTTPSDNSServerOptions{
			RemoteTLSDNSServerOptions: option.RemoteTLSDNSServerOptions{
				RemoteDNSServerOptions: option.RemoteDNSServerOptions{
					LocalDNSServerOptions: option.LocalDNSServerOptions{
						DialerOptions: option.DialerOptions{
							Detour:         detour,
							DomainStrategy: option.DomainStrategy(strategy),
							DomainResolver: &option.DomainResolveOptions{
								Server:   "local",
								Strategy: option.DomainStrategy(strategy),
							},
						},
					},
					DNSServerAddressOptions: option.DNSServerAddressOptions{
						Server:     parsed.Hostname(),
						ServerPort: port,
					},
				},
				OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
					TLS: &option.OutboundTLSOptions{Enabled: true, ServerName: serverName},
				},
			},
			Path: path,
		}
		options.Servers = append(options.Servers, option.DNSServerOptions{
			Type:    C.DNSTypeHTTPS,
			Tag:     fmt.Sprintf("remote-%d", idx),
			Options: remote,
		})
	}
	options.Servers = append(options.Servers, option.DNSServerOptions{
		Type:    C.DNSTypeLocal,
		Tag:     "local",
		Options: &option.LocalDNSServerOptions{},
	})

	uniqueDomains := make([]string, 0, len(nodeDomains))
	seen := make(map[string]struct{}, len(nodeDomains))
	for _, domain := range nodeDomains {
		domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if domain == "" || net.ParseIP(domain) != nil {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		uniqueDomains = append(uniqueDomains, domain)
	}
	sort.Strings(uniqueDomains)
	if len(uniqueDomains) > 0 {
		options.Rules = append(options.Rules, option.DNSRule{
			Type: C.RuleTypeDefault,
			DefaultOptions: option.DefaultDNSRule{
				RawDefaultDNSRule: option.RawDefaultDNSRule{
					Domain: badoption.Listable[string](uniqueDomains),
				},
				DNSRuleAction: option.DNSRuleAction{
					Action:       C.RuleActionTypeRoute,
					RouteOptions: option.DNSRouteActionOptions{Server: "local"},
				},
			},
		})
	}
	return options, nil
}
