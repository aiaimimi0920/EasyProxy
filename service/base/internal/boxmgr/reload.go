package boxmgr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"easy_proxies/internal/builder"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/pool"
)

func (m *Manager) Reload(newCfg *config.Config) error {
	if newCfg == nil {
		return errors.New("new config is nil")
	}
	intent, err := m.BeginReloadIntent(context.Background())
	if err != nil {
		return err
	}
	defer intent.End()
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	return m.reloadLocked(newCfg)
}

// reloadLocked performs one reload while the caller holds reloadMu. Keeping
// target capture under the same mutex as node CRUD prevents edits from racing
// the candidate snapshot.
func (m *Manager) reloadLocked(newCfg *config.Config) error {
	return m.reloadLockedWithEphemeralNodes(newCfg, nil, false)
}

func (m *Manager) reloadLockedWithEphemeralNodes(
	newCfg *config.Config,
	ephemeralNodes []config.NodeConfig,
	publishEphemeral bool,
) error {
	if newCfg == nil {
		return errors.New("new config is nil")
	}

	targetCfg := snapshotConfig(newCfg)
	if targetCfg == nil {
		return errors.New("new config is nil")
	}
	if targetCfg.DispatchEnabled() && len(targetCfg.Nodes) > 0 && !builder.HasValidNode(targetCfg) && !tunDirectFallback(targetCfg) {
		return m.enterIdleLockedWithEphemeralNodes(targetCfg, ephemeralNodes, publishEphemeral)
	}

	m.mu.Lock()
	if m.currentBox == nil && !m.idle {
		m.mu.Unlock()
		return errors.New("manager not started")
	}
	ctx := m.baseCtx
	oldBox := m.currentBox
	lastAppliedCfg := m.lastAppliedCfg
	sharedCfg := m.cfg
	oldIdle := m.lastAppliedIdle
	currentIdle := m.idle
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
	to := ReloadState{Config: snapshotConfig(targetCfg), Idle: false}
	for _, listener := range reloadListeners {
		if err := listener.PrepareReload(ctx, cloneReloadState(from), cloneReloadState(to)); err != nil {
			cause := fmt.Errorf("prepare reload: %w", err)
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
	// A hot apply can finish while a lifecycle listener is waiting in
	// PrepareReload. Refresh the rollback baseline after all prepare hooks so a
	// later candidate failure cannot restore an obsolete snapshot.
	oldCfg, oldIdle = m.latestAppliedRollbackState(oldCfg, oldIdle)
	from = ReloadState{Config: snapshotConfig(oldCfg), Idle: oldIdle}

	m.logger.Infof("reloading with %d nodes", len(targetCfg.Nodes))

	// For multi-port mode, we must close old instance first to release ports
	// This causes a brief interruption but avoids port conflicts
	if oldBox != nil {
		m.logger.Infof("stopping old instance to release ports...")
		if err := oldBox.Close(); err != nil {
			m.logger.Warnf("error closing old instance: %v", err)
		}
	}
	m.mu.Lock()
	if m.currentBox == oldBox {
		m.currentBox = nil
	}
	releaseDelay := m.portReleaseDelay
	m.mu.Unlock()
	if releaseDelay > 0 {
		time.Sleep(releaseDelay)
	}
	sharedStateTxn := pool.BeginSharedStateTransaction()

	// Begin a new reload generation. Nodes re-registered during createBox will
	// be marked with the new generation; stale (disabled/removed) nodes will be
	// swept after the new box is successfully started.
	var candidateGeneration monitor.Generation
	if m.monitorMgr != nil {
		candidateGeneration = m.monitorMgr.BeginReload()
	}
	m.mu.Lock()
	probeConfigErr := m.applyMonitorProbeSettings(targetCfg)
	m.mu.Unlock()
	if probeConfigErr != nil {
		cause := fmt.Errorf("apply candidate probe settings: %w", probeConfigErr)
		return m.failReload(ctx, reloadListeners, from, to, cause, nil, oldCfg, oldIdle, sharedStateTxn)
	}

	// Create and start new box instance with automatic port conflict resolution
	var instance managedBox
	maxRetries := 10
	var startErr error
	started := false
	for retry := 0; retry < maxRetries; retry++ {
		var err error
		instance, err = m.createManagedBox(ctx, targetCfg)
		if err != nil {
			cause := fmt.Errorf("create new box: %w", err)
			return m.failReload(ctx, reloadListeners, from, to, cause, instance, oldCfg, oldIdle, sharedStateTxn)
		}
		if err = instance.Start(); err != nil {
			// Check if it's a port conflict error
			if conflictPort := extractPortFromBindError(err); conflictPort > 0 {
				m.logger.Warnf("port %d is in use, reassigning and retrying...", conflictPort)
				if reassigned := reassignConflictingPort(targetCfg, conflictPort); reassigned {
					if closeErr := instance.Close(); closeErr != nil {
						cause := errors.Join(
							fmt.Errorf("start new box: %w", err),
							fmt.Errorf("close conflicted candidate: %w", closeErr),
						)
						return m.failReload(ctx, reloadListeners, from, to, cause, instance, oldCfg, oldIdle, sharedStateTxn)
					}
					instance = nil
					if resetErr := sharedStateTxn.ResetCandidate(); resetErr != nil {
						cause := errors.Join(
							fmt.Errorf("start new box: %w", err),
							fmt.Errorf("reset candidate shared state: %w", resetErr),
						)
						return m.failReload(ctx, reloadListeners, from, to, cause, nil, oldCfg, oldIdle, sharedStateTxn)
					}
					startErr = err
					continue
				}
			}
			cause := fmt.Errorf("start new box: %w", err)
			return m.failReload(ctx, reloadListeners, from, to, cause, instance, oldCfg, oldIdle, sharedStateTxn)
		}
		started = true
		break // Success
	}
	if !started {
		cause := fmt.Errorf("start new box after %d retries: %w", maxRetries, startErr)
		return m.failReload(ctx, reloadListeners, from, to, cause, instance, oldCfg, oldIdle, sharedStateTxn)
	}

	// Sweep stale monitor entries (disabled/removed nodes) now that the new box
	// has successfully registered all active nodes with the current generation.
	if m.monitorMgr != nil {
		m.monitorMgr.SweepStaleNodes()
	}

	if targetCfg.SubscriptionRefresh.MinAvailableNodes > 0 {
		timeout := targetCfg.SubscriptionRefresh.HealthCheckTimeout
		if timeout <= 0 {
			timeout = defaultHealthCheckTimeout
		}
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		summary, probeErr := m.monitorMgr.ProbeGeneration(probeCtx, candidateGeneration, timeout)
		cancel()
		if probeErr != nil || summary.Available < targetCfg.SubscriptionRefresh.MinAvailableNodes {
			healthErr := probeErr
			if healthErr == nil {
				healthErr = fmt.Errorf(
					"%d/%d candidate nodes available (need >= %d)",
					summary.Available,
					summary.Total,
					targetCfg.SubscriptionRefresh.MinAvailableNodes,
				)
			}
			m.logger.Warnf("reload health check failed: %v", healthErr)
			cause := fmt.Errorf("reload health check failed: %w", healthErr)
			return m.failReload(ctx, reloadListeners, from, to, cause, instance, oldCfg, oldIdle, sharedStateTxn)
		}
	} else if m.monitorMgr != nil {
		m.monitorMgr.RequestProbeAllOnce(periodicHealthTimeout)
	}
	monitorTransition, err := m.prepareMonitorServerTransition(targetCfg)
	if err != nil {
		cause := fmt.Errorf("prepare monitor listener: %w", err)
		return m.failReload(ctx, reloadListeners, from, to, cause, instance, oldCfg, oldIdle, sharedStateTxn)
	}

	m.mu.Lock()
	m.currentBox = instance
	m.cfg = targetCfg
	m.idle = false
	m.mu.Unlock()

	for _, listener := range reloadListeners {
		if err := listener.CompleteReload(ctx, cloneReloadState(from), cloneReloadState(to)); err != nil {
			if monitorTransition != nil {
				monitorTransition.Abort()
			}
			cause := fmt.Errorf("complete reload: %w", err)
			return m.failReload(ctx, reloadListeners, from, to, cause, instance, oldCfg, oldIdle, sharedStateTxn)
		}
	}
	if monitorTransition != nil {
		if err := monitorTransition.Activate(ctx); err != nil {
			monitorTransition.Abort()
			cause := fmt.Errorf("activate monitor listener: %w", err)
			return m.failReload(ctx, reloadListeners, from, to, cause, instance, oldCfg, oldIdle, sharedStateTxn)
		}
	}
	if err := sharedStateTxn.Commit(); err != nil {
		if monitorTransition != nil {
			_ = monitorTransition.Rollback()
		}
		cause := fmt.Errorf("commit candidate shared state: %w", err)
		return m.failReload(ctx, reloadListeners, from, to, cause, instance, oldCfg, oldIdle, sharedStateTxn)
	}
	m.mu.Lock()
	m.applyConfigSettings(targetCfg)
	m.lastAppliedCfg = snapshotConfig(targetCfg)
	m.lastAppliedIdle = false
	m.lastAppliedMode = targetCfg.Mode
	m.lastAppliedBasePort = targetCfg.MultiPort.BasePort
	if publishEphemeral {
		m.ephemeralNodes = cloneNodes(ephemeralNodes)
	}
	monitorServer := m.monitorServer
	listeners := append([]ConfigUpdateListener(nil), m.configListeners...)
	m.mu.Unlock()

	// Activate has already published targetCfg into the monitor server while
	// holding its config update barrier. The rollback bookkeeping above is
	// completed before the barrier is released, so a queued edit cannot mutate
	// the candidate before lastAppliedCfg is recorded.
	if monitorTransition == nil && monitorServer != nil {
		monitorServer.SetConfig(targetCfg)
	}
	// Release the monitor config-update barrier before invoking extension
	// listeners. A listener may synchronously call Server.SetConfig; keeping the
	// transition lock across that callback would self-deadlock. Bookkeeping above
	// is complete before the barrier is released, so a re-entrant edit cannot
	// race lastAppliedCfg or rollback state.
	if monitorTransition != nil {
		monitorTransition.Finalize()
	}
	for _, listener := range listeners {
		listener.OnConfigUpdate(targetCfg)
	}

	m.logger.Infof("reload completed successfully with %d nodes", len(targetCfg.Nodes))
	return nil
}

// AddConfigListener registers a listener to be notified when config changes after reload.
func (m *Manager) AddConfigListener(l ConfigUpdateListener) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configListeners = append(m.configListeners, l)
}

// AddReloadLifecycleListener registers a listener for transactional reload hooks.
func (m *Manager) AddReloadLifecycleListener(l ReloadLifecycleListener) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reloadListeners = append(m.reloadListeners, l)
}

