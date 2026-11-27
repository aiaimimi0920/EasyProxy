package boxmgr

import (
	"easy_proxies/internal/outbound/gatewayroute"
)

type gatewayRouteStatsReporter interface {
	Snapshot() gatewayroute.Stats
}

func readBoxGatewayRouteStats(b managedBox) (gatewayroute.Stats, bool) {
	if b == nil || b.Outbound() == nil {
		return gatewayroute.Stats{}, false
	}
	out, ok := b.Outbound().Outbound(gatewayroute.Tag)
	if !ok || out == nil {
		return gatewayroute.Stats{}, false
	}
	reporter, ok := out.(gatewayRouteStatsReporter)
	if !ok {
		return gatewayroute.Stats{}, false
	}
	return reporter.Snapshot(), true
}

// GatewayRouteStats returns cumulative native TUN routing statistics across
// box reloads along with the current active connection count.
func (m *Manager) GatewayRouteStats() (gatewayroute.Stats, bool) {
	if m == nil {
		return gatewayroute.Stats{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if live, ok := readBoxGatewayRouteStats(m.currentBox); ok {
		return live, true
	}
	s := m.gatewayTotals.Snapshot()
	return s, s != (gatewayroute.Stats{})
}
