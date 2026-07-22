package boxmgr

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"easy_proxies/internal/builder"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/store"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
)

// Ensure Manager implements monitor.NodeManager.
var _ monitor.NodeManager = (*Manager)(nil)

const (
	defaultDrainTimeout       = 10 * time.Second
	defaultHealthCheckTimeout = 30 * time.Second
	healthCheckPollInterval   = 500 * time.Millisecond
	// periodicHealthInterval is configured via cfg.Management.HealthCheckInterval
	periodicHealthTimeout = 10 * time.Second
)

// Logger defines logging interface for the manager.
type Logger interface {
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// Option configures the Manager.
type Option func(*Manager)

// WithLogger sets a custom logger.
func WithLogger(l Logger) Option {
	return func(m *Manager) { m.logger = l }
}

// WithStore sets the data store.
func WithStore(s store.Store) Option {
	return func(m *Manager) { m.store = s }
}

// ConfigUpdateListener is notified when the active config changes (e.g., after reload).
type ConfigUpdateListener interface {
	OnConfigUpdate(cfg *config.Config)
}

// ReloadState is an immutable snapshot of one side of a reload transaction.
type ReloadState struct {
	Config *config.Config
	Idle   bool
}

// ReloadLifecycleListener coordinates components that must change topology
// around a box reload. CompleteReload runs after the candidate box is live.
type ReloadLifecycleListener interface {
	PrepareReload(ctx context.Context, from, to ReloadState) error
	CompleteReload(ctx context.Context, from, to ReloadState) error
	FailedReload(ctx context.Context, from, to ReloadState, cause error, restored bool) error
}

// ReloadIntentListener is notified before a reload caller starts capturing its
// target configuration. It lets hot-update components reject edits that would
// otherwise race a stale target assembled from disk or remote subscriptions.
type ReloadIntentListener interface {
	BeginReloadIntent(ctx context.Context) error
	EndReloadIntent(ctx context.Context)
}

// ReloadIntent holds one nestable reload-intent notification. End must be
// called exactly once; it is safe to defer immediately after construction.
type ReloadIntent struct {
	once             sync.Once
	ctx              context.Context
	listeners        []ReloadIntentListener
	endMutationGuard func()
	endWindow        func()
}

// End releases this reload intent and re-enables hot updates when no other
// intent remains active in the listener.
func (i *ReloadIntent) End() {
	if i == nil {
		return
	}
	i.once.Do(func() {
		for idx := len(i.listeners) - 1; idx >= 0; idx-- {
			i.listeners[idx].EndReloadIntent(i.ctx)
		}
		if i.endMutationGuard != nil {
			i.endMutationGuard()
		}
		if i.endWindow != nil {
			i.endWindow()
		}
	})
}

type managedBox interface {
	Start() error
	Close() error
	Outbound() adapter.OutboundManager
}

type boxFactory func(ctx context.Context, cfg *config.Config) (managedBox, error)

// Manager owns the lifecycle of the active sing-box instance.
type Manager struct {
	mu       sync.RWMutex
	reloadMu sync.Mutex

	reloadIntentMu    sync.Mutex
	reloadIntentCond  *sync.Cond
	reloadIntentCount int

	currentBox    managedBox
	monitorMgr    *monitor.Manager
	monitorServer *monitor.Server
	cfg           *config.Config
	monitorCfg    monitor.Config
	store         store.Store

	drainTimeout      time.Duration
	minAvailableNodes int
	logger            Logger

	baseCtx            context.Context
	healthCheckStarted bool
	configListeners    []ConfigUpdateListener
	reloadListeners    []ReloadLifecycleListener
	idle               bool // true when manager was started but stopped due to 0 enabled nodes
	ephemeralNodes     []config.NodeConfig
	boxFactory         boxFactory
	portReleaseDelay   time.Duration

	lastAppliedCfg  *config.Config
	lastAppliedIdle bool

	// lastAppliedMode and lastAppliedBasePort track the mode/BasePort from the
	// last successful Start/Reload. Used by TriggerReload to detect changes,
	// since m.cfg may have been mutated by updateAllSettings before reload.
	lastAppliedMode     string
	lastAppliedBasePort uint16
}

// New creates a BoxManager with the given config.
func New(cfg *config.Config, monitorCfg monitor.Config, opts ...Option) *Manager {
	m := &Manager{
		cfg:              cfg,
		monitorCfg:       monitorCfg,
		portReleaseDelay: 500 * time.Millisecond,
	}
	m.applyConfigSettings(cfg)
	for _, opt := range opts {
		opt(m)
	}
	if m.logger == nil {
		m.logger = defaultLogger{}
	}
	if m.drainTimeout <= 0 {
		m.drainTimeout = defaultDrainTimeout
	}
	return m
}

// Start creates and starts the initial sing-box instance.
func (m *Manager) Start(ctx context.Context) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	if m.cfg == nil {
		m.mu.Unlock()
		return errors.New("box manager requires config")
	}
	if m.currentBox != nil || m.idle {
		m.mu.Unlock()
		return errors.New("sing-box already running")
	}
	m.baseCtx = ctx
	sharedCfg := m.cfg
	m.mu.Unlock()

	cfg := snapshotConfig(sharedCfg)
	if cfg == nil {
		return errors.New("box manager requires config")
	}
	m.mu.Lock()
	m.applyConfigSettings(cfg)
	m.mu.Unlock()
	if err := m.ensureMonitor(ctx); err != nil {
		return err
	}

	// Keep the manager in an explicit idle state when the dispatcher owns the
	// entry but the proxy source is empty. This lets DIRECT traffic continue
	// while a later reload activates the pool without a process restart.
	if cfg.DispatchEnabled() && len(cfg.Nodes) == 0 {
		return m.startIdle(cfg, "no proxy nodes")
	}

	// Try to start, with automatic port conflict resolution
	var instance managedBox
	maxRetries := 10
	for retry := 0; retry < maxRetries; retry++ {
		var err error
		instance, err = m.createManagedBox(ctx, cfg)
		if err != nil {
			if cfg.DispatchEnabled() && errors.Is(err, builder.ErrNoValidNodes) {
				return m.startIdle(cfg, "no valid proxy nodes")
			}
			return err
		}
		if err := m.restoreMonitorStatsFromStore(ctx); err != nil {
			m.logger.Warnf("failed to restore monitor stats from store: %v", err)
		}
		if err = instance.Start(); err != nil {
			_ = instance.Close()
			// Check if it's a port conflict error
			if conflictPort := extractPortFromBindError(err); conflictPort > 0 {
				m.logger.Warnf("port %d is in use, reassigning and retrying...", conflictPort)
				if reassigned := reassignConflictingPort(cfg, conflictPort); reassigned {
					pool.ResetSharedStateStore() // Reset shared state for rebuild
					continue
				}
			}
			return fmt.Errorf("start sing-box: %w", err)
		}
		break // Success
	}

	m.mu.Lock()
	m.currentBox = instance
	m.cfg = cfg
	m.idle = false
	m.lastAppliedCfg = snapshotConfig(cfg)
	m.lastAppliedIdle = false
	m.lastAppliedMode = cfg.Mode
	m.lastAppliedBasePort = cfg.MultiPort.BasePort
	monitorServer := m.monitorServer
	m.mu.Unlock()
	if monitorServer != nil {
		monitorServer.SetConfig(cfg)
	}

	// Start periodic health check after nodes are registered.
	m.startPeriodicHealthCheck(cfg)

	// Wait for initial health check if min nodes configured
	if cfg.SubscriptionRefresh.MinAvailableNodes > 0 {
		timeout := cfg.SubscriptionRefresh.HealthCheckTimeout
		if timeout <= 0 {
			timeout = defaultHealthCheckTimeout
		}
		if err := m.waitForHealthCheck(timeout); err != nil {
			m.logger.Warnf("initial health check warning: %v", err)
			// Don't fail startup, just warn
		}
	}

	m.logger.Infof("sing-box instance started with %d nodes", len(cfg.Nodes))
	return nil
}

