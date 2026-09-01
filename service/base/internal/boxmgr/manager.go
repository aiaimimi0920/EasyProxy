package boxmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"easy_proxies/internal/builder"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/store"

	"github.com/sagernet/sing-box/adapter"
)

// Ensure Manager implements monitor.NodeManager.
var _ monitor.NodeManager = (*Manager)(nil)

const (
	defaultDrainTimeout       = 10 * time.Second
	defaultHealthCheckTimeout = 2 * time.Minute
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
	if cfg.DispatchEnabled() && len(cfg.Nodes) == 0 && !tunDirectFallback(cfg) {
		return m.startIdle(cfg, "no proxy nodes")
	}

	// Try to start, with automatic port conflict resolution
	var instance managedBox
	maxRetries := 10
	for retry := 0; retry < maxRetries; retry++ {
		var err error
		instance, err = m.createManagedBox(ctx, cfg)
		if err != nil {
			if cfg.DispatchEnabled() && errors.Is(err, builder.ErrNoValidNodes) && !tunDirectFallback(cfg) {
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
	if cfg.SubscriptionRefresh.MinAvailableNodes > 0 && len(cfg.Nodes) > 0 {
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

func tunDirectFallback(cfg *config.Config) bool {
	return cfg != nil && cfg.Gateway.Enabled && strings.EqualFold(cfg.Gateway.Mode, "tun") &&
		strings.EqualFold(cfg.Gateway.Routing.NoAvailableProxyPolicy, "DIRECT")
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