// beginReloadMutationGuard blocks new node/config mutations before a reload
// target is captured, and waits for an already-running mutation to finish.
// Holding intentMu while taking reloadMu closes the hand-off race between the
// mutation gate and the reload transaction.
func (m *Manager) beginReloadMutationGuard() func() {
	m.reloadIntentMu.Lock()
	if m.reloadIntentCond == nil {
		m.reloadIntentCond = sync.NewCond(&m.reloadIntentMu)
	}
	m.reloadIntentCount++
	m.reloadIntentMu.Unlock()

	m.reloadMu.Lock()
	m.reloadMu.Unlock()

	return func() {
		m.reloadIntentMu.Lock()
		if m.reloadIntentCount > 0 {
			m.reloadIntentCount--
		}
		if m.reloadIntentCount == 0 && m.reloadIntentCond != nil {
			m.reloadIntentCond.Broadcast()
		}
		m.reloadIntentMu.Unlock()
	}
}

func (m *Manager) lockConfigMutation(ctx context.Context) error {
	m.reloadIntentMu.Lock()
	if m.reloadIntentCond == nil {
		m.reloadIntentCond = sync.NewCond(&m.reloadIntentMu)
	}
	for m.reloadIntentCount > 0 {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				m.reloadIntentMu.Unlock()
				return err
			}
		}
		m.reloadIntentCond.Wait()
	}
	// Keep intentMu held while acquiring reloadMu so a new intent cannot begin
	// target capture between the gate check and the mutation lock.
	m.reloadMu.Lock()
	m.reloadIntentMu.Unlock()
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			m.reloadMu.Unlock()
			return err
		}
	}
	return nil
}

