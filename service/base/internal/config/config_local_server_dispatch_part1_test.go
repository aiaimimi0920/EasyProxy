package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNormalizeLocalServerMigratesCanonicalCredential(t *testing.T) {
	cfg := localServerNormalizeConfig()

	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize returned error: %v", err)
	}

	if got, want := cfg.LocalServer.Auth.Username, "legacy_user"; got != want {
		t.Fatalf("canonical username = %q, want %q", got, want)
	}
	if got, want := cfg.LocalServer.Auth.Password, "shared-secret"; got != want {
		t.Fatalf("canonical password = %q, want %q", got, want)
	}
	if got, want := cfg.Listener.Username, cfg.LocalServer.Auth.Username; got != want {
		t.Fatalf("listener username = %q, want canonical %q", got, want)
	}
	if got, want := cfg.Listener.Password, cfg.LocalServer.Auth.Password; got != want {
		t.Fatalf("listener password = %q, want canonical %q", got, want)
	}
	if got, want := cfg.Management.Password, cfg.LocalServer.Auth.Password; got != want {
		t.Fatalf("management password = %q, want canonical %q", got, want)
	}
	if got, want := cfg.LocalServer.SharedRevision, int64(1); got != want {
		t.Fatalf("shared revision = %d, want %d", got, want)
	}
	if got, want := cfg.LocalServer.CredentialGeneration, uint64(2); got != want {
		t.Fatalf("credential generation = %d, want %d", got, want)
	}
}

func TestNormalizeLocalServerCredentialMigrationIsIdempotent(t *testing.T) {
	cfg := localServerNormalizeConfig()

	if err := cfg.normalize(); err != nil {
		t.Fatalf("first normalize returned error: %v", err)
	}
	if got := cfg.LocalServer.CredentialGeneration; got != 2 {
		t.Fatalf("credential generation after first normalize = %d, want 2", got)
	}

	if err := cfg.normalize(); err != nil {
		t.Fatalf("second normalize returned error: %v", err)
	}
	if got := cfg.LocalServer.CredentialGeneration; got != 2 {
		t.Fatalf("credential generation after second normalize = %d, want 2", got)
	}
}

func TestNormalizeLocalServerRejectsBypassTopology(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "hybrid mode",
			mutate: func(cfg *Config) {
				cfg.Mode = "hybrid"
			},
		},
		{
			name: "listener protocol is not mixed",
			mutate: func(cfg *Config) {
				cfg.Listener.Protocol = InboundProtocolHTTP
			},
		},
		{
			name: "extra listeners bypass dispatcher",
			mutate: func(cfg *Config) {
				cfg.ExtraListeners = []ExtraListenerConfig{{Address: "127.0.0.1", Port: 22325}}
			},
		},
		{
			name: "local and routing listen conflict",
			mutate: func(cfg *Config) {
				cfg.Routing.Listen = "127.0.0.1:22326"
			},
		},
		{
			name: "legacy passwords conflict during migration",
			mutate: func(cfg *Config) {
				cfg.Listener.Password = "listener-secret"
				cfg.Management.Password = "management-secret"
			},
		},
		{
			name: "no password source",
			mutate: func(cfg *Config) {
				cfg.Listener.Password = ""
				cfg.Management.Password = ""
			},
		},
		{
			name: "explicit canonical username is invalid",
			mutate: func(cfg *Config) {
				cfg.LocalServer.Auth.Username = "invalid+username"
				cfg.LocalServer.Auth.Password = "canonical-secret"
			},
		},
		{
			name: "password contains NUL",
			mutate: func(cfg *Config) {
				cfg.LocalServer.Auth.Password = "canonical\x00secret"
			},
		},
		{
			name: "password exceeds 256 bytes",
			mutate: func(cfg *Config) {
				cfg.LocalServer.Auth.Password = strings.Repeat("x", 257)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := localServerNormalizeConfig()
			tt.mutate(cfg)
			if err := cfg.normalize(); err == nil {
				t.Fatal("normalize accepted a Local Server topology that can bypass the dispatcher")
			}
		})
	}
}

