package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestGatewayDefaultsFailOpen(t *testing.T) {
	cfg := &Config{}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Gateway.Routing.NoAvailableProxyPolicy, "DIRECT"; got != want {
		t.Fatalf("no_available_proxy_policy = %q, want %q", got, want)
	}
	if got, want := cfg.Gateway.Listen, "0.0.0.0:15001"; got != want {
		t.Fatalf("gateway listen = %q, want %q", got, want)
	}
}

func TestGatewayRejectsInvalidCIDRAndPolicy(t *testing.T) {
	cfg := &Config{Gateway: GatewayConfig{
		Enabled: true,
		Routing: GatewayRoutingConfig{NoAvailableProxyPolicy: "DROP"},
		Ingress: GatewayIngressConfig{TrustedCIDRs: []string{"not-a-cidr"}},
	}}
	if err := cfg.normalize(); err == nil {
		t.Fatal("expected gateway validation error")
	}
}

func TestGatewayDeviceAliasesClone(t *testing.T) {
	cfg := &Config{Gateway: GatewayConfig{Devices: map[string]GatewayDeviceConfig{
		"laptop": {Addresses: []string{"192.168.15.100", "100.64.0.20"}},
	}}}
	clone := cfg.Clone()
	clone.Gateway.Devices["laptop"].Addresses[0] = "10.0.0.1"
	if got := cfg.Gateway.Devices["laptop"].Addresses[0]; got != "192.168.15.100" {
		t.Fatalf("device aliases were not deep-cloned: got %q", got)
	}
}

func TestGatewayTunDefaults(t *testing.T) {
	cfg := &Config{Gateway: GatewayConfig{
		Enabled: true,
		Mode:    "tun",
		Ingress: GatewayIngressConfig{TrustedCIDRs: []string{"192.0.2.0/24"}},
	}}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize() error = %v", err)
	}
	if got, want := cfg.Gateway.Tun.InterfaceName, "easyproxy0"; got != want {
		t.Fatalf("gateway.tun.interface_name = %q, want %q", got, want)
	}
	if got, want := cfg.Gateway.Tun.Stack, "mixed"; got != want {
		t.Fatalf("gateway.tun.stack = %q, want %q", got, want)
	}
	if got, want := cfg.Gateway.Tun.MTU, uint32(1500); got != want {
		t.Fatalf("gateway.tun.mtu = %d, want %d", got, want)
	}
	if !cfg.Gateway.Tun.IPv4 || !cfg.Gateway.Tun.UDP || !cfg.Gateway.Tun.StrictRoute {
		t.Fatalf("gateway.tun defaults = %+v, want IPv4/UDP/strict_route enabled", cfg.Gateway.Tun)
	}
	if cfg.Gateway.Tun.IPv6 {
		t.Fatal("gateway.tun.ipv6 defaulted to true")
	}
	if got, want := cfg.Gateway.Tun.FakeIPv4Range, "198.18.0.0/16"; got != want {
		t.Fatalf("gateway.tun.fake_ipv4_range = %q, want %q", got, want)
	}
	if got, want := cfg.Gateway.Capture.TCP, "disabled"; got != want {
		t.Fatalf("gateway.capture.tcp = %q, want %q in TUN mode", got, want)
	}
	if got, want := cfg.Gateway.Capture.UDP, "disabled"; got != want {
		t.Fatalf("gateway.capture.udp = %q, want %q in TUN mode", got, want)
	}
}

func TestGatewayTunRejectsConflicts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "tproxy capture", mutate: func(cfg *Config) { cfg.Gateway.Capture.TCP = "tproxy" }},
		{name: "DNS hijack without DNS", mutate: func(cfg *Config) { cfg.Gateway.Tun.DNSHijack = true }},
		{name: "invalid stack", mutate: func(cfg *Config) { cfg.Gateway.Tun.Stack = "invalid" }},
		{name: "MTU below minimum", mutate: func(cfg *Config) { cfg.Gateway.Tun.MTU = 1279 }},
		{name: "malformed fake IPv4 prefix", mutate: func(cfg *Config) { cfg.Gateway.Tun.FakeIPv4Range = "not-a-prefix" }},
		{name: "fake IPv4 overlaps trusted CIDR", mutate: func(cfg *Config) {
			cfg.Gateway.Ingress.TrustedCIDRs = []string{"198.18.1.0/24"}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Gateway: GatewayConfig{
				Enabled: true,
				Mode:    "tun",
				Ingress: GatewayIngressConfig{TrustedCIDRs: []string{"192.0.2.0/24"}},
			}}
			tt.mutate(cfg)
			if err := cfg.normalize(); err == nil {
				t.Fatal("normalize() unexpectedly accepted invalid TUN configuration")
			}
		})
	}
}

