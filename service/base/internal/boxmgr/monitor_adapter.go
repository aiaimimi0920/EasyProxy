package boxmgr

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/store"
)

func (m *Manager) restoreMonitorStatsFromStore(ctx context.Context) error {
	if m.store == nil || m.monitorMgr == nil {
		return nil
	}

	nodes, err := m.store.ListNodes(ctx, store.NodeFilter{})
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes) == 0 {
		return nil
	}

	statsByNodeID, err := m.store.GetAllNodeStats(ctx)
	if err != nil {
		return fmt.Errorf("get node stats: %w", err)
	}
	if len(statsByNodeID) == 0 {
		return nil
	}

	restored := 0
	for _, node := range nodes {
		if !node.Enabled {
			continue
		}
		stats, ok := statsByNodeID[node.ID]
		if !ok || stats == nil {
			continue
		}

		state := monitor.PersistedState{
			FailureCount:         stats.FailureCount,
			SuccessCount:         stats.SuccessCount,
			TrafficSuccessCount:  stats.TrafficSuccessCount,
			Blacklisted:          stats.Blacklisted,
			BlacklistedUntil:     stats.BlacklistedUntil,
			LastError:            stats.LastError,
			LastFailureAt:        stats.LastFailureAt,
			LastSuccessAt:        stats.LastSuccessAt,
			LastTrafficSuccessAt: stats.LastTrafficSuccessAt,
			LastProbeAt:          stats.LastProbeAt,
			LastProbeSuccessAt:   stats.LastProbeSuccessAt,
			LastLatencyMs:        stats.LastLatencyMs,
			Available:            stats.Available,
			InitialCheckDone:     stats.InitialCheckDone,
			TotalUpload:          stats.TotalUploadBytes,
			TotalDownload:        stats.TotalDownloadBytes,
		}

		if state.LastTrafficSuccessAt.IsZero() && (state.TotalUpload > 0 || state.TotalDownload > 0) && !state.LastSuccessAt.IsZero() {
			state.LastTrafficSuccessAt = state.LastSuccessAt
		}

		if m.monitorMgr.RestorePersistedState(node.URI, node.Name, state) {
			restored++
		}
	}

	if restored > 0 {
		m.logger.Infof("restored persisted monitor stats for %d nodes", restored)
	}
	return nil
}

// ensureMonitor initializes monitor manager and server if needed.
func (m *Manager) ensureMonitor(ctx context.Context) error {
	m.mu.Lock()
	monitorMgr := m.monitorMgr
	createdManager := false
	if monitorMgr == nil {
		var err error
		monitorMgr, err = monitor.NewManager(m.monitorCfg)
		if err != nil {
			m.mu.Unlock()
			return fmt.Errorf("init monitor manager: %w", err)
		}
		monitorMgr.SetLogger(monitorLoggerAdapter{logger: m.logger})
		m.monitorMgr = monitorMgr
		createdManager = true
	}

	createdServer := false
	if m.monitorServer == nil {
		m.monitorServer = monitor.NewServer(m.monitorCfg, monitorMgr, log.Default())
		createdServer = true
	}
	server := m.monitorServer
	store := m.store
	startEnabled := m.monitorCfg.Enabled
	m.mu.Unlock()

	if server != nil {
		server.SetNodeManager(m)
		server.SetStore(store)
		if startEnabled {
			if err := server.Start(ctx); err != nil {
				if createdServer {
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					server.Shutdown(shutdownCtx)
					cancel()
				}
				if createdManager {
					monitorMgr.Stop()
				}
				m.mu.Lock()
				if createdServer && m.monitorServer == server {
					m.monitorServer = nil
				}
				if createdManager && m.monitorMgr == monitorMgr {
					m.monitorMgr = nil
				}
				m.mu.Unlock()
				return fmt.Errorf("start monitor server: %w", err)
			}
		}
	}
	return nil
}

