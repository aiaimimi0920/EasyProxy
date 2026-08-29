package pool

import (
	"testing"
	"time"

	"easy_proxies/internal/monitor"

	E "github.com/sagernet/sing/common/exceptions"
)

func TestSelectMemberAutoPrefersHigherAvailabilityScore(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	healthyEntry := mgr.Register(monitor.NodeInfo{Tag: "healthy", Name: "Healthy"})
	healthyEntry.MarkInitialCheckDone(true)
	healthyState := acquireSharedState("healthy")
	healthyState.attachEntry(healthyEntry)

	penalizedEntry := mgr.Register(monitor.NodeInfo{Tag: "penalized", Name: "Penalized"})
	penalizedEntry.MarkInitialCheckDone(true)
	penalizedEntry.ApplyUsageReportFailure(15, true)
	penalizedState := acquireSharedState("penalized")
	penalizedState.attachEntry(penalizedEntry)

	p := &poolOutbound{mode: modeAuto}
	selected := p.selectMember([]*memberState{
		{tag: "penalized", shared: penalizedState, entry: penalizedEntry},
		{tag: "healthy", shared: healthyState, entry: healthyEntry},
	}, nil, nil)
	if selected == nil {
		t.Fatal("expected a selected member")
	}
	if selected.tag != "healthy" {
		t.Fatalf("expected healthy member to be selected, got %q", selected.tag)
	}
}

func TestSelectMemberStablePrefersLongLivedCandidates(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      time.Nanosecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	longEntry := mgr.Register(monitor.NodeInfo{Tag: "long-lived"})
	longEntry.MarkInitialCheckDone(true)
	normalEntry := mgr.Register(monitor.NodeInfo{Tag: "normal"})
	normalEntry.MarkInitialCheckDone(true)
	normalEntry.ApplyUsageReportFailure(20, true)

	// Give the uptime threshold a chance to elapse while keeping the test
	// independent of wall-clock hours.
	time.Sleep(2 * time.Millisecond)
	if !longEntry.Snapshot().LongLived {
		t.Fatalf("expected long-lived test candidate, got %+v", longEntry.Snapshot())
	}

	p := &poolOutbound{
		mode:   modeSequential,
		sticky: newStickyState(time.Minute),
	}
	directive := &SelectionDirective{Strategy: StrategyStable}
	got := p.selectMemberWithDirective(
		[]*memberState{
			{tag: "normal", entry: normalEntry},
			{tag: "long-lived", entry: longEntry},
		},
		nil,
		nil,
		directive,
	)
	if got == nil || got.tag != "long-lived" {
		t.Fatalf("stable should prefer long-lived candidate, got %+v", got)
	}
}

func TestSelectMemberStableFallsBackWhenNoLongLivedCandidate(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      time.Nanosecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	entry := mgr.Register(monitor.NodeInfo{Tag: "normal"})
	entry.MarkInitialCheckDone(true)

	p := &poolOutbound{
		mode:   modeSequential,
		sticky: newStickyState(time.Minute),
	}
	got := p.selectMemberWithDirective(
		[]*memberState{{tag: "normal", entry: entry}},
		nil,
		nil,
		&SelectionDirective{Strategy: StrategyStable},
	)
	if got == nil || got.tag != "normal" {
		t.Fatalf("stable should fall back to healthy candidate, got %+v", got)
	}
}

func TestSelectMemberStableHonorsExplicitNoLongLivedFilter(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      time.Nanosecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	longEntry := mgr.Register(monitor.NodeInfo{Tag: "long-lived"})
	longEntry.MarkInitialCheckDone(true)
	normalEntry := mgr.Register(monitor.NodeInfo{Tag: "normal"})
	normalEntry.MarkInitialCheckDone(true)
	normalEntry.ApplyUsageReportFailure(20, true)
	time.Sleep(2 * time.Millisecond)

	p := &poolOutbound{
		mode:   modeSequential,
		sticky: newStickyState(time.Minute),
	}
	nolong := false
	got := p.selectMemberWithDirective(
		[]*memberState{
			{tag: "normal", entry: normalEntry},
			{tag: "long-lived", entry: longEntry},
		},
		nil,
		nil,
		&SelectionDirective{Strategy: StrategyStable, Filter: NodeFilter{LongLived: &nolong}},
	)
	if got == nil || got.tag != "normal" {
		t.Fatalf("explicit nolong filter should remain strict, got %+v", got)
	}
}

