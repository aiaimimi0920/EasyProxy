package subscription

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/store"
)

func TestFetchSubscriptionSourcesParsesClashYAML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(strings.TrimSpace(`
proxies:
  - {name: "Manifest Clash", server: "198.51.100.10", port: 8388, type: "ss", cipher: "aes-256-gcm", password: "secret-pass", udp: true}
`)))
	}))
	defer server.Close()

	manager := New(&config.Config{}, nil)
	sources := []RuntimeSource{
		{
			ID:     "manifest-sub",
			Kind:   SourceKindSubscription,
			Name:   "Aggregator Stable",
			Input:  server.URL,
			Origin: "manifest",
		},
	}

	nodes, err := manager.fetchSubscriptionSources(sources)
	if err != nil {
		t.Fatalf("fetchSubscriptionSources() error = %v", err)
	}

	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	if !strings.HasPrefix(nodes[0].URI, "ss://") {
		t.Fatalf("expected ss URI, got %q", nodes[0].URI)
	}
	if nodes[0].Source != config.NodeSourceManifest {
		t.Fatalf("expected manifest source, got %q", nodes[0].Source)
	}
	if strings.TrimSpace(nodes[0].Name) == "" {
		t.Fatalf("expected parsed node name to be preserved")
	}
}

func TestSyncRuntimeNodesToStorePersistsOnlyRuntimeSources(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "easyproxy.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	manager := New(&config.Config{}, nil, WithStore(dataStore))
	nodes := []config.NodeConfig{
		{
			Name:   "subscription-node",
			URI:    "ss://subscription-node#subscription",
			Source: config.NodeSourceSubscription,
		},
		{
			Name:   "manifest-node",
			URI:    "ss://manifest-node#manifest",
			Source: config.NodeSourceManifest,
		},
		{
			Name:   "manual-node",
			URI:    "ss://manual-node#manual",
			Source: config.NodeSourceManual,
		},
	}

	if err := manager.syncRuntimeNodesToStore(nodes); err != nil {
		t.Fatalf("syncRuntimeNodesToStore() error = %v", err)
	}

	storeNodes, err := dataStore.ListNodes(ctx, store.NodeFilter{})
	if err != nil {
		t.Fatalf("ListNodes() error = %v", err)
	}
	if len(storeNodes) != 2 {
		t.Fatalf("expected only runtime nodes to be persisted, got %d rows", len(storeNodes))
	}

	sources := make(map[string]string, len(storeNodes))
	for _, node := range storeNodes {
		sources[node.URI] = node.Source
	}
	if sources["ss://subscription-node#subscription"] != store.NodeSourceSubscription {
		t.Fatalf("expected subscription runtime node to persist with subscription source, got %+v", storeNodes)
	}
	if sources["ss://manifest-node#manifest"] != store.NodeSourceManifest {
		t.Fatalf("expected manifest runtime node to persist with manifest source, got %+v", storeNodes)
	}
	if _, exists := sources["ss://manual-node#manual"]; exists {
		t.Fatalf("did not expect manual node to be persisted by runtime sync, got %+v", storeNodes)
	}
}

func TestCreateNewConfigAssignsHybridPortsAndCredentials(t *testing.T) {
	manager := New(&config.Config{
		Mode: "hybrid",
		MultiPort: config.MultiPortConfig{
			BasePort: 31000,
			Username: "hybrid-user",
			Password: "hybrid-pass",
		},
		Nodes: []config.NodeConfig{
			{
				Name:   "local-inline",
				URI:    "ss://local-inline#local-inline",
				Source: config.NodeSourceInline,
			},
		},
	}, nil)

	newCfg := manager.createNewConfig([]config.NodeConfig{
		{
			Name:   "runtime-node",
			URI:    "ss://runtime-node#runtime-node",
			Source: config.NodeSourceManifest,
		},
	})

	if len(newCfg.Nodes) != 2 {
		t.Fatalf("expected 2 nodes in merged config, got %d", len(newCfg.Nodes))
	}

	if newCfg.Nodes[0].Port != 31000 || newCfg.Nodes[1].Port != 31001 {
		t.Fatalf("expected sequential hybrid ports starting at 31000, got %+v", newCfg.Nodes)
	}

	for _, node := range newCfg.Nodes {
		if node.Username != "hybrid-user" || node.Password != "hybrid-pass" {
			t.Fatalf("expected hybrid credentials to be applied to %q, got %+v", node.Name, node)
		}
	}
}