func TestGatewayTunRequiresTrustedCIDRForEachEnabledFamily(t *testing.T) {
	tests := []struct {
		name    string
		trusted []string
	}{
		{name: "missing IPv4", trusted: []string{"2001:db8:1::/64"}},
		{name: "missing IPv6", trusted: []string{"192.0.2.0/24"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Gateway: GatewayConfig{
				Enabled: true,
				Mode:    "tun",
				Ingress: GatewayIngressConfig{TrustedCIDRs: tt.trusted},
				Tun: GatewayTunConfig{
					IPv4: true,
					IPv6: true,
				},
			}}
			if err := cfg.normalize(); err == nil {
				t.Fatal("normalize() accepted an enabled address family without a trusted CIDR")
			}
		})
	}
}

func TestDisabledTunGatewayAllowsTemplateCIDRsToRemainEmpty(t *testing.T) {
	cfg := &Config{Gateway: GatewayConfig{Mode: "tun"}}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize() rejected a disabled TUN template: %v", err)
	}
}

func TestGatewayTunClone(t *testing.T) {
	cfg := &Config{
		Routing: RoutingConfig{RuleFiles: []string{"rules/local.yaml"}},
		Gateway: GatewayConfig{Tun: GatewayTunConfig{
			Addresses: []string{"172.31.255.1/30", "fd31:255::1/126"},
		}},
	}
	clone := cfg.Clone()
	clone.Routing.RuleFiles[0] = "changed"
	clone.Gateway.Tun.Addresses[0] = "10.0.0.1/30"
	if got := cfg.Routing.RuleFiles[0]; got != "rules/local.yaml" {
		t.Fatalf("routing rule files were not deep-cloned: got %q", got)
	}
	if got := cfg.Gateway.Tun.Addresses[0]; got != "172.31.255.1/30" {
		t.Fatalf("TUN addresses were not deep-cloned: got %q", got)
	}
}

func TestLoadForReloadResolvesRoutingRuleFilesRelativeToConfig(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rulePath := filepath.Join(rulesDir, "local.yaml")
	if err := os.WriteFile(rulePath, []byte("payload:\n  - DOMAIN-SUFFIX,example.com,DIRECT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("mode: pool\nrouting:\n  rule_files:\n    - rules/local.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForReload(configPath)
	if err != nil {
		t.Fatalf("LoadForReload() error = %v", err)
	}
	if got, want := cfg.Routing.RuleFiles, []string{rulePath}; !reflect.DeepEqual(got, want) {
		t.Fatalf("routing.rule_files = %#v, want %#v", got, want)
	}
}

func TestLoadForReloadDefaultsGeoIPEnabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("mode: pool\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForReload(configPath)
	if err != nil {
		t.Fatalf("LoadForReload() error = %v", err)
	}
	if !cfg.GeoIP.Enabled {
		t.Fatal("geoip.enabled = false, want default true")
	}
	if got, want := cfg.GeoIP.DatabasePath, filepath.Join(dir, "GeoLite2-Country.mmdb"); got != want {
		t.Fatalf("geoip.database_path = %q, want %q", got, want)
	}
	if !cfg.GeoIP.AutoUpdateEnabled {
		t.Fatal("geoip.auto_update_enabled = false, want default true")
	}
	if got, want := cfg.GeoIP.AutoUpdateInterval, 24*time.Hour; got != want {
		t.Fatalf("geoip.auto_update_interval = %v, want %v", got, want)
	}
	if got, want := cfg.SubscriptionRefresh.HealthCheckTimeout, 2*time.Minute; got != want {
		t.Fatalf("subscription_refresh.health_check_timeout = %v, want %v", got, want)
	}
}

func TestLoadForReloadPreservesExplicitGeoIPDisable(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	data := []byte("mode: pool\ngeoip:\n  enabled: false\n  database_path: \"\"\n  auto_update_enabled: false\n  auto_update_interval: 1h\n")
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForReload(configPath)
	if err != nil {
		t.Fatalf("LoadForReload() error = %v", err)
	}
	if cfg.GeoIP.Enabled || cfg.GeoIP.AutoUpdateEnabled {
		t.Fatalf("explicit GeoIP disable was overwritten: %+v", cfg.GeoIP)
	}
	if cfg.GeoIP.DatabasePath != "" {
		t.Fatalf("geoip.database_path = %q, want explicit empty value", cfg.GeoIP.DatabasePath)
	}
	if got, want := cfg.GeoIP.AutoUpdateInterval, time.Hour; got != want {
		t.Fatalf("geoip.auto_update_interval = %v, want %v", got, want)
	}
}

func TestGatewayTunPreservesExplicitFalseBooleansFromYAML(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("mode: pool\ngateway:\n  enabled: true\n  mode: tun\n  ingress:\n    trusted_cidrs: [2001:db8:1::/64]\n  tun:\n    ipv4: false\n    ipv6: true\n    udp: false\n    strict_route: false\n")
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForReload(configPath)
	if err != nil {
		t.Fatalf("LoadForReload() error = %v", err)
	}
	if cfg.Gateway.Tun.IPv4 || cfg.Gateway.Tun.UDP || cfg.Gateway.Tun.StrictRoute {
		t.Fatalf("explicit false TUN booleans were overwritten: %+v", cfg.Gateway.Tun)
	}
	if !cfg.Gateway.Tun.IPv6 {
		t.Fatal("explicit gateway.tun.ipv6 true was lost")
	}
}
