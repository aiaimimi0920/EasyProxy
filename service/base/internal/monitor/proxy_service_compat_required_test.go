package monitor

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"easy_proxies/internal/config"
)

func TestProxyCompatPoolLeaseIsSharedOrStrictlyPinned(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatal(err)
	}
	entry := mgr.Register(NodeInfo{Tag: "node-a", Name: "Node A", Mode: "pool", Port: 22323})
	entry.MarkInitialCheckDone(true)
	s := &Server{cfg: Config{ProxyUsername: "proxy-user", ProxyPassword: "secret"}, mgr: mgr, proxyCompat: newProxyCompatState()}
	cfg := &config.Config{Mode: "pool"}
	cfg.Listener.Port = 22323
	cfg.Listener.Protocol = "mixed"
	cfg.Listener.Username = "proxy-user"
	cfg.Listener.Password = "secret"
	cfg.Routing.Enabled = true
	s.SetConfig(cfg)
	r := httptest.NewRequest(http.MethodPost, "http://easyproxy:29888/proxy/leases/checkout", nil)
	request := proxyCompatCheckoutRequest{HostID: "worker", Metadata: map[string]string{"selectedNodeTag": "spoof", "selectedNodeMode": "dedicated-node"}}
	candidate, runtimeCfg, err := s.resolveProxyCompatCandidate(r, request)
	if err != nil || candidate.EndpointMode != "shared-pool" || strings.Contains(candidate.Username, "pin-strict=") {
		t.Fatalf("ordinary pool route = %#v, %v", candidate, err)
	}
	request.RequireDedicatedNode = true
	candidate, runtimeCfg, err = s.resolveProxyCompatCandidate(r, request)
	if err != nil || candidate.EndpointMode != "pinned-node" {
		t.Fatalf("strict pool route = %#v, %v", candidate, err)
	}
	lease, _ := s.createProxyCompatLease(request, runtimeCfg, candidate)
	parsed, err := url.Parse(lease.ProxyURL)
	if err != nil || !strings.Contains(parsed.User.Username(), "+pin-strict=node-a+nosplit") {
		t.Fatalf("lease URL does not encode the selected tag: %v", err)
	}
	if lease.Metadata["selectedNodeTag"] != "node-a" || lease.Metadata["selectedNodeMode"] != "pinned-node" {
		t.Fatalf("request metadata overwrote routing facts: %#v", lease.Metadata)
	}
	if candidate.EndpointPort != 22323 {
		t.Fatal("strict username binding must preserve the existing listener")
	}
	cfg.Routing.Enabled = false
	if _, _, err = s.resolveProxyCompatCandidate(r, request); err == nil {
		t.Fatal("plain pool listener cannot fulfill a strict node requirement")
	}
	cfg.LocalServer.Enabled = true
	cfg.LocalServer.Listen = "0.0.0.0:22324"
	if s.resolveProxyCompatRuntime(r).SupportsRequiredPin {
		t.Fatal("a different dispatch port must not advertise strict support on the shared listener")
	}
}