func (m *Manager) startIdle(cfg *config.Config, reason string) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	m.mu.Lock()
	m.currentBox = nil
	m.cfg = cfg
	m.idle = true
	m.lastAppliedCfg = snapshotConfig(cfg)
	m.lastAppliedIdle = true
	m.lastAppliedMode = cfg.Mode
	m.lastAppliedBasePort = cfg.MultiPort.BasePort
	monitorServer := m.monitorServer
	m.mu.Unlock()
	if monitorServer != nil {
		monitorServer.SetConfig(cfg)
	}
	m.startPeriodicHealthCheck(cfg)
	m.logger.Infof("dispatcher started in idle mode (%s)", reason)
	return nil
}

// Reload gracefully switches to a new configuration.
// For multi-port mode, we must stop the old instance first to release ports.
// Supports transitioning from idle state (0 nodes → has nodes).
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
	if targetCfg.DispatchEnabled() && len(targetCfg.Nodes) > 0 && !builder.HasValidNode(targetCfg) {
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
	m.restoreAppliedState(ctx, oldCfg, false, nil)
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
func (m *Manager) PrepareMonitor(ctx context.Context) error {
	return m.ensureMonitor(ctx)
}

// createBox builds a sing-box instance from config.
func (m *Manager) createBox(ctx context.Context, cfg *config.Config) (*box.Box, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}
	if m.monitorMgr == nil {
		return nil, errors.New("monitor manager not initialized")
	}

	opts, err := builder.Build(cfg)
	if err != nil {
		return nil, fmt.Errorf("build sing-box options: %w", err)
	}

	inboundRegistry := include.InboundRegistry()
	outboundRegistry := include.OutboundRegistry()
	pool.Register(outboundRegistry)
	endpointRegistry := include.EndpointRegistry()
	dnsRegistry := include.DNSTransportRegistry()
	serviceRegistry := include.ServiceRegistry()

	boxCtx := box.Context(ctx, inboundRegistry, outboundRegistry, endpointRegistry, dnsRegistry, serviceRegistry)
	boxCtx = monitor.ContextWith(boxCtx, m.monitorMgr)

	instance, err := box.New(box.Options{Context: boxCtx, Options: opts})
	if err != nil {
		return nil, fmt.Errorf("create sing-box instance: %w", err)
	}
	return instance, nil
}

