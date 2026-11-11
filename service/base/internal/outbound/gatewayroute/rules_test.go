package gatewayroute

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"easy_proxies/internal/config"
	"easy_proxies/internal/routerule"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type testCountryLookup struct{}

func (testCountryLookup) CountryISO(ip netip.Addr) string {
	if ip == netip.MustParseAddr("203.0.113.8") {
		return "CN"
	}
	return "US"
}

func TestDefaultGatewayRoutesRealIPAlongsideSniffedDomain(t *testing.T) {
	for _, tc := range []struct {
		name, domain, address, network string
		wantDirect                     bool
	}{
		{"domestic TCP", "unlisted.example", "203.0.113.8:443", N.NetworkTCP, true},
		{"domestic UDP", "unlisted.example", "203.0.113.8:443", N.NetworkUDP, true},
		{"proxy override", "blocked.example", "203.0.113.8:443", N.NetworkTCP, false},
		{"direct override", "allowed.example", "198.51.100.8:443", N.NetworkTCP, true},
		{"unknown fallback", "unlisted.example", "198.51.100.8:443", N.NetworkTCP, false},
		{"proxy UDP domain", "unlisted.example", "198.51.100.8:443", N.NetworkUDP, false},
		{"fake IP restored domain", "", "www.baidu.com:443", N.NetworkTCP, true},
		{"literal domestic", "", "203.0.113.8:443", N.NetworkTCP, true},
		{"private", "", "192.168.1.8:443", N.NetworkTCP, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			direct, proxy := &recordingOutbound{tag: "direct"}, &recordingOutbound{tag: "pool"}
			route := newTestRoute(nil, "PROXY", "PROXY", direct, proxy)
			route.options.UseDefaultRules = true
			route.options.Rules = []string{"DOMAIN,blocked.example,PROXY", "DOMAIN,allowed.example,DIRECT"}
			route.applyRules(nil)
			route.engine.SetGeoLookup(testCountryLookup{})
			ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Domain: tc.domain})
			dst := M.ParseSocksaddr(tc.address)
			if tc.network == N.NetworkUDP {
				conn, err := route.ListenPacket(ctx, dst)
				if err != nil {
					t.Fatal(err)
				}
				_ = conn.Close()
				if tc.wantDirect && direct.lastPacketDestination != dst {
					t.Fatal("DIRECT UDP must retain the original destination IP")
				}
				if !tc.wantDirect && tc.domain != "" && proxy.lastPacketDestination.Fqdn != tc.domain {
					t.Fatal("PROXY UDP must retain sniffed-domain resolution")
				}
			} else {
				conn, err := route.DialContext(ctx, tc.network, dst)
				if err != nil {
					t.Fatal(err)
				}
				_ = conn.Close()
				if tc.wantDirect && direct.lastDestination != dst {
					t.Fatal("DIRECT must retain the original destination IP")
				}
				if !tc.wantDirect && tc.domain != "" && proxy.lastDestination.Fqdn != tc.domain {
					t.Fatal("PROXY must retain sniffed-domain resolution")
				}
			}
			stats := route.Snapshot()
			if (stats.Direct == 1) != tc.wantDirect || stats.Direct+stats.Proxy != 1 {
				t.Fatalf("unexpected route stats: %+v", stats)
			}
		})
	}
}

func TestGatewayProvidersLoadBetweenUserAndDefaultRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("baidu.com\nallowed.example\n"))
	}))
	defer server.Close()
	route := newTestRoute(nil, "DIRECT", "PROXY", nil, nil)
	route.ctx = context.Background()
	route.options.UseDefaultRules = true
	route.options.Rules = []string{"DOMAIN,allowed.example,DIRECT"}
	route.options.RuleProviders = []config.RuleProvider{{URL: server.URL, Policy: "PROXY", Behavior: "domain"}}
	route.applyRules(nil)
	defer route.Close()
	if err := route.startRules(); err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]routerule.Policy{
		"baidu.com":       routerule.PolicyProxy,
		"allowed.example": routerule.PolicyDirect,
		"qq.com":          routerule.PolicyDirect,
		"unknown.example": routerule.PolicyDirect,
	} {
		if got := route.engine.Match(host); got != want {
			t.Fatalf("%s: got %s, want %s", host, got, want)
		}
	}
	// A refreshed provider snapshot replaces, rather than accumulates, old rules.
	route.applyRules([]string{"DOMAIN,new.example,PROXY"})
	if route.engine.Match("baidu.com") != routerule.PolicyDirect || route.engine.Match("new.example") != routerule.PolicyProxy {
		t.Fatal("provider snapshot was not replaced")
	}
}

func TestGatewayConfiguredGeoIPFailureIsNotSilentlyIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.mmdb")
	if err := os.WriteFile(path, []byte("invalid database"), 0600); err != nil {
		t.Fatal(err)
	}
	route := newTestRoute(nil, "PROXY", "PROXY", nil, nil)
	route.options.GeoIP = config.GeoIPConfig{Enabled: true, DatabasePath: path}
	if err := route.startRules(); err == nil || !strings.Contains(err.Error(), "open gateway GEOIP database") {
		t.Fatalf("expected configured database failure, got %v", err)
	}
}