func TestAvailableMembersAppliesExplicitLongLivedFilterStrictly(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      time.Nanosecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	longEntry := mgr.Register(monitor.NodeInfo{Tag: "long-lived"})
	longEntry.MarkInitialCheckDone(true)
	normalEntry := mgr.Register(monitor.NodeInfo{Tag: "normal"})
	normalEntry.MarkInitialCheckDone(true)
	normalEntry.ApplyUsageReportFailure(20, true)
	time.Sleep(2 * time.Millisecond)
	if !longEntry.Snapshot().LongLived || normalEntry.Snapshot().LongLived {
		t.Fatalf("invalid test setup: long=%+v normal=%+v", longEntry.Snapshot(), normalEntry.Snapshot())
	}

	p := &poolOutbound{
		members: []*memberState{
			{tag: "long-lived", entry: longEntry},
			{tag: "normal", entry: normalEntry},
		},
	}

	for _, tt := range []struct {
		name     string
		wantLong bool
		wantTag  string
	}{
		{name: "long", wantLong: true, wantTag: "long-lived"},
		{name: "nolong", wantLong: false, wantTag: "normal"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := p.availableMembersLocked(
				time.Now(),
				"",
				nil,
				nil,
				nil,
				false,
				false,
				nil,
				&SelectionDirective{Filter: NodeFilter{LongLived: &tt.wantLong}},
			)
			if len(got) != 1 || got[0].tag != tt.wantTag {
				t.Fatalf("explicit %s filter returned %+v, want only %s", tt.name, got, tt.wantTag)
			}
		})
	}
}

func TestSelectMemberStableHonorsPinnedHealthyNonLongLivedCandidate(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      time.Nanosecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	longEntry := mgr.Register(monitor.NodeInfo{Tag: "long-lived"})
	longEntry.MarkInitialCheckDone(true)
	pinnedEntry := mgr.Register(monitor.NodeInfo{Tag: "pinned"})
	pinnedEntry.MarkInitialCheckDone(true)
	pinnedEntry.ApplyUsageReportFailure(20, true)
	time.Sleep(2 * time.Millisecond)
	if !longEntry.Snapshot().LongLived || pinnedEntry.Snapshot().LongLived {
		t.Fatalf("invalid test setup: long=%+v pinned=%+v", longEntry.Snapshot(), pinnedEntry.Snapshot())
	}

	p := &poolOutbound{
		mode:   modeSequential,
		sticky: newStickyState(time.Minute),
	}
	got := p.selectMemberWithDirective(
		[]*memberState{
			{tag: "pinned", entry: pinnedEntry},
			{tag: "long-lived", entry: longEntry},
		},
		nil,
		nil,
		&SelectionDirective{Strategy: StrategyStable, PinnedTag: "pinned"},
	)
	if got == nil || got.tag != "pinned" {
		t.Fatalf("healthy pinned candidate should win implicit long-lived preference, got %+v", got)
	}
}