func TestBootstrapRuntimeNodesAssignsHybridPortsAndCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ss://YWVzLTI1Ni1nY206c2VjcmV0QDE5OC41MS4xMDAuMTA6ODM4OA==#bootstrap-node\n"))
	}))
	defer server.Close()

	cfg := &config.Config{
		Mode:          "hybrid",
		Subscriptions: []string{server.URL},
		MultiPort: config.MultiPortConfig{
			BasePort: 33000,
			Username: "bootstrap-user",
			Password: "bootstrap-pass",
		},
	}
	manager := New(cfg, nil)

	if err := manager.BootstrapRuntimeNodes(); err != nil {
		t.Fatalf("BootstrapRuntimeNodes() error = %v", err)
	}

	if len(cfg.Nodes) != 1 {
		t.Fatalf("expected 1 bootstrapped node, got %d", len(cfg.Nodes))
	}

	node := cfg.Nodes[0]
	if node.Port != 33000 {
		t.Fatalf("expected hybrid bootstrap port 33000, got %+v", node)
	}
	if node.Username != "bootstrap-user" || node.Password != "bootstrap-pass" {
		t.Fatalf("expected hybrid bootstrap credentials, got %+v", node)
	}
	if node.Source != config.NodeSourceSubscription {
		t.Fatalf("expected bootstrapped source to remain subscription, got %+v", node)
	}
}

func TestShouldStartImmediateRefreshSkipsAfterBootstrap(t *testing.T) {
	manager := New(&config.Config{
		SourceSync: config.SourceSyncConfig{
			Enabled:     true,
			ManifestURL: "https://example.com/manifest",
		},
	}, nil)

	if !manager.shouldStartImmediateRefresh() {
		t.Fatal("expected initial enabled manager to trigger immediate refresh")
	}

	manager.mu.Lock()
	manager.status.LastRefresh = time.Now()
	manager.mu.Unlock()

	if manager.shouldStartImmediateRefresh() {
		t.Fatal("expected bootstrapped manager to skip redundant immediate refresh")
	}
}

func TestBootstrapRuntimeNodesPreservesFallbackStatus(t *testing.T) {
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ss://YWVzLTI1Ni1nY206c2VjcmV0QDE5OC41MS4xMDAuMTA6ODM4OA==#fallback-node\n"))
	}))
	defer fallbackServer.Close()

	cfg := &config.Config{
		Mode: "hybrid",
		MultiPort: config.MultiPortConfig{
			BasePort: 33100,
		},
		SourceSync: config.SourceSyncConfig{
			Enabled:               true,
			ManifestURL:           "http://127.0.0.1:1/manifest/broken",
			RequestTimeout:        100 * time.Millisecond,
			FallbackSubscriptions: []string{fallbackServer.URL},
		},
	}
	manager := New(cfg, nil)

	if err := manager.BootstrapRuntimeNodes(); err != nil {
		t.Fatalf("BootstrapRuntimeNodes() error = %v", err)
	}

	status := manager.SourceSyncStatus()
	if status.ManifestHealthy {
		t.Fatalf("expected broken manifest to remain unhealthy, got %+v", status)
	}
	if !status.FallbackActive || status.FallbackSourceCount != 1 {
		t.Fatalf("expected fallback source status to be preserved, got %+v", status)
	}
	if status.LastError == "" {
		t.Fatalf("expected fallback bootstrap to retain manifest error, got %+v", status)
	}
	if len(cfg.Nodes) != 1 || cfg.Nodes[0].Port != 33100 {
		t.Fatalf("expected fallback bootstrap node with assigned hybrid port, got %+v", cfg.Nodes)
	}
}
