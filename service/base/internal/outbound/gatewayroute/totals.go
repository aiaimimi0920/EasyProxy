package gatewayroute

import (
	"context"
	"sync/atomic"
)

// Totals is shared by every route created by a single box manager. A route
// finishing a dial during teardown still credits that manager exactly once.
type Totals struct {
	directCount, proxyCount, noNodeFallbackCount atomic.Uint64
	tcpCount, udpCount, ipv4Count, ipv6Count     atomic.Uint64
}

type totalsKey struct{}

func WithTotals(ctx context.Context, t *Totals) context.Context {
	return context.WithValue(ctx, totalsKey{}, t)
}

func totalsFrom(ctx context.Context) *Totals {
	if t, ok := ctx.Value(totalsKey{}).(*Totals); ok && t != nil {
		return t
	}
	return new(Totals)
}

func (t *Totals) Snapshot() Stats {
	return Stats{
		Direct: t.directCount.Load(), Proxy: t.proxyCount.Load(), NoNodeFallback: t.noNodeFallbackCount.Load(),
		TCP: t.tcpCount.Load(), UDP: t.udpCount.Load(), IPv4: t.ipv4Count.Load(), IPv6: t.ipv6Count.Load(),
	}
}
