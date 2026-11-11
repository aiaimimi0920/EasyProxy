package pool

import (
	"context"
	"easy_proxies/internal/config"
	"errors"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestSharedSelectionLiveAcrossPeersAndClear(t *testing.T) {
	p := tierTestPool(t)
	p.options.SharedSelection = true
	var current atomic.Pointer[SelectionDirective]
	p.sharedSelection = func(context.Context) (*SelectionDirective, error) { return current.Load(), nil }
	for i, country := range []string{"US", "JP", ""} {
		d := &SelectionDirective{ProfileID: "shared", ProfileRevision: int64(i + 1), Strategy: StrategySession}
		if country != "" {
			d.SelectionTiers = []config.SelectionTier{{Kind: "country", Country: country}}
		}
		current.Store(d)
		for _, peer := range []string{"192.0.2.1:123", "192.0.2.2:456", "[2001:db8::1]:789"} {
			ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Source: M.ParseSocksaddr(peer)})
			ctx, err := p.sharedSelectionContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got := DirectiveFrom(ctx)
			if country == "" {
				if got != nil {
					t.Fatal("clear retained shared selection")
				}
				continue
			}
			if got.ProfileRevision != int64(i+1) || got.SessionKey != M.ParseSocksaddr(peer).Addr.String() {
				t.Fatalf("wrong live peer snapshot: %+v", got)
			}
			member, err := p.pickMember("tcp", nil, got)
			if err != nil || p.options.Metadata[member.tag].CountryISO != country {
				t.Fatalf("country=%s member=%v error=%v", country, member, err)
			}
		}
	}
	// Explicit directives, including an empty device selector, must win.
	device := &SelectionDirective{ProfileID: "device"}
	ctx, _ := p.sharedSelectionContext(WithDirective(context.Background(), device))
	if DirectiveFrom(ctx) != device {
		t.Fatal("device selector overridden")
	}
	p.sharedSelection = func(context.Context) (*SelectionDirective, error) { return nil, errors.New("pending") }
	if _, err := p.sharedSelectionContext(context.Background()); err == nil {
		t.Fatal("pending generation admitted")
	}
	p.options.SharedSelection = false
	if _, err := p.sharedSelectionContext(context.Background()); err != nil {
		t.Fatal("dedicated pool consulted shared profile")
	}
}
func TestSharedSelectionActualDialFallback(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			p := tierTestPool(t)
			p.options.SharedSelection = true
			p.logger = log.NewNOPFactory().Logger()
			p.sharedSelection = func(context.Context) (*SelectionDirective, error) {
				return &SelectionDirective{SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "US"}, {Kind: "country", Country: "JP"}}}, nil
			}
			var calls []string
			for _, m := range p.members {
				m.outbound = tierRecordingOutbound{tag: m.tag, calls: &calls}
			}
			if network == "tcp" {
				_, _ = p.DialContext(context.Background(), network, M.Socksaddr{})
			} else {
				_, _ = p.ListenPacket(context.Background(), M.Socksaddr{})
			}
			var countries []string
			for _, tag := range calls {
				countries = append(countries, p.options.Metadata[tag].CountryISO)
			}
			if !reflect.DeepEqual(countries, []string{"US", "US", "JP", "JP", "JP"}) {
				t.Fatalf("fallback order=%v", countries)
			}
		})
	}
}
