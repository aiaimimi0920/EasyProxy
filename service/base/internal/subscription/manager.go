package subscription

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/store"
)

// Logger defines logging interface.
type Logger interface {
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// ConnectorRuntime manages local execution of manifest connector sources.
type ConnectorRuntime interface {
	Reconcile(cfg *config.Config, sources []RuntimeSource) ([]RuntimeSource, error)
	StopAll() error
}

// Option configures the Manager.
type Option func(*Manager)

const (
	manualRefreshCompletionGrace = 30 * time.Second
	manualRefreshDefaultFetch    = 30 * time.Second
	manualRefreshDefaultHealth   = 2 * time.Minute
	manualRefreshDefaultDrain    = 30 * time.Second
	maximumDuration              = time.Duration(1<<63 - 1)
)

// refreshReloadIntent is the portion of boxmgr.ReloadIntent needed by the
// refresh transaction. Keeping this small makes the fetch/retry boundary
// independently testable without starting a sing-box instance.
type refreshReloadIntent interface {
	End()
}

type refreshReloader interface {
	BeginReloadIntent(context.Context) (refreshReloadIntent, error)
	CurrentPortMap() map[string]uint16
	CurrentEphemeralNodes() []config.NodeConfig
	ReloadWithPortMapAndEphemeralNodes(*config.Config, map[string]uint16, []config.NodeConfig) error
}

type boxManagerRefreshReloader struct {
	manager *boxmgr.Manager
}

func (r boxManagerRefreshReloader) BeginReloadIntent(ctx context.Context) (refreshReloadIntent, error) {
	return r.manager.BeginReloadIntent(ctx)
}

func (r boxManagerRefreshReloader) CurrentPortMap() map[string]uint16 {
	return r.manager.CurrentPortMap()
}

func (r boxManagerRefreshReloader) CurrentEphemeralNodes() []config.NodeConfig {
	return r.manager.CurrentEphemeralNodes()
}

func (r boxManagerRefreshReloader) ReloadWithPortMapAndEphemeralNodes(
	newCfg *config.Config,
	portMap map[string]uint16,
	ephemeralNodes []config.NodeConfig,
) error {
	return r.manager.ReloadWithPortMapAndEphemeralNodes(newCfg, portMap, ephemeralNodes)
}

// WithLogger sets a custom logger.
func WithLogger(l Logger) Option {
	return func(m *Manager) { m.logger = l }
}

// WithStore sets the data store.
func WithStore(s store.Store) Option {
	return func(m *Manager) { m.store = s }
}

// WithConnectorRuntime overrides the default connector runtime manager.
func WithConnectorRuntime(rt ConnectorRuntime) Option {
	return func(m *Manager) { m.connectorRuntime = rt }
}

func withPreferredIPSelector(selector preferredIPRuntimeSelector) Option {
	return func(m *Manager) { m.preferredIPSelector = selector }
}

func withRefreshReloader(reloader refreshReloader) Option {
	return func(m *Manager) { m.refreshReloader = reloader }
}

// Ensure Manager implements boxmgr.ConfigUpdateListener.
var _ boxmgr.ConfigUpdateListener = (*Manager)(nil)

// Manager handles periodic subscription refresh.
type Manager struct {
	mu sync.RWMutex

	baseCfg             *config.Config
	boxMgr              *boxmgr.Manager
	refreshReloader     refreshReloader
	logger              Logger
	httpClient          *http.Client // Custom HTTP client with connection pooling
	store               store.Store  // Data store for persisting nodes
	connectorRuntime    ConnectorRuntime
	preferredIPSelector preferredIPRuntimeSelector

	status           monitor.SubscriptionStatus
	sourceSyncStatus monitor.SourceSyncStatus
	ctx              context.Context
	cancel           context.CancelFunc
	refreshMu        sync.Mutex // prevents concurrent refreshes
	manualRefresh    chan struct{}
	configChanged    chan struct{} // signals config updates to the refresh loop
	refreshDone      chan struct{} // closed after each refresh cycle, then replaced
}

type activeSourceSnapshot struct {
	SubscriptionSources    []RuntimeSource
	EphemeralProxySources  []RuntimeSource
	FallbackActive         bool
	LocalSourceCount       int
	ManifestSourceCount    int
	FallbackSourceCount    int
	ConnectorSourceCount   int
	ConnectorInstanceCount int
}

func hasRuntimeRefreshSources(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	if len(cfg.Subscriptions) > 0 {
		return true
	}
	if hasEnabledLocalConnectors(cfg.Connectors) {
		return true
	}
	return cfg.SourceSync.Enabled &&
		(strings.TrimSpace(cfg.SourceSync.ManifestURL) != "" || len(cfg.SourceSync.FallbackSubscriptions) > 0)
}

// New creates a SubscriptionManager.
func New(cfg *config.Config, boxMgr *boxmgr.Manager, opts ...Option) *Manager {
	ctx, cancel := context.WithCancel(context.Background())

	// Create optimized HTTP client with connection pooling
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second, // Overall timeout
	}

	m := &Manager{
		baseCfg:             cfg,
		boxMgr:              boxMgr,
		ctx:                 ctx,
		cancel:              cancel,
		manualRefresh:       make(chan struct{}, 1),
		configChanged:       make(chan struct{}, 1),
		refreshDone:         make(chan struct{}),
		httpClient:          httpClient,
		preferredIPSelector: runPreferredIPSelection,
	}
	if boxMgr != nil {
		m.refreshReloader = boxManagerRefreshReloader{manager: boxMgr}
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.logger == nil {
		m.logger = defaultLogger{}
	}
	if m.connectorRuntime == nil {
		m.connectorRuntime = newConnectorRuntimeManager(ctx, m.logger)
	}
	return m
}

// SetBoxManager attaches the runtime box manager after a bootstrap-only manager
// was created from config. This allows source-sync bootstrap to happen before
// sing-box starts when no local nodes are configured yet.
func (m *Manager) SetBoxManager(boxMgr *boxmgr.Manager) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.boxMgr = boxMgr
	if boxMgr == nil {
		m.refreshReloader = nil
	} else {
		m.refreshReloader = boxManagerRefreshReloader{manager: boxMgr}
	}
}

