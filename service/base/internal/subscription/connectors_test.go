package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"gopkg.in/yaml.v3"
)

type fakeConnectorRuntime struct {
	got      []RuntimeSource
	returned []RuntimeSource
	err      error
}

func (f *fakeConnectorRuntime) Reconcile(_ *config.Config, sources []RuntimeSource) ([]RuntimeSource, error) {
	f.got = append([]RuntimeSource(nil), sources...)
	return append([]RuntimeSource(nil), f.returned...), f.err
}

func (f *fakeConnectorRuntime) StopAll() error {
	return nil
}

func TestRefreshPreferredEntryIPsRejectsStaleConfigAfterReload(t *testing.T) {
	t.Parallel()
	oldCfg := preferredIPTestConfig(t, "Old Template", "old-token")
	newCfg := preferredIPTestConfig(t, "Old Template", "new-token")

	selectionStarted := make(chan struct{})
	releaseSelection := make(chan struct{})
	manager := New(
		oldCfg,
		nil,
		WithConnectorRuntime(&fakeConnectorRuntime{}),
		withPreferredIPSelector(func(_ context.Context, _ string, _ config.ConnectorRuntimeConfig, _ config.ConnectorSourceConfig, _ monitor.PreferredIPRefreshOptions) ([]preferredIPResultRow, string, string, error) {
			close(selectionStarted)
			<-releaseSelection
			return []preferredIPResultRow{{IP: "198.41.132.114"}}, "artifacts", "result.csv", nil
		}),
	)

	refreshDone := make(chan error, 1)
	go func() {
		_, err := manager.RefreshPreferredEntryIPs(context.Background(), "Old Template", monitor.PreferredIPRefreshOptions{TopCount: 1})
		refreshDone <- err
	}()

	<-selectionStarted
	manager.OnConfigUpdate(newCfg)
	close(releaseSelection)

	err := <-refreshDone
	if !errors.Is(err, monitor.ErrConnectorConflict) {
		t.Fatalf("RefreshPreferredEntryIPs() error = %v, want connector conflict", err)
	}
	assertPreferredConnectors(t, oldCfg, nil)
	assertPreferredConnectors(t, newCfg, nil)
}

func TestRefreshPreferredEntryIPsWaitsForReloadIntentBeforeCommit(t *testing.T) {
	t.Parallel()
	cfg := preferredIPTestConfig(t, "ECH Template", "token")
	boxManager := boxmgr.New(cfg, monitor.Config{})
	selectionStarted := make(chan struct{})
	releaseSelection := make(chan struct{})
	manager := New(
		cfg,
		boxManager,
		WithConnectorRuntime(&fakeConnectorRuntime{}),
		withPreferredIPSelector(func(_ context.Context, _ string, _ config.ConnectorRuntimeConfig, _ config.ConnectorSourceConfig, _ monitor.PreferredIPRefreshOptions) ([]preferredIPResultRow, string, string, error) {
			close(selectionStarted)
			<-releaseSelection
			return []preferredIPResultRow{{IP: "198.41.132.114"}}, "artifacts", "result.csv", nil
		}),
	)

	refreshCtx, cancelRefresh := context.WithCancel(context.Background())
	defer cancelRefresh()
	refreshDone := make(chan error, 1)
	go func() {
		_, err := manager.RefreshPreferredEntryIPs(refreshCtx, "ECH Template", monitor.PreferredIPRefreshOptions{TopCount: 1})
		refreshDone <- err
	}()

	<-selectionStarted
	intent, err := boxManager.BeginReloadIntent(context.Background())
	if err != nil {
		t.Fatalf("BeginReloadIntent() error = %v", err)
	}
	defer intent.End()
	close(releaseSelection)

	select {
	case err := <-refreshDone:
		t.Fatalf("RefreshPreferredEntryIPs returned during reload intent: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	cfg.RLock()
	committed := len(cfg.Connectors) > 1
	cfg.RUnlock()
	if committed {
		t.Fatal("preferred connectors committed while reload intent was active")
	}

	cancelRefresh()
	manager.cancel()
	intent.End()
	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("RefreshPreferredEntryIPs did not finish after reload intent ended")
	}
	cfg.RLock()
	defer cfg.RUnlock()
	if len(cfg.Connectors) != 1 {
		t.Fatalf("connectors changed after canceled reload-guarded refresh: %#v", cfg.Connectors)
	}
}

func TestRefreshPreferredEntryIPsRollsBackMemoryWhenSaveFails(t *testing.T) {
	t.Parallel()
	cfg := preferredIPTestConfig(t, "ECH Template", "token")
	cfg.SetFilePath(t.TempDir())
	original := cloneConnectors(cfg.Connectors)
	manager := New(
		cfg,
		nil,
		WithConnectorRuntime(&fakeConnectorRuntime{}),
		withPreferredIPSelector(func(_ context.Context, _ string, _ config.ConnectorRuntimeConfig, _ config.ConnectorSourceConfig, _ monitor.PreferredIPRefreshOptions) ([]preferredIPResultRow, string, string, error) {
			return []preferredIPResultRow{{IP: "198.41.132.114"}}, "artifacts", "result.csv", nil
		}),
	)

	_, err := manager.RefreshPreferredEntryIPs(context.Background(), "ECH Template", monitor.PreferredIPRefreshOptions{TopCount: 1})
	if err == nil || !strings.Contains(err.Error(), "保存连接器配置失败") {
		t.Fatalf("RefreshPreferredEntryIPs() error = %v, want save failure", err)
	}

	cfg.RLock()
	got := cloneConnectors(cfg.Connectors)
	cfg.RUnlock()
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("connectors changed after save failure: got %#v, want %#v", got, original)
	}
}

