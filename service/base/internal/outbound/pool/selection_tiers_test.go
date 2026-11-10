package pool

import (
	"context"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"testing"
	"time"
)

func tierTestPool(t *testing.T) *poolOutbound {
	t.Helper()
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatal(err)
	}
	p := &poolOutbound{mode: modeSequential, sticky: newStickyState(time.Minute), options: Options{Metadata: map[string]MemberMeta{}}}
	for _, tag := range []string{"jp-a", "jp-b", "jp-c", "us", "cold"} {
		e := mgr.Register(monitor.NodeInfo{Tag: tag})
		p.members = append(p.members, &memberState{tag: tag, entry: e, outbound: failingOutbound{}})
		country := "JP"
		if tag == "us" || tag == "cold" {
			country = "US"
		}
		p.options.Metadata[tag] = MemberMeta{CountryISO: country}
	}
	return p
}

func TestSelectionTiersFallthroughAndMetrics(t *testing.T) {
	p := tierTestPool(t)
	for i, m := range p.members[:4] {
		for _, d := range []time.Duration{10, 20, 30} {
			m.entry.RecordSuccessWithLatency((d + time.Duration(i)*20) * time.Millisecond)
		}
	}
	// US has lower jitter but higher latency than JP; this must override a soft pin.
	for i := 0; i < 8; i++ {
		p.members[3].entry.RecordSuccessWithLatency(100 * time.Millisecond)
	}
	d := &SelectionDirective{Strategy: StrategyStable, PinnedTag: "jp-a", SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "XX"}, {Kind: "stability"}}}
	got, err := p.pickMember("tcp", nil, d)
	if err != nil || got.tag != "us" {
		t.Fatalf("stability = %v %v", got, err)
	}
	d.SelectionTiers[1].Kind = "latency"
	got, err = p.pickMember("tcp", nil, d)
	if err != nil || got.tag != "jp-a" {
		t.Fatalf("latency = %v %v", got, err)
	}
	d.Filter.Countries = []string{"US"}
	got, err = p.pickMember("tcp", nil, d)
	if err != nil || got.tag != "us" {
		t.Fatalf("hard filter = %v %v", got, err)
	}
	d.RequiredTag = "cold"
	got, err = p.pickMember("tcp", nil, d)
	if err != nil || got.tag != "cold" {
		t.Fatalf("cold general fallback = %v %v", got, err)
	}
	got, err = p.pickMember("tcp", map[string]struct{}{"cold": {}}, d)
	if err == nil || got != nil {
		t.Fatal("required tag escaped")
	}
}

func TestSelectionTierRetryBudgets(t *testing.T) {
	p := tierTestPool(t)
	d := &SelectionDirective{SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "JP"}, {Kind: "country", Country: "US"}}}
	ctx, local, retries, cancel := p.selectionDialBudget(WithDirective(context.Background(), d))
	defer cancel()
	if retries != 5 || ctx.Err() != nil || d.tierAttempts != nil {
		t.Fatal("budget or directive isolation")
	}
	excluded := map[string]struct{}{}
	for i := 0; i < 4; i++ {
		got, err := p.pickMember("tcp", excluded, local)
		if err != nil {
			t.Fatal(err)
		}
		country := p.options.Metadata[got.tag].CountryISO
		if (i < 2 && country != "JP") || (i >= 2 && country != "US") {
			t.Fatalf("attempt %d chose %s", i, got.tag)
		}
		excluded[got.tag] = struct{}{}
	}
	got, err := p.pickMember("tcp", excluded, local)
	if err != nil || p.options.Metadata[got.tag].CountryISO != "JP" {
		t.Fatalf("general fallback = %v %v", got, err)
	}
}

func TestSelectionTierLegacyAndNamespaces(t *testing.T) {
	p := tierTestPool(t)
	d := &SelectionDirective{ProfileID: "a", ProfileRevision: 1, Strategy: StrategyStable, SessionKey: "session"}
	_, original, retries, cancel := p.selectionDialBudget(WithDirective(context.Background(), d))
	cancel()
	if original != d || retries != p.options.MaxRetries {
		t.Fatal("legacy changed")
	}
	a := *d
	b := *d
	a.tierNamespace = "tier:0:country:JP"
	b.tierNamespace = "tier:general"
	if a.stableBucketKey() == b.stableBucketKey() || a.namespacedSessionKey() == b.namespacedSessionKey() {
		t.Fatal("tiers share stickiness")
	}
	b = a
	b.ProfileRevision++
	if a.stableBucketKey() == b.stableBucketKey() {
		t.Fatal("revision isolation lost")
	}
	d.SelectionTiers = []config.SelectionTier{{Kind: "stability"}}
	// No measured samples: general fallback can use cold candidates.
	if got, err := p.pickMember("tcp", nil, d); err != nil || got == nil {
		t.Fatalf("cold fallthrough = %v %v", got, err)
	}
	p.members[0].entry.MarkInitialCheckDone(false)
	d.RequiredTag = p.members[0].tag
	if got, err := p.pickMember("tcp", nil, d); err == nil || got != nil {
		t.Fatal("known unavailable selected")
	}
	d.SelectionTiers = nil
	if got, err := p.pickMember("tcp", nil, d); err != nil || got == nil {
		t.Fatal("legacy health behavior changed")
	}
}

func TestProfileWithoutSessionSourceNeverCreatesStickyBinding(t *testing.T) {
	for _, key := range []string{"", " \t\n"} {
		for _, tiers := range [][]config.SelectionTier{nil, {}, {{Kind: "country", Country: "JP"}}, {{Kind: "country", Country: "ZZ"}}} {
			p := tierTestPool(t)
			d := &SelectionDirective{ProfileID: "profile", ProfileRevision: 7, Strategy: StrategySession, SessionKey: key, SelectionTiers: tiers}
			if d.namespacedSessionKey() != "" {
				t.Fatal("missing identity acquired profile namespace")
			}
			first, err := p.pickMember("tcp", nil, d)
			if err != nil {
				t.Fatal(err)
			}
			second, err := p.pickMember("tcp", nil, d)
			if err != nil {
				t.Fatal(err)
			}
			if first.tag == second.tag {
				t.Fatalf("anonymous flows collapsed for tiers=%v", tiers)
			}
			if len(p.sticky.sessions) != 0 {
				t.Fatalf("anonymous session saved: %v", p.sticky.sessions)
			}
		}
	}
}
