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
