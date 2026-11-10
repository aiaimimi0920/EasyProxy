package pool

import (
	"context"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"errors"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	"net"
	"testing"
	"time"
)

type tierRecordingOutbound struct {
	failingOutbound
	tag   string
	calls *[]string
}

func (o tierRecordingOutbound) Network() []string { return []string{"tcp", "udp"} }
func (o tierRecordingOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	*o.calls = append(*o.calls, o.tag)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("test dial failure")
}
func (o tierRecordingOutbound) ListenPacket(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	_, err := o.DialContext(ctx, "udp", M.Socksaddr{})
	return nil, err
}

func TestSelectionTiersDialAndPacketFallthrough(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			p := tierTestPool(t)
			p.logger = log.NewNOPFactory().Logger()
			var calls []string
			for _, m := range p.members {
				m.outbound = tierRecordingOutbound{tag: m.tag, calls: &calls}
			}
			d := &SelectionDirective{SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "JP"}, {Kind: "country", Country: "US"}}}
			ctx := WithDirective(context.Background(), d)
			var err error
			if network == "tcp" {
				_, err = p.DialContext(ctx, network, M.Socksaddr{})
			} else {
				_, err = p.ListenPacket(ctx, M.Socksaddr{})
			}
			if err == nil || len(calls) != 5 {
				t.Fatalf("calls=%v error=%v", calls, err)
			}
			seen := map[string]bool{}
			for i, tag := range calls {
				if seen[tag] {
					t.Fatalf("retried failed tag %s", tag)
				}
				seen[tag] = true
				country := p.options.Metadata[tag].CountryISO
				if (i < 2 && country != "JP") || (i >= 2 && i < 4 && country != "US") {
					t.Fatalf("wrong tier order: %v", calls)
				}
			}
		})
	}
}

func TestSelectionTiersHardHealthAndNetworkConstraints(t *testing.T) {
	p := tierTestPool(t)
	for _, m := range p.members {
		m.entry.RecordSuccessWithLatency(time.Millisecond)
	}
	d := &SelectionDirective{SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "JP"}, {Kind: "latency"}}}
	p.members[0].entry.RecordFailure(monitor.ErrUpstreamAuthenticationRequired, "test")
	got, err := p.pickMember("tcp", nil, d)
	if err != nil || got.tag == "jp-a" {
		t.Fatalf("quarantine escaped: %v %v", got, err)
	}
	d.RequiredTag = "jp-a"
	if _, err := p.pickMember("tcp", nil, d); err == nil {
		t.Fatal("required tag escaped quarantine")
	}
	d.RequiredTag = ""
	if _, err := p.pickMember("udp", nil, d); err == nil {
		t.Fatal("network constraint escaped")
	}
}
