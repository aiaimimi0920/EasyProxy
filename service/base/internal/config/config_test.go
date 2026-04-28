package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsProxyURIRecognizesHTTPAndSOCKS5(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want bool
	}{
		{name: "http", uri: "http://alice:secret@example.com:8080", want: true},
		{name: "socks5", uri: "socks5://alice:secret@example.com:1080", want: true},
		{name: "vmess", uri: "vmess://example", want: true},
		{name: "invalid", uri: "ftp://example.com", want: false},
		{name: "html garbage", uri: "http://<meta property=\"og:type\" content=\"website\">", want: false},
	}

	for _, tt := range tests {
		if got := IsProxyURI(tt.uri); got != tt.want {
			t.Fatalf("%s: IsProxyURI(%q) = %v, want %v", tt.name, tt.uri, got, tt.want)
		}
	}
}

func TestParseSubscriptionContentSkipsGarbageHTTPLines(t *testing.T) {
	content := strings.TrimSpace(`
http://<meta property="og:type" content="website">
http://set: function setWithExpiry(key, value, ttl) {
http://alice:secret@example.com:8080/proxy
`)

	nodes, err := ParseSubscriptionContent(content)
	if err != nil {
		t.Fatalf("ParseSubscriptionContent() error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	if nodes[0].URI != "http://alice:secret@example.com:8080/proxy" {
		t.Fatalf("expected the valid proxy URI to survive, got %q", nodes[0].URI)
	}
}

func TestApplyDefaultsSetsNeutralProbeTargets(t *testing.T) {
	cfg := &Config{}

	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults() error = %v", err)
	}

	if cfg.Management.ProbeTarget != "" {
		t.Fatalf("expected single probe target to stay empty by default, got %q", cfg.Management.ProbeTarget)
	}
	if len(cfg.Management.ProbeTargets) == 0 {
		t.Fatal("expected default probe targets to be populated")
	}
	wantTargets := []string{
		"https://connectivitycheck.gstatic.com/generate_204",
		"https://cp.cloudflare.com/generate_204",
		"https://www.msftconnecttest.com/connecttest.txt",
		"https://www.google.com/generate_204",
		"https://www.google.com/robots.txt",
		"https://www.youtube.com/robots.txt",
	}
	for _, want := range wantTargets {
		found := false
		for _, target := range cfg.Management.ProbeTargets {
			if target == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected probe target %q in defaults, got %v", want, cfg.Management.ProbeTargets)
		}
	}
	if cfg.Pool.Mode != "auto" {
		t.Fatalf("unexpected default pool mode: %q", cfg.Pool.Mode)
	}
}

func TestNormalizeVLESSFlowCanonicalizesLegacyUDP443Variant(t *testing.T) {
	if got := NormalizeVLESSFlow("xtls-rprx-vision-udp443"); got != "xtls-rprx-vision" {
		t.Fatalf("expected legacy UDP443 flow to normalize, got %q", got)
	}
	if got := NormalizeVLESSFlow("xtls-rprx-vision"); got != "xtls-rprx-vision" {
		t.Fatalf("expected plain vision flow to remain unchanged, got %q", got)
	}
}

func TestParseSubscriptionContentParsesClashYAMLBeyondInitialHeader(t *testing.T) {
	content := strings.TrimSpace(`
port: 7890
socks-port: 7891
allow-lan: true
mode: rule
log-level: info
dns:
  enable: true
  ipv6: true
proxies:
  - {name: "Delayed Clash", server: "198.51.100.20", port: 8443, type: "vless", uuid: "11111111-1111-1111-1111-111111111111", tls: true, servername: "edge.example.com"}
`)

	nodes, err := ParseSubscriptionContent(content)
	if err != nil {
		t.Fatalf("ParseSubscriptionContent() error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	if !strings.HasPrefix(nodes[0].URI, "vless://") {
		t.Fatalf("expected parsed Clash YAML to produce a VLESS URI, got %q", nodes[0].URI)
	}
	if nodes[0].Name != "Delayed Clash" {
		t.Fatalf("expected Clash proxy name to be preserved, got %q", nodes[0].Name)
	}
}

func TestLoadForReloadIncludesNodesFile(t *testing.T) {
	dir := t.TempDir()
	nodesPath := filepath.Join(dir, "nodes.txt")
	configPath := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(nodesPath, []byte("http://alice:secret@example.com:8080/proxy\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(nodesPath) error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte("nodes_file: nodes.txt\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath) error = %v", err)
	}

	cfg, err := LoadForReload(configPath)
	if err != nil {
		t.Fatalf("LoadForReload() error = %v", err)
	}
	if len(cfg.Nodes) != 1 {
		t.Fatalf("expected 1 node from nodes_file on reload, got %d", len(cfg.Nodes))
	}
	if cfg.Nodes[0].URI != "http://alice:secret@example.com:8080/proxy" {
		t.Fatalf("unexpected nodes_file URI: %q", cfg.Nodes[0].URI)
	}
	if cfg.Nodes[0].Source != NodeSourceFile {
		t.Fatalf("expected nodes_file source, got %q", cfg.Nodes[0].Source)
	}
}
