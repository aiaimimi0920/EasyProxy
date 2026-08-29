package subscription

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"easy_proxies/internal/config"
)

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