func (m *Manager) createManagedBox(ctx context.Context, cfg *config.Config) (managedBox, error) {
	if m.boxFactory != nil {
		return m.boxFactory(ctx, cfg)
	}
	return m.createBox(ctx, cfg)
}

// gracefulSwitch swaps the current box with a new one.
func (m *Manager) gracefulSwitch(newBox managedBox) error {
	if newBox == nil {
		return errors.New("new box is nil")
	}

	m.mu.Lock()
	old := m.currentBox
	m.currentBox = newBox
	drainTimeout := m.drainTimeout
	m.mu.Unlock()

	if old != nil {
		go m.drainOldBox(old, drainTimeout)
	}

	m.logger.Infof("switched to new instance, draining old for %s", drainTimeout)
	return nil
}

// drainOldBox waits for drain timeout then closes the old box.
func (m *Manager) drainOldBox(oldBox managedBox, timeout time.Duration) {
	if oldBox == nil {
		return
	}
	if timeout > 0 {
		time.Sleep(timeout)
	}
	if err := oldBox.Close(); err != nil {
		m.logger.Errorf("failed to close old instance: %v", err)
		return
	}
	m.logger.Infof("old instance closed after %s drain", timeout)
}

// waitForHealthCheck polls until enough nodes are available or timeout.
func (m *Manager) waitForHealthCheck(timeout time.Duration) error {
	return m.waitForHealthCheckAtLeast(timeout, m.minAvailableNodes)
}

func (m *Manager) waitForHealthCheckAtLeast(timeout time.Duration, minAvailableNodes int) error {
	if m.monitorMgr == nil || minAvailableNodes <= 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = defaultHealthCheckTimeout
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(healthCheckPollInterval)
	defer ticker.Stop()

	for {
		available, total := m.availableNodeCount()
		if available >= minAvailableNodes {
			m.logger.Infof("health check passed: %d/%d nodes available", available, total)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout: %d/%d nodes available (need >= %d)", available, total, minAvailableNodes)
		}
		<-ticker.C
	}
}

// availableNodeCount returns (available, total) node counts.
func (m *Manager) availableNodeCount() (int, int) {
	if m.monitorMgr == nil {
		return 0, 0
	}
	snapshots := m.monitorMgr.Snapshot()
	total := len(snapshots)
	available := 0
	for _, snap := range snapshots {
		if snap.EffectiveAvailable {
			available++
		}
	}
	return available, total
}

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

var errConfigUnavailable = errors.New("config is not initialized")

