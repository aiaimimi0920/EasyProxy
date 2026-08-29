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