// applyConfigSettings extracts runtime settings from config.
func (m *Manager) applyConfigSettings(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if cfg.SubscriptionRefresh.DrainTimeout > 0 {
		m.drainTimeout = cfg.SubscriptionRefresh.DrainTimeout
	} else if m.drainTimeout == 0 {
		m.drainTimeout = defaultDrainTimeout
	}
	m.minAvailableNodes = cfg.SubscriptionRefresh.MinAvailableNodes
	m.monitorCfg.Enabled = cfg.ManagementEnabled()
	m.monitorCfg.Listen = cfg.Management.Listen
	m.monitorCfg.Password = cfg.Management.Password
	m.monitorCfg.ExternalIP = cfg.ExternalIP
	if cfg.Mode == "hybrid" || cfg.Mode == "multi-port" {
		m.monitorCfg.ProxyUsername = cfg.MultiPort.Username
		m.monitorCfg.ProxyPassword = cfg.MultiPort.Password
	} else {
		m.monitorCfg.ProxyUsername = cfg.Listener.Username
		m.monitorCfg.ProxyPassword = cfg.Listener.Password
	}
	if err := m.applyMonitorProbeSettings(cfg); err != nil {
		m.logger.Warnf("failed to update probe settings from config: %v", err)
	}
}

func (m *Manager) applyMonitorProbeSettings(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	m.monitorCfg.ProbeTarget = cfg.Management.ProbeTarget
	m.monitorCfg.ProbeTargets = append([]string(nil), cfg.Management.ProbeTargets...)
	m.monitorCfg.SkipCertVerify = cfg.SkipCertVerify
	if m.monitorMgr == nil {
		return nil
	}
	m.monitorMgr.SetSkipCertVerify(cfg.SkipCertVerify)
	return m.monitorMgr.UpdateProbeTargets(cfg.Management.ProbeTargets, cfg.Management.ProbeTarget)
}

func (m *Manager) prepareMonitorServerTransition(activeCfg *config.Config) (*monitor.ListenerTransition, error) {
	if activeCfg == nil {
		return nil, nil
	}
	m.mu.Lock()
	monitorMgr := m.monitorMgr
	server := m.monitorServer
	store := m.store
	if monitorMgr != nil && server == nil {
		server = monitor.NewServer(m.monitorCfg, monitorMgr, log.Default())
		m.monitorServer = server
	}
	m.mu.Unlock()
	if monitorMgr == nil || server == nil {
		return nil, nil
	}
	server.SetNodeManager(m)
	server.SetStore(store)
	return server.PrepareListener(activeCfg.ManagementEnabled(), activeCfg.Management.Listen, activeCfg)
}

func (m *Manager) syncMonitorServerLifecycle(
	ctx context.Context,
	_ monitor.Config,
	activeCfg *config.Config,
) error {
	transition, err := m.prepareMonitorServerTransition(activeCfg)
	if err != nil || transition == nil {
		return err
	}
	if err := transition.Activate(ctx); err != nil {
		transition.Abort()
		return err
	}
	transition.Finalize()
	if server := m.MonitorServer(); server != nil {
		server.SetConfig(activeCfg)
	}
	return nil
}

func hasRuntimeSourceRefs(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	if len(cfg.Subscriptions) > 0 {
		return true
	}
	for _, connector := range cfg.Connectors {
		if connector.Enabled {
			return true
		}
	}
	if cfg.SourceSync.Enabled {
		if strings.TrimSpace(cfg.SourceSync.ManifestURL) != "" || len(cfg.SourceSync.FallbackSubscriptions) > 0 {
			return true
		}
	}
	return false
}

func (m *Manager) nodeIndexByRefLocked(ref string) int {
	ref = strings.TrimSpace(ref)
	if ref == "" || m.cfg == nil {
		return -1
	}
	for idx, node := range m.cfg.Nodes {
		if node.URI == ref || node.Name == ref {
			return idx
		}
	}
	return -1
}

// defaultLogger is the fallback logger using standard log.
type defaultLogger struct{}

func (defaultLogger) Infof(format string, args ...any) {
	log.Printf("[boxmgr] "+format, args...)
}

func (defaultLogger) Warnf(format string, args ...any) {
	log.Printf("[boxmgr] WARN: "+format, args...)
}

func (defaultLogger) Errorf(format string, args ...any) {
	log.Printf("[boxmgr] ERROR: "+format, args...)
}

// monitorLoggerAdapter adapts Logger to monitor.Logger interface.
type monitorLoggerAdapter struct {
	logger Logger
}

func (a monitorLoggerAdapter) Info(args ...any) {
	if a.logger != nil {
		a.logger.Infof("%s", fmt.Sprint(args...))
	}
}

func (a monitorLoggerAdapter) Warn(args ...any) {
	if a.logger != nil {
		a.logger.Warnf("%s", fmt.Sprint(args...))
	}
}

// --- NodeManager interface implementation ---
