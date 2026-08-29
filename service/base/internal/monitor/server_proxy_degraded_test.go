package monitor

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestProxyCompatCheckoutAllowsDegradedFallbackWhileInitialProbePending(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	handle := mgr.Register(NodeInfo{
		Tag:           "pending-degraded-a",
		Name:          "Pending Degraded A",
		ListenAddress: "127.0.0.1",
		Port:          37221,
	})
	handle.MarkInitialCheckDone(false)

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
		t.Fatalf("expected degraded checkout status %d while initial probe is pending, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode degraded checkout response: %v", err)
	}
	if resp.Result.Lease.Metadata["selectedNodeTag"] != "pending-degraded-a" {
		t.Fatalf("expected degraded pending node to be selected, got %+v", resp.Result.Lease.Metadata)
	}
	if resp.Result.Lease.Metadata["selectedNodeSelectionTier"] != "degraded" {
		t.Fatalf("expected degraded selection tier, got %+v", resp.Result.Lease.Metadata)
	}
}

func TestProxyCompatCheckoutRejectsSourceExcludedDegradedCandidates(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:           "excluded-a",
		Name:          "Excluded A",
		ListenAddress: "127.0.0.1",
		Port:          37231,
		SourceRef:     "local:bad-sub-1",
		SourceName:    "bad-sub-1",
	})
	first.MarkInitialCheckDone(false)
	first.RecordFailure(errors.New("context deadline exceeded"), "www.google.com:443")

	second := mgr.Register(NodeInfo{
		Tag:           "excluded-b",
		Name:          "Excluded B",
		ListenAddress: "127.0.0.1",
		Port:          37232,
		SourceRef:     "local:bad-sub-2",
		SourceName:    "bad-sub-2",
	})
	second.MarkInitialCheckDone(false)
	second.RecordFailure(errors.New("tls handshake: EOF"), "www.google.com:443")

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

	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(checkoutBody))
	req.Host = "easy-proxy-service:9888"
	rec := httptest.NewRecorder()
	s.handleProxyCheckout(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected checkout status %d when all degraded candidates are source-excluded, got %d: %s", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "NO_PROXY_PROVIDER_ROUTE") {
		t.Fatalf("expected NO_PROXY_PROVIDER_ROUTE response, got %s", rec.Body.String())
	}
}

func TestProxyCompatCheckoutDoesNotWaitForInitialProbeWhenCandidatesExist(t *testing.T) {
	mgr, err := NewManager(Config{
		ProbeTargets: []string{"https://platform.openai.com/login"},
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	mgr.healthMu.Lock()
	mgr.healthTimeout = 150 * time.Millisecond
	mgr.healthMu.Unlock()

	handle := mgr.Register(NodeInfo{
		Tag:           "pending-fast-checkout",
		Name:          "Pending Fast Checkout",
		ListenAddress: "127.0.0.1",
		Port:          37222,
	})
	handle.MarkInitialCheckDone(false)

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

	start := time.Now()
	s.handleProxyCheckout(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected checkout status %d while initial probe is pending, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if elapsed >= 50*time.Millisecond {
		t.Fatalf("expected checkout to use existing candidates without waiting for initial probe, took %s", elapsed)
	}
}

func TestProxyCompatCheckoutFallsBackWhenAllCandidatesAreCoolingForNonRegistrationServices(t *testing.T) {
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
		s.compatState().recordServiceFailureForSnapshot("quota-service", snap, "eUdf5", decision)
	}

	checkoutBody, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "quota-service",
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

func TestProxyCompatCheckoutRejectsAllCoolingCandidatesForRegistrationServices(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	first := mgr.Register(NodeInfo{
		Tag:           "cooling-a",
		Name:          "Cooling A",
		ListenAddress: "127.0.0.1",
		Port:          36211,
	})
	first.MarkInitialCheckDone(true)

	second := mgr.Register(NodeInfo{
		Tag:           "cooling-b",
		Name:          "Cooling B",
		ListenAddress: "127.0.0.1",
		Port:          36212,
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
		Metadata: map[string]string{
			"stage": "registration",
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
		t.Fatalf("expected strict registration cooldown status %d, got %d: %s", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "NO_PROXY_PROVIDER_ROUTE") {
		t.Fatalf("expected no route error body, got %s", rec.Body.String())
	}
}
