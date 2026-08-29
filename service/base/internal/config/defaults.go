package config

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"
)

func (c *Config) applyDefaults() error {
	if c.Mode == "" {
		c.Mode = "pool"
	}
	// Normalize mode name: support both multi-port and multi_port
	if c.Mode == "multi_port" {
		c.Mode = "multi-port"
	}
	switch c.Mode {
	case "pool", "multi-port", "hybrid":
	default:
		return fmt.Errorf("unsupported mode %q (use 'pool', 'multi-port', or 'hybrid')", c.Mode)
	}
	if c.Listener.Address == "" {
		c.Listener.Address = "0.0.0.0"
	}
	if c.Listener.Port == 0 {
		c.Listener.Port = 22323
	}
	if c.Listener.Protocol == "" {
		c.Listener.Protocol = InboundProtocolHTTP
	}
	listenerProtocol, err := NormalizeInboundProtocol(c.Listener.Protocol)
	if err != nil {
		return err
	}
	c.Listener.Protocol = listenerProtocol
	if c.Pool.Mode == "" {
		c.Pool.Mode = "auto"
	}
	if c.Pool.FailureThreshold <= 0 {
		c.Pool.FailureThreshold = 3
	}
	if c.Pool.BlacklistDuration <= 0 {
		c.Pool.BlacklistDuration = 24 * time.Hour
	}
	if c.MultiPort.Address == "" {
		c.MultiPort.Address = "0.0.0.0"
	}
	if c.MultiPort.BasePort == 0 {
		c.MultiPort.BasePort = 25000
	}
	if c.MultiPort.Protocol == "" {
		c.MultiPort.Protocol = InboundProtocolHTTP
	}
	multiPortProtocol, err := NormalizeInboundProtocol(c.MultiPort.Protocol)
	if err != nil {
		return err
	}
	c.MultiPort.Protocol = multiPortProtocol
	if c.Management.Listen == "" {
		c.Management.Listen = "127.0.0.1:29888"
	}
	if len(c.Management.ProbeTargets) == 0 && c.Management.ProbeTarget == "" {
		c.Management.ProbeTargets = DefaultManagementProbeTargets()
	}
	if c.Management.Enabled == nil {
		defaultEnabled := true
		c.Management.Enabled = &defaultEnabled
	}
	if c.Management.HealthCheckInterval <= 0 {
		c.Management.HealthCheckInterval = 2 * time.Hour
	}

	// Subscription refresh defaults
	if c.SubscriptionRefresh.Interval <= 0 {
		c.SubscriptionRefresh.Interval = 24 * time.Hour
	}
	if c.SubscriptionRefresh.Timeout <= 0 {
		c.SubscriptionRefresh.Timeout = 30 * time.Second
	}
	if c.SubscriptionRefresh.HealthCheckTimeout <= 0 {
		c.SubscriptionRefresh.HealthCheckTimeout = 2 * time.Minute
	}
	if c.SubscriptionRefresh.DrainTimeout <= 0 {
		c.SubscriptionRefresh.DrainTimeout = 30 * time.Second
	}
	if c.SubscriptionRefresh.MinAvailableNodes <= 0 {
		c.SubscriptionRefresh.MinAvailableNodes = 1
	}
	if c.SourceSync.RefreshInterval <= 0 {
		c.SourceSync.RefreshInterval = 1 * time.Hour
	}
	if c.SourceSync.RequestTimeout <= 0 {
		c.SourceSync.RequestTimeout = 15 * time.Second
	}
	if strings.TrimSpace(c.SourceSync.DefaultDirectProxyScheme) == "" {
		c.SourceSync.DefaultDirectProxyScheme = "http"
	}
	if c.SourceSync.ConnectorRuntime.Enabled == nil {
		defaultEnabled := true
		c.SourceSync.ConnectorRuntime.Enabled = &defaultEnabled
	}
	if strings.TrimSpace(c.SourceSync.ConnectorRuntime.ListenHost) == "" {
		c.SourceSync.ConnectorRuntime.ListenHost = "127.0.0.1"
	}
	if c.SourceSync.ConnectorRuntime.ListenStartPort == 0 {
		c.SourceSync.ConnectorRuntime.ListenStartPort = 30000
	}
	if c.SourceSync.ConnectorRuntime.StartupTimeout <= 0 {
		c.SourceSync.ConnectorRuntime.StartupTimeout = 30 * time.Second
	}
	if strings.TrimSpace(c.SourceSync.ConnectorRuntime.PreferredIP.BinaryPath) == "" {
		c.SourceSync.ConnectorRuntime.PreferredIP.BinaryPath = "cfst"
	}
	if c.SourceSync.ConnectorRuntime.PreferredIP.Timeout <= 0 {
		c.SourceSync.ConnectorRuntime.PreferredIP.Timeout = 5 * time.Minute
	}
	if c.SourceSync.ConnectorRuntime.PreferredIP.FanoutCount <= 0 {
		c.SourceSync.ConnectorRuntime.PreferredIP.FanoutCount = 5
	}
	if strings.TrimSpace(c.SourceSync.ConnectorRuntime.PreferredIP.WorkingDirectory) == "" {
		baseDir := "."
		if strings.TrimSpace(c.filePath) != "" {
			baseDir = filepath.Dir(c.filePath)
		}
		c.SourceSync.ConnectorRuntime.PreferredIP.WorkingDirectory = filepath.Join(baseDir, "data", "connectors", "preferred-ip")
	}
	if strings.TrimSpace(c.SourceSync.ConnectorRuntime.PreferredIP.IPFilePath) == "" {
		c.SourceSync.ConnectorRuntime.PreferredIP.IPFilePath = "/usr/share/easyproxy/cfst/ip.txt"
	}

	if c.DNS.Enabled == nil {
		enabled := true
		c.DNS.Enabled = &enabled
	}
	if len(c.DNS.RemoteServers) == 0 {
		c.DNS.RemoteServers = []string{"https://cloudflare-dns.com/dns-query"}
	}
	if strings.TrimSpace(c.DNS.Detour) == "" {
		c.DNS.Detour = "proxy-pool"
	}
	if strings.TrimSpace(c.DNS.Strategy) == "" {
		c.DNS.Strategy = "prefer_ipv4"
	}

	// Routing / smart-dispatch defaults. The feature is opt-in (Enabled defaults
	// to false) so existing deployments keep their current behaviour untouched.
	if strings.TrimSpace(c.Routing.DefaultStrategy) == "" {
		c.Routing.DefaultStrategy = "stable"
	}
	if c.Routing.LongLived.MinUptime <= 0 {
		c.Routing.LongLived.MinUptime = 2 * time.Hour
	}
	if c.Routing.LongLived.MinSuccessRate <= 0 {
		c.Routing.LongLived.MinSuccessRate = 0.9
	}
	if c.Routing.Session.TTL <= 0 {
		c.Routing.Session.TTL = 10 * time.Minute
	}
	if c.Routing.UseDefaultRules == nil {
		useDefault := true
		c.Routing.UseDefaultRules = &useDefault
	}
	for idx := range c.Routing.RuleProviders {
		if c.Routing.RuleProviders[idx].Interval <= 0 {
			c.Routing.RuleProviders[idx].Interval = 24 * time.Hour
		}
	}

	c.Gateway.Mode = strings.ToLower(strings.TrimSpace(c.Gateway.Mode))
	if c.Gateway.Mode == "" {
		c.Gateway.Mode = "transparent"
	}
	if c.Gateway.Listen == "" {
		c.Gateway.Listen = "0.0.0.0:15001"
	}
	if c.Gateway.Capture.TCP == "" {
		if c.Gateway.Mode == "tun" {
			c.Gateway.Capture.TCP = "disabled"
		} else {
			c.Gateway.Capture.TCP = "tproxy"
		}
	}
	if c.Gateway.Capture.UDP == "" {
		c.Gateway.Capture.UDP = "disabled"
	}
	// Preserving the original destination is an invariant of the transparent
	// TCP path, so enabled gateways always force it on.
	if c.Gateway.Enabled {
		c.Gateway.Capture.PreserveOriginalDestination = true
	}
	if c.Gateway.Routing.FinalPolicy == "" {
		c.Gateway.Routing.FinalPolicy = "PROXY"
	}
	if c.Gateway.Routing.NoAvailableProxyPolicy == "" {
		c.Gateway.Routing.NoAvailableProxyPolicy = "DIRECT"
	}
	if c.Gateway.DNS.Listen == "" {
		c.Gateway.DNS.Listen = "0.0.0.0:53"
	}
	if c.Gateway.Tun.InterfaceName == "" {
		c.Gateway.Tun.InterfaceName = "easyproxy0"
	}
	if len(c.Gateway.Tun.Addresses) == 0 {
		c.Gateway.Tun.Addresses = []string{"172.31.255.1/30", "fd31:255::1/126"}
	}
	if c.Gateway.Tun.Stack == "" {
		c.Gateway.Tun.Stack = "mixed"
	}
	if c.Gateway.Tun.MTU == 0 {
		c.Gateway.Tun.MTU = 1500
	}
	if !c.Gateway.Tun.ipv4Set && !c.Gateway.Tun.IPv4 && !c.Gateway.Tun.IPv6 {
		c.Gateway.Tun.IPv4 = true
	}
	if !c.Gateway.Tun.udpSet {
		c.Gateway.Tun.UDP = true
	}
	if !c.Gateway.Tun.strictRouteSet {
		c.Gateway.Tun.StrictRoute = true
	}
	if c.Gateway.Tun.FakeIPv4Range == "" {
		c.Gateway.Tun.FakeIPv4Range = "198.18.0.0/16"
	}
	if c.Gateway.Tun.FakeIPv6Range == "" {
		c.Gateway.Tun.FakeIPv6Range = "fc00::/18"
	}
	if err := c.normalizeGateway(); err != nil {
		return err
	}

	if c.LogLevel == "" {
		c.LogLevel = "info"
	}

	return nil
}

