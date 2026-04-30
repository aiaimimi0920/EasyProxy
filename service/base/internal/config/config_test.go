package config

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestFetchSubscriptionNodesReusesFreshCache(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		_, _ = w.Write([]byte("ss://YWVzLTI1Ni1nY206c2VjcmV0QDE5OC41MS4xMDAuMTA6ODM4OA==#cached-node\n"))
	}))
	defer server.Close()

	cacheDir := filepath.Join(t.TempDir(), "subscription-cache")
	nodes, err := FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Hour)
	if err != nil {
		t.Fatalf("FetchSubscriptionNodes() first call error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node from first fetch, got %d", len(nodes))
	}

	nodes, err = FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Hour)
	if err != nil {
		t.Fatalf("FetchSubscriptionNodes() second call error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node from cached fetch, got %d", len(nodes))
	}
	if requestCount != 1 {
		t.Fatalf("expected remote subscription to be fetched once, got %d requests", requestCount)
	}
}

func TestFetchSubscriptionNodesFallsBackToStaleCacheOnFailure(t *testing.T) {
	shouldFail := false
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if shouldFail {
			http.Error(w, "temporarily blocked", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ss://YWVzLTI1Ni1nY206c2VjcmV0QDE5OC41MS4xMDAuMTA6ODM4OA==#stale-node\n"))
	}))
	defer server.Close()

	cacheDir := filepath.Join(t.TempDir(), "subscription-cache")
	nodes, err := FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Millisecond)
	if err != nil {
		t.Fatalf("FetchSubscriptionNodes() warm cache error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node from warm fetch, got %d", len(nodes))
	}

	time.Sleep(5 * time.Millisecond)
	shouldFail = true

	nodes, err = FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Millisecond)
	if err != nil {
		t.Fatalf("expected stale cache fallback, got error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected cached node on fallback, got %d", len(nodes))
	}
	if requestCount != 2 {
		t.Fatalf("expected two remote attempts (warm + failed refresh), got %d", requestCount)
	}
}

func TestFetchSubscriptionNodesCachesRecentFailures(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.Error(w, "temporarily blocked", http.StatusTooManyRequests)
	}))
	defer server.Close()

	cacheDir := filepath.Join(t.TempDir(), "subscription-cache")
	_, err := FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Hour)
	if err == nil {
		t.Fatal("expected initial fetch failure")
	}
	if requestCount != 1 {
		t.Fatalf("expected 1 remote attempt after initial failure, got %d", requestCount)
	}

	_, err = FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Hour)
	if err == nil {
		t.Fatal("expected cached failure to be surfaced")
	}
	if !strings.Contains(err.Error(), "cooling down") {
		t.Fatalf("expected failure cache cooldown error, got %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("expected recent failure cache to suppress refetch, got %d remote attempts", requestCount)
	}
}

func TestSubscriptionCacheDirResolvesRelativeDatabasePathAgainstConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		DatabasePath: filepath.Join("data", "runtime.db"),
	}
	cfg.SetFilePath(filepath.Join(dir, "config.yaml"))

	got := cfg.SubscriptionCacheDir()
	want := filepath.Join(dir, "data", "subscription-cache")
	if got != want {
		t.Fatalf("SubscriptionCacheDir() = %q, want %q", got, want)
	}
}