// BootstrapRuntimeNodes materializes manifest/fallback runtime sources into the
// in-memory config before the initial box manager startup. This is required for
// source_sync-only deployments where no local nodes exist yet.
func (m *Manager) BootstrapRuntimeNodes() error {
	if m == nil {
		return fmt.Errorf("subscription manager is nil")
	}

	snapshot, err := m.buildActiveSourceSnapshot()
	if err != nil {
		return err
	}

	subscriptionNodes, err := m.fetchSubscriptionSources(snapshot.SubscriptionSources)
	if err != nil {
		return err
	}

	ephemeralNodes := append(subscriptionNodes, m.materializeProxySources(snapshot.EphemeralProxySources)...)
	newCfg := m.createNewConfig(ephemeralNodes)
	if newCfg == nil {
		return fmt.Errorf("config is nil")
	}

	m.mu.RLock()
	baseCfg := m.baseCfg
	m.mu.RUnlock()
	if baseCfg == nil {
		return fmt.Errorf("config is nil")
	}

	baseCfg.Lock()
	baseCfg.Nodes = append([]config.NodeConfig(nil), newCfg.Nodes...)
	baseCfg.Unlock()

	m.mu.Lock()
	m.status.NodeCount = len(newCfg.Nodes)
	m.status.LastRefresh = time.Now()
	m.mu.Unlock()

	if err := m.syncRuntimeNodesToStore(ephemeralNodes); err != nil {
		m.logger.Warnf("failed to sync bootstrap runtime nodes to store: %v", err)
	}

	return nil
}

// Start begins the background goroutine that manages periodic subscription refresh.
// The goroutine dynamically checks config to decide whether to actually perform refreshes,
// so it's safe to call Start() even when subscription refresh is initially disabled.
func (m *Manager) Start() {
	if m.isEnabled() {
		m.logger.Infof("starting subscription refresh, interval: %s", m.currentInterval())
	} else {
		m.logger.Infof("subscription manager started (auto-refresh currently disabled, will activate on config change)")
	}

	go m.refreshLoop()
	if m.shouldStartImmediateRefresh() {
		go m.doRefresh()
	}
}

