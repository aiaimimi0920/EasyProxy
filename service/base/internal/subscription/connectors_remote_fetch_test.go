package subscription

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

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

func TestZenProxyFetchRetriesTransientTransportError(t *testing.T) {
	requestCount := 0
	runtime := &connectorRuntimeManager{
		ctx: context.Background(),
		httpClient: &http.Client{Transport: connectorRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestCount++
			if got := req.Header.Get("Authorization"); got != "Bearer zen-key" {
				t.Fatalf("unexpected auth header on attempt %d: %q", requestCount, got)
			}
			if requestCount == 1 {
				return nil, connectorTimeoutError{}
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"count":1,"proxies":[{"id":"proxy-1","name":"HTTP Node","type":"http","outbound":{"type":"http","server":"proxy.example.com","server_port":8080}}]}`)),
				Request:    req,
			}, nil
		})},
	}

	fetched, err := runtime.fetchZenProxyConnectorSource(
		&config.Config{},
		RuntimeSource{ID: "zen", Kind: SourceKindConnector, Name: "Zen", Input: "https://zen.example.com"},
		zenProxyConnectorConfig{APIKey: "zen-key", Count: 1},
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("fetchZenProxyConnectorSource() error = %v", err)
	}
	if len(fetched) != 1 || requestCount != 2 {
		t.Fatalf("expected one fetched source after two attempts, got sources=%d attempts=%d", len(fetched), requestCount)
	}
}

func TestZenProxyFetchRetriesOnlyTransientStatuses(t *testing.T) {
	tests := []struct {
		name         string
		firstStatus  int
		wantAttempts int
		wantError    bool
	}{
		{name: "service unavailable", firstStatus: http.StatusServiceUnavailable, wantAttempts: 2},
		{name: "unauthorized", firstStatus: http.StatusUnauthorized, wantAttempts: 1, wantError: true},
		{name: "rate limited", firstStatus: http.StatusTooManyRequests, wantAttempts: 1, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requestCount++
				if requestCount == 1 {
					http.Error(w, http.StatusText(test.firstStatus), test.firstStatus)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"count":1,"proxies":[{"id":"proxy-1","name":"HTTP Node","type":"http","outbound":{"type":"http","server":"proxy.example.com","server_port":8080}}]}`))
			}))
			defer server.Close()

			runtime := newConnectorRuntimeManager(context.Background(), defaultLogger{}).(*connectorRuntimeManager)
			fetched, err := runtime.fetchZenProxyConnectorSource(
				&config.Config{},
				RuntimeSource{ID: "zen", Kind: SourceKindConnector, Name: "Zen", Input: server.URL},
				zenProxyConnectorConfig{APIKey: "zen-key", Count: 1},
				2*time.Second,
			)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError=%v", err, test.wantError)
			}
			if !test.wantError && len(fetched) != 1 {
				t.Fatalf("expected one fetched source, got %d", len(fetched))
			}
			if requestCount != test.wantAttempts {
				t.Fatalf("attempts = %d, want %d", requestCount, test.wantAttempts)
			}
		})
	}
}