func TestConnectorMutationsRollBackMemoryWhenSaveFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Manager) error
	}{
		{
			name: "create",
			mutate: func(manager *Manager) error {
				_, err := manager.CreateConnector(context.Background(), config.ConnectorSourceConfig{
					Name:          "New Connector",
					Input:         "https://new.example.com/connect",
					Enabled:       true,
					ConnectorType: connectorTypeECHWorker,
				})
				return err
			},
		},
		{
			name: "update",
			mutate: func(manager *Manager) error {
				_, err := manager.UpdateConnector(context.Background(), "ECH Template", config.ConnectorSourceConfig{
					Name:          "ECH Template",
					Input:         "https://updated.example.com/connect",
					Enabled:       true,
					ConnectorType: connectorTypeECHWorker,
				})
				return err
			},
		},
		{
			name: "delete",
			mutate: func(manager *Manager) error {
				return manager.DeleteConnector(context.Background(), "ECH Template")
			},
		},
		{
			name: "set enabled",
			mutate: func(manager *Manager) error {
				return manager.SetConnectorEnabled(context.Background(), "ECH Template", true)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := preferredIPTestConfig(t, "ECH Template", "token")
			cfg.SetFilePath(t.TempDir())
			original := cloneConnectors(cfg.Connectors)
			manager := New(cfg, nil, WithConnectorRuntime(&fakeConnectorRuntime{}))

			if err := test.mutate(manager); err == nil {
				t.Fatal("connector mutation unexpectedly succeeded")
			}

			cfg.RLock()
			got := cloneConnectors(cfg.Connectors)
			cfg.RUnlock()
			if !reflect.DeepEqual(got, original) {
				t.Fatalf("connectors changed after save failure: got %#v, want %#v", got, original)
			}
		})
	}
}

