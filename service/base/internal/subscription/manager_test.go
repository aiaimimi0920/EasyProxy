package subscription

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/store"
)

type fakeRefreshReloadIntent struct {
	once sync.Once
	end  func()
}

func TestManualRefreshWaitTimeoutCoversAllRefreshStages(t *testing.T) {
	got := manualRefreshWaitTimeout(30*time.Second, 2*time.Minute, 30*time.Second)
	if want := 4 * time.Minute; got != want {
		t.Fatalf("manualRefreshWaitTimeout() = %v, want %v", got, want)
	}

	got = manualRefreshWaitTimeout(0, -time.Second, -time.Second)
	if want := 4 * time.Minute; got != want {
		t.Fatalf("manualRefreshWaitTimeout() defaults = %v, want %v", got, want)
	}

	got = manualRefreshWaitTimeout(maximumDuration, maximumDuration, maximumDuration)
	if got != maximumDuration {
		t.Fatalf("manualRefreshWaitTimeout() overflow result = %v, want %v", got, maximumDuration)
	}
}

func (i *fakeRefreshReloadIntent) End() {
	i.once.Do(i.end)
}

type fakeRefreshReloader struct {
	mu             sync.Mutex
	ephemeralNodes []config.NodeConfig
	beginCount     int
	endCount       int
	reloadCount    int
}

func (r *fakeRefreshReloader) BeginReloadIntent(context.Context) (refreshReloadIntent, error) {
	r.mu.Lock()
	r.beginCount++
	r.mu.Unlock()
	return &fakeRefreshReloadIntent{end: func() {
		r.mu.Lock()
		r.endCount++
		r.mu.Unlock()
	}}, nil
}

func (r *fakeRefreshReloader) CurrentPortMap() map[string]uint16 {
	return nil
}

func (r *fakeRefreshReloader) CurrentEphemeralNodes() []config.NodeConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]config.NodeConfig(nil), r.ephemeralNodes...)
}

func (r *fakeRefreshReloader) ReloadWithPortMapAndEphemeralNodes(
	_ *config.Config,
	_ map[string]uint16,
	ephemeralNodes []config.NodeConfig,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadCount++
	r.ephemeralNodes = append([]config.NodeConfig(nil), ephemeralNodes...)
	return nil
}

func (r *fakeRefreshReloader) snapshot() ([]config.NodeConfig, int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]config.NodeConfig(nil), r.ephemeralNodes...), r.beginCount, r.endCount, r.reloadCount
}

func TestDoRefreshRetryFetchFailureKeepsPublishedEphemeralNodes(t *testing.T) {
	oldEphemeral := []config.NodeConfig{{Name: "old-runtime", URI: "http://old-runtime.example:80"}}
	reloader := &fakeRefreshReloader{ephemeralNodes: append([]config.NodeConfig(nil), oldEphemeral...)}

	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "second fetch failed", http.StatusBadGateway)
	}))
	defer failingServer.Close()

	var manager *Manager
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		manager.OnConfigUpdate(refreshRetryTestConfig(failingServer.URL, "second"))
		_, _ = w.Write([]byte("http://127.0.0.1:18080#first-runtime\n"))
	}))
	defer firstServer.Close()

	manager = New(
		refreshRetryTestConfig(firstServer.URL, "first"),
		nil,
		WithConnectorRuntime(&fakeConnectorRuntime{}),
		withRefreshReloader(reloader),
	)
	manager.doRefresh()

	got, begins, ends, reloads := reloader.snapshot()
	if !reflect.DeepEqual(got, oldEphemeral) {
		t.Fatalf("published ephemeral nodes changed after retry fetch failure: got %+v, want %+v", got, oldEphemeral)
	}
	if begins != 1 || ends != 1 || reloads != 0 {
		t.Fatalf("reload boundary calls = begin:%d end:%d reload:%d, want 1/1/0", begins, ends, reloads)
	}
	if status := manager.Status(); !strings.Contains(status.LastError, "status 502") {
		t.Fatalf("refresh status error = %q, want second fetch failure", status.LastError)
	}
}

