package monitor

import (
	"encoding/json"
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
