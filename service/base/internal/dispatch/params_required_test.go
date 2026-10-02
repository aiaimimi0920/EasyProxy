package dispatch

import (
	"net"
	"testing"

	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/routerule"
)

func TestStrictPinUsernamePreservesDeviceAndForcesProxy(t *testing.T) {
	auth, err := splitProxyUsername("easyproxy+dev=laptop+pin-strict=node-7+nosplit")
	if err != nil || auth.ExplicitDeviceID != "laptop" {
		t.Fatalf("strict username = %#v, %v", auth, err)
	}
	base := auth.Overlay.merge(directiveOverlay{Split: boolp(true)})
	if got := base.resolve(pool.StrategyAuto, "peer").directive.RequiredTag; got != "node-7" {
		t.Fatalf("strict pin lost during resolve: %q", got)
	}
	if got := base.applyTo(pool.SelectionDirective{}, "peer").directive.RequiredTag; got != "node-7" {
		t.Fatalf("strict pin lost during profile overlay: %q", got)
	}
	s := NewServer(Config{}, nil, routerule.New([]string{"FINAL,DIRECT"}, routerule.PolicyDirect, nil), nil)
	res, policy := s.resolveLegacyRequest(auth, directiveOverlay{Split: boolp(true)}, "example.com", "peer")
	if policy != routerule.PolicyProxy || res.directive.RequiredTag != "node-7" {
		t.Fatalf("strict route leaked to direct: %#v %s", res, policy)
	}
}

func TestStrictPinFailsClosedForDisabledProfile(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	disabled, err := profile.Compile("device:laptop", profile.KindDevice, 1, profile.Definition{SchemaVersion: 1, Enabled: false, FinalPolicy: "DIRECT"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver.setDevice(disabled)
	s := NewServer(Config{LocalServer: true, Profiles: resolver}, nil, nil, nil)
	auth, err := splitProxyUsername("easyproxy+dev=laptop+pin-strict=node-7+nosplit")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = s.resolveProfileRequest(auth, directiveOverlay{}, "example.com", &net.TCPAddr{IP: net.ParseIP("192.0.2.10")})
	if err == nil {
		t.Fatal("disabled profile must not turn strict proxy requests into DIRECT")
	}
}
