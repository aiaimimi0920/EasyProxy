package pool

import (
	"testing"
	"time"

	"easy_proxies/internal/monitor"

	N "github.com/sagernet/sing/common/network"
)

type udpProtocolTestOutbound struct{ directProbeOutbound }

func (udpProtocolTestOutbound) Network() []string { return []string{N.NetworkUDP} }

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

func TestAvailableMembersRequireEffectivePreferredProtocol(t *testing.T) {
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	healthyNative := mgr.Register(monitor.NodeInfo{Tag: "healthy-native"})
	healthyNative.MarkInitialCheckDone(true)
	unhealthyNative := mgr.Register(monitor.NodeInfo{Tag: "unhealthy-native"})
	unhealthyNative.MarkInitialCheckDone(false)
	healthyFallback := mgr.Register(monitor.NodeInfo{Tag: "healthy-fallback"})
	healthyFallback.MarkInitialCheckDone(true)

	p := &poolOutbound{
		options: Options{Metadata: map[string]MemberMeta{
			"healthy-native":   {ProtocolFamily: "hysteria2"},
			"unhealthy-native": {ProtocolFamily: "hysteria2"},
			"healthy-fallback": {ProtocolFamily: "ss"},
		}},
		members: []*memberState{
			{tag: "healthy-native", outbound: udpProtocolTestOutbound{}, entry: healthyNative},
			{tag: "unhealthy-native", outbound: udpProtocolTestOutbound{}, entry: unhealthyNative},
			{tag: "healthy-fallback", outbound: udpProtocolTestOutbound{}, entry: healthyFallback},
		},
	}
	directive := &SelectionDirective{
		PreferredProtocolFamilies: []string{"hysteria2", "tuic"},
		RequireAvailablePreferred: true,
	}
	candidates := p.availableMembersLocked(time.Now(), N.NetworkUDP, nil, nil, nil, false, false, nil, directive)
	if len(candidates) != 1 || candidates[0].tag != "healthy-native" {
		t.Fatalf("required native UDP candidates = %+v", candidates)
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
