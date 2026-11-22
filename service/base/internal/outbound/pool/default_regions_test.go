package pool

import (
	"context"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
)

func defaultRegionTestPool(t *testing.T) *poolOutbound {
	t.Helper()
	p := tierTestPool(t)
	p.options.DefaultRegionExclusions = true
	for i, country := range []string{"CN", "HK", "MO", "US", "JP"} {
		p.options.Metadata[p.members[i].tag] = MemberMeta{CountryISO: country, Country: country}
		p.members[i].entry.RecordSuccessWithLatency(time.Millisecond)
	}
	return p
}

func TestDefaultRegionPolicySurvivesDialFailure(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		p := defaultRegionTestPool(t)
		p.logger = log.NewNOPFactory().Logger()
		var calls []string
		for _, m := range p.members {
			m.outbound = tierRecordingOutbound{tag: m.tag, calls: &calls}
		}
		d := &SelectionDirective{SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "US"}}}
		ctx := WithDirective(context.Background(), d)
		var err error
		if network == "tcp" {
			_, err = p.DialContext(ctx, network, M.Socksaddr{})
		} else {
			_, err = p.ListenPacket(ctx, M.Socksaddr{})
		}
		if err == nil || len(calls) != 2 {
			t.Fatalf("%s calls=%v error=%v", network, calls, err)
		}
		for i, country := range []string{"US", "JP"} {
			if p.options.Metadata[calls[i]].CountryISO != country {
				t.Fatalf("%s wrong fallback order: %v", network, calls)
			}
		}
	}
}

func TestDefaultRegionExclusionsAcrossTiersAndFallback(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		for _, d := range []*SelectionDirective{
			nil,
			{Strategy: StrategyStable},
			{SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "US"}}},
			{SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "HK"}, {Kind: "latency"}}},
		} {
			p := defaultRegionTestPool(t)
			// Exercise the same candidate policy for UDP-capable members.
			if network == "udp" {
				for _, m := range p.members {
					m.outbound = defaultRegionUDPOutbound{}
				}
			}
			excluded := map[string]struct{}{}
			for i := 0; i < 2; i++ {
				got, err := p.pickMember(network, excluded, d)
				if err != nil || got == nil || defaultExcludedRegion(p.options.Metadata[got.tag].CountryISO) {
					t.Fatalf("%s selection = %v, %v", network, got, err)
				}
				excluded[got.tag] = struct{}{}
			}
			if got, err := p.pickMember(network, excluded, d); err == nil || got != nil {
				t.Fatalf("%s fallback reintroduced excluded node: %v, %v", network, got, err)
			}
		}
	}
}

type defaultRegionUDPOutbound struct{ failingOutbound }

func (defaultRegionUDPOutbound) Network() []string { return []string{"tcp", "udp"} }

func TestDefaultRegionExplicitRequests(t *testing.T) {
	for _, country := range []string{"CN", "HK", "MO"} {
		p := defaultRegionTestPool(t)
		d := &SelectionDirective{Filter: NodeFilter{Countries: []string{country}}}
		if got, err := p.pickMember("tcp", nil, d); err == nil || got != nil {
			t.Fatal("profile filter bypassed default policy without request parameters")
		}
		d.ExplicitRegionSelection = true
		got, err := p.pickMember("tcp", nil, d)
		if err != nil || got == nil || p.options.Metadata[got.tag].CountryISO != country {
			t.Fatalf("explicit %s = %v, %v", country, got, err)
		}
	}
	for _, strict := range []bool{false, true} {
		p := defaultRegionTestPool(t)
		tag := p.members[1].tag
		d := &SelectionDirective{Strategy: StrategyStable}
		if strict {
			d.RequiredTag = tag
		} else {
			d.PinnedTag = tag
		}
		got, err := p.pickMember("tcp", nil, d)
		if err != nil || got == nil || got.tag != tag {
			t.Fatalf("explicit pin = %v, %v", got, err)
		}
		got, err = p.pickMember("tcp", map[string]struct{}{tag: {}}, d)
		if strict {
			if err == nil || got != nil {
				t.Fatal("strict pin failed open")
			}
		} else if err != nil || got == nil || defaultExcludedRegion(p.options.Metadata[got.tag].CountryISO) {
			t.Fatalf("soft pin fallback = %v, %v", got, err)
		}
		// A pinned stable binding must not leak into a subsequent default flow.
		got, err = p.pickMember("tcp", nil, &SelectionDirective{Strategy: StrategyStable})
		if err != nil || got == nil || defaultExcludedRegion(p.options.Metadata[got.tag].CountryISO) {
			t.Fatalf("sticky binding bypassed default policy: %v, %v", got, err)
		}
	}
}

func TestDefaultRegionMetadataAndDedicatedPools(t *testing.T) {
	p := defaultRegionTestPool(t)
	m := p.members[0]
	for _, value := range []string{"CN", " hk ", "mo", "China", "Hong Kong", "Macao", "Macau", "中国", "香港", "澳门"} {
		p.options.Metadata[m.tag] = MemberMeta{Country: value}
		if p.memberAllowedByDefaultRegionPolicy(m, nil) {
			t.Fatalf("country %q allowed", value)
		}
	}
	for _, meta := range []MemberMeta{{}, {CountryISO: "TW"}, {CountryISO: "US"}, {Country: "Unknown"}} {
		p.options.Metadata[m.tag] = meta
		if !p.memberAllowedByDefaultRegionPolicy(m, nil) {
			t.Fatalf("unrequested exclusion: %+v", meta)
		}
	}
	p.options.Metadata[m.tag] = MemberMeta{Region: "hk"}
	if p.memberAllowedByDefaultRegionPolicy(m, nil) {
		t.Fatal("region-only Hong Kong allowed")
	}
	p.options.DefaultRegionExclusions = false
	if !p.memberAllowedByDefaultRegionPolicy(m, nil) {
		t.Fatal("dedicated/bootstrap pool changed")
	}
}
