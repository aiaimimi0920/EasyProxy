package monitor

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
