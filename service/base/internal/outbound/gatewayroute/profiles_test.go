package gatewayroute

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/outbound/profileselect"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/routerule"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type liveProfiles struct {
	registry atomic.Pointer[profile.Registry]
}

func (p *liveProfiles) Resolve(identity profile.RequestIdentity) profile.Resolution {
	return p.registry.Load().Resolve(identity)
}
func (*liveProfiles) Observe(profile.Resolution, netip.Addr, time.Time) {}
func compiledDevice(t *testing.T, id string, revision int64, enabled bool) *profile.CompiledProfile {
	t.Helper()
	long := true
	p, err := profile.Compile(id, profile.KindDevice, revision, profile.Definition{
		Enabled: enabled, DefaultStrategy: "session", FinalPolicy: "PROXY",
		Rules:          []string{"DST-PORT,53,DIRECT"},
		NodeFilter:     profile.NodeFilter{Countries: []string{"JP"}, LongLived: &long},
		SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "JP"}, {Kind: "latency"}},
		Session:        profile.SessionPolicy{TTL: "3m"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func installProfiles(t *testing.T, resolver *liveProfiles, revision int64, enabled bool) {
	t.Helper()
	shared := compiledDevice(t, "shared", revision, enabled)
	resolver.registry.Store(profile.NewRegistry(shared, map[string]*profile.CompiledProfile{
		"laptop": compiledDevice(t, "laptop", revision, enabled), "phone": compiledDevice(t, "phone", revision, enabled),
	}, []profile.IPMapping{
		{MappingID: "v4", Prefix: netip.MustParsePrefix("192.0.2.1/32"), DeviceID: "laptop"},
		{MappingID: "v6", Prefix: netip.MustParsePrefix("2001:db8::1/128"), DeviceID: "phone"},
	}, profile.CredentialSnapshot{}, uint64(revision)))
}
func TestDeviceSelectionUsesOriginalSourceAcrossProtocols(t *testing.T) {
	resolver := &liveProfiles{}
	installProfiles(t, resolver, 1, true)
	route := newTestRoute(nil, "PROXY", "DIRECT", nil, nil)
	route.profiles = resolver
	for _, network := range []string{N.NetworkTCP, N.NetworkUDP} {
		for _, test := range []struct{ source, id string }{
			{"192.0.2.1:1000", "laptop"}, {"[2001:db8::1]:2000", "phone"},
			{"[::ffff:192.0.2.1]:3000", "laptop"}, {"192.0.2.55:4000", "shared"}, {"", "shared"},
		} {
			t.Run(network+test.source, func(t *testing.T) {
				ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Source: M.ParseSocksaddr(test.source), Domain: "example.com"})
				policy, d, _ := route.selection(ctx, network, M.ParseSocksaddr("192.0.2.1:443"))
				wantID := test.id
				if network == N.NetworkUDP {
					wantID += "/udp"
				}
				if policy != routerule.PolicyProxy || d.ProfileID != wantID || d.ProfileRevision != 1 {
					t.Fatalf("policy=%s directive=%+v", policy, d)
				}
				if d.Strategy != pool.StrategySession || d.SessionTTL != 3*time.Minute || len(d.SelectionTiers) != 2 || d.Filter.Countries[0] != "JP" || d.Filter.LongLived == nil || !*d.Filter.LongLived {
					t.Fatalf("lost profile settings: %+v", d)
				}
				if test.source == "" {
					if d.SessionKey != "" {
						t.Fatalf("invented session: %q", d.SessionKey)
					}
				} else if d.SessionKey != M.ParseSocksaddr(test.source).Addr.Unmap().String() {
					t.Fatalf("session=%q", d.SessionKey)
				}
				if network == N.NetworkUDP && (!d.RequireAvailablePreferred || len(d.PreferredProtocolFamilies) != 2) {
					t.Fatal("UDP availability restriction lost")
				}
			})
		}
	}
}
func TestLiveProfilePublicationAndPortRules(t *testing.T) {
	resolver := &liveProfiles{}
	installProfiles(t, resolver, 1, true)
	route := newTestRoute(nil, "PROXY", "DIRECT", nil, nil)
	route.profiles = resolver
	ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Source: M.ParseSocksaddr("192.0.2.1:1000")})
	if policy, _, _ := route.selection(ctx, N.NetworkUDP, M.ParseSocksaddr("1.1.1.1:53")); policy != routerule.PolicyDirect {
		t.Fatal("profile port rule ignored")
	}
	installProfiles(t, resolver, 2, false)
	policy, d, _ := route.selection(ctx, N.NetworkTCP, M.ParseSocksaddr("1.1.1.1:443"))
	if policy != routerule.PolicyDirect || d.ProfileRevision != 2 {
		t.Fatalf("stale publication: %s %+v", policy, d)
	}
	installProfiles(t, resolver, 3, true)
	policy, d, _ = route.selection(ctx, N.NetworkTCP, M.ParseSocksaddr("1.1.1.1:443"))
	if policy != routerule.PolicyProxy || d.ProfileRevision != 3 {
		t.Fatal("hot re-enable ignored")
	}
	if profileselect.FromContext(profileselect.WithResolver(context.Background(), resolver)) != resolver {
		t.Fatal("resolver context lost owner")
	}
}
func TestConcurrentProfilesDoNotShareDirectives(t *testing.T) {
	resolver := &liveProfiles{}
	installProfiles(t, resolver, 1, true)
	route := newTestRoute(nil, "PROXY", "DIRECT", nil, nil)
	route.profiles = resolver
	for _, source := range []string{"192.0.2.1:1", "[2001:db8::1]:2"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Source: M.ParseSocksaddr(source)})
			for i := 0; i < 100; i++ {
				_, d, _ := route.selection(ctx, N.NetworkTCP, M.ParseSocksaddr("8.8.8.8:443"))
				if d.SessionKey != M.ParseSocksaddr(source).Addr.String() {
					t.Fatal("cross-device session")
				}
				d.Filter.Countries[0] = "US"
			}
		})
	}
}
func TestDeviceProfileDirectiveReachesTCPAndUDPOutbound(t *testing.T) {
	resolver := &liveProfiles{}
	installProfiles(t, resolver, 1, true)
	direct := &recordingOutbound{tag: "direct"}
	proxy := &recordingOutbound{tag: "pool"}
	route := newTestRoute(nil, "PROXY", "DIRECT", direct, proxy)
	route.profiles = resolver
	ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Source: M.ParseSocksaddr("192.0.2.1:123")})
	conn, err := route.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr("1.1.1.1:443"))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if proxy.lastDirective.ProfileID != "laptop" {
		t.Fatal("TCP directive not propagated")
	}
	packet, err := route.ListenPacket(ctx, M.ParseSocksaddr("1.1.1.1:443"))
	if err != nil {
		t.Fatal(err)
	}
	packet.Close()
	if proxy.lastDirective.ProfileID != "laptop/udp" || !proxy.lastDirective.RequireAvailablePreferred {
		t.Fatal("UDP directive not propagated")
	}
	installProfiles(t, resolver, 2, false)
	conn, err = route.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr("1.1.1.1:443"))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if direct.dials.Load() != 1 {
		t.Fatal("disabled profile did not dial DIRECT")
	}
}