// ListConfigNodes returns a copy of all configured nodes.
// If a Store is available, it merges the disabled status from the store
// and also includes disabled nodes that are not in the active config.
// Port numbers are taken from the active config (m.cfg.Nodes) since they
// are dynamically assigned by NormalizeWithPortMap and may not be in the Store.
func (m *Manager) ListConfigNodes(ctx context.Context) ([]config.NodeConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cfg == nil {
		return nil, errConfigUnavailable
	}
	m.cfg.RLock()
	defer m.cfg.RUnlock()

	// If no store, just return active nodes
	if m.store == nil {
		return filterPersistentConfigNodes(m.cfg.Nodes), nil
	}

	// Build a lookup from URI → runtime port from the active config.
	// These ports are dynamically assigned by NormalizeWithPortMap and
	// reflect the actual listening ports in the current sing-box instance.
	runtimePorts := make(map[string]uint16, len(m.cfg.Nodes))
	for _, n := range m.cfg.Nodes {
		if n.Port > 0 {
			runtimePorts[n.URI] = n.Port
		}
	}

	// Fetch all nodes from store (including disabled ones)
	storeNodes, err := m.store.ListNodes(ctx, store.NodeFilter{})
	if err != nil {
		// Fallback to config nodes if store fails
		m.logger.Warnf("failed to list nodes from store: %v, falling back to config", err)
		return cloneNodes(m.cfg.Nodes), nil
	}

	// Build result from store nodes (preserves disabled status)
	// Merge runtime port assignments from active config
	result := make([]config.NodeConfig, 0, len(storeNodes))
	for _, n := range storeNodes {
		if !store.IsPersistentNodeSource(n.Source) {
			continue
		}
		port := n.Port
		// Prefer runtime port from active config (dynamically assigned)
		if runtimePort, ok := runtimePorts[n.URI]; ok && runtimePort > 0 {
			port = runtimePort
		}
		result = append(result, config.NodeConfig{
			Name:     n.Name,
			URI:      n.URI,
			Port:     port,
			Username: n.Username,
			Password: n.Password,
			Source:   config.NodeSource(n.Source),
			Disabled: !n.Enabled,
		})
	}

	return result, nil
}

// CreateNode adds a new node and persists it to the Store.
// Nodes added via the WebUI are always marked as "manual" source.
func (m *Manager) CreateNode(ctx context.Context, node config.NodeConfig) (config.NodeConfig, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return config.NodeConfig{}, err
		}
	}
	if err := m.lockConfigMutation(ctx); err != nil {
		return config.NodeConfig{}, err
	}
	defer m.unlockConfigMutation()

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return config.NodeConfig{}, errConfigUnavailable
	}
	m.cfg.Lock()
	defer m.cfg.Unlock()

	normalized, err := m.prepareNodeLocked(node, "")
	if err != nil {
		return config.NodeConfig{}, err
	}

	normalized.Source = config.NodeSourceManual

	// Persist to Store if available
	if m.store != nil {
		storeNode := &store.Node{
			URI:      normalized.URI,
			Name:     normalized.Name,
			Source:   string(normalized.Source),
			Port:     normalized.Port,
			Username: normalized.Username,
			Password: normalized.Password,
			Enabled:  true,
		}
		if err := m.store.CreateNode(ctx, storeNode); err != nil {
			return config.NodeConfig{}, fmt.Errorf("save to store: %w", err)
		}
	}

	m.cfg.Nodes = append(m.cfg.Nodes, normalized)
	return normalized, nil
}

// UpdateNode updates an existing node by name and persists to the Store.
func (m *Manager) UpdateNode(ctx context.Context, ref string, node config.NodeConfig) (config.NodeConfig, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return config.NodeConfig{}, err
		}
	}
	if err := m.lockConfigMutation(ctx); err != nil {
		return config.NodeConfig{}, err
	}
	defer m.unlockConfigMutation()

	ref = strings.TrimSpace(ref)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return config.NodeConfig{}, errConfigUnavailable
	}
	m.cfg.Lock()
	defer m.cfg.Unlock()

	idx := m.nodeIndexByRefLocked(ref)
	var existingStore *store.Node
	var err error
	if m.store != nil {
		existingStore, err = m.lookupStoreNodeLocked(ctx, ref, idx)
		if err != nil {
			return config.NodeConfig{}, fmt.Errorf("lookup in store: %w", err)
		}
	}
	if idx == -1 && existingStore == nil {
		return config.NodeConfig{}, monitor.ErrNodeNotFound
	}

	currentName := ""
	if idx >= 0 {
		currentName = m.cfg.Nodes[idx].Name
	}
	normalized, err := m.prepareNodeLocked(node, currentName)
	if err != nil {
		return config.NodeConfig{}, err
	}

	// Preserve the original source
	if idx >= 0 {
		normalized.Source = m.cfg.Nodes[idx].Source
	}

	// Persist to Store if available
	if existingStore != nil {
		existingStore.URI = normalized.URI
		existingStore.Name = normalized.Name
		existingStore.Port = normalized.Port
		existingStore.Username = normalized.Username
		existingStore.Password = normalized.Password
		if err := m.store.UpdateNode(ctx, existingStore); err != nil {
			return config.NodeConfig{}, fmt.Errorf("update in store: %w", err)
		}
	}

	if idx >= 0 {
		m.cfg.Nodes[idx] = normalized
	} else if existingStore != nil && existingStore.Enabled {
		m.cfg.Nodes = append(m.cfg.Nodes, normalized)
	}
	return normalized, nil
}