func TestSelectMemberStableKeepsExistingBindingWhenLongLivedAppears(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{
		LongLivedMinUptime:      2 * time.Millisecond,
		LongLivedMinSuccessRate: 0.9,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	normalEntry := mgr.Register(monitor.NodeInfo{Tag: "normal"})
	normalEntry.MarkInitialCheckDone(true)
	normalEntry.ApplyUsageReportFailure(20, true)
	longEntry := mgr.Register(monitor.NodeInfo{Tag: "long-lived"})
	longEntry.MarkInitialCheckDone(true)

	p := &poolOutbound{mode: modeSequential, sticky: newStickyState(time.Minute)}
	directive := &SelectionDirective{Strategy: StrategyStable}
	candidates := []*memberState{
		{tag: "normal", entry: normalEntry},
		{tag: "long-lived", entry: longEntry},
	}

	first := p.selectMemberWithDirective(candidates, nil, nil, directive)
	if first == nil || first.tag != "normal" {
		t.Fatalf("expected initial fallback to normal node, got %+v", first)
	}
	time.Sleep(5 * time.Millisecond)
	if !longEntry.Snapshot().LongLived {
		t.Fatalf("expected long-lived candidate after threshold, got %+v", longEntry.Snapshot())
	}

	second := p.selectMemberWithDirective(candidates, nil, nil, directive)
	if second == nil || second.tag != "normal" {
		t.Fatalf("existing stable binding should not move when long-lived appears, got %+v", second)
	}
	third := p.selectMemberWithDirective([]*memberState{candidates[1]}, nil, nil, directive)
	if third == nil || third.tag != "long-lived" {
		t.Fatalf("binding should promote after pinned node disappears, got %+v", third)
	}
}

func TestSelectMemberAutoPenalizesBadSources(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	goodEntry := mgr.Register(monitor.NodeInfo{
		Tag:        "good-node",
		Name:       "Good Node",
		SourceRef:  "local-sub-1",
		SourceName: "subscription-1",
	})
	goodEntry.MarkInitialCheckDone(true)
	goodState := acquireSharedState("good-node")
	goodState.attachEntry(goodEntry)

	badEntry := mgr.Register(monitor.NodeInfo{
		Tag:        "bad-node",
		Name:       "Bad Node",
		SourceRef:  "local-sub-2",
		SourceName: "subscription-2",
	})
	badEntry.MarkInitialCheckDone(false)
	badEntry.RecordFailure(E.New("tls handshake: EOF"), "www.google.com:443")
	badState := acquireSharedState("bad-node")
	badState.attachEntry(badEntry)

	p := &poolOutbound{
		mode:    modeAuto,
		monitor: mgr,
		options: Options{
			Metadata: map[string]MemberMeta{
				"good-node": {Name: "Good Node", SourceRef: "local-sub-1", SourceName: "subscription-1"},
				"bad-node":  {Name: "Bad Node", SourceRef: "local-sub-2", SourceName: "subscription-2"},
			},
		},
	}

	selected := p.selectMember([]*memberState{
		{tag: "bad-node", shared: badState, entry: badEntry},
		{tag: "good-node", shared: goodState, entry: goodEntry},
	}, mgr.SourceSelectionStates(), nil)
	if selected == nil {
		t.Fatal("expected a selected member")
	}
	if selected.tag != "good-node" {
		t.Fatalf("expected good source to be preferred, got %q", selected.tag)
	}
}

func TestAvailableMembersLockedExcludesBadSecondaryClusters(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	goodEntry := mgr.Register(monitor.NodeInfo{
		Tag:            "good-node",
		Name:           "Good Node",
		SourceRef:      "manifest:agg",
		SourceName:     "Aggregator Stable",
		ProtocolFamily: "vless",
		NodeMode:       "tls/ws",
		DomainFamily:   "good.example.com",
	})
	goodEntry.MarkInitialCheckDone(true)
	goodState := acquireSharedState("good-node")
	goodState.attachEntry(goodEntry)

	badEntry := mgr.Register(monitor.NodeInfo{
		Tag:            "bad-node",
		Name:           "Bad Node",
		SourceRef:      "manifest:agg",
		SourceName:     "Aggregator Stable",
		ProtocolFamily: "vless",
		NodeMode:       "reality/tcp",
		DomainFamily:   "badcluster.example.com",
	})
	badEntry.MarkInitialCheckDone(false)
	badEntry.RecordFailure(E.New("reality verification failed"), "www.google.com:443")
	badState := acquireSharedState("bad-node")
	badState.attachEntry(badEntry)

	p := &poolOutbound{
		mode:    modeAuto,
		monitor: mgr,
		options: Options{
			Metadata: map[string]MemberMeta{
				"good-node": {
					Name:           "Good Node",
					SourceRef:      "manifest:agg",
					SourceName:     "Aggregator Stable",
					ProtocolFamily: "vless",
					NodeMode:       "tls/ws",
					DomainFamily:   "good.example.com",
				},
				"bad-node": {
					Name:           "Bad Node",
					SourceRef:      "manifest:agg",
					SourceName:     "Aggregator Stable",
					ProtocolFamily: "vless",
					NodeMode:       "reality/tcp",
					DomainFamily:   "badcluster.example.com",
				},
			},
		},
		members: []*memberState{
			{tag: "good-node", shared: goodState, entry: goodEntry},
			{tag: "bad-node", shared: badState, entry: badEntry},
		},
	}

	candidates := p.availableMembersLocked(
		time.Now(),
		"",
		nil,
		mgr.SourceSelectionStates(),
		mgr.SecondarySelectionStates(),
		true,
		true,
		nil,
		nil,
	)
	if len(candidates) != 1 || candidates[0].tag != "good-node" {
		t.Fatalf("expected only good-node to remain after secondary exclusion, got %+v", candidates)
	}
}
