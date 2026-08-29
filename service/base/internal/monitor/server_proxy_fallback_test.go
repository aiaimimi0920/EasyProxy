package monitor

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"easy_proxies/internal/config"
)

func TestHandleGatewayStatusReturnsReporterSnapshot(t *testing.T) {
	s := &Server{}
	s.SetGatewayReporter(gatewayReporterStub{status: map[string]any{
		"enabled":          true,
		"applied":          true,
		"listen":           "0.0.0.0:15001",
		"direct_fallbacks": 2,
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	rec := httptest.NewRecorder()
	s.handleGatewayStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["listen"] != "0.0.0.0:15001" || payload["direct_fallbacks"] != float64(2) {
		t.Fatalf("unexpected gateway status: %+v", payload)
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

func TestProxyCompatRegistrationRouteFailureKeepsSharedScopesEligible(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:           "route-a",
		Name:          "Route A",
		ListenAddress: "127.0.0.1",
		Port:          37301,
		NodeMode:      "shared-mode",
		DomainFamily:  "shared-route.example",
	})
	first.MarkInitialCheckDone(true)
	second := mgr.Register(NodeInfo{
		Tag:           "route-b",
		Name:          "Route B",
		ListenAddress: "127.0.0.1",
		Port:          37302,
		NodeMode:      "shared-mode",
		DomainFamily:  "shared-route.example",
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
		HostID:        "register-worker",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
		Metadata: map[string]string{
			"serviceKey": "register-service",
			"stage":      "registration",
		},
	})
	if err != nil {
		t.Fatalf("Marshal checkout request failed: %v", err)
	}

	checkoutRec := httptest.NewRecorder()
	s.handleProxyCheckout(checkoutRec, httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody)))
	if checkoutRec.Code != http.StatusOK {
		t.Fatalf("expected initial checkout status %d, got %d: %s", http.StatusOK, checkoutRec.Code, checkoutRec.Body.String())
	}
	var checkoutResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(checkoutRec.Body.Bytes(), &checkoutResp); err != nil {
		t.Fatalf("failed to decode initial checkout response: %v", err)
	}
	if checkoutResp.Result.Lease.Metadata["selectedNodeTag"] != "route-a" {
		t.Fatalf("expected route-a to be selected initially, got %+v", checkoutResp.Result.Lease.Metadata)
	}

	reportBody, err := json.Marshal(proxyCompatUsageReport{
		LeaseID:         checkoutResp.Result.Lease.ID,
		Success:         false,
		ErrorCode:       "run_exception: net::ERR_CONNECTION_CLOSED",
		ServiceKey:      "register-service",
		Stage:           "registration",
		FailureClass:    proxyCompatFailureClassRouteFailure,
		RouteConfidence: proxyCompatRouteConfidenceHigh,
	})
	if err != nil {
		t.Fatalf("Marshal report request failed: %v", err)
	}
	reportRec := httptest.NewRecorder()
	s.handleProxyReportUsage(reportRec, httptest.NewRequest(http.MethodPost, "/proxy/leases/report", bytes.NewReader(reportBody)))
	if reportRec.Code != http.StatusOK {
		t.Fatalf("expected report status %d, got %d: %s", http.StatusOK, reportRec.Code, reportRec.Body.String())
	}
	if feedback, ok := s.compatState().serviceFeedbackForNode("register-service", "route-a"); !ok || strings.TrimSpace(feedback.CooldownUntil) == "" {
		t.Fatalf("expected failed node to retain a hard service cooldown, got %+v", feedback)
	}

	var routeBSnapshot Snapshot
	for _, snap := range mgr.Snapshot() {
		if snap.Tag == "route-b" {
			routeBSnapshot = snap
			break
		}
	}
	sharedPenalty, sharedCooling := s.compatState().serviceFeedbackAggregateForSnapshot([]string{"register-service"}, routeBSnapshot)
	if sharedPenalty <= 0 {
		t.Fatal("expected shared route scopes to retain a soft penalty")
	}
	if sharedCooling {
		t.Fatal("expected one route failure not to hard-cool sibling nodes sharing mode/domain")
	}

	releaseRec := httptest.NewRecorder()
	releaseReq := httptest.NewRequest(http.MethodPost, "/proxy/leases/"+checkoutResp.Result.Lease.ID+"/release", nil)
	s.handleProxyLeaseItem(releaseRec, releaseReq)
	if releaseRec.Code != http.StatusOK {
		t.Fatalf("expected release status %d, got %d: %s", http.StatusOK, releaseRec.Code, releaseRec.Body.String())
	}

	nextRec := httptest.NewRecorder()
	s.handleProxyCheckout(nextRec, httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody)))
	if nextRec.Code != http.StatusOK {
		t.Fatalf("expected sibling route checkout status %d, got %d: %s", http.StatusOK, nextRec.Code, nextRec.Body.String())
	}
	var nextResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(nextRec.Body.Bytes(), &nextResp); err != nil {
		t.Fatalf("failed to decode sibling checkout response: %v", err)
	}
	if nextResp.Result.Lease.Metadata["selectedNodeTag"] != "route-b" {
		t.Fatalf("expected sibling route-b after route-a failure, got %+v", nextResp.Result.Lease.Metadata)
	}
}