// SetNodeEnabled enables or disables a node by name.
// This only updates the store; a reload is needed for changes to take effect.
func (m *Manager) SetNodeEnabled(ctx context.Context, ref string, enabled bool) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := m.lockConfigMutation(ctx); err != nil {
		return err
	}
	defer m.unlockConfigMutation()

	ref = strings.TrimSpace(ref)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return errConfigUnavailable
	}
	m.cfg.Lock()
	defer m.cfg.Unlock()

	// Update in Store
	idx := m.nodeIndexByRefLocked(ref)
	if m.store != nil {
		existing, err := m.lookupStoreNodeLocked(ctx, ref, idx)
		if err != nil {
			return fmt.Errorf("lookup in store: %w", err)
		}
		if existing == nil && idx == -1 {
			return monitor.ErrNodeNotFound
		}
		if existing != nil {
			existing.Enabled = enabled
			if err := m.store.UpdateNode(ctx, existing); err != nil {
				return fmt.Errorf("update in store: %w", err)
			}
		}
	} else if idx == -1 {
		// No store — just check the node exists in config
		return monitor.ErrNodeNotFound
	}

	// If disabling, remove from active config nodes
	if !enabled {
		if idx != -1 {
			m.cfg.Nodes = append(m.cfg.Nodes[:idx], m.cfg.Nodes[idx+1:]...)
		}
	}

	return nil
}

// DeleteNode removes a node by name and deletes it from the Store.
func (m *Manager) DeleteNode(ctx context.Context, ref string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := m.lockConfigMutation(ctx); err != nil {
		return err
	}
	defer m.unlockConfigMutation()

	ref = strings.TrimSpace(ref)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return errConfigUnavailable
	}
	m.cfg.Lock()
	defer m.cfg.Unlock()

	idx := m.nodeIndexByRefLocked(ref)

	// Delete from Store if available
	if m.store != nil {
		existing, err := m.lookupStoreNodeLocked(ctx, ref, idx)
		if err != nil {
			return fmt.Errorf("lookup in store: %w", err)
		}
		if existing == nil && idx == -1 {
			return monitor.ErrNodeNotFound
		}
		if existing != nil {
			if err := m.store.DeleteNode(ctx, existing.ID); err != nil {
				return fmt.Errorf("delete from store: %w", err)
			}
		}
	} else if idx == -1 {
		return monitor.ErrNodeNotFound
	}

	if idx != -1 {
		m.cfg.Nodes = append(m.cfg.Nodes[:idx], m.cfg.Nodes[idx+1:]...)
	}
	return nil
}

func (m *Manager) lookupStoreNodeLocked(ctx context.Context, ref string, activeIdx int) (*store.Node, error) {
	if m.store == nil {
		return nil, nil
	}

	if activeIdx >= 0 && activeIdx < len(m.cfg.Nodes) {
		activeURI := strings.TrimSpace(m.cfg.Nodes[activeIdx].URI)
		if activeURI != "" {
			node, err := m.store.GetNodeByURI(ctx, activeURI)
			if err != nil {
				return nil, err
			}
			if node != nil {
				return node, nil
			}
		}
	}

	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	if node, err := m.store.GetNodeByURI(ctx, ref); err != nil {
		return nil, err
	} else if node != nil {
		return node, nil
	}
	return m.store.GetNodeByName(ctx, ref)
}

// TriggerReload reloads the sing-box instance by re-reading config from disk
// and loading nodes from the SQLite Store.
func (m *Manager) TriggerReload(ctx context.Context) error {
	return m.triggerReload(ctx, nil, false)
}

// TriggerReloadWithEphemeralNodes reloads from the persistent configuration
// using an explicit runtime-node candidate. The candidate is published only
// if the reload or idle transition commits successfully.
func (m *Manager) TriggerReloadWithEphemeralNodes(ctx context.Context, ephemeralNodes []config.NodeConfig) error {
	return m.triggerReload(ctx, ephemeralNodes, true)
}