func (c *Config) normalizeGateway() error {
	if c == nil {
		return nil
	}
	g := &c.Gateway
	if g.Mode != "transparent" && g.Mode != "tun" {
		return fmt.Errorf("unsupported gateway mode %q (use %q or %q)", g.Mode, "transparent", "tun")
	}
	if _, _, err := net.SplitHostPort(strings.TrimSpace(g.Listen)); err != nil {
		return fmt.Errorf("gateway.listen must be host:port: %w", err)
	}
	for _, value := range []struct {
		name  string
		value string
	}{
		{name: "gateway.capture.tcp", value: g.Capture.TCP},
		{name: "gateway.capture.udp", value: g.Capture.UDP},
	} {
		mode := strings.ToLower(strings.TrimSpace(value.value))
		if mode != "tproxy" && mode != "disabled" {
			return fmt.Errorf("unsupported %s mode %q", value.name, value.value)
		}
	}
	if g.Mode == "tun" && (g.Capture.TCP != "disabled" || g.Capture.UDP != "disabled") {
		return errors.New("gateway.mode tun conflicts with TPROXY capture; set gateway.capture.tcp and gateway.capture.udp to disabled")
	}
	for _, value := range []struct {
		name  string
		value string
	}{
		{name: "gateway.routing.final_policy", value: g.Routing.FinalPolicy},
		{name: "gateway.routing.no_available_proxy_policy", value: g.Routing.NoAvailableProxyPolicy},
	} {
		policy := strings.ToUpper(strings.TrimSpace(value.value))
		if policy != "DIRECT" && policy != "PROXY" {
			return fmt.Errorf("unsupported %s %q", value.name, value.value)
		}
	}
	trustedNetworks := make([]*net.IPNet, 0, len(g.Ingress.TrustedCIDRs))
	for _, cidr := range g.Ingress.TrustedCIDRs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil {
			return fmt.Errorf("invalid gateway trusted CIDR %q: %w", cidr, err)
		}
		trustedNetworks = append(trustedNetworks, network)
	}
	if g.Mode == "tun" {
		if err := validateGatewayTun(g, trustedNetworks); err != nil {
			return err
		}
	}
	for name, device := range g.Devices {
		if strings.TrimSpace(name) == "" {
			return errors.New("gateway device name cannot be empty")
		}
		for _, address := range device.Addresses {
			if net.ParseIP(strings.TrimSpace(address)) == nil {
				return fmt.Errorf("invalid gateway device address %q for %q", address, name)
			}
		}
	}
	return nil
}

// normalizeInternal applies defaults, loads external nodes, and validates config.
// If skipSubscriptionFetch is true (reload/refresh scenario), only inline nodes
// from config.yaml are loaded. Subscription and manual nodes are managed by the
// SQLite Store and loaded by the caller (app.go / boxmgr).
