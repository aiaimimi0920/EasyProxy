package gateway

import (
	"net/netip"
	"strings"

	"easy_proxies/internal/config"
)

const (
	tunIngressMark = "0x1/0x1"
	tunEgressMark  = "0x2/0x2"
	tunIPv4Table   = "100"
	tunIPv6Table   = "101"
)

func buildTunGatewayCommands(cfg config.GatewayConfig) []gatewayCommand {
	device := strings.TrimSpace(cfg.Tun.InterfaceName)
	commands := []gatewayCommand{
		{name: "nft", args: []string{"delete", "table", "inet", "easyproxy_gateway"}},
		{name: "ip", args: []string{"link", "show", "dev", device}},
	}
	if cfg.Tun.IPv4 {
		commands = append(commands,
			gatewayCommand{name: "ip", args: []string{"rule", "add", "priority", "100", "fwmark", tunEgressMark, "lookup", "main"}},
			gatewayCommand{name: "ip", args: []string{"rule", "add", "priority", "110", "fwmark", tunIngressMark, "lookup", tunIPv4Table}},
			gatewayCommand{name: "ip", args: []string{"route", "add", "default", "dev", device, "table", tunIPv4Table}},
		)
	}
	if cfg.Tun.IPv6 {
		commands = append(commands,
			gatewayCommand{name: "ip", args: []string{"-6", "rule", "add", "priority", "100", "fwmark", tunEgressMark, "lookup", "main"}},
			gatewayCommand{name: "ip", args: []string{"-6", "rule", "add", "priority", "110", "fwmark", tunIngressMark, "lookup", tunIPv6Table}},
			gatewayCommand{name: "ip", args: []string{"-6", "route", "add", "default", "dev", device, "table", tunIPv6Table}},
		)
	}
	commands = append(commands,
		gatewayCommand{name: "nft", args: []string{"add", "table", "inet", "easyproxy_gateway"}},
		gatewayCommand{name: "nft", args: []string{"add", "chain", "inet", "easyproxy_gateway", "prerouting", "{", "type", "filter", "hook", "prerouting", "priority", "mangle;", "policy", "accept;", "}"}},
		gatewayCommand{name: "nft", args: []string{"add", "rule", "inet", "easyproxy_gateway", "prerouting", "meta", "mark", "0x2", "return"}},
		gatewayCommand{name: "nft", args: []string{"add", "rule", "inet", "easyproxy_gateway", "prerouting", "tcp", "dport", "{", "22", ",", "22323", ",", "29888", "}", "return"}},
		gatewayCommand{name: "nft", args: []string{"add", "rule", "inet", "easyproxy_gateway", "prerouting", "udp", "dport", "{", "67", ",", "68", ",", "546", ",", "547", "}", "return"}},
	)
	if cfg.Tun.DNSHijack && cfg.DNS.Enabled {
		commands = appendTunIngressRules(commands, cfg, true)
	}
	commands = appendTunFakeIPRules(commands, cfg)
	commands = appendTunBypasses(commands, cfg)
	commands = appendTunIngressRules(commands, cfg, false)
	return commands
}

func appendTunFakeIPRules(commands []gatewayCommand, cfg config.GatewayConfig) []gatewayCommand {
	if !cfg.Tun.FakeIP {
		return commands
	}
	qualifiers := tunIngressQualifiers(cfg.Ingress)
	for _, trustedCIDR := range cfg.Ingress.TrustedCIDRs {
		trusted, err := netip.ParsePrefix(strings.TrimSpace(trustedCIDR))
		if err != nil {
			continue
		}
		family, destination := "ip", cfg.Tun.FakeIPv4Range
		if trusted.Addr().Is6() {
			family, destination = "ip6", cfg.Tun.FakeIPv6Range
		}
		if (family == "ip" && !cfg.Tun.IPv4) || (family == "ip6" && !cfg.Tun.IPv6) {
			continue
		}
		if _, err := netip.ParsePrefix(strings.TrimSpace(destination)); err != nil {
			continue
		}
		for _, qualifier := range qualifiers {
			args := []string{"add", "rule", "inet", "easyproxy_gateway", "prerouting"}
			args = append(args, qualifier...)
			args = append(args, family, "saddr", trusted.String(), family, "daddr", destination)
			args = appendTunTransport(args, cfg, false)
			args = append(args, "meta", "mark", "set", "0x1")
			commands = append(commands, gatewayCommand{name: "nft", args: args})
		}
	}
	return commands
}

