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

func TestProxyCompatCheckoutUsesCanonicalDeviceCredentialsInLocalServerMode(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	entry := harness.server.mgr.Register(NodeInfo{
		Tag:           "local-node",
		Name:          "Local Node",
		ListenAddress: "127.0.0.1",
		Port:          34001,
	})
	entry.MarkInitialCheckDone(true)

	body, err := json.Marshal(proxyCompatCheckoutRequest{
		HostID:        "Laptop-Work",
		ProvisionMode: "reuse-only",
		BindingMode:   "shared-instance",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(body))
	req.Host = "easy-proxy.local:29888"
	recorder := httptest.NewRecorder()
	harness.server.handleProxyCheckout(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("checkout status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.Lease.Username != "easyproxy+dev=laptop-work" || response.Result.Lease.Password != "secret" {
		t.Fatalf("lease credentials = %q/%q", response.Result.Lease.Username, response.Result.Lease.Password)
	}
}

func TestProxyCompatCheckoutKeepsActiveCredentialsDuringPendingEnable(t *testing.T) {
	harness := newLocalServerMonitorWithEnabled(t, "easyproxy", "old-secret", 1, false)
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer old-secret")
	update := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"enabled":       true,
		"auth_username": "new-user",
		"auth_password": "new-secret",
	}, headers)
	if update.Code != http.StatusOK || update.Body["need_reload"] != true {
		t.Fatalf("pending enable = %#v", update)
	}
	entry := harness.server.mgr.Register(NodeInfo{
		Tag:           "legacy-node",
		Name:          "Legacy Node",
		ListenAddress: "127.0.0.1",
		Port:          34001,
	})
	entry.MarkInitialCheckDone(true)

	body, err := json.Marshal(proxyCompatCheckoutRequest{HostID: "Laptop-Work", ProvisionMode: "reuse-only", BindingMode: "shared-instance"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/proxy/leases/checkout", bytes.NewReader(body))
	req.Host = "easy-proxy.local:29888"
	recorder := httptest.NewRecorder()
	harness.server.handleProxyCheckout(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("checkout status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Result proxyCompatCheckoutResult `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.Lease.Username != "easyproxy" || response.Result.Lease.Password != "old-secret" {
		t.Fatalf("pending lease credentials = %q/%q", response.Result.Lease.Username, response.Result.Lease.Password)
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
