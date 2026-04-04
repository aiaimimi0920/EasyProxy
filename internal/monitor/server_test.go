package monitor

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