func TestPendingProfileGenerationNeverFallsBackDirect(t *testing.T) {
	resolver := &liveProfiles{}
	installProfiles(t, resolver, 1, false)
	direct := &recordingOutbound{tag: "direct"}
	proxy := &recordingOutbound{tag: "pool"}
	route := newTestRoute(nil, "PROXY", "DIRECT", direct, proxy)
	route.profiles = resolver
	route.admission = new(profileselect.Admission)
	ctx := context.Background()
	destination := M.ParseSocksaddr("1.1.1.1:443")
	if conn, err := route.DialContext(ctx, N.NetworkTCP, destination); err == nil || conn != nil {
		t.Fatal("pending TCP generation admitted")
	}
	if conn, err := route.ListenPacket(ctx, destination); err == nil || conn != nil {
		t.Fatal("pending UDP generation admitted")
	}
	if direct.dials.Load()+direct.packetDials.Load()+proxy.dials.Load()+proxy.packetDials.Load() != 0 {
		t.Fatal("pending generation attempted fallback")
	}
	installProfiles(t, resolver, 2, true)
	route.admission.Commit()
	conn, err := route.DialContext(ctx, N.NetworkTCP, destination)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if proxy.lastDirective.ProfileRevision != 2 {
		t.Fatal("committed generation used old profile")
	}
	route.admission.Close()
	if conn, err := route.DialContext(ctx, N.NetworkTCP, destination); err == nil || conn != nil {
		t.Fatal("closed generation admitted")
	}
}