// Stop stops the periodic refresh.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}

	// Close idle connections
	if m.httpClient != nil {
		m.httpClient.CloseIdleConnections()
	}
	if m.connectorRuntime != nil {
		_ = m.connectorRuntime.StopAll()
	}
}

// RefreshNow triggers an immediate refresh, regardless of whether auto-refresh is enabled.
// It only requires that subscription URLs are configured.
func (m *Manager) RefreshNow() error {
	m.mu.RLock()
	baseCfg := m.baseCfg
	m.mu.RUnlock()
	if baseCfg == nil {
		return fmt.Errorf("配置未初始化")
	}
	baseCfg.RLock()
	hasRefreshSources := hasRuntimeRefreshSources(baseCfg)
	timeout := baseCfg.SubscriptionRefresh.Timeout
	healthCheckTimeout := baseCfg.SubscriptionRefresh.HealthCheckTimeout
	drainTimeout := baseCfg.SubscriptionRefresh.DrainTimeout
	if baseCfg.SourceSync.RequestTimeout > timeout {
		timeout = baseCfg.SourceSync.RequestTimeout
	}
	baseCfg.RUnlock()

	if !hasRefreshSources {
		return fmt.Errorf("没有配置可刷新的来源")
	}

	select {
	case m.manualRefresh <- struct{}{}:
	default:
		// Already a refresh pending
	}

	// Wait for refresh to complete or timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(m.ctx, manualRefreshWaitTimeout(timeout, healthCheckTimeout, drainTimeout))
	defer cancel()

	// Snapshot the current done channel before waiting
	m.mu.RLock()
	doneCh := m.refreshDone
	m.mu.RUnlock()

	select {
	case <-ctx.Done():
		return fmt.Errorf("refresh timeout")
	case <-doneCh:
		status := m.Status()
		if status.LastError != "" {
			return fmt.Errorf("refresh failed: %s", status.LastError)
		}
		return nil
	}
}

func manualRefreshWaitTimeout(fetchTimeout, healthCheckTimeout, drainTimeout time.Duration) time.Duration {
	if fetchTimeout <= 0 {
		fetchTimeout = manualRefreshDefaultFetch
	}
	if healthCheckTimeout <= 0 {
		healthCheckTimeout = manualRefreshDefaultHealth
	}
	if drainTimeout <= 0 {
		drainTimeout = manualRefreshDefaultDrain
	}
	// Source sync can make one manifest request followed by one source request
	// before the reload drains the old box and probes the candidate generation.
	waitTimeout := manualRefreshCompletionGrace
	for _, stageTimeout := range []time.Duration{fetchTimeout, fetchTimeout, healthCheckTimeout, drainTimeout} {
		if stageTimeout > maximumDuration-waitTimeout {
			return maximumDuration
		}
		waitTimeout += stageTimeout
	}
	return waitTimeout
}

// Status returns the current refresh status, including dynamic config state.
func (m *Manager) Status() monitor.SubscriptionStatus {
	m.mu.RLock()
	status := m.status
	baseCfg := m.baseCfg
	m.mu.RUnlock()
	if baseCfg != nil {
		baseCfg.RLock()
		status.Enabled = isEnabledConfig(baseCfg)
		status.HasSubscriptions = hasRuntimeRefreshSources(baseCfg)
		baseCfg.RUnlock()
	}

	// Check if nodes have been modified since last refresh
	status.NodesModified = m.CheckNodesModified()
	return status
}

// SourceSyncStatus returns the latest runtime source sync state.
func (m *Manager) SourceSyncStatus() monitor.SourceSyncStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sourceSyncStatus
}

// refreshLoop runs the background loop that manages periodic and manual refreshes.
// It dynamically reads config to decide whether to auto-refresh and at what interval.