func (m *Manager) triggerReload(
	ctx context.Context,
	candidateEphemeralNodes []config.NodeConfig,
	publishEphemeral bool,
) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	intentCtx := ctx
	if intentCtx == nil {
		intentCtx = context.Background()
	}
	intent, err := m.BeginReloadIntent(intentCtx)
	if err != nil {
		return err
	}
	defer intent.End()
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	m.mu.RLock()
	portMap := m.cfg.BuildPortMap() // Preserve existing port assignments
	oldMode := m.lastAppliedMode
	oldBasePort := m.lastAppliedBasePort
	cfgPath := ""
	if m.cfg != nil {
		cfgPath = m.cfg.FilePath()
	}
	m.mu.RUnlock()

	// Re-read config from disk using LoadForReload (only gets inline nodes + settings)
	var newCfg *config.Config
	if cfgPath != "" {
		var loadErr error
		newCfg, loadErr = config.LoadForReload(cfgPath)
		if loadErr != nil {
			m.logger.Warnf("failed to reload config from disk: %v, falling back to in-memory copy", loadErr)
			m.mu.RLock()
			newCfg = m.copyConfigLocked()
			m.mu.RUnlock()
		} else {
			m.logger.Infof("reloaded config from disk: %s", cfgPath)
		}
	} else {
		m.mu.RLock()
		newCfg = m.copyConfigLocked()
		m.mu.RUnlock()
	}

	if newCfg == nil {
		return errConfigUnavailable
	}

	// Merge inline nodes (from config.yaml) with persistent local store nodes.
	// Inline nodes take priority; store nodes are added if their URI is not already present.
	if m.store != nil {
		storeNodes, err := m.store.ListNodes(ctx, store.NodeFilter{})
		if err != nil {
			m.logger.Warnf("failed to list nodes from store during reload: %v", err)
		} else if len(storeNodes) > 0 {
			// Build set of URIs already present from inline nodes
			inlineURIs := make(map[string]bool, len(newCfg.Nodes))
			for _, n := range newCfg.Nodes {
				inlineURIs[n.URI] = true
			}

			// Merge store nodes, skipping duplicates and disabled nodes
			for _, n := range storeNodes {
				if !store.IsPersistentNodeSource(n.Source) {
					continue
				}
				if !n.Enabled {
					continue
				}
				if inlineURIs[n.URI] {
					continue // inline node takes priority
				}
				newCfg.Nodes = append(newCfg.Nodes, config.NodeConfig{
					Name:     n.Name,
					URI:      n.URI,
					Port:     n.Port,
					Username: n.Username,
					Password: n.Password,
					Source:   config.NodeSource(n.Source),
				})
			}
			m.logger.Infof("merged nodes for reload: %d inline + store nodes = %d total", len(inlineURIs), len(newCfg.Nodes))
		}
	}

	ephemeralNodes := cloneNodes(candidateEphemeralNodes)
	if !publishEphemeral {
		m.mu.RLock()
		ephemeralNodes = cloneNodes(m.ephemeralNodes)
		m.mu.RUnlock()
	}
	if len(ephemeralNodes) > 0 && hasRuntimeSourceRefs(newCfg) {
		existing := make(map[string]struct{}, len(newCfg.Nodes))
		for _, node := range newCfg.Nodes {
			existing[node.URI] = struct{}{}
		}
		for _, node := range ephemeralNodes {
			if _, ok := existing[node.URI]; ok {
				continue
			}
			newCfg.Nodes = append(newCfg.Nodes, node)
		}
	}

	// If no enabled nodes available after merging, enter idle state:
	// stop the running box gracefully so disabled nodes are no longer served.
	if len(newCfg.Nodes) == 0 {
		return m.enterIdleLockedWithEphemeralNodes(newCfg, ephemeralNodes, publishEphemeral)
	}

	// Detect mode or base port changes — if either changed, discard old port
	// assignments so all nodes get fresh ports from the new BasePort.
	modeChanged := newCfg.Mode != oldMode
	basePortChanged := newCfg.MultiPort.BasePort != oldBasePort
	if modeChanged || basePortChanged {
		m.logger.Infof("mode/base-port changed (mode: %s→%s, base: %d→%d), reassigning all ports",
			oldMode, newCfg.Mode, oldBasePort, newCfg.MultiPort.BasePort)
		portMap = nil // Discard old port map
		for idx := range newCfg.Nodes {
			newCfg.Nodes[idx].Port = 0 // Clear all ports for reassignment
		}
	}

	return m.reloadWithPortMapAndEphemeralNodesLocked(newCfg, portMap, ephemeralNodes, publishEphemeral)
}

// ReloadWithPortMap gracefully switches to a new configuration, preserving port assignments.
func (m *Manager) ReloadWithPortMap(newCfg *config.Config, portMap map[string]uint16) error {
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
	return m.reloadWithPortMapLocked(newCfg, portMap)
}

