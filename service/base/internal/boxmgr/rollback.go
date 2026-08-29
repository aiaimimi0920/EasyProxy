package boxmgr

import (
	"context"
	"errors"
	"fmt"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/pool"
)

func (m *Manager) startPeriodicHealthCheck(cfg *config.Config) {
	if m == nil || cfg == nil {
		return
	}
	m.mu.Lock()
	if m.monitorMgr == nil || m.healthCheckStarted {
		m.mu.Unlock()
		return
	}
	monitorMgr := m.monitorMgr
	interval := cfg.Management.HealthCheckInterval
	m.healthCheckStarted = true
	m.mu.Unlock()
	monitorMgr.StartPeriodicHealthCheck(interval, periodicHealthTimeout)
}

func (m *Manager) activeConfig() *config.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// latestAppliedRollbackState returns the newest independent snapshot that a
// successful hot apply has published. Reload transactions call this at their
// lifecycle boundaries because RecordAppliedConfig intentionally does not take
// reloadMu (the controller may already hold its operation mutex).
func (m *Manager) latestAppliedRollbackState(fallbackCfg *config.Config, fallbackIdle bool) (*config.Config, bool) {
	m.mu.RLock()
	lastAppliedCfg := m.lastAppliedCfg
	lastAppliedIdle := m.lastAppliedIdle
	m.mu.RUnlock()
	if cfg := snapshotConfig(lastAppliedCfg); cfg != nil {
		return cfg, lastAppliedIdle
	}
	return snapshotConfig(fallbackCfg), fallbackIdle
}

func (m *Manager) restoreAppliedState(_ context.Context, cfg *config.Config, idle bool, instance managedBox) {
	activeCfg := snapshotConfig(cfg)
	m.mu.Lock()
	m.currentBox = instance
	m.cfg = activeCfg
	m.idle = idle
	m.lastAppliedIdle = idle
	if activeCfg != nil {
		m.applyConfigSettings(activeCfg)
		m.lastAppliedMode = activeCfg.Mode
		m.lastAppliedBasePort = activeCfg.MultiPort.BasePort
	}
	monitorServer := m.monitorServer
	m.mu.Unlock()

	if monitorServer != nil {
		monitorServer.SetConfig(activeCfg)
	}
}

func (m *Manager) rollbackToOldConfig(
	ctx context.Context,
	oldCfg *config.Config,
	oldIdle bool,
	sharedStateTxn *pool.SharedStateTransaction,
) error {
	if oldCfg == nil {
		m.mu.Lock()
		m.currentBox = nil
		m.mu.Unlock()
		return errors.New("previous config is nil")
	}

	if sharedStateTxn != nil {
		if err := sharedStateTxn.Rollback(); err != nil {
			m.clearFailedRollbackState(ctx, oldCfg)
			return fmt.Errorf("restore previous shared state: %w", err)
		}
	}
	var rollbackGeneration monitor.Generation
	if m.monitorMgr != nil {
		rollbackGeneration = m.monitorMgr.BeginReload()
	}
	m.mu.Lock()
	probeConfigErr := m.applyMonitorProbeSettings(oldCfg)
	m.mu.Unlock()
	if probeConfigErr != nil {
		m.clearFailedRollbackState(ctx, oldCfg)
		return fmt.Errorf("restore previous probe settings: %w", probeConfigErr)
	}
	if oldIdle {
		if m.monitorMgr != nil {
			m.monitorMgr.SweepStaleNodes()
		}
		m.restoreAppliedState(ctx, oldCfg, true, nil)
		return nil
	}
	m.logger.Warnf("attempting rollback to previous config...")
	instance, err := m.createManagedBox(ctx, oldCfg)
	if err != nil {
		m.logger.Errorf("rollback failed to create box: %v", err)
		m.clearFailedRollbackState(ctx, oldCfg)
		return err
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		m.logger.Errorf("rollback failed to start box: %v", err)
		m.clearFailedRollbackState(ctx, oldCfg)
		return err
	}
	if m.monitorMgr != nil {
		m.monitorMgr.SweepStaleNodes()
	}
	m.restoreAppliedState(ctx, oldCfg, false, instance)
	if m.monitorMgr != nil {
		if _, ok := m.monitorMgr.ProbeTargets(); ok {
			probeCtx := ctx
			if probeCtx == nil {
				probeCtx = context.Background()
			}
			go func(generation monitor.Generation) {
				_, _ = m.monitorMgr.ProbeGeneration(probeCtx, generation, periodicHealthTimeout)
			}(rollbackGeneration)
		}
	}
	m.logger.Infof("rollback successful")
	return nil
}

func (m *Manager) clearFailedRollbackState(ctx context.Context, oldCfg *config.Config) {
	pool.ResetSharedStateStore()
	if m.monitorMgr != nil {
		m.monitorMgr.BeginReload()
		m.monitorMgr.SweepStaleNodes()
	}
	// A failed rollback has no running box. Preserve the last applied config in
	// explicit idle mode so a later reload can rebuild the runtime.
	m.restoreAppliedState(ctx, oldCfg, true, nil)
}

// Close terminates the active instance and auxiliary components.
func (m *Manager) Close() error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	m.mu.Lock()
	currentBox := m.currentBox
	monitorServer := m.monitorServer
	monitorMgr := m.monitorMgr
	m.currentBox = nil
	m.monitorServer = nil
	m.monitorMgr = nil
	m.healthCheckStarted = false
	m.baseCtx = nil
	m.idle = false
	m.mu.Unlock()

	var closeErr error
	if currentBox != nil {
		closeErr = errors.Join(closeErr, currentBox.Close())
	}
	if monitorServer != nil {
		monitorServer.Shutdown(context.Background())
	}
	if monitorMgr != nil {
		monitorMgr.Stop()
	}
	return closeErr
}

// MonitorManager returns the shared monitor manager.