func TestCreateConnectorMutatesConfigInstalledDuringReloadIntent(t *testing.T) {
	t.Parallel()
	oldCfg := preferredIPTestConfig(t, "Old Template", "old-token")
	newCfg := preferredIPTestConfig(t, "New Template", "new-token")
	boxManager := boxmgr.New(oldCfg, monitor.Config{})
	manager := New(oldCfg, boxManager, WithConnectorRuntime(&fakeConnectorRuntime{}))

	intent, err := boxManager.BeginReloadIntent(context.Background())
	if err != nil {
		t.Fatalf("BeginReloadIntent() error = %v", err)
	}
	mutationDone := make(chan error, 1)
	go func() {
		_, err := manager.CreateConnector(context.Background(), config.ConnectorSourceConfig{
			Name:          "Created After Reload",
			Input:         "https://created.example.com/connect",
			Enabled:       true,
			ConnectorType: connectorTypeECHWorker,
		})
		mutationDone <- err
	}()

	select {
	case err := <-mutationDone:
		t.Fatalf("CreateConnector returned during reload intent: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	manager.OnConfigUpdate(newCfg)
	intent.End()
	select {
	case err := <-mutationDone:
		if err != nil {
			t.Fatalf("CreateConnector() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CreateConnector did not finish after reload intent ended")
	}

	oldCfg.RLock()
	oldHasCreated := connectorIndexByName(oldCfg.Connectors, "Created After Reload") >= 0
	oldCfg.RUnlock()
	if oldHasCreated {
		t.Fatal("CreateConnector mutated the retired config")
	}
	newCfg.RLock()
	newHasCreated := connectorIndexByName(newCfg.Connectors, "Created After Reload") >= 0
	newCfg.RUnlock()
	if !newHasCreated {
		t.Fatal("CreateConnector did not mutate the config installed during reload")
	}
}

func preferredIPTestConfig(t *testing.T, name, accessToken string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{
		Connectors: []config.ConnectorSourceConfig{
			{
				Name:          name,
				Input:         "https://ech.example.com/connect",
				Enabled:       false,
				TemplateOnly:  true,
				ConnectorType: connectorTypeECHWorker,
				ConnectorConfig: map[string]any{
					"access_token": accessToken,
				},
			},
		},
	}
	cfg.SetFilePath(path)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return cfg
}

func cloneConnectors(connectors []config.ConnectorSourceConfig) []config.ConnectorSourceConfig {
	cloned := make([]config.ConnectorSourceConfig, len(connectors))
	for index, connector := range connectors {
		cloned[index] = cloneConnectorConfig(connector)
	}
	return cloned
}

func assertPreferredConnectors(t *testing.T, cfg *config.Config, want []config.ConnectorSourceConfig) {
	t.Helper()
	cfg.RLock()
	defer cfg.RUnlock()
	var got []config.ConnectorSourceConfig
	for _, connector := range cfg.Connectors {
		if strings.HasPrefix(connector.Name, preferredConnectorNamePrefix("Old Template")) {
			got = append(got, cloneConnectorConfig(connector))
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preferred connectors = %#v, want %#v", got, want)
	}
}

func TestBuildECHWorkerConnectorSpec(t *testing.T) {
	cfg := &config.Config{
		SourceSync: config.SourceSyncConfig{
			ConnectorRuntime: config.ConnectorRuntimeConfig{
				ListenHost: "127.0.0.1",
			},
		},
	}

	source := RuntimeSource{
		ID:    "ech-1",
		Kind:  SourceKindConnector,
		Name:  "ECH SG",
		Input: "https://ech.example.com",
		Options: map[string]any{
			"connector_type": connectorTypeECHWorker,
			"connector_config": map[string]any{
				"local_protocol": "http",
				"access_token":   "token-123",
				"path":           "/connect",
				"proxy_ip":       "tw.william.us.ci",
				"server_ip":      "104.17.0.1",
			},
		},
	}

	spec, err := buildECHWorkerConnectorSpec(cfg, source, 0, "/usr/local/bin/ech-workers", "/tmp/connectors")
	if err != nil {
		t.Fatalf("buildECHWorkerConnectorSpec() error = %v", err)
	}

	if spec.Key != "ech-1" {
		t.Fatalf("unexpected key: %q", spec.Key)
	}
	if spec.DisplayName != "ECH SG" {
		t.Fatalf("unexpected display name: %q", spec.DisplayName)
	}
	if spec.LocalProtocol != "http" {
		t.Fatalf("unexpected local protocol: %q", spec.LocalProtocol)
	}
	if len(spec.Args) == 0 || spec.Args[0] != "-f" || spec.Args[1] != "ech.example.com:443/connect" {
		t.Fatalf("unexpected server args: %#v", spec.Args)
	}
}

func TestConnectorRuntimeManagerReconcileFetchesZenProxyClientSources(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if got := r.Header.Get("Authorization"); got != "Bearer zen-key" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		if got := r.URL.Query().Get("count"); got != "2" {
			t.Fatalf("unexpected count query: %q", got)
		}
		if got := r.URL.Query().Get("country"); got != "US" {
			t.Fatalf("unexpected country query: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count": 2,
			"proxies": []map[string]any{
				{
					"id":   "proxy-1",
					"name": "VMess Node",
					"type": "vmess",
					"outbound": map[string]any{
						"type":        "vmess",
						"server":      "vmess.example.com",
						"server_port": 443,
						"uuid":        "11111111-1111-1111-1111-111111111111",
						"alter_id":    0,
						"security":    "auto",
						"tls": map[string]any{
							"enabled":     true,
							"server_name": "edge.example.com",
							"insecure":    true,
						},
						"transport": map[string]any{
							"type": "ws",
							"path": "/ws",
							"headers": map[string]any{
								"Host": "edge.example.com",
							},
						},
					},
				},
				{
					"id":   "proxy-2",
					"name": "HTTP Node",
					"type": "http",
					"outbound": map[string]any{
						"type":        "http",
						"server":      "http.example.com",
						"server_port": 443,
						"username":    "alice",
						"password":    "secret",
						"tls": map[string]any{
							"enabled":     true,
							"server_name": "origin.example.com",
							"insecure":    true,
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	runtime := newConnectorRuntimeManager(context.Background(), defaultLogger{}).(*connectorRuntimeManager)
	cfg := &config.Config{
		SourceSync: config.SourceSyncConfig{
			RequestTimeout:           5 * time.Second,
			DefaultDirectProxyScheme: "http",
		},
	}

	sources := []RuntimeSource{
		{
			ID:     "manifest-zenproxy",
			Kind:   SourceKindConnector,
			Name:   "ZenProxy Provider",
			Input:  server.URL,
			Origin: "manifest",
			Options: map[string]any{
				"connector_type": connectorTypeZenProxyClient,
				"connector_config": map[string]any{
					"api_key": "zen-key",
					"count":   2,
					"country": "US",
				},
			},
		},
	}

	fetchedSources, fetchErr := runtime.fetchZenProxyRuntimeSources(cfg, sources)
	if fetchErr != nil {
		t.Fatalf("fetchZenProxyRuntimeSources() error = %v", fetchErr)
	}
	if len(fetchedSources) != 2 {
		t.Fatalf("expected 2 fetched runtime sources before reconcile, got %d", len(fetchedSources))
	}

	runtimeSources, err := runtime.Reconcile(cfg, sources)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if len(runtimeSources) != 2 {
		t.Fatalf("expected 2 runtime sources, got %d (requestCount=%d)", len(runtimeSources), requestCount)
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 ZenProxy fetch requests (direct + reconcile), got %d", requestCount)
	}
	if runtimeSources[0].Kind != SourceKindProxyURI || runtimeSources[1].Kind != SourceKindProxyURI {
		t.Fatalf("expected proxy uri runtime sources, got %#v", runtimeSources)
	}
	if runtimeSources[0].ID != "manifest-zenproxy" || runtimeSources[1].ID != "manifest-zenproxy" {
		t.Fatalf("expected shared provider source ref, got %#v", runtimeSources)
	}
	if !strings.HasPrefix(runtimeSources[0].Input, "vmess://") {
		t.Fatalf("expected vmess uri, got %q", runtimeSources[0].Input)
	}
	if !strings.HasPrefix(runtimeSources[1].Input, "http://alice:secret@http.example.com:443?") {
		t.Fatalf("expected http uri, got %q", runtimeSources[1].Input)
	}
	if got := extractStringOption(runtimeSources[0].Options, "connector_type"); got != connectorTypeZenProxyClient {
		t.Fatalf("unexpected connector type metadata: %q", got)
	}
	if got := extractStringOption(runtimeSources[0].Options, "connector_proxy_id"); got != "proxy-1" {
		t.Fatalf("unexpected connector proxy id metadata: %q", got)
	}
}

func TestSourceKeyKeepsDistinctConnectorConfigs(t *testing.T) {
	first := RuntimeSource{
		Kind:  SourceKindConnector,
		Input: "https://ech.example.com",
		Options: map[string]any{
			"connector_type": connectorTypeECHWorker,
			"connector_config": map[string]any{
				"access_token": "ech-token",
				"server_ip":    "198.41.132.114",
			},
		},
	}
	second := RuntimeSource{
		Kind:  SourceKindConnector,
		Input: "https://ech.example.com",
		Options: map[string]any{
			"connector_type": connectorTypeECHWorker,
			"connector_config": map[string]any{
				"access_token": "ech-token",
				"server_ip":    "198.41.140.152",
			},
		},
	}

	if sourceKey(first) == sourceKey(second) {
		t.Fatalf("expected distinct keys for different connector configs")
	}
}

func TestNormalizeManagedConnectorAcceptsZenProxyClient(t *testing.T) {
	connector, err := normalizeManagedConnector(config.ConnectorSourceConfig{
		Name:          "ZenProxy Provider",
		Input:         "https://zenproxy.top",
		ConnectorType: connectorTypeZenProxyClient,
		ConnectorConfig: map[string]any{
			"api_key": "demo-key",
			"count":   8,
		},
	})
	if err != nil {
		t.Fatalf("normalizeManagedConnector() error = %v", err)
	}

	if connector.ConnectorType != connectorTypeZenProxyClient {
		t.Fatalf("unexpected connector type: %q", connector.ConnectorType)
	}
	if extractStringOption(connector.ConnectorConfig, "api_key") != "demo-key" {
		t.Fatalf("expected api_key to be preserved, got %#v", connector.ConnectorConfig)
	}
	if extractIntOption(connector.ConnectorConfig, "count", 0) != 8 {
		t.Fatalf("expected count to be preserved, got %#v", connector.ConnectorConfig)
	}
}

func TestBuildActiveSourceSnapshotIncludesConnectorRuntimeSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer manifest-token" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(manifestResponse{
			Success: true,
			Sources: []manifestSource{
				{
					ID:      "remote-sub",
					Kind:    SourceKindSubscription,
					Name:    "Remote Sub",
					Enabled: true,
					Input:   "https://remote.example.com/sub",
				},
				{
					ID:      "remote-proxy",
					Kind:    SourceKindProxyURI,
					Name:    "Remote Proxy",
					Enabled: true,
					Input:   "http://user:pass@proxy.example.com:8080",
				},
				{
					ID:      "remote-ech",
					Kind:    SourceKindConnector,
					Name:    "Remote ECH",
					Enabled: true,
					Input:   "https://ech.example.com/connect",
					Options: map[string]any{
						"connector_type": connectorTypeECHWorker,
						"connector_config": map[string]any{
							"local_protocol": "socks5",
							"access_token":   "ech-token",
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	fakeRuntime := &fakeConnectorRuntime{
		returned: []RuntimeSource{
			{
				ID:     "local-ech-runtime",
				Kind:   SourceKindProxyURI,
				Name:   "Local ECH Runtime",
				Input:  "socks5://127.0.0.1:30000",
				Origin: "manifest",
			},
			{
				ID:     "remote-ech-runtime",
				Kind:   SourceKindProxyURI,
				Name:   "Remote ECH Runtime",
				Input:  "socks5://127.0.0.1:30001",
				Origin: "manifest",
			},
		},
	}

	cfg := &config.Config{
		Subscriptions: []string{"https://local.example.com/sub"},
		Connectors: []config.ConnectorSourceConfig{
			{
				Name:          "Local ECH Template",
				Input:         "https://local-ech.example.com/connect",
				Enabled:       true,
				ConnectorType: connectorTypeECHWorker,
				ConnectorConfig: map[string]any{
					"access_token":   "local-ech-token",
					"local_protocol": "socks5",
				},
			},
		},
		SourceSync: config.SourceSyncConfig{
			Enabled:                  true,
			ManifestURL:              server.URL,
			ManifestToken:            "manifest-token",
			DefaultDirectProxyScheme: "http",
		},
	}

	manager := New(cfg, nil, WithConnectorRuntime(fakeRuntime))
	snapshot, err := manager.buildActiveSourceSnapshot()
	if err != nil {
		t.Fatalf("buildActiveSourceSnapshot() error = %v", err)
	}

	if len(fakeRuntime.got) != 2 {
		t.Fatalf("connector runtime got unexpected sources: %#v", fakeRuntime.got)
	}
	if fakeRuntime.got[0].Origin != "local" || fakeRuntime.got[1].Origin != "manifest" {
		t.Fatalf("expected local connector precedence before manifest connectors, got %#v", fakeRuntime.got)
	}
	if snapshot.LocalSourceCount != 2 {
		t.Fatalf("unexpected local source count: %d", snapshot.LocalSourceCount)
	}
	if snapshot.ManifestSourceCount != 3 {
		t.Fatalf("unexpected manifest source count: %d", snapshot.ManifestSourceCount)
	}
	if snapshot.ConnectorSourceCount != 2 {
		t.Fatalf("unexpected connector source count: %d", snapshot.ConnectorSourceCount)
	}
	if snapshot.ConnectorInstanceCount != 2 {
		t.Fatalf("unexpected connector instance count: %d", snapshot.ConnectorInstanceCount)
	}
	if len(snapshot.SubscriptionSources) != 2 {
		t.Fatalf("unexpected subscription source count: %d", len(snapshot.SubscriptionSources))
	}
	if len(snapshot.EphemeralProxySources) != 3 {
		t.Fatalf("unexpected ephemeral proxy source count: %d", len(snapshot.EphemeralProxySources))
	}
}

func TestBootstrapRuntimeNodesMaterializesConnectorSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer manifest-token" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(manifestResponse{
			Success: true,
			Sources: []manifestSource{
				{
					ID:      "remote-ech",
					Kind:    SourceKindConnector,
					Name:    "Remote ECH",
					Enabled: true,
					Input:   "https://ech.example.com/connect",
					Options: map[string]any{
						"connector_type": connectorTypeECHWorker,
						"connector_config": map[string]any{
							"local_protocol": "socks5",
							"access_token":   "ech-token",
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	fakeRuntime := &fakeConnectorRuntime{
		returned: []RuntimeSource{
			{
				ID:     "remote-ech-runtime",
				Kind:   SourceKindProxyURI,
				Name:   "Remote ECH Runtime",
				Input:  "socks5://127.0.0.1:30000",
				Origin: "manifest",
			},
		},
	}

	cfg := &config.Config{
		SourceSync: config.SourceSyncConfig{
			Enabled:                  true,
			ManifestURL:              server.URL,
			ManifestToken:            "manifest-token",
			DefaultDirectProxyScheme: "http",
		},
	}

	manager := New(cfg, nil, WithConnectorRuntime(fakeRuntime))
	if err := manager.BootstrapRuntimeNodes(); err != nil {
		t.Fatalf("BootstrapRuntimeNodes() error = %v", err)
	}

	if len(cfg.Nodes) != 1 {
		t.Fatalf("unexpected node count after bootstrap: %d", len(cfg.Nodes))
	}
	if cfg.Nodes[0].URI != "socks5://127.0.0.1:30000" {
		t.Fatalf("unexpected bootstrapped uri: %q", cfg.Nodes[0].URI)
	}
	if cfg.Nodes[0].Source != config.NodeSourceManifest {
		t.Fatalf("unexpected node source: %q", cfg.Nodes[0].Source)
	}

	status := manager.SourceSyncStatus()
	if !status.ManifestHealthy {
		t.Fatalf("expected manifest to be healthy after bootstrap")
	}
	if status.ConnectorSourceCount != 1 || status.ConnectorInstanceCount != 1 {
		t.Fatalf("unexpected connector status: %#v", status)
	}
}

func TestBuildActiveSourceSnapshotPreservesDistinctConnectorVariants(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(manifestResponse{
			Success: true,
			Sources: []manifestSource{
				{
					ID:      "remote-ech-1",
					Kind:    SourceKindConnector,
					Name:    "Remote ECH 1",
					Enabled: true,
					Input:   "https://ech.example.com",
					Options: map[string]any{
						"connector_type": connectorTypeECHWorker,
						"connector_config": map[string]any{
							"access_token": "ech-token",
							"server_ip":    "198.41.132.114",
						},
					},
				},
				{
					ID:      "remote-ech-2",
					Kind:    SourceKindConnector,
					Name:    "Remote ECH 2",
					Enabled: true,
					Input:   "https://ech.example.com",
					Options: map[string]any{
						"connector_type": connectorTypeECHWorker,
						"connector_config": map[string]any{
							"access_token": "ech-token",
							"server_ip":    "198.41.140.152",
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	fakeRuntime := &fakeConnectorRuntime{
		returned: []RuntimeSource{
			{
				ID:     "remote-ech-runtime-1",
				Kind:   SourceKindProxyURI,
				Name:   "Remote ECH Runtime 1",
				Input:  "socks5://127.0.0.1:30000",
				Origin: "manifest",
			},
			{
				ID:     "remote-ech-runtime-2",
				Kind:   SourceKindProxyURI,
				Name:   "Remote ECH Runtime 2",
				Input:  "socks5://127.0.0.1:30001",
				Origin: "manifest",
			},
		},
	}

	cfg := &config.Config{
		SourceSync: config.SourceSyncConfig{
			Enabled:                  true,
			ManifestURL:              server.URL,
			DefaultDirectProxyScheme: "http",
		},
	}

	manager := New(cfg, nil, WithConnectorRuntime(fakeRuntime))
	snapshot, err := manager.buildActiveSourceSnapshot()
	if err != nil {
		t.Fatalf("buildActiveSourceSnapshot() error = %v", err)
	}

	if len(fakeRuntime.got) != 2 {
		t.Fatalf("expected 2 connector sources, got %d", len(fakeRuntime.got))
	}
	if snapshot.ManifestSourceCount != 2 {
		t.Fatalf("unexpected manifest source count: %d", snapshot.ManifestSourceCount)
	}
	if snapshot.ConnectorSourceCount != 2 {
		t.Fatalf("unexpected connector source count: %d", snapshot.ConnectorSourceCount)
	}
	if snapshot.ConnectorInstanceCount != 2 {
		t.Fatalf("unexpected connector instance count: %d", snapshot.ConnectorInstanceCount)
	}
	if len(snapshot.EphemeralProxySources) != 2 {
		t.Fatalf("unexpected ephemeral proxy source count: %d", len(snapshot.EphemeralProxySources))
	}
}

func TestBuildConnectorSpecsAutoFanoutSingleECHSource(t *testing.T) {
	binaryPath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}

	manager := &connectorRuntimeManager{
		ctx:         context.Background(),
		logger:      defaultLogger{},
		instances:   make(map[string]*connectorInstance),
		fanoutCache: make(map[string][]RuntimeSource),
		preferredIPSelector: func(_ context.Context, _ string, _ config.ConnectorRuntimeConfig, _ config.ConnectorSourceConfig, options monitor.PreferredIPRefreshOptions) ([]preferredIPResultRow, string, string, error) {
			if options.TopCount != 2 {
				t.Fatalf("unexpected top count: %d", options.TopCount)
			}
			return []preferredIPResultRow{
				{IP: "198.41.132.114"},
				{IP: "198.41.140.152"},
			}, "", "", nil
		},
	}

	cfg := &config.Config{
		SourceSync: config.SourceSyncConfig{
			ConnectorRuntime: config.ConnectorRuntimeConfig{
				ListenHost:       "127.0.0.1",
				BinaryPath:       binaryPath,
				WorkingDirectory: t.TempDir(),
				PreferredIP: config.PreferredIPGeneratorConfig{
					FanoutCount: 2,
				},
			},
		},
	}

	sources := []RuntimeSource{
		{
			ID:     "manifest-ech",
			Kind:   SourceKindConnector,
			Name:   "Manifest ECH",
			Input:  "https://ech.example.com",
			Origin: "manifest",
			Options: map[string]any{
				"connector_type": connectorTypeECHWorker,
				"connector_config": map[string]any{
					"access_token":   "ech-token",
					"local_protocol": "socks5",
				},
			},
		},
	}

	specs, err := manager.buildConnectorSpecs(cfg, sources)
	if err != nil {
		t.Fatalf("buildConnectorSpecs() error = %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("expected 2 specs after fanout, got %d", len(specs))
	}
	if !strings.Contains(strings.Join(specs[0].Args, " "), "-ip 198.41.132.114") {
		t.Fatalf("expected first spec to use preferred ip, got %#v", specs[0].Args)
	}
	if !strings.Contains(strings.Join(specs[1].Args, " "), "-ip 198.41.140.152") {
		t.Fatalf("expected second spec to use preferred ip, got %#v", specs[1].Args)
	}
}

func TestBuildActiveSourceSnapshotIncludesLocalConnectorRuntimeSources(t *testing.T) {
	fakeRuntime := &fakeConnectorRuntime{
		returned: []RuntimeSource{
			{
				ID:     "local-ech-runtime",
				Kind:   SourceKindProxyURI,
				Name:   "Local ECH Runtime",
				Input:  "socks5://127.0.0.1:30010",
				Origin: "manifest",
			},
		},
	}

	cfg := &config.Config{
		Connectors: []config.ConnectorSourceConfig{
			{
				Name:          "Local ECH Template",
				Input:         "https://ech.example.com",
				Enabled:       true,
				ConnectorType: connectorTypeECHWorker,
				ConnectorConfig: map[string]any{
					"access_token":   "ech-token",
					"local_protocol": "socks5",
				},
			},
		},
		SourceSync: config.SourceSyncConfig{
			DefaultDirectProxyScheme: "http",
		},
	}

	manager := New(cfg, nil, WithConnectorRuntime(fakeRuntime))
	snapshot, err := manager.buildActiveSourceSnapshot()
	if err != nil {
		t.Fatalf("buildActiveSourceSnapshot() error = %v", err)
	}

	if snapshot.LocalSourceCount != 1 {
		t.Fatalf("unexpected local source count: %d", snapshot.LocalSourceCount)
	}
	if snapshot.ConnectorSourceCount != 1 {
		t.Fatalf("unexpected connector source count: %d", snapshot.ConnectorSourceCount)
	}
	if snapshot.ConnectorInstanceCount != 1 {
		t.Fatalf("unexpected connector instance count: %d", snapshot.ConnectorInstanceCount)
	}
	if len(snapshot.EphemeralProxySources) != 1 {
		t.Fatalf("unexpected ephemeral proxy source count: %d", len(snapshot.EphemeralProxySources))
	}
	if len(fakeRuntime.got) != 1 || fakeRuntime.got[0].Origin != "local" {
		t.Fatalf("connector runtime got unexpected local sources: %#v", fakeRuntime.got)
	}
}

func TestSubscriptionNodesWithECHRemainSubscriptionNodes(t *testing.T) {
	subServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Join([]string{
			"vless://11111111-1111-1111-1111-111111111111@sub.example.com:443?encryption=none&security=tls&ech=cloudflare-ech.com%2Bhttps%3A%2F%2Fdns.alidns.com%2Fdns-query#subscription-ech",
		}, "\n")))
	}))
	defer subServer.Close()

	fakeRuntime := &fakeConnectorRuntime{}

	cfg := &config.Config{
		Subscriptions: []string{subServer.URL},
		SourceSync: config.SourceSyncConfig{
			DefaultDirectProxyScheme: "http",
		},
	}

	manager := New(cfg, nil, WithConnectorRuntime(fakeRuntime))
	snapshot, err := manager.buildActiveSourceSnapshot()
	if err != nil {
		t.Fatalf("buildActiveSourceSnapshot() error = %v", err)
	}
	if len(fakeRuntime.got) != 0 {
		t.Fatalf("subscription content should not enter connector runtime: %#v", fakeRuntime.got)
	}
	if len(snapshot.SubscriptionSources) != 1 {
		t.Fatalf("unexpected subscription source count: %d", len(snapshot.SubscriptionSources))
	}

	nodes, err := manager.fetchSubscriptionSources(snapshot.SubscriptionSources)
	if err != nil {
		t.Fatalf("fetchSubscriptionSources() error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("unexpected node count from subscription: %d", len(nodes))
	}
	if nodes[0].Source != config.NodeSourceSubscription {
		t.Fatalf("unexpected node source: %q", nodes[0].Source)
	}
	if !strings.Contains(nodes[0].URI, "ech=") {
		t.Fatalf("expected ordinary subscription node URI to retain ech parameter: %q", nodes[0].URI)
	}
}
