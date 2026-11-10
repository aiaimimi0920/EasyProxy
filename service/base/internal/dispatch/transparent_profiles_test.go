package dispatch

import (
	"context"
	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/routerule"
	"net/netip"
	"testing"
	"time"
)

type transparentResolver struct {
	registry *profile.Registry
	last     profile.RequestIdentity
}

func (r *transparentResolver) Resolve(id profile.RequestIdentity) profile.Resolution {
	r.last = id
	return r.registry.Resolve(id)
}
func (*transparentResolver) Observe(profile.Resolution, netip.Addr, time.Time) {}
func TestTransparentProfilesUseRemotePeerAndLiveRevision(t *testing.T) {
	def := profile.Definition{Enabled: true, DefaultStrategy: "session", FinalPolicy: "PROXY", Rules: []string{"DST-PORT,53,DIRECT"}}
	shared, err := profile.Compile("shared", profile.KindShared, 1, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	device, err := profile.Compile("device", profile.KindDevice, 2, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &transparentResolver{registry: profile.NewRegistry(shared, map[string]*profile.CompiledProfile{"phone": device}, []profile.IPMapping{{MappingID: "phone", Prefix: netip.MustParsePrefix("2001:db8::1/128"), DeviceID: "phone"}}, profile.CredentialSnapshot{}, 1)}
	router := NewTransparentRouter(TransparentRouterConfig{Profiles: r}, nil, nil, nil)
	remote := mustTCPAddr(t, "[2001:db8::1]:555")
	ctx, policy := router.resolveProfile(context.Background(), remote, "192.0.2.1", 443, routerule.PolicyDirect)
	d := pool.DirectiveFrom(ctx)
	if policy != routerule.PolicyProxy || d.ProfileID != "device" || d.ProfileRevision != 2 || d.SessionKey != "2001:db8::1" || r.last.ExplicitDeviceID != "" {
		t.Fatalf("identity/policy mismatch: %+v %+v %s", r.last, d, policy)
	}
	_, policy = router.resolveProfile(context.Background(), remote, "192.0.2.1", 53, routerule.PolicyProxy)
	if policy != routerule.PolicyDirect {
		t.Fatal("profile port rule ignored")
	}
	def.Enabled = false
	device, err = profile.Compile("device", profile.KindDevice, 3, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.registry = profile.NewRegistry(device, nil, nil, profile.CredentialSnapshot{}, 2)
	ctx, policy = router.resolveProfile(context.Background(), nil, "2001:db8::1", 443, routerule.PolicyProxy)
	d = pool.DirectiveFrom(ctx)
	if policy != routerule.PolicyDirect || d.ProfileRevision != 3 || d.SessionKey != "" || r.last.PeerIP.IsValid() {
		t.Fatal("live shared fallback or missing-source semantics lost")
	}
}
func TestTransparentLegacyTierCallbacks(t *testing.T) {
	var tiers []config.SelectionTier
	router := NewTransparentRouter(TransparentRouterConfig{SelectionTiers: func() []config.SelectionTier { return tiers }, DefaultStrategy: func() pool.Strategy { return pool.StrategySession }}, nil, nil, nil)
	ctx, _ := router.resolveProfile(context.Background(), nil, "example.com", 443, routerule.PolicyProxy)
	if pool.DirectiveFrom(ctx) != nil {
		t.Fatal("empty legacy tiers changed directive")
	}
	tiers = []config.SelectionTier{{Kind: "country", Country: "JP"}}
	ctx, _ = router.resolveProfile(context.Background(), mustTCPAddr(t, "192.0.2.1:1"), "example.com", 443, routerule.PolicyProxy)
	d := pool.DirectiveFrom(ctx)
	if d == nil || len(d.SelectionTiers) != 1 || d.SessionKey != "192.0.2.1" {
		t.Fatalf("hot tiers missing: %+v", d)
	}
}
