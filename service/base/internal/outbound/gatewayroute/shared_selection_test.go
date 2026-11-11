package gatewayroute

import (
	"context"
	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"
	"errors"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"sync/atomic"
	"testing"
)

func TestLegacyTUNSharedSelectionHotRefresh(t *testing.T) {
	r := newTestRoute(nil, "PROXY", "DIRECT", nil, nil)
	r.options.SelectionTiers = []config.SelectionTier{{Kind: "country", Country: "DE"}}
	var current atomic.Pointer[pool.SelectionDirective]
	r.sharedSelection = func(context.Context) (*pool.SelectionDirective, error) { return current.Load(), nil }
	for i, country := range []string{"US", "JP", ""} {
		d := &pool.SelectionDirective{ProfileRevision: int64(i + 1), Filter: pool.NodeFilter{Countries: []string{"US", "JP"}}}
		if country != "" {
			d.SelectionTiers = []config.SelectionTier{{Kind: "country", Country: country}}
		}
		current.Store(d)
		for _, network := range []string{"tcp", "udp"} {
			for _, peer := range []string{"192.0.2.1:123", "192.0.2.2:456", "[2001:db8::1]:789"} {
				ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Source: M.ParseSocksaddr(peer)})
				policy, got, _ := r.selection(ctx, network, M.ParseSocksaddr("203.0.113.1:443"))
				if policy != routerule.PolicyProxy || got.ProfileRevision != int64(i+1) {
					t.Fatalf("legacy policy/revision changed: %s %+v", policy, got)
				}
				if country == "" {
					if len(got.SelectionTiers) != 0 {
						t.Fatal("cleared tiers retained startup value")
					}
				} else if got.SelectionTiers[0].Country != country || len(got.Filter.Countries) != 2 {
					t.Fatalf("lost shared selection: %+v", got)
				}
				if network == "udp" && (!got.RequireAvailablePreferred || len(got.PreferredProtocolFamilies) != 2) {
					t.Fatal("UDP restrictions lost")
				}
			}
		}
	}
}

func TestLegacyTUNSharedSourceFailureDoesNotFailOpen(t *testing.T) {
	r := newTestRoute(nil, "PROXY", "DIRECT", nil, nil)
	r.sharedSelection = func(context.Context) (*pool.SelectionDirective, error) { return nil, errors.New("generation closed") }
	if _, err := r.DialContext(context.Background(), "tcp", M.ParseSocksaddr("203.0.113.1:443")); err == nil {
		t.Fatal("TCP source failure fell back direct")
	}
	if _, _, err := r.ListenPacketWithDestination(context.Background(), M.ParseSocksaddr("203.0.113.1:443")); err == nil {
		t.Fatal("UDP source failure fell back direct")
	}
}