func (m *Manager) unlockConfigMutation() {
	m.reloadMu.Unlock()
}

// BeginConfigMutation serializes a persisted configuration edit with reload
// target capture and commit. The returned release function must be called once.
func (m *Manager) BeginConfigMutation(ctx context.Context) (func(), error) {
	if err := m.lockConfigMutation(ctx); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(m.unlockConfigMutation)
	}, nil
}

// BeginReloadIntent announces a reload before its target configuration is
// captured. Callers that perform disk I/O or network refreshes before Reload
// should hold the returned token across that work.
func (m *Manager) BeginReloadIntent(ctx context.Context) (*ReloadIntent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	reloadListeners := append([]ReloadLifecycleListener(nil), m.reloadListeners...)
	monitorServer := m.monitorServer
	m.mu.RUnlock()

	intent := &ReloadIntent{ctx: ctx}
	intent.endMutationGuard = m.beginReloadMutationGuard()
	if monitorServer != nil {
		monitorServer.BeginReloadWindow()
		intent.endWindow = monitorServer.EndReloadWindow
	}
	for _, listener := range reloadListeners {
		intentListener, ok := listener.(ReloadIntentListener)
		if !ok {
			continue
		}
		if err := intentListener.BeginReloadIntent(ctx); err != nil {
			intent.End()
			return nil, fmt.Errorf("begin reload intent: %w", err)
		}
		intent.listeners = append(intent.listeners, intentListener)
	}
	return intent, nil
}

