package subscription

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
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
		ctx:       context.Background(),
		logger:    defaultLogger{},
		instances: make(map[string]*connectorInstance),
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
				ListenHost: "127.0.0.1",
				BinaryPath: binaryPath,
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
