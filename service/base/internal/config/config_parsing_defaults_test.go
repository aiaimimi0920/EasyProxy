package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
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
	if got, want := cfg.Management.Listen, "127.0.0.1:29888"; got != want {
		t.Fatalf("unexpected default management listen: got %q want %q", got, want)
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

func TestApplyDefaultsUsesConservativeRefreshIntervals(t *testing.T) {
	cfg := &Config{}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults() error = %v", err)
	}
	if got, want := cfg.SubscriptionRefresh.Interval, 24*time.Hour; got != want {
		t.Fatalf("subscription refresh interval = %v, want %v", got, want)
	}
	if got, want := cfg.SourceSync.RefreshInterval, time.Hour; got != want {
		t.Fatalf("source sync refresh interval = %v, want %v", got, want)
	}
}

func TestNormalizeVLESSFlowCanonicalizesLegacyUDP443Variant(t *testing.T) {
	if got := NormalizeVLESSFlow("xtls-rprx-vision-udp443"); got != "xtls-rprx-vision" {
		t.Fatalf("expected legacy UDP443 flow to normalize, got %q", got)
	}
	if got := NormalizeVLESSFlow("xtls-rprx-vision-udp443-udp443"); got != "xtls-rprx-vision" {
		t.Fatalf("expected repeated legacy UDP443 flow to normalize, got %q", got)
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

func TestParseSubscriptionContentParsesClashYAMLShadowsocksObfsPlugin(t *testing.T) {
	content := strings.TrimSpace(`
proxies:
  - name: "Glados SS"
    type: ss
    server: b497b27.r8.glados-config.net
    port: 2377
    cipher: chacha20-ietf-poly1305
    password: t0srmdxrm3xyjnvqz9ewlxb2myq7rjuv
    plugin: obfs
    plugin-opts:
      mode: tls
      host: b497b27.default.microsoft.lt:100531
`)

	nodes, err := ParseSubscriptionContent(content)
	if err != nil {
		t.Fatalf("ParseSubscriptionContent() error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	if !strings.HasPrefix(nodes[0].URI, "ss://") {
		t.Fatalf("expected parsed Clash YAML to produce an SS URI, got %q", nodes[0].URI)
	}
	if !strings.Contains(nodes[0].URI, "plugin=obfs-local") {
		t.Fatalf("expected shadowsocks plugin to normalize to obfs-local, got %q", nodes[0].URI)
	}
	if !strings.Contains(nodes[0].URI, "plugin-opts=") ||
		!strings.Contains(nodes[0].URI, "obfs%3Dtls") ||
		!strings.Contains(nodes[0].URI, "obfs-host%3Db497b27.default.microsoft.lt%3A100531") {
		t.Fatalf("expected plugin opts to preserve obfs mode/host, got %q", nodes[0].URI)
	}
}

func TestParseSubscriptionContentAcceptsUnpaddedURLSafeBase64(t *testing.T) {
	baseURI := "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none#"
	rawURI := ""
	encoded := ""
	for padding := 0; padding < 3 && !strings.ContainsAny(encoded, "-_"); padding++ {
		for candidate := rune(0x80); candidate < 0x1000; candidate++ {
			rawURI = baseURI + strings.Repeat("x", padding) + string(candidate)
			encoded = base64.RawURLEncoding.EncodeToString([]byte(rawURI + "\n"))
			if strings.ContainsAny(encoded, "-_") {
				break
			}
		}
	}
	if rawURI == "" || !strings.ContainsAny(encoded, "-_") {
		t.Fatal("failed to construct a URL-safe base64 fixture")
	}

	nodes, err := ParseSubscriptionContent(encoded)
	if err != nil {
		t.Fatalf("ParseSubscriptionContent() error = %v", err)
	}
	if len(nodes) != 1 || nodes[0].URI != rawURI {
		t.Fatalf("unexpected URL-safe base64 parse result: %#v", nodes)
	}
}

func TestParseClashYAMLPreservesExtendedProtocolOptions(t *testing.T) {
	content := `proxies:
  - name: vmess-grpc
    type: vmess
    server: vmess.example.com
    port: 443
    uuid: 11111111-1111-1111-1111-111111111111
    cipher: auto
    network: grpc
    tls: true
    alpn: [h2, http/1.1]
    packet-encoding: xudp
    grpc-opts:
      grpc-service-name: vmess-service
  - name: trojan-grpc
    type: trojan
    server: trojan.example.com
    port: 443
    password: secret
    network: grpc
    alpn: [h2]
    grpc-opts:
      grpc-service-name: trojan-service
  - name: hysteria2-extended
    type: hysteria2
    server: hysteria.example.com
    port: 443
    password: p@ss
    alpn: [h3]
    up-mbps: 20
    down-mbps: 80
    obfs: salamander
    obfs-password: obfs-secret
`

	nodes, err := ParseSubscriptionContent(content)
	if err != nil {
		t.Fatalf("ParseSubscriptionContent() error = %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}

	for index, expectations := range [][]string{
		{"type=grpc", "serviceName=vmess-service", "packetEncoding=xudp", "alpn=h2%2Chttp%2F1.1"},
		{"type=grpc", "serviceName=trojan-service", "alpn=h2"},
		{"p%40ss@", "upMbps=20", "downMbps=80", "obfs=salamander", "obfs-password=obfs-secret", "alpn=h3"},
	} {
		for _, expected := range expectations {
			if !strings.Contains(nodes[index].URI, expected) {
				t.Fatalf("node %d URI missing %q: %q", index, expected, nodes[index].URI)
			}
		}
	}
}
