package config

import (
	"fmt"
	"net"
	"strings"
)

func validateGatewayTun(g *GatewayConfig, trustedNetworks []*net.IPNet) error {
	tun := &g.Tun
	if name := strings.TrimSpace(tun.InterfaceName); !validIdentityToken(name) || len(name) > 15 {
		return fmt.Errorf("invalid gateway.tun.interface_name %q", tun.InterfaceName)
	}
	tun.InterfaceName = strings.TrimSpace(tun.InterfaceName)
	tun.Stack = strings.ToLower(strings.TrimSpace(tun.Stack))
	switch tun.Stack {
	case "system", "gvisor", "mixed":
	default:
		return fmt.Errorf("unsupported gateway.tun.stack %q", tun.Stack)
	}
	if tun.MTU < 1280 || tun.MTU > 9000 {
		return fmt.Errorf("gateway.tun.mtu must be between 1280 and 9000: %d", tun.MTU)
	}
	if !tun.IPv4 && !tun.IPv6 {
		return fmt.Errorf("gateway.tun must enable IPv4 or IPv6")
	}
	if tun.DNSHijack && !g.DNS.Enabled {
		return fmt.Errorf("gateway.tun.dns_hijack requires gateway.dns.enabled")
	}

	fakeIPv4, err := parseGatewayPrefix("gateway.tun.fake_ipv4_range", tun.FakeIPv4Range, true)
	if err != nil {
		return err
	}
	fakeIPv6, err := parseGatewayPrefix("gateway.tun.fake_ipv6_range", tun.FakeIPv6Range, false)
	if err != nil {
		return err
	}
	for _, trusted := range trustedNetworks {
		if gatewayNetworksOverlap(fakeIPv4, trusted) || gatewayNetworksOverlap(fakeIPv6, trusted) {
			return fmt.Errorf("gateway TUN fake-IP range overlaps trusted CIDR %q", trusted.String())
		}
	}

	hasIPv4Address := false
	hasIPv6Address := false
	for _, value := range tun.Addresses {
		ip, network, err := net.ParseCIDR(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid gateway.tun.addresses entry %q: %w", value, err)
		}
		if ip.To4() != nil {
			hasIPv4Address = true
		} else {
			hasIPv6Address = true
		}
		if gatewayNetworksOverlap(network, fakeIPv4) || gatewayNetworksOverlap(network, fakeIPv6) {
			return fmt.Errorf("gateway TUN address %q overlaps a fake-IP range", value)
		}
		for _, trusted := range trustedNetworks {
			if gatewayNetworksOverlap(network, trusted) {
				return fmt.Errorf("gateway TUN address %q overlaps trusted CIDR %q", value, trusted.String())
			}
		}
	}
	if tun.IPv4 && !hasIPv4Address {
		return fmt.Errorf("gateway.tun.ipv4 requires an IPv4 TUN address")
	}
	if tun.IPv6 && !hasIPv6Address {
		return fmt.Errorf("gateway.tun.ipv6 requires an IPv6 TUN address")
	}
	return nil
}

func parseGatewayPrefix(name, value string, wantIPv4 bool) (*net.IPNet, error) {
	ip, network, err := net.ParseCIDR(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", name, value, err)
	}
	if (ip.To4() != nil) != wantIPv4 {
		return nil, fmt.Errorf("%s has the wrong address family: %q", name, value)
	}
	return network, nil
}

func gatewayNetworksOverlap(left, right *net.IPNet) bool {
	if left == nil || right == nil {
		return false
	}
	return left.Contains(right.IP) || right.Contains(left.IP)
}
