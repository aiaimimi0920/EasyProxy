package pool

import (
	"testing"
	"time"
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
