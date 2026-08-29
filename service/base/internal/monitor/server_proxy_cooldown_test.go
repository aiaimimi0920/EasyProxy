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
