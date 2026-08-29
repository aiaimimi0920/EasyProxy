package config

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestFetchSubscriptionNodesRetriesHTTP1AfterHTTP2HeaderTimeout(t *testing.T) {
	fallbackRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackRequests++
		_, _ = w.Write([]byte("ss://YWVzLTI1Ni1nY206c2VjcmV0QDE5OC41MS4xMDAuMTA6ODM4OA==#h1-fallback\n"))
	}))
	defer server.Close()

	initialAttempts := 0
	failingHTTP2Client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			initialAttempts++
			return nil, errors.New("Get \"" + req.URL.String() + "\": http2: timeout awaiting response headers")
		}),
	}

	nodes, err := FetchSubscriptionNodesWithClient(server.URL, time.Second, "", 0, failingHTTP2Client)
	if err != nil {
		t.Fatalf("expected HTTP/1.1 fallback to recover subscription fetch, got error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node from HTTP/1.1 fallback, got %d", len(nodes))
	}
	if initialAttempts != 1 {
		t.Fatalf("expected one initial HTTP/2-like attempt, got %d", initialAttempts)
	}
	if fallbackRequests != 1 {
		t.Fatalf("expected one HTTP/1.1 fallback request, got %d", fallbackRequests)
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

func TestRoutingTakesOverPoolInbound(t *testing.T) {
	mk := func(enabled bool, listenerAddr string, listenerPort uint16, routingListen string) *Config {
		c := &Config{}
		c.Listener.Address = listenerAddr
		c.Listener.Port = listenerPort
		c.Routing.Enabled = enabled
		c.Routing.Listen = routingListen
		return c
	}

	tests := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{
			name: "routing disabled never takes over",
			cfg:  mk(false, "0.0.0.0", 22323, ""),
			want: false,
		},
		{
			name: "route A: default listen takes over same port",
			cfg:  mk(true, "0.0.0.0", 22323, ""),
			want: true,
		},
		{
			name: "route A: empty host equals 0.0.0.0",
			cfg:  mk(true, "", 22323, ""),
			want: true,
		},
		{
			name: "route A: explicit routing.listen matching port",
			cfg:  mk(true, "0.0.0.0", 22323, "0.0.0.0:22323"),
			want: true,
		},
		{
			name: "route B: different port coexists",
			cfg:  mk(true, "0.0.0.0", 22323, "0.0.0.0:22324"),
			want: false,
		},
	}

	for _, tt := range tests {
		if got := tt.cfg.RoutingTakesOverPoolInbound(); got != tt.want {
			t.Fatalf("%s: RoutingTakesOverPoolInbound() = %v, want %v", tt.name, got, tt.want)
		}
	}
}