// failReload closes a rejected candidate and restores the last applied state.
func (m *Manager) failReload(
	ctx context.Context,
	reloadListeners []ReloadLifecycleListener,
	from ReloadState,
	to ReloadState,
	cause error,
	candidate managedBox,
	oldCfg *config.Config,
	oldIdle bool,
	sharedStateTxn *pool.SharedStateTransaction,
) error {
	oldCfg, oldIdle = m.latestAppliedRollbackState(oldCfg, oldIdle)
	from = ReloadState{Config: snapshotConfig(oldCfg), Idle: oldIdle}

	m.mu.Lock()
	if candidate != nil && m.currentBox == candidate {
		m.currentBox = nil
	}
	m.mu.Unlock()
	var candidateCloseErr error
	if candidate != nil {
		if err := candidate.Close(); err != nil {
			candidateCloseErr = fmt.Errorf("close failed candidate: %w", err)
			m.logger.Warnf("%v", candidateCloseErr)
		}
	}

	rollbackErr := m.rollbackToOldConfig(ctx, oldCfg, oldIdle, sharedStateTxn)
	restored := rollbackErr == nil && candidateCloseErr == nil
	restored, listenerErr := m.notifyReloadFailed(ctx, reloadListeners, from, to, cause, restored)
	if restored {
		m.notifyConfigListeners(m.activeConfig())
	}
	var wrappedRollbackErr error
	if rollbackErr != nil {
		wrappedRollbackErr = fmt.Errorf("rollback failed: %w", rollbackErr)
	}
	var wrappedListenerErr error
	if listenerErr != nil {
		wrappedListenerErr = fmt.Errorf("reload listener restore failed: %w", listenerErr)
	}
	return errors.Join(cause, candidateCloseErr, wrappedRollbackErr, wrappedListenerErr)
}

func (m *Manager) notifyReloadFailed(
	ctx context.Context,
	listeners []ReloadLifecycleListener,
	from ReloadState,
	to ReloadState,
	cause error,
	restored bool,
) (bool, error) {
	var restoreErr error
	for _, listener := range listeners {
		if err := listener.FailedReload(ctx, cloneReloadState(from), cloneReloadState(to), cause, restored); err != nil {
			restored = false
			restoreErr = errors.Join(restoreErr, err)
		}
	}
	return restored, restoreErr
}

func (m *Manager) notifyConfigListeners(cfg *config.Config) {
	m.mu.RLock()
	listeners := append([]ConfigUpdateListener(nil), m.configListeners...)
	m.mu.RUnlock()
	for _, listener := range listeners {
		listener.OnConfigUpdate(cfg)
	}
}

// CurrentReloadState returns the last successfully applied immutable runtime
// snapshot. It is safe to call before Start and preserves the committed idle
// bit so lifecycle consumers do not infer state from currentBox alone.
func (m *Manager) CurrentReloadState() ReloadState {
	if m == nil {
		return ReloadState{}
	}
	m.mu.RLock()
	cfg := snapshotConfig(m.lastAppliedCfg)
	if cfg == nil {
		cfg = snapshotConfig(m.cfg)
	}
	idle := m.lastAppliedIdle
	m.mu.RUnlock()
	return ReloadState{Config: cfg, Idle: idle}
}
