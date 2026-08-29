package monitor

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"easy_proxies/internal/config"
)

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

func TestProxyCompatRegistrationLoginBlockedUsesServiceCooldownWithoutGlobalPenalty(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:           "login-a",
		Name:          "Login A",
		ListenAddress: "127.0.0.1",
		Port:          36101,
		NodeMode:      "reality/tcp",
		DomainFamily:  "shared-login.example",
	})
	first.MarkInitialCheckDone(true)

	second := mgr.Register(NodeInfo{
		Tag:           "login-b",
		Name:          "Login B",
		ListenAddress: "127.0.0.1",
		Port:          36102,
		NodeMode:      "reality/tcp",
		DomainFamily:  "shared-login.example",
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
		Metadata: map[string]string{
			"serviceKey": "register-service",
			"stage":      "registration",
		},
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
	if checkoutResp.Result.Lease.Metadata["selectedNodeTag"] != "login-a" {
		t.Fatalf("expected first node to be selected initially, got %+v", checkoutResp.Result.Lease.Metadata)
	}

	reportBody, err := json.Marshal(proxyCompatUsageReport{
		LeaseID:         checkoutResp.Result.Lease.ID,
		Success:         false,
		ErrorCode:       "proxy route failure blocked https://platform.openai.com/login status=403",
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
		case "login-a":
			firstScore = snap.AvailabilityScore
			firstBlacklisted = snap.Blacklisted
		case "login-b":
			secondScore = snap.AvailabilityScore
		}
	}
	if firstBlacklisted {
		t.Fatalf("expected login blocked failure to avoid global blacklisting, got %+v", snapshots)
	}
	if firstScore != secondScore {
		t.Fatalf("expected login blocked failure to avoid global route penalty, got first=%d second=%d", firstScore, secondScore)
	}
	feedback, ok := s.compatState().serviceFeedbackForNode("register-service", "login-a")
	if !ok {
		t.Fatal("expected service-scoped cooldown feedback for register-service / login-a")
	}
	if feedback.LastErrorClass != "route:login_blocked" {
		t.Fatalf("expected route:login_blocked feedback class, got %+v", feedback)
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
	if nextResp.Result.Lease.Metadata["selectedNodeTag"] != "login-b" {
		t.Fatalf("expected registration service to avoid cooled login node, got %+v", nextResp.Result.Lease.Metadata)
	}

	otherBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "quota-service",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
		Metadata: map[string]string{
			"serviceKey": "quota-service",
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
		t.Fatalf("expected other service checkout to succeed, got %d: %s", otherRec.Code, otherRec.Body.String())
	}

	var otherResp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(otherRec.Body.Bytes(), &otherResp); err != nil {
		t.Fatalf("failed to decode other service checkout response: %v", err)
	}
	if otherResp.Result.Lease.Metadata["selectedNodeTag"] != "login-a" {
		t.Fatalf("expected other service to keep login-a eligible, got %+v", otherResp.Result.Lease.Metadata)
	}
}