func TestNormalizeLocalServerUsesDefaultUsernameWhenLegacyUsernameIsInvalid(t *testing.T) {
	cfg := localServerNormalizeConfig()
	cfg.Listener.Username = "legacy+username"

	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize returned error: %v", err)
	}
	if got, want := cfg.LocalServer.Auth.Username, "easyproxy"; got != want {
		t.Fatalf("canonical username = %q, want fallback %q", got, want)
	}

	explicit := localServerNormalizeConfig()
	explicit.LocalServer.Auth = LocalServerAuthConfig{
		Username: "canonical+username",
		Password: "canonical-secret",
	}
	if err := explicit.normalize(); err == nil {
		t.Fatal("normalize accepted an invalid explicit canonical username")
	}
}

func TestNormalizeLocalServerCanonicalCredentialOverridesLegacyFields(t *testing.T) {
	cfg := localServerNormalizeConfig()
	cfg.LocalServer.Auth = LocalServerAuthConfig{
		Username: "canonical-user",
		Password: "canonical-secret",
	}
	cfg.Listener.Username = "legacy+invalid"
	cfg.Listener.Password = "listener-secret"
	cfg.Management.Password = "management-secret"

	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize returned error: %v", err)
	}
	if got, want := cfg.Listener.Username, "canonical-user"; got != want {
		t.Fatalf("listener username = %q, want %q", got, want)
	}
	if got, want := cfg.Listener.Password, "canonical-secret"; got != want {
		t.Fatalf("listener password = %q, want %q", got, want)
	}
	if got, want := cfg.Management.Password, "canonical-secret"; got != want {
		t.Fatalf("management password = %q, want %q", got, want)
	}
}

func TestNormalizeWithPortMapRejectsInvalidLocalServerTopology(t *testing.T) {
	cfg := localServerNormalizeConfig()
	cfg.Listener.Protocol = InboundProtocolHTTP
	cfg.Nodes = []NodeConfig{{
		Name: "valid-node",
		URI:  "socks5://127.0.0.1:1080",
		Port: 25001,
	}}

	if err := cfg.NormalizeWithPortMap(nil); err == nil {
		t.Fatal("NormalizeWithPortMap accepted an invalid Local Server topology")
	}
}

func TestNormalizeWithPortMapReassignsDuplicatePreservedPort(t *testing.T) {
	const preservedPort uint16 = 25001

	cfg := &Config{
		Mode: "hybrid",
		Listener: ListenerConfig{
			Address:  "127.0.0.1",
			Port:     22323,
			Protocol: InboundProtocolHTTP,
		},
		MultiPort: MultiPortConfig{
			Address:  "127.0.0.1",
			BasePort: 25000,
			Protocol: InboundProtocolHTTP,
		},
		Nodes: []NodeConfig{
			{
				Name: "stale-port-node",
				URI:  "socks5://127.0.0.1:1081",
				Port: preservedPort,
			},
			{
				Name: "preserved-node",
				URI:  "socks5://127.0.0.1:1080",
			},
		},
	}

	portMap := map[string]uint16{
		cfg.Nodes[1].NodeKey(): preservedPort,
	}
	if err := cfg.NormalizeWithPortMap(portMap); err != nil {
		t.Fatalf("NormalizeWithPortMap() error = %v", err)
	}
	if got := cfg.Nodes[1].Port; got != preservedPort {
		t.Fatalf("preserved node port = %d, want %d", got, preservedPort)
	}
	if got := cfg.Nodes[0].Port; got == 0 || got == preservedPort {
		t.Fatalf("duplicate node port = %d, want a non-zero reassigned port", got)
	}
}

