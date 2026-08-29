package boxmgr

import (
	"context"
	"errors"
	"fmt"

	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/pool"
)

func (m *Manager) enterIdle(newCfg *config.Config) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	return m.enterIdleLocked(newCfg)
}

func (m *Manager) enterIdleLocked(newCfg *config.Config) error {
	return m.enterIdleLockedWithEphemeralNodes(newCfg, nil, false)
}

func (m *Manager) enterIdleLockedWithEphemeralNodes(
	newCfg *config.Config,
	ephemeralNodes []config.NodeConfig,
	publishEphemeral bool,
) error {
	if newCfg == nil {
		return errors.New("new config is nil")
	}

	targetCfg := snapshotConfig(newCfg)
	m.mu.Lock()
	if m.currentBox == nil && !m.idle {
		m.mu.Unlock()
		return errors.New("manager not started")
	}
	oldBox := m.currentBox
	lastAppliedCfg := m.lastAppliedCfg
	sharedCfg := m.cfg
	oldIdle := m.lastAppliedIdle
	currentIdle := m.idle
	ctx := m.baseCtx
	reloadListeners := append([]ReloadLifecycleListener(nil), m.reloadListeners...)
	m.mu.Unlock()
	oldCfg := snapshotConfig(lastAppliedCfg)
	if oldCfg == nil {
		oldCfg = snapshotConfig(sharedCfg)
		oldIdle = currentIdle
	}

	if ctx == nil {
		ctx = context.Background()
	}
	from := ReloadState{Config: snapshotConfig(oldCfg), Idle: oldIdle}
	to := ReloadState{Config: snapshotConfig(targetCfg), Idle: true}
	for _, listener := range reloadListeners {
		if err := listener.PrepareReload(ctx, cloneReloadState(from), cloneReloadState(to)); err != nil {
			cause := fmt.Errorf("prepare idle reload: %w", err)
			oldCfg, oldIdle = m.latestAppliedRollbackState(oldCfg, oldIdle)
			from = ReloadState{Config: snapshotConfig(oldCfg), Idle: oldIdle}
			m.restoreAppliedState(ctx, oldCfg, oldIdle, oldBox)
			restored, restoreErr := m.notifyReloadFailed(ctx, reloadListeners, from, to, cause, true)
			if restored {
				m.notifyConfigListeners(m.activeConfig())
			}
			return errors.Join(cause, restoreErr)
		}
	}
	oldCfg, oldIdle = m.latestAppliedRollbackState(oldCfg, oldIdle)
	from = ReloadState{Config: snapshotConfig(oldCfg), Idle: oldIdle}

	if oldBox != nil {
		m.logger.Infof("stopping instance (all nodes disabled)...")
		if err := oldBox.Close(); err != nil {
			m.logger.Warnf("error closing instance during idle transition: %v", err)
		}
	}
	m.mu.Lock()
	if m.currentBox == oldBox {
		m.currentBox = nil
	}
	m.mu.Unlock()

	sharedStateTxn := pool.BeginSharedStateTransaction()
	if m.monitorMgr != nil {
		m.monitorMgr.BeginReload()
		m.monitorMgr.SweepStaleNodes()
	}
	monitorTransition, err := m.prepareMonitorServerTransition(targetCfg)
	if err != nil {
		cause := fmt.Errorf("prepare idle monitor listener: %w", err)
		return m.failReload(ctx, reloadListeners, from, to, cause, nil, oldCfg, oldIdle, sharedStateTxn)
	}

	m.mu.Lock()
	m.currentBox = nil
	m.cfg = targetCfg
	m.idle = true
	m.mu.Unlock()

	for _, listener := range reloadListeners {
		if err := listener.CompleteReload(ctx, cloneReloadState(from), cloneReloadState(to)); err != nil {
			if monitorTransition != nil {
				monitorTransition.Abort()
			}
			cause := fmt.Errorf("complete idle reload: %w", err)
			return m.failReload(ctx, reloadListeners, from, to, cause, nil, oldCfg, oldIdle, sharedStateTxn)
		}
	}
	if monitorTransition != nil {
		if err := monitorTransition.Activate(ctx); err != nil {
			monitorTransition.Abort()
			cause := fmt.Errorf("activate idle monitor listener: %w", err)
			return m.failReload(ctx, reloadListeners, from, to, cause, nil, oldCfg, oldIdle, sharedStateTxn)
		}
	}
	if err := sharedStateTxn.Commit(); err != nil {
		if monitorTransition != nil {
			_ = monitorTransition.Rollback()
		}
		cause := fmt.Errorf("commit idle shared state: %w", err)
		return m.failReload(ctx, reloadListeners, from, to, cause, nil, oldCfg, oldIdle, sharedStateTxn)
	}
	m.mu.Lock()
	m.applyConfigSettings(targetCfg)
	m.lastAppliedCfg = snapshotConfig(targetCfg)
	m.lastAppliedIdle = true
	m.lastAppliedMode = targetCfg.Mode
	m.lastAppliedBasePort = targetCfg.MultiPort.BasePort
	if publishEphemeral {
		m.ephemeralNodes = cloneNodes(ephemeralNodes)
	}
	monitorServer := m.monitorServer
	listeners := append([]ConfigUpdateListener(nil), m.configListeners...)
	m.mu.Unlock()

	if monitorTransition == nil && monitorServer != nil {
		monitorServer.SetConfig(targetCfg)
	}
	if monitorTransition != nil {
		monitorTransition.Finalize()
	}
	for _, listener := range listeners {
		listener.OnConfigUpdate(targetCfg)
	}

	m.logger.Infof("entered idle state (0 enabled nodes); re-enable nodes and reload to resume")
	return nil
}

// CurrentPortMap returns the current port mapping from the active configuration.