func appendTunBypasses(commands []gatewayCommand, cfg config.GatewayConfig) []gatewayCommand {
	if cfg.Tun.IPv4 {
		for _, cidr := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4"} {
			commands = append(commands, nftReturnDestination("ip", cidr))
		}
	}
	if cfg.Tun.IPv6 {
		for _, cidr := range []string{"::/128", "::1/128", "fe80::/10", "fc00::/7", "ff00::/8"} {
			commands = append(commands, nftReturnDestination("ip6", cidr))
		}
	}
	return commands
}

func nftReturnDestination(family, cidr string) gatewayCommand {
	return gatewayCommand{name: "nft", args: []string{"add", "rule", "inet", "easyproxy_gateway", "prerouting", family, "daddr", cidr, "return"}}
}

func appendTunIngressRules(commands []gatewayCommand, cfg config.GatewayConfig, dnsOnly bool) []gatewayCommand {
	qualifiers := tunIngressQualifiers(cfg.Ingress)
	for _, cidr := range cfg.Ingress.TrustedCIDRs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
		if err != nil || (prefix.Addr().Is4() && !cfg.Tun.IPv4) || (prefix.Addr().Is6() && !cfg.Tun.IPv6) {
			continue
		}
		family := "ip"
		if prefix.Addr().Is6() {
			family = "ip6"
		}
		for _, qualifier := range qualifiers {
			args := []string{"add", "rule", "inet", "easyproxy_gateway", "prerouting"}
			args = append(args, qualifier...)
			args = append(args, family, "saddr", prefix.String())
			args = appendTunTransport(args, cfg, dnsOnly)
			args = append(args, "meta", "mark", "set", "0x1")
			commands = append(commands, gatewayCommand{name: "nft", args: args})
		}
	}
	return commands
}

func appendTunTransport(args []string, cfg config.GatewayConfig, dnsOnly bool) []string {
	if dnsOnly {
		return append(args, "meta", "l4proto", "{", "tcp", ",", "udp", "}", "th", "dport", "53")
	}
	if cfg.Tun.UDP {
		return append(args, "meta", "l4proto", "{", "tcp", ",", "udp", "}")
	}
	return append(args, "meta", "l4proto", "tcp")
}

func tunIngressQualifiers(ingress config.GatewayIngressConfig) [][]string {
	qualifiers := make([][]string, 0, len(ingress.Interfaces)+len(ingress.InterfacePatterns))
	for _, iface := range ingress.Interfaces {
		if value := strings.TrimSpace(iface); value != "" {
			qualifiers = append(qualifiers, []string{"iifname", value})
		}
	}
	for _, pattern := range ingress.InterfacePatterns {
		if value := strings.TrimSpace(pattern); value != "" {
			qualifiers = append(qualifiers, []string{"iifname", value})
		}
	}
	if len(qualifiers) == 0 {
		return [][]string{nil}
	}
	return qualifiers
}

func tunIPCleanup(args []string) ([]string, bool) {
	inverse := append([]string(nil), args...)
	operation := -1
	for idx, value := range inverse {
		if value == "add" && idx > 0 && (inverse[idx-1] == "rule" || inverse[idx-1] == "route") {
			operation = idx
			break
		}
	}
	if operation < 0 {
		return nil, false
	}
	inverse[operation] = "del"
	return inverse, true
}