func TestSaveSettingsPersistsLocalServerAndRoutingNodeFilter(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	nodesPath := filepath.Join(dir, "preserved-nodes.txt")
	if err := os.WriteFile(nodesPath, []byte("socks5://127.0.0.1:1081#file-node\n"), 0o644); err != nil {
		t.Fatalf("write nodes file: %v", err)
	}

	const initialYAML = `mode: pool
listener:
  address: 127.0.0.1
  port: 22323
  protocol: mixed
  username: legacy_user
  password: shared-secret
management:
  password: shared-secret
local_server:
  enabled: true
  listen: 127.0.0.1:22324
routing:
  enabled: false
  node_filter:
    countries: [US, JP]
    regions: [americas, asia]
    long_lived: true
nodes_file: preserved-nodes.txt
database_path: preserved.db
nodes:
  - name: preserved-node
    uri: socks5://127.0.0.1:1080
`
	if err := os.WriteFile(configPath, []byte(initialYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	cfg.Lock()
	dnsEnabled := false
	cfg.DNS = DNSConfig{
		Enabled:       &dnsEnabled,
		RemoteServers: []string{"https://dns.example.test/query"},
		Detour:        "node-pool",
		Strategy:      "ipv4_only",
	}
	err = cfg.SaveSettings()
	cfg.Unlock()
	if err != nil {
		t.Fatalf("SaveSettings returned error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var saved Config
	if err := yaml.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode saved config: %v", err)
	}

	if !saved.LocalServer.Enabled {
		t.Fatal("saved local_server.enabled = false, want true")
	}
	if got, want := saved.LocalServer.Listen, "127.0.0.1:22324"; got != want {
		t.Fatalf("saved local_server.listen = %q, want %q", got, want)
	}
	if got, want := saved.LocalServer.Auth.Username, "legacy_user"; got != want {
		t.Fatalf("saved canonical username = %q, want %q", got, want)
	}
	if got, want := saved.LocalServer.Auth.Password, "shared-secret"; got != want {
		t.Fatalf("saved canonical password = %q, want %q", got, want)
	}
	if got, want := saved.LocalServer.SharedRevision, int64(1); got != want {
		t.Fatalf("saved shared revision = %d, want %d", got, want)
	}
	if got, want := saved.LocalServer.CredentialGeneration, uint64(2); got != want {
		t.Fatalf("saved credential generation = %d, want %d", got, want)
	}
	if got, want := saved.Listener.Username, saved.LocalServer.Auth.Username; got != want {
		t.Fatalf("saved listener username = %q, want canonical %q", got, want)
	}
	if got, want := saved.Listener.Password, saved.LocalServer.Auth.Password; got != want {
		t.Fatalf("saved listener password = %q, want canonical %q", got, want)
	}
	if got, want := saved.Management.Password, saved.LocalServer.Auth.Password; got != want {
		t.Fatalf("saved management password = %q, want canonical %q", got, want)
	}
	if got, want := saved.Routing.NodeFilter.Countries, []string{"US", "JP"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("saved node filter countries = %#v, want %#v", got, want)
	}
	if got, want := saved.Routing.NodeFilter.Regions, []string{"americas", "asia"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("saved node filter regions = %#v, want %#v", got, want)
	}
	if saved.Routing.NodeFilter.LongLived == nil || !*saved.Routing.NodeFilter.LongLived {
		t.Fatalf("saved node filter long_lived = %v, want true", saved.Routing.NodeFilter.LongLived)
	}

	if got, want := saved.NodesFile, "preserved-nodes.txt"; got != want {
		t.Fatalf("saved nodes_file = %q, want preserved value %q", got, want)
	}
	if got, want := saved.DatabasePath, "preserved.db"; got != want {
		t.Fatalf("saved database_path = %q, want preserved value %q", got, want)
	}
	if saved.DNS.Enabled == nil || *saved.DNS.Enabled {
		t.Fatalf("saved DNS enabled = %#v, want false", saved.DNS.Enabled)
	}
	if got, want := saved.DNS.RemoteServers, []string{"https://dns.example.test/query"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("saved DNS remote servers = %#v, want %#v", got, want)
	}
	if got, want := saved.DNS.Detour, "node-pool"; got != want {
		t.Fatalf("saved DNS detour = %q, want %q", got, want)
	}
	if got, want := saved.DNS.Strategy, "ipv4_only"; got != want {
		t.Fatalf("saved DNS strategy = %q, want %q", got, want)
	}
	if len(saved.Nodes) != 1 || saved.Nodes[0].Name != "preserved-node" {
		t.Fatalf("saved nodes = %#v, want original non-settings node", saved.Nodes)
	}
}

func TestDispatchOwnsPrimaryInboundInLocalServerMode(t *testing.T) {
	cfg := &Config{
		Listener: ListenerConfig{
			Address: "127.0.0.1",
			Port:    22323,
		},
		LocalServer: LocalServerConfig{
			Enabled: true,
			Listen:  "127.0.0.1:22324",
		},
	}

	if !cfg.DispatchOwnsPrimaryInbound() {
		t.Fatal("Local Server did not claim the primary pool inbound")
	}
	if got, want := cfg.DispatchListen(), "127.0.0.1:22324"; got != want {
		t.Fatalf("DispatchListen() = %q, want Local Server listen %q", got, want)
	}
	if !cfg.DispatchEnabled() {
		t.Fatal("DispatchEnabled() = false with Local Server enabled")
	}
}