// ReloadWithPortMapAndEphemeralNodes reloads one runtime-generated candidate
// and publishes its ephemeral nodes only after the reload commits. A rejected
// candidate leaves the previously published ephemeral set unchanged.
func (m *Manager) ReloadWithPortMapAndEphemeralNodes(
	newCfg *config.Config,
	portMap map[string]uint16,
	ephemeralNodes []config.NodeConfig,
) error {
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
	if err := m.reloadWithPortMapAndEphemeralNodesLocked(newCfg, portMap, ephemeralNodes, true); err != nil {
		return err
	}
	return nil
}

func (m *Manager) reloadWithPortMapLocked(newCfg *config.Config, portMap map[string]uint16) error {
	return m.reloadWithPortMapAndEphemeralNodesLocked(newCfg, portMap, nil, false)
}

func (m *Manager) reloadWithPortMapAndEphemeralNodesLocked(
	newCfg *config.Config,
	portMap map[string]uint16,
	ephemeralNodes []config.NodeConfig,
	publishEphemeral bool,
) error {
	if newCfg == nil {
		return errors.New("new config is nil")
	}

	// Always normalize config (apply defaults, assign ports, etc.).
	// If portMap is provided, existing nodes keep their ports; otherwise all ports are reassigned.
	if portMap == nil {
		portMap = make(map[string]uint16)
	}
	if err := newCfg.NormalizeWithPortMap(portMap); err != nil {
		return fmt.Errorf("normalize config with port map: %w", err)
	}

	return m.reloadLockedWithEphemeralNodes(newCfg, ephemeralNodes, publishEphemeral)
}

// enterIdle stops the running sing-box instance when there are 0 enabled nodes.
// The manager enters an idle state and can be resumed by TriggerReload when
// nodes are re-enabled.
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
func (m *Manager) CurrentPortMap() map[string]uint16 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg == nil {
		return nil
	}
	return m.cfg.BuildPortMap()
}

// --- Helper functions ---

// portBindErrorRegex matches "listen tcp4 0.0.0.0:24282: bind: address already in use"
var portBindErrorRegex = regexp.MustCompile(`listen tcp[46]? [^:]+:(\d+): bind: address already in use`)

// extractPortFromBindError extracts the port number from a bind error message.
func extractPortFromBindError(err error) uint16 {
	if err == nil {
		return 0
	}
	matches := portBindErrorRegex.FindStringSubmatch(err.Error())
	if len(matches) < 2 {
		return 0
	}
	var port int
	fmt.Sscanf(matches[1], "%d", &port)
	if port > 0 && port <= 65535 {
		return uint16(port)
	}
	return 0
}

