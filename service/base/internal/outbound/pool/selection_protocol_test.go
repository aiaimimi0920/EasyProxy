package pool

import (
	"testing"
	"time"

	"easy_proxies/internal/monitor"
)

func TestSelectMemberPrefersRequestedProtocolFamily(t *testing.T) {
	p := &poolOutbound{
		mode:   modeSequential,
		sticky: newStickyState(time.Minute),
		options: Options{Metadata: map[string]MemberMeta{
			"shadowsocks": {ProtocolFamily: "ss"},
			"native-udp":  {ProtocolFamily: "Hysteria2"},
		}},
	}
	candidates := []*memberState{{tag: "shadowsocks"}, {tag: "native-udp"}}
	got := p.selectMemberWithDirective(candidates, nil, nil, &SelectionDirective{
		Strategy:                  StrategyAuto,
		PreferredProtocolFamilies: []string{"hysteria2", "tuic"},
	})
	if got == nil || got.tag != "native-udp" {
		t.Fatalf("preferred protocol selection = %+v", got)
	}
}

func TestSelectMemberFallsBackWhenPreferredProtocolFamilyIsUnavailable(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	fallbackEntry := mgr.Register(monitor.NodeInfo{Tag: "shadowsocks"})
	fallbackEntry.MarkInitialCheckDone(true)
	preferredEntry := mgr.Register(monitor.NodeInfo{Tag: "native-udp"})
	preferredEntry.MarkInitialCheckDone(false)

	p := &poolOutbound{
		mode:   modeAuto,
		sticky: newStickyState(time.Minute),
		options: Options{Metadata: map[string]MemberMeta{
			"shadowsocks": {ProtocolFamily: "ss"},
			"native-udp":  {ProtocolFamily: "hysteria2"},
		}},
	}
	candidates := []*memberState{
		{tag: "native-udp", entry: preferredEntry},
		{tag: "shadowsocks", entry: fallbackEntry},
	}
	got := p.selectMemberWithDirective(candidates, nil, nil, &SelectionDirective{
		Strategy:                  StrategyAuto,
		PreferredProtocolFamilies: []string{"hysteria2", "tuic"},
	})
	if got == nil || got.tag != "shadowsocks" {
		t.Fatalf("unavailable preferred protocol selection = %+v", got)
	}

	preferredEntry.MarkInitialCheckDone(true)
	got = p.selectMemberWithDirective(candidates, nil, nil, &SelectionDirective{
		Strategy:                  StrategyAuto,
		PreferredProtocolFamilies: []string{"hysteria2", "tuic"},
	})
	if got == nil || got.tag != "native-udp" {
		t.Fatalf("available preferred protocol selection = %+v", got)
	}
}

func TestStableBucketSeparatesTransportPreferences(t *testing.T) {
	tcp := SelectionDirective{ProfileID: "gateway/tcp"}
	udp := SelectionDirective{
		ProfileID:                 "gateway/udp",
		PreferredProtocolFamilies: []string{"hysteria2"},
	}
	if tcp.stableBucketKey() == udp.stableBucketKey() {
		t.Fatal("TCP and UDP directives shared a stable bucket")
	}
}
