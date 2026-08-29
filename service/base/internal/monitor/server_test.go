package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/store"
)

type recordingRoutingController struct {
	applyCalls  int
	applyResult bool
	lastConfig  *config.Config
}

type localServerMonitorHarness struct {
	server   *Server
	profiles *profile.Manager
	config   *config.Config
	store    store.Store
}

type jsonTestResponse struct {
	Code int
	Body map[string]any
}

func newLocalServerMonitor(t *testing.T, username, password string, generation uint64) localServerMonitorHarness {
	return newLocalServerMonitorWithEnabled(t, username, password, generation, true)
}

func newLocalServerMonitorWithEnabled(t *testing.T, username, password string, generation uint64, enabled bool) localServerMonitorHarness {
	return newLocalServerMonitorWithStoreDecorator(t, username, password, generation, enabled, nil)
}

func newLocalServerMonitorWithStoreDecorator(t *testing.T, username, password string, generation uint64, enabled bool, decorate func(store.Store) store.Store) localServerMonitorHarness {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("mode: pool\nlistener:\n  address: 127.0.0.1\n  port: 22323\n  protocol: mixed\nmanagement: {}\nrouting: {}\nlocal_server: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Mode:       "pool",
		Listener:   config.ListenerConfig{Address: "127.0.0.1", Port: 22323, Protocol: config.InboundProtocolMixed, Username: username, Password: password},
		Management: config.ManagementConfig{Password: password},
		Routing:    config.RoutingConfig{Enabled: true, FinalPolicy: "PROXY"},
		LocalServer: config.LocalServerConfig{
			Enabled:              enabled,
			SharedRevision:       1,
			CredentialGeneration: generation,
			Auth:                 config.LocalServerAuthConfig{Username: username, Password: password},
		},
	}
	cfg.SetFilePath(configPath)
	profileStore := st
	if decorate != nil {
		profileStore = decorate(st)
	}
	profiles, err := profile.NewManager(ctx, cfg, profileStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(profiles.Close)
	monitorManager, err := NewManager(Config{Password: password})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(monitorManager.Stop)
	server := NewServer(Config{Password: password, ProxyUsername: username, ProxyPassword: password}, monitorManager, nil)
	server.SetConfig(cfg)
	server.SetStore(st)
	server.SetProfileManager(profiles)
	return localServerMonitorHarness{server: server, profiles: profiles, config: cfg, store: st}
}

func performJSONRequest(t *testing.T, server *Server, method, path string, body any, headers http.Header) jsonTestResponse {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	server.handler.ServeHTTP(rr, req)
	result := jsonTestResponse{Code: rr.Code, Body: map[string]any{}}
	if rr.Body.Len() != 0 {
		if err := json.Unmarshal(rr.Body.Bytes(), &result.Body); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func monitorRequestStatus(t *testing.T, listen, password string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+listen+"/api/nodes", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if password != "" {
		req.Header.Set("Authorization", password)
	}
	response, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET monitor listener %s: %v", listen, err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

type swappingNodeManager struct {
	server      *Server
	replacement NodeManager
	reloadCalls atomic.Int32
}

type swappingConnectorManager struct {
	server       *Server
	replacement  ConnectorManager
	createCalls  atomic.Int32
	refreshCalls atomic.Int32
}

func (m *swappingConnectorManager) ListConfigConnectors(context.Context) ([]config.ConnectorSourceConfig, error) {
	return nil, nil
}

func (m *swappingConnectorManager) CreateConnector(
	_ context.Context,
	connector config.ConnectorSourceConfig,
) (config.ConnectorSourceConfig, error) {
	m.createCalls.Add(1)
	if m.server != nil && m.replacement != nil {
		m.server.SetConnectorManager(m.replacement)
	}
	return connector, nil
}

func (m *swappingConnectorManager) UpdateConnector(
	_ context.Context,
	_ string,
	connector config.ConnectorSourceConfig,
) (config.ConnectorSourceConfig, error) {
	return connector, nil
}

func (m *swappingConnectorManager) DeleteConnector(context.Context, string) error { return nil }

func (m *swappingConnectorManager) SetConnectorEnabled(context.Context, string, bool) error {
	return nil
}

func (m *swappingConnectorManager) RefreshRuntimeSources(context.Context) error {
	m.refreshCalls.Add(1)
	return nil
}

func (m *swappingConnectorManager) RefreshPreferredEntryIPs(
	context.Context,
	string,
	PreferredIPRefreshOptions,
) (*PreferredIPRefreshResult, error) {
	return &PreferredIPRefreshResult{}, nil
}

func (m *swappingNodeManager) ListConfigNodes(context.Context) ([]config.NodeConfig, error) {
	return nil, nil
}

func (m *swappingNodeManager) CreateNode(_ context.Context, node config.NodeConfig) (config.NodeConfig, error) {
	return node, nil
}

func (m *swappingNodeManager) UpdateNode(_ context.Context, _ string, node config.NodeConfig) (config.NodeConfig, error) {
	return node, nil
}

func (m *swappingNodeManager) DeleteNode(context.Context, string) error { return nil }

func (m *swappingNodeManager) SetNodeEnabled(context.Context, string, bool) error {
	if m.server != nil && m.replacement != nil {
		m.server.SetNodeManager(m.replacement)
	}
	return nil
}

func (m *swappingNodeManager) TriggerReload(context.Context) error {
	m.reloadCalls.Add(1)
	return nil
}

func (r *recordingRoutingController) RoutingStatus() RoutingStatus {
	return RoutingStatus{}
}

func (r *recordingRoutingController) ApplyHot(cfg *config.Config) bool {
	r.applyCalls++
	if cfg != nil {
		cfg.RLock()
		r.lastConfig = cfg.Clone()
		cfg.RUnlock()
	}
	return r.applyResult
}

func appendCompatUsageHistory(state *proxyCompatState, records ...proxyCompatUsageRecord) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.usageRecords = append(state.usageRecords, records...)
}

type gatewayReporterStub struct {
	status any
}

func (s gatewayReporterStub) GatewayStatus() any { return s.status }
