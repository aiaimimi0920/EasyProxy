package boxmgr

import (
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/pool"

	"github.com/sagernet/sing-box/adapter"
)

func (m *Manager) MonitorManager() *monitor.Manager {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.monitorMgr
}

// SetLongLivedThresholds updates the live monitor without rebuilding sing-box.
func (m *Manager) SetLongLivedThresholds(minUptime time.Duration, minRate float64) {
	m.mu.Lock()
	m.monitorCfg.LongLivedMinUptime = minUptime
	m.monitorCfg.LongLivedMinSuccessRate = minRate
	monitorMgr := m.monitorMgr
	m.mu.Unlock()

	if monitorMgr != nil {
		monitorMgr.SetLongLivedThresholds(minUptime, minRate)
	}
}

// RecordAppliedConfig advances the rollback snapshot after a successful
// non-structural hot apply. The active config pointer and idle state are
// intentionally preserved.
func (m *Manager) RecordAppliedConfig(cfg *config.Config) {
	applied := snapshotConfig(cfg)
	if applied == nil {
		return
	}

	m.mu.Lock()
	base := m.lastAppliedCfg
	if base == nil {
		base = m.cfg
	}
	m.lastAppliedCfg = mergeHotAppliedConfig(base, applied)
	m.lastAppliedMode = m.lastAppliedCfg.Mode
	m.lastAppliedBasePort = m.lastAppliedCfg.MultiPort.BasePort
	m.mu.Unlock()
}

// mergeHotAppliedConfig publishes only the fields that the routing controller
// can apply without rebuilding sing-box. Structural fields (mode, listeners,
// nodes, pool settings, session TTL, and GeoIP listener settings) remain from
// the last applied snapshot so a later rollback cannot restore a config that
// was merely edited in memory but never reloaded. Local Server credentials are
// hot state because the dispatcher and management server share the Profile
// Manager snapshot without rebinding either listener.
func mergeHotAppliedConfig(base, applied *config.Config) *config.Config {
	if applied == nil {
		return nil
	}
	merged := snapshotConfig(base)
	if merged == nil {
		return applied
	}

	merged.Routing.DefaultStrategy = applied.Routing.DefaultStrategy
	merged.Routing.FinalPolicy = applied.Routing.FinalPolicy
	merged.Routing.Rules = append([]string(nil), applied.Routing.Rules...)
	merged.Routing.RuleProviders = append([]config.RuleProvider(nil), applied.Routing.RuleProviders...)
	merged.Routing.LongLived = applied.Routing.LongLived
	if applied.Routing.UseDefaultRules == nil {
		merged.Routing.UseDefaultRules = nil
	} else {
		useDefaults := *applied.Routing.UseDefaultRules
		merged.Routing.UseDefaultRules = &useDefaults
	}
	if merged.Routing.Enabled && applied.Routing.Enabled {
		merged.GeoIP.Enabled = applied.GeoIP.Enabled
		merged.GeoIP.DatabasePath = applied.GeoIP.DatabasePath
	}
	if merged.LocalServer.Enabled && applied.LocalServer.Enabled && merged.DispatchListen() == applied.DispatchListen() {
		merged.Routing.Enabled = applied.Routing.Enabled
		merged.Routing.NodeFilter.Countries = append([]string(nil), applied.Routing.NodeFilter.Countries...)
		merged.Routing.NodeFilter.Regions = append([]string(nil), applied.Routing.NodeFilter.Regions...)
		if applied.Routing.NodeFilter.LongLived == nil {
			merged.Routing.NodeFilter.LongLived = nil
		} else {
			longLived := *applied.Routing.NodeFilter.LongLived
			merged.Routing.NodeFilter.LongLived = &longLived
		}
		merged.Routing.Session = applied.Routing.Session
		merged.LocalServer.SharedRevision = applied.LocalServer.SharedRevision
		merged.LocalServer.Auth = applied.LocalServer.Auth
		merged.LocalServer.CredentialGeneration = applied.LocalServer.CredentialGeneration
		merged.Listener.Username = applied.Listener.Username
		merged.Listener.Password = applied.Listener.Password
		merged.Management.Password = applied.Management.Password
	}
	return merged
}

// MonitorServer returns the monitor HTTP server.
func (m *Manager) MonitorServer() *monitor.Server {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.monitorServer
}

// PoolOutbound returns the live proxy-pool outbound from the current box, or
// nil if no box is running or the pool outbound is absent. It always reads the
// current box so callers stay correct across reloads (which swap the box).
func (m *Manager) PoolOutbound() (adapter.Outbound, bool) {
	m.mu.RLock()
	b := m.currentBox
	m.mu.RUnlock()
	if b == nil {
		return nil, false
	}
	return b.Outbound().Outbound(pool.Tag)
}

// StickySnapshot returns the live pool's stable/session affinity snapshot, or
// (zero, false) when no pool outbound is currently running.
func (m *Manager) StickySnapshot() (pool.StickySnapshot, bool) {
	out, ok := m.PoolOutbound()
	if !ok || out == nil {
		return pool.StickySnapshot{}, false
	}
	type stickyReporter interface {
		StickySnapshot() pool.StickySnapshot
	}
	sr, ok := out.(stickyReporter)
	if !ok {
		return pool.StickySnapshot{}, false
	}
	return sr.StickySnapshot(), true
}

// PrepareMonitor initializes the shared monitor manager/server ahead of the
// main box startup so callers can wire API integrations before startup blocks
// on initial health checks.