func TestDoRefreshRepeatedConfigChangesKeepPublishedEphemeralNodes(t *testing.T) {
	oldEphemeral := []config.NodeConfig{{Name: "old-runtime", URI: "http://old-runtime.example:80"}}
	reloader := &fakeRefreshReloader{ephemeralNodes: append([]config.NodeConfig(nil), oldEphemeral...)}

	var manager *Manager
	requestCount := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		nextURL := fmt.Sprintf("%s?revision=%d", server.URL, requestCount)
		manager.OnConfigUpdate(refreshRetryTestConfig(nextURL, fmt.Sprintf("revision-%d", requestCount)))
		_, _ = w.Write([]byte(fmt.Sprintf("http://127.0.0.1:%d#runtime-%d\n", 18080+requestCount, requestCount)))
	}))
	defer server.Close()

	manager = New(
		refreshRetryTestConfig(server.URL+"?revision=0", "initial"),
		nil,
		WithConnectorRuntime(&fakeConnectorRuntime{}),
		withRefreshReloader(reloader),
	)
	manager.doRefresh()

	got, begins, ends, reloads := reloader.snapshot()
	if !reflect.DeepEqual(got, oldEphemeral) {
		t.Fatalf("published ephemeral nodes changed after unstable retries: got %+v, want %+v", got, oldEphemeral)
	}
	if requestCount != 3 || begins != 3 || ends != 3 || reloads != 0 {
		t.Fatalf("retry calls = requests:%d begin:%d end:%d reload:%d, want 3/3/3/0", requestCount, begins, ends, reloads)
	}
	if status := manager.Status(); !strings.Contains(status.LastError, "configuration changed repeatedly") {
		t.Fatalf("refresh status error = %q, want repeated configuration change", status.LastError)
	}
}

func TestRuntimeNodeSetsEqualIgnoresOrderAndAssignedPorts(t *testing.T) {
	left := []config.NodeConfig{
		{Name: "one", URI: "http://one.example:80", Port: 24001, SourceKind: "subscription", SourceRef: "one"},
		{Name: "two", URI: "http://two.example:80", Port: 24002, SourceKind: "subscription", SourceRef: "two"},
	}
	right := []config.NodeConfig{
		{Name: "two", URI: "http://two.example:80", Port: 25010, SourceKind: "subscription", SourceRef: "two"},
		{Name: "one", URI: "http://one.example:80", Port: 25011, SourceKind: "subscription", SourceRef: "one"},
	}

	if !runtimeNodeSetsEqual(left, right) {
		t.Fatal("expected equivalent runtime node sets")
	}
	right[0].URI = "http://changed.example:80"
	if runtimeNodeSetsEqual(left, right) {
		t.Fatal("expected changed runtime node URI to require a reload")
	}
}

func TestDoRefreshSkipsReloadWhenRuntimeSourceSetIsUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("http://127.0.0.1:18080#stable-runtime\n"))
	}))
	defer server.Close()

	cfg := refreshRetryTestConfig(server.URL, "stable")
	cfg.SetFilePath(filepath.Join(t.TempDir(), "config.yaml"))
	reloader := &fakeRefreshReloader{}
	manager := New(
		cfg,
		nil,
		WithConnectorRuntime(&fakeConnectorRuntime{}),
		withRefreshReloader(reloader),
	)

	manager.doRefresh()
	manager.doRefresh()

	got, begins, ends, reloads := reloader.snapshot()
	if len(got) != 1 || got[0].Name != "stable-runtime" {
		t.Fatalf("published runtime nodes = %+v, want stable runtime node", got)
	}
	if begins != 2 || ends != 2 || reloads != 1 {
		t.Fatalf("reload boundary calls = begin:%d end:%d reload:%d, want 2/2/1", begins, ends, reloads)
	}
	if status := manager.Status(); status.LastError != "" || status.NodeCount != 1 {
		t.Fatalf("refresh status after no-op refresh = %+v", status)
	}
}

func refreshRetryTestConfig(subscriptionURL, revision string) *config.Config {
	return &config.Config{
		Mode:          "pool",
		Subscriptions: []string{subscriptionURL},
		SubscriptionRefresh: config.SubscriptionRefreshConfig{
			Enabled:  true,
			Timeout:  time.Second,
			Interval: time.Hour,
		},
		Routing: config.RoutingConfig{FinalPolicy: revision},
	}
}

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

func TestBaseConfigSnapshotWaitsForConfigWriter(t *testing.T) {
	cfg := &config.Config{Nodes: []config.NodeConfig{{URI: "ss://node#snapshot", Port: 31000}}}
	manager := New(cfg, nil)
	cfg.Lock()
	started := make(chan struct{})
	done := make(chan *config.Config, 1)
	go func() {
		close(started)
		done <- manager.baseConfigSnapshot()
	}()
	<-started

	select {
	case <-done:
		cfg.Unlock()
		t.Fatal("baseConfigSnapshot read config while the write lock was held")
	case <-time.After(50 * time.Millisecond):
	}

	cfg.Unlock()
	select {
	case snapshot := <-done:
		if snapshot == nil || snapshot.Nodes[0].URI != "ss://node#snapshot" {
			t.Fatalf("unexpected config snapshot: %+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("baseConfigSnapshot did not resume after the config write lock was released")
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
