package dispatch

import (
	"easy_proxies/internal/routerule"
	"net"
	"net/http"
	"testing"
)

func protectedTestServer() *Server {
	return NewServer(Config{Username: "ordinary", Password: "ordinary-secret", ProtectedClients: map[string]ProtectedClient{"claude-code": {Password: "dedicated-secret", NodeTag: "chosen-node"}}}, nil, routerule.New([]string{"FINAL,DIRECT"}, routerule.PolicyDirect, nil), nil)
}

func TestProtectedClientAuthenticationRejectsInvalidBindings(t *testing.T) {
	for _, tc := range []struct{ name, user, pass, tag string }{
		{"bad password", "claude-code", "wrong", "chosen-node"},
		{"missing password", "claude-code", "", "chosen-node"},
		{"empty binding", "claude-code", "dedicated-secret", ""},
		{"whitespace binding", "claude-code", "dedicated-secret", "  "},
		{"parameter override", "claude-code+pin-strict=other", "dedicated-secret", "chosen-node"},
		{"direct override", "claude-code+split", "dedicated-secret", "chosen-node"},
		{"unknown user", "unknown", "dedicated-secret", "chosen-node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := protectedTestServer()
			s.cfg.ProtectedClients["claude-code"] = ProtectedClient{Password: "dedicated-secret", NodeTag: tc.tag}
			if _, err := s.authenticateProxy(tc.user, tc.pass); err == nil {
				t.Fatal("request must be rejected")
			}
		})
	}
}

func TestProtectedClientOverridesDirectAndRequestParameters(t *testing.T) {
	s := protectedTestServer()
	auth, err := s.authenticateProxy("claude-code", "dedicated-secret")
	if err != nil {
		t.Fatal(err)
	}
	overlay, _ := parseTokens("pin-strict=other+split+us")
	res, policy := s.resolveLegacyRequest(auth, overlay, "example.invalid", "peer")
	if policy != routerule.PolicyProxy || res.directive.RequiredTag != "chosen-node" {
		t.Fatalf("wrong protected route: %#v %s", res, policy)
	}
	res, policy, _, err = s.resolveProfileRequest(auth, overlay, "example.invalid", nil)
	if err != nil || policy != routerule.PolicyProxy || res.directive.RequiredTag != "chosen-node" {
		t.Fatalf("profile path changed protected route: %#v %s %v", res, policy, err)
	}
}

func TestProtectedClientPreservesOrdinaryRoute(t *testing.T) {
	s := protectedTestServer()
	auth, err := s.authenticateProxy("ordinary", "ordinary-secret")
	if err != nil {
		t.Fatal(err)
	}
	res, policy := s.resolveLegacyRequest(auth, directiveOverlay{}, "example.invalid", "peer")
	if policy != routerule.PolicyDirect || res.directive.RequiredTag != "" {
		t.Fatalf("ordinary route changed: %#v %s", res, policy)
	}
}

func TestProtectedClientHTTPAuthentication(t *testing.T) {
	s := protectedTestServer()
	r, _ := http.NewRequest("CONNECT", "https://example.invalid", nil)
	if _, err := s.authenticateHTTPRequest(r); err == nil {
		t.Fatal("missing credentials accepted")
	}
	// SetBasicAuth writes Authorization, which must not authenticate a proxy.
	r.SetBasicAuth("claude-code", "dedicated-secret")
	if _, err := s.authenticateHTTPRequest(r); err == nil {
		t.Fatal("origin authorization used as proxy credentials")
	}
	r.Header.Set("Proxy-Authorization", r.Header.Get("Authorization"))
	r.Header.Del("Authorization")
	auth, err := s.authenticateHTTPRequest(r)
	if err != nil || auth.protectedNode != "chosen-node" {
		t.Fatalf("protected CONNECT authentication failed: %v", err)
	}
}

func TestProtectedOnlyListenerRejectsUnauthenticatedAndUnknownClients(t *testing.T) {
	s := protectedTestServer()
	s.cfg.Username = ""
	s.cfg.Password = ""
	if _, err := s.authenticateProxy("unknown", "anything"); err == nil {
		t.Fatal("unknown identity accepted")
	}
	r, _ := http.NewRequest("CONNECT", "https://example.invalid", nil)
	if _, err := s.authenticateHTTPRequest(r); err == nil {
		t.Fatal("missing identity accepted")
	}
	if _, err := s.authenticateProxy("claude-code", "dedicated-secret"); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedOnlyListenerRejectsSOCKS5NoAuth(t *testing.T) {
	s := protectedTestServer()
	s.cfg.Username = ""
	s.cfg.Password = ""

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	done := make(chan bool, 1)
	go func() {
		_, ok := s.socksHandshake(serverConn)
		done <- ok
	}()

	// Offer only SOCKS5 NO AUTH (0x00); must be rejected with 0xFF when ProtectedClients is non-empty.
	if _, err := clientConn.Write([]byte{socks5Version, 1, authNoAuth}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := clientConn.Read(reply); err != nil {
		t.Fatalf("read greeting reply: %v", err)
	}
	if reply[0] != socks5Version || reply[1] != authNone {
		t.Fatalf("SOCKS5 greeting reply = %v, want [5 255]", reply)
	}
	if ok := <-done; ok {
		t.Fatal("SOCKS5 NO AUTH bypassed ProtectedClients")
	}
}
