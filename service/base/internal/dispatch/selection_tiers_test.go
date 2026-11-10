package dispatch

import (
	"easy_proxies/internal/config"
	"easy_proxies/internal/profile"
	"net"
	"reflect"
	"testing"
)

func TestLegacyTierSnapshotHotApplyAndParameterCompatibility(t *testing.T) {
	tiers := []config.SelectionTier{{Kind: "country", Country: "US"}, {Kind: "latency"}}
	server := NewServer(Config{SelectionTiers: tiers}, nil, nil, nil)
	tiers[0].Country = "JP"
	parsed, err := splitProxyUsername("user+cc=KR")
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := server.resolveLegacyRequest(parsed, directiveOverlay{}, "example.com", "192.0.2.1")
	if resolved.directive.SelectionTiers[0].Country != "US" || resolved.directive.Filter.Countries[0] != "KR" {
		t.Fatalf("tier/filter compatibility %+v", resolved.directive)
	}
	server.SetSelectionTiers([]config.SelectionTier{{Kind: "stability"}})
	if resolved.directive.SelectionTiers[0].Country != "US" {
		t.Fatal("hot apply mutated in-flight directive")
	}
	next, _ := server.resolveLegacyRequest(parsed, directiveOverlay{}, "example.com", "192.0.2.1")
	if next.directive.SelectionTiers[0].Kind != "stability" {
		t.Fatal("hot apply not visible")
	}
}
func TestDeviceTierDirectivesAcrossExplicitProtocols(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	def := resolver.device.Definition()
	def.SelectionTiers = []config.SelectionTier{{Kind: "country", Country: "US"}, {Kind: "latency"}}
	compiled, err := profile.Compile("device:laptop", profile.KindDevice, 2, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver.setDevice(compiled)
	harness := newProfileTestServer(t, resolver)
	assertHTTPUsesProfile(t, harness, "easyproxy+dev=laptop", "secret", "device:laptop")
	assertCONNECTUsesProfile(t, harness, "easyproxy+dev=laptop", "secret", "device:laptop")
	assertSOCKSUsesProfile(t, harness, "easyproxy+dev=laptop", "secret", "device:laptop")
	for _, dial := range harness.outbound.snapshotDials() {
		if !reflect.DeepEqual(dial.directive.SelectionTiers, def.SelectionTiers) || dial.directive.ProfileRevision != 2 {
			t.Fatalf("protocol lost tiers/revision: %+v", dial.directive)
		}
	}
}
func TestProfileTiersDoNotOverrideDirectOrDisabled(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	def := resolver.device.Definition()
	def.Enabled = false
	def.SelectionTiers = []config.SelectionTier{{Kind: "country", Country: "US"}}
	compiled, err := profile.Compile("device:laptop", profile.KindDevice, 3, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver.setDevice(compiled)
	server := NewServer(Config{Profiles: resolver}, nil, nil, nil)
	auth, err := splitProxyUsername("easyproxy+dev=laptop+nosplit")
	if err != nil {
		t.Fatal(err)
	}
	_, policy, _, err := server.resolveProfileRequest(auth, directiveOverlay{}, "example.com", &net.TCPAddr{IP: net.ParseIP("192.0.2.1")})
	if err != nil || string(policy) != "DIRECT" {
		t.Fatalf("disabled profile escaped DIRECT: %v %v", policy, err)
	}
}

func TestUnknownPeerDoesNotCreateSyntheticSession(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	def := resolver.device.Definition()
	def.DefaultStrategy = "session"
	compiled, err := profile.Compile("device:laptop", profile.KindDevice, 1, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver.setDevice(compiled)
	server := NewServer(Config{Profiles: resolver}, nil, nil, nil)
	auth, _ := splitProxyUsername("easyproxy+dev=laptop")
	result, _, _, err := server.resolveProfileRequest(auth, directiveOverlay{}, "example.com", nil)
	if err != nil || result.directive.SessionKey != "" {
		t.Fatalf("unknown peer session=%q err=%v", result.directive.SessionKey, err)
	}
}
