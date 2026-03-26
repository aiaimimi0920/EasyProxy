package subscription

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"easy_proxies/internal/config"
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
				ID:     "remote-ech-runtime",
				Kind:   SourceKindProxyURI,
				Name:   "Remote ECH Runtime",
				Input:  "socks5://127.0.0.1:30000",
				Origin: "manifest",
			},
		},
	}

	cfg := &config.Config{
		Subscriptions: []string{"https://local.example.com/sub"},
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

	if len(fakeRuntime.got) != 1 || fakeRuntime.got[0].Kind != SourceKindConnector {
		t.Fatalf("connector runtime got unexpected sources: %#v", fakeRuntime.got)
	}
	if snapshot.LocalSourceCount != 1 {
		t.Fatalf("unexpected local source count: %d", snapshot.LocalSourceCount)
	}
	if snapshot.ManifestSourceCount != 3 {
		t.Fatalf("unexpected manifest source count: %d", snapshot.ManifestSourceCount)
	}
	if snapshot.ConnectorSourceCount != 1 {
		t.Fatalf("unexpected connector source count: %d", snapshot.ConnectorSourceCount)
	}
	if snapshot.ConnectorInstanceCount != 1 {
		t.Fatalf("unexpected connector instance count: %d", snapshot.ConnectorInstanceCount)
	}
	if len(snapshot.SubscriptionSources) != 2 {
		t.Fatalf("unexpected subscription source count: %d", len(snapshot.SubscriptionSources))
	}
	if len(snapshot.EphemeralProxySources) != 2 {
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

func TestBuildActiveSourceSnapshotIncludesLocalConnectorRuntimeSources(t *testing.T) {
	fakeRuntime := &fakeConnectorRuntime{
		returned: []RuntimeSource{
			{
				ID:     "local-ech-runtime",
				Kind:   SourceKindProxyURI,
				Name:   "Local ECH Runtime",
				Input:  "socks5://127.0.0.1:30010",
				Origin: "local",
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
	if len(fakeRuntime.got) != 1 || fakeRuntime.got[0].Kind != SourceKindConnector {
		t.Fatalf("connector runtime got unexpected local sources: %#v", fakeRuntime.got)
	}
}
