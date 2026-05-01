package monitor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestWithAuthAllowsDirectManagementPassword(t *testing.T) {
	s := &Server{
		cfg:      Config{Password: "secret-password"},
		sessions: map[string]*Session{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	req.Header.Set("Authorization", "secret-password")
	rec := httptest.NewRecorder()

	called := false
	handler := s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	handler(rec, req)

	if !called {
		t.Fatal("expected handler to be called for direct management password auth")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestWithAuthAllowsBearerManagementPassword(t *testing.T) {
	s := &Server{
		cfg:      Config{Password: "secret-password"},
		sessions: map[string]*Session{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	req.Header.Set("Authorization", "Bearer secret-password")
	rec := httptest.NewRecorder()

	called := false
	handler := s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	handler(rec, req)

	if !called {
		t.Fatal("expected handler to be called for bearer management password auth")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestWithAuthAllowsBearerSessionToken(t *testing.T) {
	token := "session-token"
	s := &Server{
		cfg: Config{Password: "secret-password"},
		sessions: map[string]*Session{
			token: {
				Token:     token,
				CreatedAt: time.Now().Add(-time.Minute),
				ExpiresAt: time.Now().Add(time.Hour),
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	called := false
	handler := s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	handler(rec, req)

	if !called {
		t.Fatal("expected handler to be called for bearer session token auth")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
}

func TestWithAuthRejectsInvalidAuthorization(t *testing.T) {
	s := &Server{
		cfg:      Config{Password: "secret-password"},
		sessions: map[string]*Session{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	req.Header.Set("Authorization", "wrong-password")
	rec := httptest.NewRecorder()

	handler := s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestHandleNodesOnlyAvailableFilter(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	healthy := mgr.Register(NodeInfo{Tag: "healthy", Name: "beta-healthy"})
	healthy.MarkInitialCheckDone(true)

	unhealthy := mgr.Register(NodeInfo{Tag: "unhealthy", Name: "alpha-unhealthy"})
	unhealthy.MarkInitialCheckDone(false)

	blacklisted := mgr.Register(NodeInfo{Tag: "blacklisted", Name: "gamma-blacklisted"})
	blacklisted.MarkInitialCheckDone(true)
	blacklisted.Blacklist(time.Now().Add(time.Minute))

	s := &Server{mgr: mgr}
	req := httptest.NewRequest(http.MethodGet, "/api/nodes?only_available=1", nil)
	rec := httptest.NewRecorder()

	s.handleNodes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var payload struct {
		Nodes          []Snapshot `json:"nodes"`
		TotalNodes     int        `json:"total_nodes"`
		AllTotalNodes  int        `json:"all_total_nodes"`
		AvailableNodes int        `json:"available_nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if payload.TotalNodes != 1 {
		t.Fatalf("expected 1 returned node, got %d", payload.TotalNodes)
	}
	if payload.AllTotalNodes != 3 {
		t.Fatalf("expected 3 total nodes before filtering, got %d", payload.AllTotalNodes)
	}
	if payload.AvailableNodes != 1 {
		t.Fatalf("expected 1 available node, got %d", payload.AvailableNodes)
	}
	if len(payload.Nodes) != 1 || payload.Nodes[0].Tag != "healthy" {
		t.Fatalf("expected only healthy node to be returned, got %+v", payload.Nodes)
	}
}

func TestHandleNodesOnlyAvailableIncludesTrafficProvenNodes(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	probeHealthy := mgr.Register(NodeInfo{Tag: "probe-healthy", Name: "probe-healthy"})
	probeHealthy.MarkInitialCheckDone(true)

	trafficProven := mgr.Register(NodeInfo{Tag: "traffic-proven", Name: "traffic-proven"})
	trafficProven.MarkInitialCheckDone(false)
	trafficProven.RecordFailure(errors.New("tls handshake: EOF"), "www.google.com:443")
	trafficProven.RecordSuccess("api.openai.com:443")

	s := &Server{mgr: mgr}
	req := httptest.NewRequest(http.MethodGet, "/api/nodes?only_available=1", nil)
	rec := httptest.NewRecorder()

	s.handleNodes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var payload struct {
		Nodes               []Snapshot `json:"nodes"`
		AvailableNodes      int        `json:"available_nodes"`
		ProbeAvailableNodes int        `json:"probe_available_nodes"`
		TrafficProvenNodes  int        `json:"traffic_proven_nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if payload.AvailableNodes != 2 {
		t.Fatalf("expected 2 effective available nodes, got %d", payload.AvailableNodes)
	}
	if payload.ProbeAvailableNodes != 1 {
		t.Fatalf("expected 1 probe-available node, got %d", payload.ProbeAvailableNodes)
	}
	if payload.TrafficProvenNodes != 1 {
		t.Fatalf("expected 1 traffic-proven node, got %d", payload.TrafficProvenNodes)
	}
	if len(payload.Nodes) != 2 {
		t.Fatalf("expected 2 returned nodes, got %d", len(payload.Nodes))
	}
	if payload.Nodes[0].Tag != "probe-healthy" && payload.Nodes[1].Tag != "probe-healthy" {
		t.Fatalf("expected probe-healthy node in payload, got %+v", payload.Nodes)
	}
	if payload.Nodes[0].Tag != "traffic-proven" && payload.Nodes[1].Tag != "traffic-proven" {
		t.Fatalf("expected traffic-proven node in payload, got %+v", payload.Nodes)
	}
}

func TestHandleNodesPreferAvailableOrdering(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	unhealthy := mgr.Register(NodeInfo{Tag: "unhealthy", Name: "alpha-unhealthy"})
	unhealthy.MarkInitialCheckDone(false)

	healthy := mgr.Register(NodeInfo{Tag: "healthy", Name: "beta-healthy"})
	healthy.MarkInitialCheckDone(true)

	unchecked := mgr.Register(NodeInfo{Tag: "unchecked", Name: "charlie-unchecked"})

	s := &Server{mgr: mgr}
	req := httptest.NewRequest(http.MethodGet, "/api/nodes?prefer_available=1", nil)
	rec := httptest.NewRecorder()

	s.handleNodes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var payload struct {
		Nodes          []Snapshot `json:"nodes"`
		TotalNodes     int        `json:"total_nodes"`
		AvailableNodes int        `json:"available_nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if payload.TotalNodes != 3 {
		t.Fatalf("expected 3 returned nodes, got %d", payload.TotalNodes)
	}
	if payload.AvailableNodes != 1 {
		t.Fatalf("expected 1 available node, got %d", payload.AvailableNodes)
	}
	if len(payload.Nodes) != 3 {
		t.Fatalf("expected 3 nodes in payload, got %d", len(payload.Nodes))
	}
	if payload.Nodes[0].Tag != "healthy" {
		t.Fatalf("expected available node to be ordered first, got %q", payload.Nodes[0].Tag)
	}

	_ = unchecked
}

func TestHandleSourceSyncSourceHealthFiltersBySourceRef(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	healthy := mgr.Register(NodeInfo{
		Tag:        "zen-good",
		Name:       "Zen Good",
		SourceRef:  "manifest:conn_zenproxy_primary",
		SourceName: "ZenProxy Primary",
		SourceKind: "connector",
	})
	healthy.MarkInitialCheckDone(true)

	pending := mgr.Register(NodeInfo{
		Tag:        "zen-pending",
		Name:       "Zen Pending",
		SourceRef:  "manifest:conn_zenproxy_primary",
		SourceName: "ZenProxy Primary",
		SourceKind: "connector",
	})
	_ = pending

	other := mgr.Register(NodeInfo{
		Tag:        "other-good",
		Name:       "Other Good",
		SourceRef:  "manifest:aggregator-global",
		SourceName: "Aggregator Global",
		SourceKind: "proxy_uri",
	})
	other.MarkInitialCheckDone(true)

	s := &Server{mgr: mgr}
	req := httptest.NewRequest(http.MethodGet, "/api/source-sync/source-health?source_ref=manifest:conn_zenproxy_primary", nil)
	rec := httptest.NewRecorder()

	s.handleSourceSyncSourceHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Sources []SourceHealthState `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode source health response: %v", err)
	}

	if len(payload.Sources) != 1 {
		t.Fatalf("expected exactly one source in response, got %+v", payload.Sources)
	}
	state := payload.Sources[0]
	if state.Ref != "manifest:conn_zenproxy_primary" {
		t.Fatalf("unexpected source ref: %+v", state)
	}
	if state.TotalNodes != 2 || state.EffectiveAvailableNodes != 1 || state.PendingNodes != 1 {
		t.Fatalf("unexpected zenproxy source counts: %+v", state)
	}
	if state.ProbeAvailableNodes != 1 || state.BlacklistedNodes != 0 || state.UnavailableNodes != 0 {
		t.Fatalf("unexpected zenproxy source breakdown: %+v", state)
	}
}

func TestHandleSourceSyncSourceHealthNotFound(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	s := &Server{mgr: mgr}
	req := httptest.NewRequest(http.MethodGet, "/api/source-sync/source-health?ref=manifest:missing", nil)
	rec := httptest.NewRecorder()

	s.handleSourceSyncSourceHealth(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d: %s", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

func TestUpdateAllSettingsPropagatesSkipCertVerifyToManager(t *testing.T) {
	cfg := &config.Config{}
	cfg.Mode = "pool"
	cfg.LogLevel = "info"
	cfg.Listener.Address = "0.0.0.0"
	cfg.Listener.Port = 8080
	cfg.Listener.Protocol = "http"
	cfg.MultiPort.Address = "0.0.0.0"
	cfg.MultiPort.BasePort = 10000
	cfg.MultiPort.Protocol = "http"
	cfg.Pool.Mode = "auto"
	cfg.Pool.BlacklistDuration = time.Minute
	cfg.SubscriptionRefresh.Interval = time.Minute
	cfg.SubscriptionRefresh.Timeout = 30 * time.Second
	cfg.SubscriptionRefresh.HealthCheckTimeout = 30 * time.Second
	cfg.SubscriptionRefresh.DrainTimeout = 10 * time.Second
	cfg.SourceSync.RefreshInterval = time.Minute
	cfg.SourceSync.RequestTimeout = 30 * time.Second
	cfg.GeoIP.AutoUpdateInterval = time.Hour
	cfg.Management.HealthCheckInterval = time.Minute
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg.SetFilePath(configPath)

	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	s := &Server{
		cfg:    Config{},
		cfgSrc: cfg,
		mgr:    mgr,
		logger: log.New(io.Discard, "", 0),
	}

	req := allSettingsRequest{
		Mode:                          "pool",
		LogLevel:                      "info",
		ListenerAddress:               "0.0.0.0",
		ListenerPort:                  8080,
		ListenerProtocol:              "http",
		MultiPortAddress:              "0.0.0.0",
		MultiPortBasePort:             10000,
		MultiPortProtocol:             "http",
		PoolMode:                      "auto",
		PoolBlacklistDuration:         "1m",
		SubRefreshInterval:            "1m",
		SubRefreshTimeout:             "30s",
		SubRefreshHealthCheckTimeout:  "30s",
		SubRefreshDrainTimeout:        "10s",
		SourceSyncRefreshInterval:     "1m",
		SourceSyncRequestTimeout:      "30s",
		GeoIPAutoUpdateInterval:       "1h",
		ManagementHealthCheckInterval: "1m",
		SkipCertVerify:                true,
	}

	if err := s.updateAllSettings(req); err != nil {
		t.Fatalf("updateAllSettings() error = %v", err)
	}
	if !mgr.SkipCertVerify() {
		t.Fatal("expected manager skip_cert_verify to update immediately")
	}

	req.SkipCertVerify = false
	if err := s.updateAllSettings(req); err != nil {
		t.Fatalf("second updateAllSettings() error = %v", err)
	}
	if mgr.SkipCertVerify() {
		t.Fatal("expected manager skip_cert_verify to reflect latest settings update")
	}
}

func TestHandleSettingsReportsReloadRequirement(t *testing.T) {
	makeServer := func(t *testing.T, initialMode string, initialSkip bool) (*Server, *config.Config) {
		t.Helper()

		cfg := &config.Config{}
		cfg.Mode = initialMode
		cfg.LogLevel = "info"
		cfg.Listener.Address = "0.0.0.0"
		cfg.Listener.Port = 8080
		cfg.Listener.Protocol = "http"
		cfg.MultiPort.Address = "0.0.0.0"
		cfg.MultiPort.BasePort = 10000
		cfg.MultiPort.Protocol = "http"
		cfg.Pool.Mode = "auto"
		cfg.Pool.BlacklistDuration = time.Minute
		cfg.SubscriptionRefresh.Interval = time.Minute
		cfg.SubscriptionRefresh.Timeout = 30 * time.Second
		cfg.SubscriptionRefresh.HealthCheckTimeout = 30 * time.Second
		cfg.SubscriptionRefresh.DrainTimeout = 10 * time.Second
		cfg.SourceSync.Enabled = false
		cfg.SourceSync.ManifestURL = ""
		cfg.SourceSync.ManifestToken = ""
		cfg.SourceSync.RefreshInterval = time.Minute
		cfg.SourceSync.RequestTimeout = 30 * time.Second
		cfg.SourceSync.DefaultDirectProxyScheme = "http"
		cfg.GeoIP.AutoUpdateInterval = time.Hour
		cfg.Management.HealthCheckInterval = time.Minute
		cfg.Management.Listen = "0.0.0.0:9888"
		cfg.Management.ProbeTarget = ""
		cfg.Management.ProbeTargets = nil
		cfg.Management.Password = ""
		cfg.SkipCertVerify = initialSkip

		configPath := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		cfg.SetFilePath(configPath)

		mgr, err := NewManager(Config{})
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		s := &Server{
			cfg:    Config{},
			cfgSrc: cfg,
			mgr:    mgr,
			logger: log.New(io.Discard, "", 0),
		}
		return s, cfg
	}

	type testCase struct {
		name        string
		initialMode string
		initialSkip bool
		req         allSettingsRequest
		wantReload  bool
	}

	baseReq := allSettingsRequest{
		Mode:                          "pool",
		LogLevel:                      "info",
		ListenerAddress:               "0.0.0.0",
		ListenerPort:                  8080,
		ListenerProtocol:              "http",
		MultiPortAddress:              "0.0.0.0",
		MultiPortBasePort:             10000,
		MultiPortProtocol:             "http",
		PoolMode:                      "auto",
		PoolBlacklistDuration:         "1m",
		SubRefreshInterval:            "1m",
		SubRefreshTimeout:             "30s",
		SubRefreshHealthCheckTimeout:  "30s",
		SubRefreshDrainTimeout:        "10s",
		SourceSyncRefreshInterval:     "1m",
		SourceSyncRequestTimeout:      "30s",
		GeoIPAutoUpdateInterval:       "1h",
		ManagementListen:              "0.0.0.0:9888",
		ManagementHealthCheckInterval: "1m",
	}

	cases := []testCase{
		{
			name:        "skip cert verify only",
			initialMode: "pool",
			initialSkip: false,
			req: func() allSettingsRequest {
				r := baseReq
				r.SkipCertVerify = true
				return r
			}(),
			wantReload: false,
		},
		{
			name:        "mode change",
			initialMode: "pool",
			initialSkip: false,
			req: func() allSettingsRequest {
				r := baseReq
				r.Mode = "multi-port"
				return r
			}(),
			wantReload: true,
		},
		{
			name:        "management listen change",
			initialMode: "pool",
			initialSkip: false,
			req: func() allSettingsRequest {
				r := baseReq
				r.ManagementListen = "127.0.0.1:9999"
				return r
			}(),
			wantReload: true,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := makeServer(t, tt.initialMode, tt.initialSkip)
			body, err := json.Marshal(tt.req)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}

			req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
			rec := httptest.NewRecorder()

			s.handleSettings(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
			}

			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if got := payload["need_reload"]; got != tt.wantReload {
				t.Fatalf("expected need_reload=%v, got %v", tt.wantReload, got)
			}
		})
	}
}

func TestProxyCompatCheckoutLifecycle(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	healthy := mgr.Register(NodeInfo{
		Tag:           "preferred-node",
		Name:          "Preferred Node",
		ListenAddress: "127.0.0.1",
		Port:          34001,
		Region:        "jp",
		Country:       "Japan",
	})
	healthy.MarkInitialCheckDone(true)

	unhealthy := mgr.Register(NodeInfo{
		Tag:  "unhealthy-node",
		Name: "Unhealthy Node",
		Port: 34002,
	})
	unhealthy.MarkInitialCheckDone(false)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	checkoutReq := proxyCompatCheckoutRequest{
		HostID:        "register-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
	}
	checkoutBody, err := json.Marshal(checkoutReq)
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	req.Host = "easy-proxies-service:9888"
	rec := httptest.NewRecorder()
	s.handleProxyCheckout(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected checkout status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var checkoutResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &checkoutResp); err != nil {
		t.Fatalf("failed to decode checkout response: %v", err)
	}

	if checkoutResp.Result.Lease.ID == "" {
		t.Fatal("expected lease id to be populated")
	}
	if checkoutResp.Result.Lease.ProviderTypeKey != "easy-proxies" {
		t.Fatalf("unexpected provider type: %s", checkoutResp.Result.Lease.ProviderTypeKey)
	}
	if checkoutResp.Result.Lease.ProxyURL != "http://node-user:node-pass@easy-proxies-service:34001" {
		t.Fatalf("unexpected proxy url: %s", checkoutResp.Result.Lease.ProxyURL)
	}
	if checkoutResp.Result.Lease.Metadata["selectedNodeTag"] != "preferred-node" {
		t.Fatalf("unexpected selected node tag: %+v", checkoutResp.Result.Lease.Metadata)
	}

	reportReq := proxyCompatUsageReport{
		LeaseID:   checkoutResp.Result.Lease.ID,
		Success:   true,
		LatencyMs: 123,
	}
	reportBody, err := json.Marshal(reportReq)
	if err != nil {
		t.Fatalf("Marshal report request failed: %v", err)
	}

	reportRecorder := httptest.NewRecorder()
	s.handleProxyReportUsage(reportRecorder, httptest.NewRequest(http.MethodPost, "/proxy/leases/report", bytes.NewReader(reportBody)))
	if reportRecorder.Code != http.StatusOK {
		t.Fatalf("expected report status %d, got %d: %s", http.StatusOK, reportRecorder.Code, reportRecorder.Body.String())
	}

	readRecorder := httptest.NewRecorder()
	readReq := httptest.NewRequest(http.MethodGet, "/proxy/leases/"+checkoutResp.Result.Lease.ID, nil)
	s.handleProxyLeaseItem(readRecorder, readReq)
	if readRecorder.Code != http.StatusOK {
		t.Fatalf("expected read status %d, got %d: %s", http.StatusOK, readRecorder.Code, readRecorder.Body.String())
	}

	var readResp struct {
		Lease proxyCompatLease `json:"lease"`
	}
	if err := json.Unmarshal(readRecorder.Body.Bytes(), &readResp); err != nil {
		t.Fatalf("failed to decode read response: %v", err)
	}
	if readResp.Lease.Status != "active" {
		t.Fatalf("expected active lease, got %s", readResp.Lease.Status)
	}

	releaseRecorder := httptest.NewRecorder()
	releaseReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/"+checkoutResp.Result.Lease.ID+"/release", nil)
	s.handleProxyLeaseItem(releaseRecorder, releaseReq)
	if releaseRecorder.Code != http.StatusOK {
		t.Fatalf("expected release status %d, got %d: %s", http.StatusOK, releaseRecorder.Code, releaseRecorder.Body.String())
	}

	postReleaseRecorder := httptest.NewRecorder()
	postReleaseReq := httptest.NewRequest(http.MethodGet, "/proxy/leases/"+checkoutResp.Result.Lease.ID, nil)
	s.handleProxyLeaseItem(postReleaseRecorder, postReleaseReq)

	var postReleaseResp struct {
		Lease proxyCompatLease `json:"lease"`
	}
	if err := json.Unmarshal(postReleaseRecorder.Body.Bytes(), &postReleaseResp); err != nil {
		t.Fatalf("failed to decode released lease: %v", err)
	}
	if postReleaseResp.Lease.Status != "released" {
		t.Fatalf("expected released lease, got %s", postReleaseResp.Lease.Status)
	}
}

func TestProxyCompatRejectsUnsupportedProviderType(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	node := mgr.Register(NodeInfo{Tag: "healthy", Name: "Healthy Node", Port: 34001})
	node.MarkInitialCheckDone(true)

	s := &Server{
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	body, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:          "register-service",
		ProviderTypeKey: "official",
		ProvisionMode:   "reuse-only",
		BindingMode:     "shared-instance",
	})
	if err != nil {
		t.Fatalf("Marshal request failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(body))
	s.handleProxyCheckout(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d: %s", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
}

func TestProxyCompatFailureReportsReduceAvailabilityScore(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:           "a-node",
		Name:          "A Node",
		ListenAddress: "127.0.0.1",
		Port:          34001,
	})
	first.MarkInitialCheckDone(true)

	second := mgr.Register(NodeInfo{
		Tag:           "b-node",
		Name:          "B Node",
		ListenAddress: "127.0.0.1",
		Port:          34002,
	})
	second.MarkInitialCheckDone(true)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	cfg.Pool.FailureThreshold = 3
	cfg.Pool.BlacklistDuration = 24 * time.Hour
	s.SetConfig(cfg)

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "register-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	checkoutReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	checkoutReq.Host = "easy-proxy-service:9888"
	checkoutRec := httptest.NewRecorder()
	s.handleProxyCheckout(checkoutRec, checkoutReq)
	if checkoutRec.Code != http.StatusOK {
		t.Fatalf("expected checkout status %d, got %d: %s", http.StatusOK, checkoutRec.Code, checkoutRec.Body.String())
	}

	var checkoutResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(checkoutRec.Body.Bytes(), &checkoutResp); err != nil {
		t.Fatalf("failed to decode checkout response: %v", err)
	}
	if checkoutResp.Result.Lease.Metadata["selectedNodeTag"] != "a-node" {
		t.Fatalf("expected first node to be selected initially, got %+v", checkoutResp.Result.Lease.Metadata)
	}

	reportBody, err := json.Marshal(proxyCompatUsageReport{
		LeaseID:   checkoutResp.Result.Lease.ID,
		Success:   false,
		ErrorCode: "upstream-timeout",
	})
	if err != nil {
		t.Fatalf("Marshal report request failed: %v", err)
	}

	reportRec := httptest.NewRecorder()
	s.handleProxyReportUsage(reportRec, httptest.NewRequest(http.MethodPost, "/proxy/leases/report", bytes.NewReader(reportBody)))
	if reportRec.Code != http.StatusOK {
		t.Fatalf("expected report status %d, got %d: %s", http.StatusOK, reportRec.Code, reportRec.Body.String())
	}

	releaseRec := httptest.NewRecorder()
	releaseReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/"+checkoutResp.Result.Lease.ID+"/release", nil)
	s.handleProxyLeaseItem(releaseRec, releaseReq)
	if releaseRec.Code != http.StatusOK {
		t.Fatalf("expected release status %d, got %d: %s", http.StatusOK, releaseRec.Code, releaseRec.Body.String())
	}

	snapshots := mgr.Snapshot()
	var firstScore int
	var secondScore int
	for _, snap := range snapshots {
		switch snap.Tag {
		case "a-node":
			firstScore = snap.AvailabilityScore
		case "b-node":
			secondScore = snap.AvailabilityScore
		}
	}
	if firstScore >= secondScore {
		t.Fatalf("expected first node score to drop below second node score, got first=%d second=%d", firstScore, secondScore)
	}

	nextReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	nextReq.Host = "easy-proxy-service:9888"
	nextRec := httptest.NewRecorder()
	s.handleProxyCheckout(nextRec, nextReq)
	if nextRec.Code != http.StatusOK {
		t.Fatalf("expected second checkout status %d, got %d: %s", http.StatusOK, nextRec.Code, nextRec.Body.String())
	}

	var nextResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(nextRec.Body.Bytes(), &nextResp); err != nil {
		t.Fatalf("failed to decode second checkout response: %v", err)
	}
	if nextResp.Result.Lease.Metadata["selectedNodeTag"] != "b-node" {
		t.Fatalf("expected second node to be preferred after failure report, got %+v", nextResp.Result.Lease.Metadata)
	}
}

func TestProxyCompatRiskFailureCoolsNodeOnlyForReportingHost(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:           "risk-a",
		Name:          "Risk A",
		ListenAddress: "127.0.0.1",
		Port:          35001,
	})
	first.MarkInitialCheckDone(true)

	second := mgr.Register(NodeInfo{
		Tag:           "risk-b",
		Name:          "Risk B",
		ListenAddress: "127.0.0.1",
		Port:          35002,
	})
	second.MarkInitialCheckDone(true)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "register-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	checkoutReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	checkoutReq.Host = "easy-proxy-service:9888"
	checkoutRec := httptest.NewRecorder()
	s.handleProxyCheckout(checkoutRec, checkoutReq)
	if checkoutRec.Code != http.StatusOK {
		t.Fatalf("expected checkout status %d, got %d: %s", http.StatusOK, checkoutRec.Code, checkoutRec.Body.String())
	}

	var checkoutResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(checkoutRec.Body.Bytes(), &checkoutResp); err != nil {
		t.Fatalf("failed to decode checkout response: %v", err)
	}
	if checkoutResp.Result.Lease.Metadata["selectedNodeTag"] != "risk-a" {
		t.Fatalf("expected first node to be selected initially, got %+v", checkoutResp.Result.Lease.Metadata)
	}

	firstFailureAt := time.Now()
	reportBody, err := json.Marshal(proxyCompatUsageReport{
		LeaseID:   checkoutResp.Result.Lease.ID,
		Success:   false,
		ErrorCode: "eUdf5",
	})
	if err != nil {
		t.Fatalf("Marshal report request failed: %v", err)
	}

	reportRec := httptest.NewRecorder()
	s.handleProxyReportUsage(reportRec, httptest.NewRequest(http.MethodPost, "/proxy/leases/report", bytes.NewReader(reportBody)))
	if reportRec.Code != http.StatusOK {
		t.Fatalf("expected report status %d, got %d: %s", http.StatusOK, reportRec.Code, reportRec.Body.String())
	}

	releaseRec := httptest.NewRecorder()
	releaseReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/"+checkoutResp.Result.Lease.ID+"/release", nil)
	s.handleProxyLeaseItem(releaseRec, releaseReq)
	if releaseRec.Code != http.StatusOK {
		t.Fatalf("expected release status %d, got %d: %s", http.StatusOK, releaseRec.Code, releaseRec.Body.String())
	}

	snapshots := mgr.Snapshot()
	var firstSnap Snapshot
	var secondSnap Snapshot
	for _, snap := range snapshots {
		switch snap.Tag {
		case "risk-a":
			firstSnap = snap
		case "risk-b":
			secondSnap = snap
		}
	}
	if firstSnap.Blacklisted {
		t.Fatalf("expected eUdf5 to avoid global blacklist, got %+v", firstSnap)
	}
	if firstSnap.AvailabilityScore != secondSnap.AvailabilityScore {
		t.Fatalf("expected service-scoped eUdf5 to avoid global score penalty, got first=%d second=%d", firstSnap.AvailabilityScore, secondSnap.AvailabilityScore)
	}

	feedback, ok := s.compatState().serviceFeedbackForNode("register-service", "risk-a")
	if !ok {
		t.Fatal("expected service-scoped feedback for register-service / risk-a")
	}
	if feedback.ConsecutiveFailures != 1 {
		t.Fatalf("expected first eUdf5 to record one consecutive failure, got %+v", feedback)
	}
	firstCooldownUntil, err := time.Parse(time.RFC3339, feedback.CooldownUntil)
	if err != nil {
		t.Fatalf("expected cooldown timestamp to parse, got %v", err)
	}
	firstCooldown := firstCooldownUntil.Sub(firstFailureAt)
	if firstCooldown < 2*time.Minute || firstCooldown > 4*time.Minute {
		t.Fatalf("expected first eUdf5 local cooldown around 3 minutes, got %s", firstCooldown)
	}

	nextReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	nextReq.Host = "easy-proxy-service:9888"
	nextRec := httptest.NewRecorder()
	s.handleProxyCheckout(nextRec, nextReq)
	if nextRec.Code != http.StatusOK {
		t.Fatalf("expected second checkout status %d, got %d: %s", http.StatusOK, nextRec.Code, nextRec.Body.String())
	}

	var nextResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(nextRec.Body.Bytes(), &nextResp); err != nil {
		t.Fatalf("failed to decode second checkout response: %v", err)
	}
	if nextResp.Result.Lease.Metadata["selectedNodeTag"] != "risk-b" {
		t.Fatalf("expected same host to avoid risk-a during cooldown, got %+v", nextResp.Result.Lease.Metadata)
	}

	otherHostBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "quota-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
	})
	if err != nil {
		t.Fatalf("Marshal other host request failed: %v", err)
	}

	otherHostReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(otherHostBody))
	otherHostReq.Host = "easy-proxy-service:9888"
	otherHostRec := httptest.NewRecorder()
	s.handleProxyCheckout(otherHostRec, otherHostReq)
	if otherHostRec.Code != http.StatusOK {
		t.Fatalf("expected different host checkout status %d, got %d: %s", http.StatusOK, otherHostRec.Code, otherHostRec.Body.String())
	}

	var otherHostResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(otherHostRec.Body.Bytes(), &otherHostResp); err != nil {
		t.Fatalf("failed to decode different host checkout response: %v", err)
	}
	if otherHostResp.Result.Lease.Metadata["selectedNodeTag"] != "risk-a" {
		t.Fatalf("expected different host to keep using risk-a, got %+v", otherHostResp.Result.Lease.Metadata)
	}

	secondFailureAt := time.Now()
	s.applyProxyCompatUsageFeedback("risk-a", "register-service", proxyCompatUsageRecord{
		Success:         false,
		ErrorCode:       "eUdf5",
		ServiceKey:      "register-service",
		Stage:           "registration",
		FailureClass:    proxyCompatFailureClassBusinessRisk,
		RouteConfidence: proxyCompatRouteConfidenceMedium,
	})
	escalatedFeedback, ok := s.compatState().serviceFeedbackForNode("register-service", "risk-a")
	if !ok {
		t.Fatal("expected escalated service feedback for register-service / risk-a")
	}
	if escalatedFeedback.ConsecutiveFailures != 2 {
		t.Fatalf("expected second eUdf5 to escalate consecutive failures, got %+v", escalatedFeedback)
	}
	escalatedCooldownUntil, err := time.Parse(time.RFC3339, escalatedFeedback.CooldownUntil)
	if err != nil {
		t.Fatalf("expected escalated cooldown timestamp to parse, got %v", err)
	}
	escalatedCooldown := escalatedCooldownUntil.Sub(secondFailureAt)
	if escalatedCooldown < 2*time.Minute || escalatedCooldown > 4*time.Minute {
		t.Fatalf("expected second eUdf5 local cooldown to stay around 3 minutes before service-level cooling, got %s", escalatedCooldown)
	}
}

func appendCompatUsageHistory(state *proxyCompatState, records ...proxyCompatUsageRecord) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.usageRecords = append(state.usageRecords, records...)
}

func TestProxyCompatInfersRouteFailureFromRuntimeNetworkError(t *testing.T) {
	errorClass, confidence := inferProxyCompatFailureSemantics(
		"run_exception:WebDriverException: unknown error: net::ERR_CONNECTION_CLOSED",
	)
	if errorClass != proxyCompatFailureClassRouteFailure {
		t.Fatalf("expected route failure, got %q", errorClass)
	}
	if confidence != proxyCompatRouteConfidenceHigh {
		t.Fatalf("expected high confidence, got %q", confidence)
	}
}

func TestProxyCompatBusinessRiskServiceBaselineCoolsClearlyBadNode(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	bad := mgr.Register(NodeInfo{Tag: "bad-a", Name: "Bad A", ListenAddress: "127.0.0.1", Port: 36301})
	bad.MarkInitialCheckDone(true)
	good := mgr.Register(NodeInfo{Tag: "good-b", Name: "Good B", ListenAddress: "127.0.0.1", Port: 36302})
	good.MarkInitialCheckDone(true)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	history := make([]proxyCompatUsageRecord, 0, 15)
	for idx := 0; idx < 10; idx++ {
		history = append(history, proxyCompatUsageRecord{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "good-b",
			ReportedAt:      time.Now().Add(-time.Duration(idx+20) * time.Minute).Format(time.RFC3339),
			Success:         true,
			ServiceKey:      "business-service",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassNone,
		})
	}
	for idx := 0; idx < 5; idx++ {
		history = append(history, proxyCompatUsageRecord{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "bad-a",
			ReportedAt:      time.Now().Add(-time.Duration(idx+5) * time.Minute).Format(time.RFC3339),
			Success:         false,
			ErrorCode:       "sentinel rate limit",
			ServiceKey:      "business-service",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassBusinessRisk,
			RouteConfidence: proxyCompatRouteConfidenceMedium,
		})
	}
	appendCompatUsageHistory(s.compatState(), history...)

	s.applyProxyCompatUsageFeedback("bad-a", "register-service", proxyCompatUsageRecord{
		Success:         false,
		ErrorCode:       "sentinel rate limit",
		ServiceKey:      "business-service",
		Stage:           "registration",
		FailureClass:    proxyCompatFailureClassBusinessRisk,
		RouteConfidence: proxyCompatRouteConfidenceMedium,
	})

	if _, ok := s.compatState().serviceFeedbackForNode("business-service", "bad-a"); !ok {
		t.Fatal("expected service baseline to cool bad-a for business-service")
	}

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "other-host",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
		Metadata: map[string]string{
			"serviceKey": "business-service",
		},
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	req.Host = "easy-proxy-service:9888"
	rec := httptest.NewRecorder()
	s.handleProxyCheckout(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected checkout status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode checkout response: %v", err)
	}
	if resp.Result.Lease.Metadata["selectedNodeTag"] != "good-b" {
		t.Fatalf("expected business baseline to steer away from bad-a, got %+v", resp.Result.Lease.Metadata)
	}
}

func TestProxyCompatBusinessRiskDoesNotCoolNoisyServiceByAbsoluteFailures(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	bad := mgr.Register(NodeInfo{Tag: "bad-a", Name: "Bad A", ListenAddress: "127.0.0.1", Port: 36401})
	bad.MarkInitialCheckDone(true)
	peer := mgr.Register(NodeInfo{Tag: "peer-b", Name: "Peer B", ListenAddress: "127.0.0.1", Port: 36402})
	peer.MarkInitialCheckDone(true)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	history := make([]proxyCompatUsageRecord, 0, 10)
	for idx := 0; idx < 5; idx++ {
		history = append(history, proxyCompatUsageRecord{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "bad-a",
			ReportedAt:      time.Now().Add(-time.Duration(idx+10) * time.Minute).Format(time.RFC3339),
			Success:         false,
			ErrorCode:       "sentinel rate limit",
			ServiceKey:      "noisy-service",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassBusinessRisk,
			RouteConfidence: proxyCompatRouteConfidenceMedium,
		})
	}
	for idx := 0; idx < 4; idx++ {
		history = append(history, proxyCompatUsageRecord{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "peer-b",
			ReportedAt:      time.Now().Add(-time.Duration(idx+4) * time.Minute).Format(time.RFC3339),
			Success:         false,
			ErrorCode:       "sentinel rate limit",
			ServiceKey:      "noisy-service",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassBusinessRisk,
			RouteConfidence: proxyCompatRouteConfidenceMedium,
		})
	}
	history = append(history, proxyCompatUsageRecord{
		ID:              mustGenerateCompatID("usage"),
		SelectedNodeTag: "peer-b",
		ReportedAt:      time.Now().Add(-3 * time.Minute).Format(time.RFC3339),
		Success:         true,
		ServiceKey:      "noisy-service",
		Stage:           "registration",
		FailureClass:    proxyCompatFailureClassNone,
	})
	appendCompatUsageHistory(s.compatState(), history...)

	s.applyProxyCompatUsageFeedback("bad-a", "register-service", proxyCompatUsageRecord{
		Success:         false,
		ErrorCode:       "sentinel rate limit",
		ServiceKey:      "noisy-service",
		Stage:           "registration",
		FailureClass:    proxyCompatFailureClassBusinessRisk,
		RouteConfidence: proxyCompatRouteConfidenceMedium,
	})

	if _, ok := s.compatState().serviceFeedbackForNode("noisy-service", "bad-a"); ok {
		t.Fatal("did not expect noisy-service to service-cool bad-a from absolute failures alone")
	}
	if _, ok := s.compatState().serviceFeedbackForNode("register-service", "bad-a"); !ok {
		t.Fatal("expected reporting host to still get a short local avoidance window")
	}
}

func TestProxyCompatRegistrationRoutingPrefersLowSentinelNodes(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	bad := mgr.Register(NodeInfo{Tag: "bad-a", Name: "Bad A", ListenAddress: "127.0.0.1", Port: 36501})
	bad.MarkInitialCheckDone(true)
	good := mgr.Register(NodeInfo{Tag: "good-b", Name: "Good B", ListenAddress: "127.0.0.1", Port: 36502})
	good.MarkInitialCheckDone(true)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	history := []proxyCompatUsageRecord{
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "bad-a",
			ReportedAt:      time.Now().Add(-9 * time.Minute).Format(time.RFC3339),
			Success:         false,
			ErrorCode:       "blocked by sentinel rate limit",
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassBusinessRisk,
			RouteConfidence: proxyCompatRouteConfidenceMedium,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "bad-a",
			ReportedAt:      time.Now().Add(-8 * time.Minute).Format(time.RFC3339),
			Success:         false,
			ErrorCode:       "blocked by sentinel rate limit",
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassBusinessRisk,
			RouteConfidence: proxyCompatRouteConfidenceMedium,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "bad-a",
			ReportedAt:      time.Now().Add(-7 * time.Minute).Format(time.RFC3339),
			Success:         false,
			ErrorCode:       "blocked by sentinel rate limit",
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassBusinessRisk,
			RouteConfidence: proxyCompatRouteConfidenceMedium,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "bad-a",
			ReportedAt:      time.Now().Add(-6 * time.Minute).Format(time.RFC3339),
			Success:         true,
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassNone,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "good-b",
			ReportedAt:      time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
			Success:         true,
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassNone,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "good-b",
			ReportedAt:      time.Now().Add(-4 * time.Minute).Format(time.RFC3339),
			Success:         true,
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassNone,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "good-b",
			ReportedAt:      time.Now().Add(-3 * time.Minute).Format(time.RFC3339),
			Success:         true,
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassNone,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "good-b",
			ReportedAt:      time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
			Success:         true,
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassNone,
		},
	}
	appendCompatUsageHistory(s.compatState(), history...)

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "accio-register-2",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
		Metadata: map[string]string{
			"serviceKey": "accio-register",
			"stage":      "registration",
		},
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	req.Host = "easy-proxy-service:9888"
	rec := httptest.NewRecorder()
	s.handleProxyCheckout(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected checkout status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode checkout response: %v", err)
	}
	if resp.Result.Lease.Metadata["selectedNodeTag"] != "good-b" {
		t.Fatalf("expected accio-register routing to prefer low-sentinel node, got %+v", resp.Result.Lease.Metadata)
	}
}

func TestProxyCompatCheckoutAvoidsRecentSuccessfulNodeReuseWhenRequested(t *testing.T) {
	buildServer := func() *Server {
		mgr, err := NewManager(Config{})
		if err != nil {
			t.Fatalf("NewManager failed: %v", err)
		}

		hot := mgr.Register(NodeInfo{Tag: "hot-a", Name: "Hot A", ListenAddress: "127.0.0.1", Port: 36511})
		hot.MarkInitialCheckDone(true)
		fresh := mgr.Register(NodeInfo{Tag: "fresh-b", Name: "Fresh B", ListenAddress: "127.0.0.1", Port: 36512})
		fresh.MarkInitialCheckDone(true)

		s := &Server{
			cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
			mgr:         mgr,
			sessions:    map[string]*Session{},
			proxyCompat: newProxyCompatState(),
		}

		cfg := &config.Config{}
		cfg.Listener.Port = 2323
		cfg.Listener.Protocol = "http"
		cfg.Management.Listen = "0.0.0.0:9888"
		cfg.MultiPort.Protocol = "http"
		cfg.MultiPort.Username = "node-user"
		cfg.MultiPort.Password = "node-pass"
		cfg.Mode = "hybrid"
		s.SetConfig(cfg)

		appendCompatUsageHistory(
			s.compatState(),
			proxyCompatUsageRecord{
				ID:              mustGenerateCompatID("usage"),
				SelectedNodeTag: "hot-a",
				ReportedAt:      time.Now().Add(-4 * time.Minute).Format(time.RFC3339),
				Success:         true,
				ServiceKey:      "accio-register",
				Stage:           "registration",
				FailureClass:    proxyCompatFailureClassNone,
			},
			proxyCompatUsageRecord{
				ID:              mustGenerateCompatID("usage"),
				SelectedNodeTag: "hot-a",
				ReportedAt:      time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
				Success:         true,
				ServiceKey:      "accio-register",
				Stage:           "registration",
				FailureClass:    proxyCompatFailureClassNone,
			},
		)
		return s
	}

	checkoutNode := func(avoidRecentSuccessReuse bool) proxyCompatLease {
		s := buildServer()
		metadata := map[string]string{
			"serviceKey": "accio-register",
			"stage":      "registration",
		}
		if avoidRecentSuccessReuse {
			metadata["avoidRecentSuccessReuse"] = "true"
			metadata["recentSuccessReuseThreshold"] = "2"
			metadata["recentSuccessReuseWindowMinutes"] = "20"
		}
		checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
			HostID:        "accio-register-2",
			ProvisionMode: "reuse-only",
			BindingMode:   "shared-instance",
			Metadata:      metadata,
		})
		if err != nil {
			t.Fatalf("Marshal checkout request failed: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
		req.Host = "easy-proxy-service:9888"
		rec := httptest.NewRecorder()
		s.handleProxyCheckout(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected checkout status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
		}

		var resp struct {
			Result proxyCompatCheckoutResult `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode checkout response: %v", err)
		}
		return resp.Result.Lease
	}

	defaultLease := checkoutNode(false)
	if defaultLease.Metadata["selectedNodeTag"] != "hot-a" {
		t.Fatalf("expected default checkout to reuse recent winner, got %+v", defaultLease.Metadata)
	}

	avoidingLease := checkoutNode(true)
	if avoidingLease.Metadata["selectedNodeTag"] != "fresh-b" {
		t.Fatalf("expected opt-in reuse avoidance to prefer fresh node, got %+v", avoidingLease.Metadata)
	}
	if avoidingLease.Metadata["selectedNodeRecentSuccessPenalty"] != "0" {
		t.Fatalf("expected fresh node to have zero recent-success penalty, got %+v", avoidingLease.Metadata)
	}
}

func TestProxyCompatSentinelHotspotCoolsRegistrationServiceEarlier(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	bad := mgr.Register(NodeInfo{Tag: "bad-a", Name: "Bad A", ListenAddress: "127.0.0.1", Port: 36601})
	bad.MarkInitialCheckDone(true)
	good := mgr.Register(NodeInfo{Tag: "good-b", Name: "Good B", ListenAddress: "127.0.0.1", Port: 36602})
	good.MarkInitialCheckDone(true)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	history := []proxyCompatUsageRecord{
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "bad-a",
			ReportedAt:      time.Now().Add(-8 * time.Minute).Format(time.RFC3339),
			Success:         false,
			ErrorCode:       "blocked by sentinel rate limit",
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassBusinessRisk,
			RouteConfidence: proxyCompatRouteConfidenceMedium,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "bad-a",
			ReportedAt:      time.Now().Add(-7 * time.Minute).Format(time.RFC3339),
			Success:         false,
			ErrorCode:       "blocked by sentinel rate limit",
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassBusinessRisk,
			RouteConfidence: proxyCompatRouteConfidenceMedium,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "bad-a",
			ReportedAt:      time.Now().Add(-6 * time.Minute).Format(time.RFC3339),
			Success:         false,
			ErrorCode:       "blocked by sentinel rate limit",
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassBusinessRisk,
			RouteConfidence: proxyCompatRouteConfidenceMedium,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "good-b",
			ReportedAt:      time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
			Success:         true,
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassNone,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "good-b",
			ReportedAt:      time.Now().Add(-4 * time.Minute).Format(time.RFC3339),
			Success:         true,
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassNone,
		},
		{
			ID:              mustGenerateCompatID("usage"),
			SelectedNodeTag: "good-b",
			ReportedAt:      time.Now().Add(-3 * time.Minute).Format(time.RFC3339),
			Success:         true,
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassNone,
		},
	}
	appendCompatUsageHistory(s.compatState(), history...)

	s.applyProxyCompatUsageFeedback("bad-a", "accio-register-2", proxyCompatUsageRecord{
		Success:         false,
		ErrorCode:       "blocked by sentinel rate limit",
		ServiceKey:      "accio-register",
		Stage:           "registration",
		FailureClass:    proxyCompatFailureClassBusinessRisk,
		RouteConfidence: proxyCompatRouteConfidenceMedium,
	})

	if _, ok := s.compatState().serviceFeedbackForNode("accio-register", "bad-a"); !ok {
		t.Fatal("expected sentinel hotspot to create service-scoped cooldown for accio-register")
	}
}

func TestProxyCompatRiskFailureCoolsBusinessClusterOnlyForReportingHost(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:            "risk-a",
		Name:           "Risk A",
		ListenAddress:  "127.0.0.1",
		Port:           35101,
		ProtocolFamily: "vless",
		NodeMode:       "reality/tcp",
		DomainFamily:   "badcluster.example",
	})
	first.MarkInitialCheckDone(true)

	second := mgr.Register(NodeInfo{
		Tag:            "risk-b",
		Name:           "Risk B",
		ListenAddress:  "127.0.0.1",
		Port:           35102,
		ProtocolFamily: "vless",
		NodeMode:       "reality/tcp",
		DomainFamily:   "badcluster.example",
	})
	second.MarkInitialCheckDone(true)

	third := mgr.Register(NodeInfo{
		Tag:            "safe-c",
		Name:           "Safe C",
		ListenAddress:  "127.0.0.1",
		Port:           35103,
		ProtocolFamily: "vless",
		NodeMode:       "tls/ws",
		DomainFamily:   "goodcluster.example",
	})
	third.MarkInitialCheckDone(true)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "register-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	checkoutReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	checkoutReq.Host = "easy-proxy-service:9888"
	checkoutRec := httptest.NewRecorder()
	s.handleProxyCheckout(checkoutRec, checkoutReq)
	if checkoutRec.Code != http.StatusOK {
		t.Fatalf("expected checkout status %d, got %d: %s", http.StatusOK, checkoutRec.Code, checkoutRec.Body.String())
	}

	var checkoutResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(checkoutRec.Body.Bytes(), &checkoutResp); err != nil {
		t.Fatalf("failed to decode checkout response: %v", err)
	}
	if checkoutResp.Result.Lease.Metadata["selectedNodeTag"] != "risk-a" {
		t.Fatalf("expected first node to be selected initially, got %+v", checkoutResp.Result.Lease.Metadata)
	}

	reportBody, err := json.Marshal(proxyCompatUsageReport{
		LeaseID:   checkoutResp.Result.Lease.ID,
		Success:   false,
		ErrorCode: "eUdf5",
	})
	if err != nil {
		t.Fatalf("Marshal report request failed: %v", err)
	}

	reportRec := httptest.NewRecorder()
	s.handleProxyReportUsage(reportRec, httptest.NewRequest(http.MethodPost, "/proxy/leases/report", bytes.NewReader(reportBody)))
	if reportRec.Code != http.StatusOK {
		t.Fatalf("expected report status %d, got %d: %s", http.StatusOK, reportRec.Code, reportRec.Body.String())
	}

	modeFeedback, ok := s.compatState().serviceFeedbackForRef("register-service", proxyCompatServiceFeedbackRef{
		Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeNodeMode, "reality/tcp"),
		ScopeKind:  proxyCompatFeedbackScopeNodeMode,
		ScopeValue: "reality/tcp",
	})
	if !ok {
		t.Fatal("expected host-scoped node_mode feedback for register-service")
	}
	if modeFeedback.ScopeKind != proxyCompatFeedbackScopeNodeMode {
		t.Fatalf("expected node_mode scope feedback, got %+v", modeFeedback)
	}

	domainFeedback, ok := s.compatState().serviceFeedbackForRef("register-service", proxyCompatServiceFeedbackRef{
		Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeDomainFamily, "badcluster.example"),
		ScopeKind:  proxyCompatFeedbackScopeDomainFamily,
		ScopeValue: "badcluster.example",
	})
	if !ok {
		t.Fatal("expected host-scoped domain_family feedback for register-service")
	}
	if domainFeedback.ScopeKind != proxyCompatFeedbackScopeDomainFamily {
		t.Fatalf("expected domain_family scope feedback, got %+v", domainFeedback)
	}

	nextReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	nextReq.Host = "easy-proxy-service:9888"
	nextRec := httptest.NewRecorder()
	s.handleProxyCheckout(nextRec, nextReq)
	if nextRec.Code != http.StatusOK {
		t.Fatalf("expected second checkout status %d, got %d: %s", http.StatusOK, nextRec.Code, nextRec.Body.String())
	}

	var nextResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(nextRec.Body.Bytes(), &nextResp); err != nil {
		t.Fatalf("failed to decode second checkout response: %v", err)
	}
	if nextResp.Result.Lease.Metadata["selectedNodeTag"] != "safe-c" {
		t.Fatalf("expected same host to avoid the bad business cluster, got %+v", nextResp.Result.Lease.Metadata)
	}

	otherHostBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "quota-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
	})
	if err != nil {
		t.Fatalf("Marshal other host request failed: %v", err)
	}

	otherHostReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(otherHostBody))
	otherHostReq.Host = "easy-proxy-service:9888"
	otherHostRec := httptest.NewRecorder()
	s.handleProxyCheckout(otherHostRec, otherHostReq)
	if otherHostRec.Code != http.StatusOK {
		t.Fatalf("expected different host checkout status %d, got %d: %s", http.StatusOK, otherHostRec.Code, otherHostRec.Body.String())
	}

	var otherHostResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(otherHostRec.Body.Bytes(), &otherHostResp); err != nil {
		t.Fatalf("failed to decode different host checkout response: %v", err)
	}
	selectedOtherHostTag := otherHostResp.Result.Lease.Metadata["selectedNodeTag"]
	if selectedOtherHostTag != "risk-a" && selectedOtherHostTag != "risk-b" {
		t.Fatalf("expected different host to remain eligible for the original business cluster, got %+v", otherHostResp.Result.Lease.Metadata)
	}
}

func TestProxyCompatCheckoutFallsBackWhenNoEffectiveNodes(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:           "degraded-a",
		Name:          "Degraded A",
		ListenAddress: "127.0.0.1",
		Port:          36101,
	})
	first.MarkInitialCheckDone(false)

	second := mgr.Register(NodeInfo{
		Tag:           "degraded-b",
		Name:          "Degraded B",
		ListenAddress: "127.0.0.1",
		Port:          36102,
	})
	second.MarkInitialCheckDone(false)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "register-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	req.Host = "easy-proxy-service:9888"
	rec := httptest.NewRecorder()
	s.handleProxyCheckout(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected degraded checkout status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode degraded checkout response: %v", err)
	}
	if resp.Result.Lease.Metadata["selectedNodeSelectionTier"] != "degraded" {
		t.Fatalf("expected degraded selection tier, got %+v", resp.Result.Lease.Metadata)
	}
}

func TestProxyCompatCheckoutRejectsSharedPoolFallbackInHybridMode(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	portless := mgr.Register(NodeInfo{
		Tag:           "portless-a",
		Name:          "Portless A",
		ListenAddress: "127.0.0.1",
		Port:          0,
	})
	portless.MarkInitialCheckDone(true)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "register-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	req.Host = "easy-proxy-service:9888"
	rec := httptest.NewRecorder()
	s.handleProxyCheckout(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected checkout status %d, got %d: %s", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "NO_PROXY_PROVIDER_ROUTE") {
		t.Fatalf("expected NO_PROXY_PROVIDER_ROUTE response, got %s", rec.Body.String())
	}
}

func TestProxyCompatDegradedCheckoutAvoidsSameServiceActiveNode(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:           "degraded-a",
		Name:          "Degraded A",
		ListenAddress: "127.0.0.1",
		Port:          37101,
	})
	first.MarkInitialCheckDone(false)

	second := mgr.Register(NodeInfo{
		Tag:           "degraded-b",
		Name:          "Degraded B",
		ListenAddress: "127.0.0.1",
		Port:          37102,
	})
	second.MarkInitialCheckDone(false)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	s.compatState().storeLease(&proxyCompatLeaseState{
		Lease: proxyCompatLease{
			ID:     "active-register-a",
			HostID: "accio-register",
			Status: "active",
			Metadata: map[string]string{
				"serviceKey": "accio-register",
				"stage":      "registration",
			},
		},
		SelectedNodeTag: "degraded-a",
	})
	s.compatState().storeLease(&proxyCompatLeaseState{
		Lease: proxyCompatLease{
			ID:     "active-manager-b",
			HostID: "accio-manager",
			Status: "active",
			Metadata: map[string]string{
				"serviceKey": "accio-manager",
				"stage":      "quota_check",
			},
		},
		SelectedNodeTag: "degraded-b",
	})

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "accio-register-2",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
		Metadata: map[string]string{
			"serviceKey": "accio-register",
			"stage":      "registration",
		},
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	req.Host = "easy-proxy-service:9888"
	rec := httptest.NewRecorder()
	s.handleProxyCheckout(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected degraded checkout status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode degraded checkout response: %v", err)
	}
	if resp.Result.Lease.Metadata["selectedNodeTag"] != "degraded-b" {
		t.Fatalf("expected service-spread degraded checkout to avoid degraded-a, got %+v", resp.Result.Lease.Metadata)
	}
	if !strings.Contains(resp.Result.Lease.Metadata["selectedNodeSelectionTier"], "service-spread") {
		t.Fatalf("expected selection tier to note service spread, got %+v", resp.Result.Lease.Metadata)
	}
}

func TestProxyCompatCheckoutFallsBackWhenAllCandidatesAreCooling(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:           "cooling-a",
		Name:          "Cooling A",
		ListenAddress: "127.0.0.1",
		Port:          36201,
	})
	first.MarkInitialCheckDone(true)

	second := mgr.Register(NodeInfo{
		Tag:           "cooling-b",
		Name:          "Cooling B",
		ListenAddress: "127.0.0.1",
		Port:          36202,
	})
	second.MarkInitialCheckDone(true)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	decision := classifyProxyCompatUsageFailure("eUdf5")
	for _, snap := range mgr.Snapshot() {
		if snap.Tag != "cooling-a" && snap.Tag != "cooling-b" {
			continue
		}
		s.compatState().recordServiceFailureForSnapshot("register-service", snap, "eUdf5", decision)
	}

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "register-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	req.Host = "easy-proxy-service:9888"
	rec := httptest.NewRecorder()
	s.handleProxyCheckout(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected cooldown fallback checkout status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode cooldown fallback response: %v", err)
	}
	if resp.Result.Lease.Metadata["selectedNodeSelectionTier"] != "effective-cooldown-fallback" {
		t.Fatalf("expected cooldown fallback tier, got %+v", resp.Result.Lease.Metadata)
	}
}

func TestProxyCompatRegistrationConnectionClosedTripsDegradedServiceCooldown(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:           "closed-a",
		Name:          "Closed A",
		ListenAddress: "127.0.0.1",
		Port:          37201,
	})
	first.MarkInitialCheckDone(false)

	second := mgr.Register(NodeInfo{
		Tag:           "closed-b",
		Name:          "Closed B",
		ListenAddress: "127.0.0.1",
		Port:          37202,
	})
	second.MarkInitialCheckDone(false)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	errorCode := "run_exception:WebDriverException: unknown error: net::ERR_CONNECTION_CLOSED"
	for _, snap := range mgr.Snapshot() {
		if snap.Tag != "closed-a" && snap.Tag != "closed-b" {
			continue
		}
		s.applyProxyCompatUsageFeedback(snap.Tag, "accio-register", proxyCompatUsageRecord{
			Success:         false,
			ErrorCode:       errorCode,
			ServiceKey:      "accio-register",
			Stage:           "registration",
			FailureClass:    proxyCompatFailureClassRouteFailure,
			RouteConfidence: proxyCompatRouteConfidenceHigh,
		})
	}

	if _, ok := s.compatState().serviceFeedbackForNode("accio-register", "closed-a"); !ok {
		t.Fatal("expected accio-register to receive service-scoped cooldown for closed-a")
	}
	if _, ok := s.compatState().serviceFeedbackForNode("accio-register", "closed-b"); !ok {
		t.Fatal("expected accio-register to receive service-scoped cooldown for closed-b")
	}

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "accio-register-2",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
		Metadata: map[string]string{
			"serviceKey": "accio-register",
			"stage":      "registration",
		},
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	req.Host = "easy-proxy-service:9888"
	rec := httptest.NewRecorder()
	s.handleProxyCheckout(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected degraded accio-register checkout to fail once all service-cooled nodes are exhausted, got %d: %s", rec.Code, rec.Body.String())
	}

	otherBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "quota-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
		Metadata: map[string]string{
			"serviceKey": "accio-manager:quota_check",
			"stage":      "quota_check",
		},
	})
	if err != nil {
		t.Fatalf("Marshal other service request failed: %v", err)
	}

	otherReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(otherBody))
	otherReq.Host = "easy-proxy-service:9888"
	otherRec := httptest.NewRecorder()
	s.handleProxyCheckout(otherRec, otherReq)
	if otherRec.Code != http.StatusOK {
		t.Fatalf("expected non-registration service to retain degraded fallback, got %d: %s", otherRec.Code, otherRec.Body.String())
	}
}

func TestProxyCompatAuthFailureDoesNotPenalizeNode(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:           "auth-a",
		Name:          "Auth A",
		ListenAddress: "127.0.0.1",
		Port:          36001,
	})
	first.MarkInitialCheckDone(true)

	second := mgr.Register(NodeInfo{
		Tag:           "auth-b",
		Name:          "Auth B",
		ListenAddress: "127.0.0.1",
		Port:          36002,
	})
	second.MarkInitialCheckDone(true)

	s := &Server{
		cfg:         Config{ProxyUsername: "node-user", ProxyPassword: "node-pass"},
		mgr:         mgr,
		sessions:    map[string]*Session{},
		proxyCompat: newProxyCompatState(),
	}

	cfg := &config.Config{}
	cfg.Listener.Port = 2323
	cfg.Listener.Protocol = "http"
	cfg.Management.Listen = "0.0.0.0:9888"
	cfg.MultiPort.Protocol = "http"
	cfg.MultiPort.Username = "node-user"
	cfg.MultiPort.Password = "node-pass"
	cfg.Mode = "hybrid"
	s.SetConfig(cfg)

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "register-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	checkoutReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	checkoutReq.Host = "easy-proxy-service:9888"
	checkoutRec := httptest.NewRecorder()
	s.handleProxyCheckout(checkoutRec, checkoutReq)
	if checkoutRec.Code != http.StatusOK {
		t.Fatalf("expected checkout status %d, got %d: %s", http.StatusOK, checkoutRec.Code, checkoutRec.Body.String())
	}

	var checkoutResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(checkoutRec.Body.Bytes(), &checkoutResp); err != nil {
		t.Fatalf("failed to decode checkout response: %v", err)
	}
	if checkoutResp.Result.Lease.Metadata["selectedNodeTag"] != "auth-a" {
		t.Fatalf("expected first node to be selected initially, got %+v", checkoutResp.Result.Lease.Metadata)
	}

	reportBody, err := json.Marshal(proxyCompatUsageReport{
		LeaseID:   checkoutResp.Result.Lease.ID,
		Success:   false,
		ErrorCode: "401 NOT_LOGIN",
	})
	if err != nil {
		t.Fatalf("Marshal report request failed: %v", err)
	}

	reportRec := httptest.NewRecorder()
	s.handleProxyReportUsage(reportRec, httptest.NewRequest(http.MethodPost, "/proxy/leases/report", bytes.NewReader(reportBody)))
	if reportRec.Code != http.StatusOK {
		t.Fatalf("expected report status %d, got %d: %s", http.StatusOK, reportRec.Code, reportRec.Body.String())
	}

	releaseRec := httptest.NewRecorder()
	releaseReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/"+checkoutResp.Result.Lease.ID+"/release", nil)
	s.handleProxyLeaseItem(releaseRec, releaseReq)
	if releaseRec.Code != http.StatusOK {
		t.Fatalf("expected release status %d, got %d: %s", http.StatusOK, releaseRec.Code, releaseRec.Body.String())
	}

	snapshots := mgr.Snapshot()
	var firstScore int
	var secondScore int
	var firstBlacklisted bool
	for _, snap := range snapshots {
		switch snap.Tag {
		case "auth-a":
			firstScore = snap.AvailabilityScore
			firstBlacklisted = snap.Blacklisted
		case "auth-b":
			secondScore = snap.AvailabilityScore
		}
	}
	if firstBlacklisted {
		t.Fatalf("expected auth failure to avoid blacklisting route, got %+v", snapshots)
	}
	if firstScore != secondScore {
		t.Fatalf("expected auth failure to avoid route penalty, got first=%d second=%d", firstScore, secondScore)
	}
	if _, ok := s.compatState().serviceFeedbackForNode("register-service", "auth-a"); ok {
		t.Fatal("expected auth failure to avoid service-scoped cooldown feedback")
	}

	nextReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	nextReq.Host = "easy-proxy-service:9888"
	nextRec := httptest.NewRecorder()
	s.handleProxyCheckout(nextRec, nextReq)
	if nextRec.Code != http.StatusOK {
		t.Fatalf("expected second checkout status %d, got %d: %s", http.StatusOK, nextRec.Code, nextRec.Body.String())
	}

	var nextResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(nextRec.Body.Bytes(), &nextResp); err != nil {
		t.Fatalf("failed to decode second checkout response: %v", err)
	}
	if nextResp.Result.Lease.Metadata["selectedNodeTag"] != "auth-a" {
		t.Fatalf("expected same node to remain eligible after auth failure, got %+v", nextResp.Result.Lease.Metadata)
	}
}