// isPortAvailable checks if a port is available for binding.
func isPortAvailable(address string, port uint16) bool {
	addr := fmt.Sprintf("%s:%d", address, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// reassignConflictingPort finds the node using the conflicting port and assigns a new port.
func reassignConflictingPort(cfg *config.Config, conflictPort uint16) bool {
	// Build set of used ports
	usedPorts := make(map[uint16]bool)
	if cfg.Mode == "hybrid" {
		usedPorts[cfg.Listener.Port] = true
	}
	for _, node := range cfg.Nodes {
		usedPorts[node.Port] = true
	}

	// Find and reassign the conflicting node
	for idx := range cfg.Nodes {
		if cfg.Nodes[idx].Port == conflictPort {
			// Find next available port
			newPort := conflictPort + 1
			address := cfg.MultiPort.Address
			if address == "" {
				address = "0.0.0.0"
			}
			for usedPorts[newPort] || !isPortAvailable(address, newPort) {
				newPort++
				if newPort > 65535 {
					log.Printf("❌ No available port found for node %q", cfg.Nodes[idx].Name)
					return false
				}
			}
			log.Printf("⚠️  Port %d in use, reassigning node %q to port %d", conflictPort, cfg.Nodes[idx].Name, newPort)
			cfg.Nodes[idx].Port = newPort
			return true
		}
	}
	return false
}

func cloneNodes(nodes []config.NodeConfig) []config.NodeConfig {
	if len(nodes) == 0 {
		return []config.NodeConfig{} // Return empty slice, not nil, for proper JSON serialization
	}
	out := make([]config.NodeConfig, len(nodes))
	copy(out, nodes)
	return out
}

func snapshotConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	cfg.RLock()
	defer cfg.RUnlock()
	return cfg.Clone()
}

func cloneReloadState(state ReloadState) ReloadState {
	return ReloadState{
		Config: snapshotConfig(state.Config),
		Idle:   state.Idle,
	}
}

func filterPersistentConfigNodes(nodes []config.NodeConfig) []config.NodeConfig {
	filtered := make([]config.NodeConfig, 0, len(nodes))
	for _, node := range nodes {
		if !store.IsPersistentNodeSource(string(node.Source)) {
			continue
		}
		filtered = append(filtered, node)
	}
	return filtered
}

// SetEphemeralNodes stores runtime-generated nodes that should survive reloads
// but must not be written into the persistent local store.
func (m *Manager) SetEphemeralNodes(nodes []config.NodeConfig) {
	if err := m.lockConfigMutation(context.Background()); err != nil {
		return
	}
	defer m.unlockConfigMutation()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ephemeralNodes = cloneNodes(nodes)
}

func (m *Manager) copyConfigLocked() *config.Config {
	return snapshotConfig(m.cfg)
}

func (m *Manager) nodeIndexLocked(name string) int {
	for idx, node := range m.cfg.Nodes {
		if node.Name == name {
			return idx
		}
	}
	return -1
}

func (m *Manager) portInUseLocked(port uint16, currentName string) bool {
	if port == 0 {
		return false
	}
	for _, node := range m.cfg.Nodes {
		if node.Name == currentName {
			continue
		}
		if node.Port == port {
			return true
		}
	}
	return false
}

func (m *Manager) nextAvailablePortLocked() uint16 {
	base := m.cfg.MultiPort.BasePort
	if base == 0 {
		base = 25000
	}
	used := make(map[uint16]struct{}, len(m.cfg.Nodes))
	for _, node := range m.cfg.Nodes {
		if node.Port > 0 {
			used[node.Port] = struct{}{}
		}
	}
	port := base
	for i := 0; i < 1<<16; i++ {
		if _, ok := used[port]; !ok && port != 0 {
			return port
		}
		port++
		if port == 0 {
			port = 1
		}
	}
	return base
}

func (m *Manager) prepareNodeLocked(node config.NodeConfig, currentName string) (config.NodeConfig, error) {
	node.Name = strings.TrimSpace(node.Name)
	defaultScheme := "http"
	if m.cfg != nil && strings.TrimSpace(m.cfg.SourceSync.DefaultDirectProxyScheme) != "" {
		defaultScheme = strings.TrimSpace(m.cfg.SourceSync.DefaultDirectProxyScheme)
	}
	node.URI = config.NormalizeProxyURIInput(strings.TrimSpace(node.URI), defaultScheme)

	if node.URI == "" {
		return config.NodeConfig{}, fmt.Errorf("%w: URI 不能为空", monitor.ErrInvalidNode)
	}
	if !config.IsProxyURI(node.URI) {
		return config.NodeConfig{}, fmt.Errorf("%w: 不支持的 URI 协议", monitor.ErrInvalidNode)
	}

	// Extract name from URI fragment (#name) if not provided
	if node.Name == "" {
		if currentName != "" {
			node.Name = currentName
		} else if idx := strings.LastIndex(node.URI, "#"); idx != -1 && idx < len(node.URI)-1 {
			// Extract and URL-decode the fragment
			fragment := node.URI[idx+1:]
			if decoded, err := url.QueryUnescape(fragment); err == nil && decoded != "" {
				node.Name = decoded
			}
		}
		// Fallback to auto-generated name
		if node.Name == "" {
			node.Name = fmt.Sprintf("node-%d", len(m.cfg.Nodes)+1)
		}
	}

	// Check for name conflict (excluding current node when updating)
	if idx := m.nodeIndexLocked(node.Name); idx != -1 {
		if currentName == "" || m.cfg.Nodes[idx].Name != currentName {
			return config.NodeConfig{}, fmt.Errorf("%w: 节点 %s 已存在", monitor.ErrNodeConflict, node.Name)
		}
	}

	// Handle multi-port-capable mode specifics.
	if m.cfg.Mode == "multi-port" || m.cfg.Mode == "hybrid" {
		if node.Port == 0 {
			node.Port = m.nextAvailablePortLocked()
		} else if m.portInUseLocked(node.Port, currentName) {
			return config.NodeConfig{}, fmt.Errorf("%w: 端口 %d 已被占用", monitor.ErrNodeConflict, node.Port)
		}
		if node.Username == "" {
			node.Username = m.cfg.MultiPort.Username
			node.Password = m.cfg.MultiPort.Password
		}
	}

	return node, nil
}
