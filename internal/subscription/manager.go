package subscription

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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

// Ensure Manager implements boxmgr.ConfigUpdateListener.
var _ boxmgr.ConfigUpdateListener = (*Manager)(nil)

// Manager handles periodic subscription refresh.
type Manager struct {
	mu sync.RWMutex

	baseCfg          *config.Config
	boxMgr           *boxmgr.Manager
	logger           Logger
	httpClient       *http.Client // Custom HTTP client with connection pooling
	store            store.Store  // Data store for persisting nodes
	connectorRuntime ConnectorRuntime

	status           monitor.SubscriptionStatus
	sourceSyncStatus monitor.SourceSyncStatus
	ctx              context.Context
	cancel           context.CancelFunc
	refreshMu        sync.Mutex // prevents concurrent refreshes
	manualRefresh    chan struct{}
	configChanged    chan struct{} // signals config updates to the refresh loop
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

// New creates a SubscriptionManager.
func New(cfg *config.Config, boxMgr *boxmgr.Manager, opts ...Option) *Manager {
	ctx, cancel := context.WithCancel(context.Background())

	// Create optimized HTTP client with connection pooling
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
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
		baseCfg:       cfg,
		boxMgr:        boxMgr,
		ctx:           ctx,
		cancel:        cancel,
		manualRefresh: make(chan struct{}, 1),
		configChanged: make(chan struct{}, 1),
		httpClient:    httpClient,
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
	if m.isEnabled() {
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
	hasRefreshSources := len(m.baseCfg.Subscriptions) > 0 ||
		(m.baseCfg.SourceSync.Enabled && (strings.TrimSpace(m.baseCfg.SourceSync.ManifestURL) != "" || len(m.baseCfg.SourceSync.FallbackSubscriptions) > 0))
	timeout := m.baseCfg.SubscriptionRefresh.Timeout
	healthCheckTimeout := m.baseCfg.SubscriptionRefresh.HealthCheckTimeout
	if m.baseCfg.SourceSync.RequestTimeout > timeout {
		timeout = m.baseCfg.SourceSync.RequestTimeout
	}
	m.mu.RUnlock()

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

	ctx, cancel := context.WithTimeout(m.ctx, timeout+healthCheckTimeout)
	defer cancel()

	// Poll status until refresh completes
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	startCount := m.Status().RefreshCount
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("refresh timeout")
		case <-ticker.C:
			status := m.Status()
			if status.RefreshCount > startCount {
				if status.LastError != "" {
					return fmt.Errorf("refresh failed: %s", status.LastError)
				}
				return nil
			}
		}
	}
}

// Status returns the current refresh status, including dynamic config state.
func (m *Manager) Status() monitor.SubscriptionStatus {
	m.mu.RLock()
	status := m.status
	status.Enabled = m.isEnabledLocked()
	status.HasSubscriptions = len(m.baseCfg.Subscriptions) > 0 || m.baseCfg.SourceSync.Enabled || len(m.baseCfg.SourceSync.FallbackSubscriptions) > 0
	m.mu.RUnlock()

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
func (m *Manager) refreshLoop() {
	interval := m.currentInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Set initial next refresh time
	m.mu.Lock()
	if m.isEnabledLocked() {
		m.status.NextRefresh = time.Now().Add(interval)
	}
	m.mu.Unlock()

	for {
		select {
		case <-m.ctx.Done():
			return

		case <-ticker.C:
			// Dynamically adjust interval if config changed
			newInterval := m.currentInterval()
			if newInterval != interval {
				m.logger.Infof("subscription refresh interval changed: %s → %s", interval, newInterval)
				interval = newInterval
				ticker.Reset(interval)
			}

			// Only auto-refresh when enabled and subscriptions exist
			if m.isEnabled() {
				m.doRefresh()
			}

			m.mu.Lock()
			if m.isEnabledLocked() {
				m.status.NextRefresh = time.Now().Add(interval)
			} else {
				m.status.NextRefresh = time.Time{}
			}
			m.mu.Unlock()

		case <-m.manualRefresh:
			// Manual refresh always executes (caller already verified subscriptions exist)
			m.doRefresh()
			// Reset ticker and recalculate interval after manual refresh
			newInterval := m.currentInterval()
			if newInterval != interval {
				interval = newInterval
			}
			ticker.Reset(interval)
			m.mu.Lock()
			m.status.NextRefresh = time.Now().Add(interval)
			m.mu.Unlock()

		case <-m.configChanged:
			// Config was updated (e.g., after reload), recalculate interval
			newInterval := m.currentInterval()
			if newInterval != interval {
				m.logger.Infof("subscription refresh interval changed: %s → %s", interval, newInterval)
				interval = newInterval
				ticker.Reset(interval)
			}
			m.mu.Lock()
			if m.isEnabledLocked() {
				m.status.NextRefresh = time.Now().Add(interval)
			} else {
				m.status.NextRefresh = time.Time{}
			}
			m.mu.Unlock()
		}
	}
}

// isEnabled checks if auto-refresh should run (acquires read lock).
func (m *Manager) isEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isEnabledLocked()
}

// isEnabledLocked checks if auto-refresh should run (caller must hold mu).
func (m *Manager) isEnabledLocked() bool {
	localSubscriptionsEnabled := m.baseCfg.SubscriptionRefresh.Enabled && len(m.baseCfg.Subscriptions) > 0
	sourceSyncEnabled := m.baseCfg.SourceSync.Enabled &&
		(strings.TrimSpace(m.baseCfg.SourceSync.ManifestURL) != "" || len(m.baseCfg.SourceSync.FallbackSubscriptions) > 0)
	return localSubscriptionsEnabled || sourceSyncEnabled
}

// currentInterval returns the configured refresh interval (acquires read lock).
func (m *Manager) currentInterval() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentIntervalLocked()
}

// currentIntervalLocked returns the configured refresh interval (caller must hold mu).
func (m *Manager) currentIntervalLocked() time.Duration {
	intervals := make([]time.Duration, 0, 2)
	if m.baseCfg.SubscriptionRefresh.Enabled && len(m.baseCfg.Subscriptions) > 0 {
		intervals = append(intervals, m.baseCfg.SubscriptionRefresh.Interval)
	}
	if m.baseCfg.SourceSync.Enabled {
		intervals = append(intervals, m.baseCfg.SourceSync.RefreshInterval)
	}
	if len(intervals) == 0 {
		return 1 * time.Hour
	}
	interval := intervals[0]
	for _, candidate := range intervals[1:] {
		if candidate > 0 && candidate < interval {
			interval = candidate
		}
	}
	if interval <= 0 {
		interval = 1 * time.Hour
	}
	return interval
}

// doRefresh performs a single refresh operation.
// It rebuilds the in-memory runtime source set and keeps remote/fallback nodes
// out of the persistent local store.
func (m *Manager) doRefresh() {
	if !m.refreshMu.TryLock() {
		m.logger.Warnf("refresh already in progress, skipping")
		return
	}
	defer m.refreshMu.Unlock()

	m.mu.Lock()
	m.status.IsRefreshing = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.status.IsRefreshing = false
		m.status.RefreshCount++
		m.mu.Unlock()
	}()

	m.logger.Infof("starting subscription refresh")

	snapshot, err := m.buildActiveSourceSnapshot()
	if err != nil {
		m.logger.Errorf("build source snapshot failed: %v", err)
		m.mu.Lock()
		m.status.LastError = err.Error()
		m.status.LastRefresh = time.Now()
		m.mu.Unlock()
		return
	}

	subscriptionNodes, err := m.fetchSubscriptionSources(snapshot.SubscriptionSources)
	if err != nil {
		m.logger.Errorf("fetch subscriptions failed: %v", err)
		m.mu.Lock()
		m.status.LastError = err.Error()
		m.status.LastRefresh = time.Now()
		m.mu.Unlock()
		return
	}

	ephemeralNodes := append(subscriptionNodes, m.materializeProxySources(snapshot.EphemeralProxySources)...)
	m.boxMgr.SetEphemeralNodes(ephemeralNodes)

	portMap := m.boxMgr.CurrentPortMap()
	newCfg := m.createNewConfig(ephemeralNodes)

	if err := m.boxMgr.ReloadWithPortMap(newCfg, portMap); err != nil {
		m.logger.Errorf("reload failed: %v", err)
		m.mu.Lock()
		m.status.LastError = err.Error()
		m.status.LastRefresh = time.Now()
		m.mu.Unlock()
		return
	}

	totalNodes := len(newCfg.Nodes)
	m.mu.Lock()
	m.status.LastRefresh = time.Now()
	m.status.NodeCount = totalNodes
	m.status.LastError = ""
	m.sourceSyncStatus.FallbackActive = snapshot.FallbackActive
	m.sourceSyncStatus.LocalSourceCount = snapshot.LocalSourceCount
	m.sourceSyncStatus.ManifestSourceCount = snapshot.ManifestSourceCount
	m.sourceSyncStatus.FallbackSourceCount = snapshot.FallbackSourceCount
	m.sourceSyncStatus.ConnectorSourceCount = snapshot.ConnectorSourceCount
	m.sourceSyncStatus.ConnectorInstanceCount = snapshot.ConnectorInstanceCount
	m.mu.Unlock()

	m.logger.Infof("subscription refresh completed, %d total nodes active (%d runtime-generated)", totalNodes, len(ephemeralNodes))
}

// OnConfigUpdate is called by boxmgr after a successful reload.
// It updates the subscription manager's reference to the latest config
// so that subsequent refreshes use updated subscription URLs and settings.
func (m *Manager) OnConfigUpdate(cfg *config.Config) {
	if cfg == nil {
		return
	}
	m.mu.Lock()
	m.baseCfg = cfg
	m.mu.Unlock()
	m.logger.Infof("subscription manager config updated after reload")

	// Notify the refresh loop about config changes so it can
	// recalculate interval and enable/disable auto-refresh dynamically.
	select {
	case m.configChanged <- struct{}{}:
	default:
	}
}

// CheckNodesModified always returns false — with SQLite Store,
// node modifications are tracked in the database, not via file hashes.
func (m *Manager) CheckNodesModified() bool {
	return false
}

// MarkNodesModified updates the modification status.
func (m *Manager) MarkNodesModified() {
	m.mu.Lock()
	m.status.NodesModified = true
	m.mu.Unlock()
}

func (m *Manager) buildActiveSourceSnapshot() (activeSourceSnapshot, error) {
	m.mu.RLock()
	cfg := m.baseCfg.Clone()
	m.mu.RUnlock()

	snapshot := activeSourceSnapshot{}
	if cfg == nil {
		return snapshot, fmt.Errorf("config is nil")
	}

	localSources := m.buildLocalSources(cfg)
	snapshot.LocalSourceCount = len(localSources)

	if !cfg.SourceSync.Enabled || strings.TrimSpace(cfg.SourceSync.ManifestURL) == "" {
		m.reconcileConnectorSources(cfg, nil)
		snapshot.SubscriptionSources = dedupeSourcesWithPrecedence(localSources)
		m.mu.Lock()
		m.sourceSyncStatus.Enabled = cfg.SourceSync.Enabled
		m.sourceSyncStatus.ManifestURL = strings.TrimSpace(cfg.SourceSync.ManifestURL)
		m.sourceSyncStatus.ManifestHealthy = false
		m.sourceSyncStatus.LastError = ""
		m.sourceSyncStatus.LocalSourceCount = snapshot.LocalSourceCount
		m.sourceSyncStatus.ManifestSourceCount = 0
		m.sourceSyncStatus.FallbackSourceCount = 0
		m.sourceSyncStatus.ConnectorSourceCount = 0
		m.sourceSyncStatus.ConnectorInstanceCount = 0
		m.sourceSyncStatus.FallbackActive = false
		m.mu.Unlock()
		return snapshot, nil
	}

	manifestSources, err := m.fetchManifestSources(cfg)
	if err == nil {
		var localSubscriptionSources []RuntimeSource
		var localProxySources []RuntimeSource
		var manifestSubscriptionSources []RuntimeSource
		var manifestProxySources []RuntimeSource
		var manifestConnectorSources []RuntimeSource

		for _, source := range localSources {
			if source.Kind == SourceKindSubscription {
				localSubscriptionSources = append(localSubscriptionSources, source)
			} else if source.Kind == SourceKindProxyURI {
				localProxySources = append(localProxySources, source)
			}
		}
		for _, source := range manifestSources {
			switch source.Kind {
			case SourceKindSubscription:
				manifestSubscriptionSources = append(manifestSubscriptionSources, source)
			case SourceKindProxyURI:
				manifestProxySources = append(manifestProxySources, source)
			case SourceKindConnector:
				manifestConnectorSources = append(manifestConnectorSources, source)
			}
		}
		snapshot.ConnectorSourceCount = len(manifestConnectorSources)

		connectorProxySources, connectorErr := m.reconcileConnectorSources(cfg, manifestConnectorSources)
		if connectorErr != nil {
			m.logger.Warnf("connector reconcile failed: %v", connectorErr)
		}
		snapshot.ConnectorInstanceCount = len(connectorProxySources)

		snapshot.SubscriptionSources = dedupeSourcesWithPrecedence(localSubscriptionSources, manifestSubscriptionSources)
		localProxyKeys := make(map[string]struct{}, len(localProxySources))
		for _, source := range localProxySources {
			localProxyKeys[sourceKey(source)] = struct{}{}
		}
		for _, source := range dedupeSourcesWithPrecedence(manifestProxySources, connectorProxySources) {
			if _, exists := localProxyKeys[sourceKey(source)]; exists {
				continue
			}
			snapshot.EphemeralProxySources = append(snapshot.EphemeralProxySources, source)
		}
		snapshot.ManifestSourceCount = len(manifestSources)

		m.mu.Lock()
		m.sourceSyncStatus.Enabled = true
		m.sourceSyncStatus.ManifestURL = strings.TrimSpace(cfg.SourceSync.ManifestURL)
		m.sourceSyncStatus.ManifestHealthy = true
		m.sourceSyncStatus.LastSync = time.Now()
		m.sourceSyncStatus.LastSuccess = m.sourceSyncStatus.LastSync
		m.sourceSyncStatus.LastError = ""
		m.sourceSyncStatus.FallbackActive = false
		m.sourceSyncStatus.LocalSourceCount = snapshot.LocalSourceCount
		m.sourceSyncStatus.ManifestSourceCount = snapshot.ManifestSourceCount
		m.sourceSyncStatus.FallbackSourceCount = 0
		m.sourceSyncStatus.ConnectorSourceCount = snapshot.ConnectorSourceCount
		m.sourceSyncStatus.ConnectorInstanceCount = snapshot.ConnectorInstanceCount
		m.mu.Unlock()
		return snapshot, nil
	}

	m.reconcileConnectorSources(cfg, nil)
	fallbackSources := make([]RuntimeSource, 0, len(cfg.SourceSync.FallbackSubscriptions))
	for idx, subURL := range cfg.SourceSync.FallbackSubscriptions {
		normalized := normalizeRuntimeSource(RuntimeSource{
			ID:     fmt.Sprintf("fallback-%d", idx+1),
			Kind:   SourceKindSubscription,
			Name:   fmt.Sprintf("fallback-%d", idx+1),
			Input:  subURL,
			Origin: "fallback",
		}, cfg.SourceSync.DefaultDirectProxyScheme)
		if strings.TrimSpace(normalized.Input) == "" {
			continue
		}
		fallbackSources = append(fallbackSources, normalized)
	}

	var localSubscriptionSources []RuntimeSource
	for _, source := range localSources {
		if source.Kind == SourceKindSubscription {
			localSubscriptionSources = append(localSubscriptionSources, source)
		}
	}

	snapshot.SubscriptionSources = dedupeSourcesWithPrecedence(localSubscriptionSources, fallbackSources)
	snapshot.FallbackActive = len(fallbackSources) > 0
	snapshot.FallbackSourceCount = len(fallbackSources)

	m.mu.Lock()
	m.sourceSyncStatus.Enabled = true
	m.sourceSyncStatus.ManifestURL = strings.TrimSpace(cfg.SourceSync.ManifestURL)
	m.sourceSyncStatus.ManifestHealthy = false
	m.sourceSyncStatus.LastSync = time.Now()
	m.sourceSyncStatus.LastError = err.Error()
	m.sourceSyncStatus.FallbackActive = snapshot.FallbackActive
	m.sourceSyncStatus.LocalSourceCount = snapshot.LocalSourceCount
	m.sourceSyncStatus.ManifestSourceCount = 0
	m.sourceSyncStatus.FallbackSourceCount = snapshot.FallbackSourceCount
	m.sourceSyncStatus.ConnectorSourceCount = 0
	m.sourceSyncStatus.ConnectorInstanceCount = 0
	m.mu.Unlock()

	if len(snapshot.SubscriptionSources) == 0 && snapshot.LocalSourceCount == 0 {
		return snapshot, err
	}
	return snapshot, nil
}

func (m *Manager) reconcileConnectorSources(cfg *config.Config, connectorSources []RuntimeSource) ([]RuntimeSource, error) {
	if m.connectorRuntime == nil {
		return nil, nil
	}
	return m.connectorRuntime.Reconcile(cfg, connectorSources)
}

func (m *Manager) buildLocalSources(cfg *config.Config) []RuntimeSource {
	var sources []RuntimeSource

	for idx, subURL := range cfg.Subscriptions {
		normalized := normalizeRuntimeSource(RuntimeSource{
			ID:     fmt.Sprintf("local-sub-%d", idx+1),
			Kind:   SourceKindSubscription,
			Name:   fmt.Sprintf("subscription-%d", idx+1),
			Input:  subURL,
			Origin: "local",
		}, cfg.SourceSync.DefaultDirectProxyScheme)
		if strings.TrimSpace(normalized.Input) == "" {
			continue
		}
		sources = append(sources, normalized)
	}

	for idx, node := range cfg.Nodes {
		switch node.Source {
		case config.NodeSourceInline, config.NodeSourceFile, config.NodeSourceManual:
			normalized := normalizeRuntimeSource(RuntimeSource{
				ID:     fmt.Sprintf("local-node-%d", idx+1),
				Kind:   SourceKindProxyURI,
				Name:   node.Name,
				Input:  node.URI,
				Origin: "local",
			}, cfg.SourceSync.DefaultDirectProxyScheme)
			if strings.TrimSpace(normalized.Input) == "" {
				continue
			}
			sources = append(sources, normalized)
		}
	}

	return dedupeSourcesWithPrecedence(sources)
}

func (m *Manager) fetchManifestSources(cfg *config.Config) ([]RuntimeSource, error) {
	timeout := cfg.SourceSync.RequestTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.SourceSync.ManifestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create manifest request: %w", err)
	}
	if strings.TrimSpace(cfg.SourceSync.ManifestToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.SourceSync.ManifestToken))
	}
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest returned status %d", resp.StatusCode)
	}

	var payload manifestResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if !payload.Success {
		return nil, fmt.Errorf("manifest response indicated failure")
	}

	var sources []RuntimeSource
	for _, source := range payload.Sources {
		if !source.Enabled {
			continue
		}
		normalized := normalizeRuntimeSource(RuntimeSource{
			ID:      source.ID,
			Kind:    source.Kind,
			Name:    source.Name,
			Input:   source.Input,
			Options: source.Options,
			Origin:  "manifest",
		}, cfg.SourceSync.DefaultDirectProxyScheme)
		if strings.TrimSpace(normalized.Input) == "" {
			continue
		}
		sources = append(sources, normalized)
	}

	return dedupeSourcesWithPrecedence(sources), nil
}

func (m *Manager) fetchSubscriptionSources(sources []RuntimeSource) ([]config.NodeConfig, error) {
	var allNodes []config.NodeConfig
	var lastErr error

	timeout := m.currentFetchTimeout()
	for _, source := range sources {
		if source.Kind != SourceKindSubscription {
			continue
		}
		nodes, err := m.fetchSubscription(source.Input, timeout)
		if err != nil {
			m.logger.Warnf("failed to fetch %s: %v", source.Input, err)
			lastErr = err
			continue
		}
		for idx := range nodes {
			nodes[idx].Source = mapSourceOriginToNodeSource(source.Origin)
			nodes[idx].Name = buildNodeName(nodes[idx].URI, source.Name)
		}
		allNodes = append(allNodes, nodes...)
	}

	if len(allNodes) == 0 && lastErr != nil && len(sources) > 0 {
		return nil, lastErr
	}
	return allNodes, nil
}

func (m *Manager) materializeProxySources(sources []RuntimeSource) []config.NodeConfig {
	var nodes []config.NodeConfig
	for idx, source := range sources {
		if source.Kind != SourceKindProxyURI {
			continue
		}
		uri := strings.TrimSpace(source.Input)
		if uri == "" {
			continue
		}
		name := buildNodeName(uri, source.Name)
		if name == "" {
			name = fmt.Sprintf("remote-node-%d", idx+1)
		}
		nodes = append(nodes, config.NodeConfig{
			Name:   name,
			URI:    uri,
			Source: mapSourceOriginToNodeSource(source.Origin),
		})
	}
	return nodes
}

func mapSourceOriginToNodeSource(origin string) config.NodeSource {
	switch origin {
	case "manifest":
		return config.NodeSourceManifest
	case "fallback":
		return config.NodeSourceFallback
	default:
		return config.NodeSourceSubscription
	}
}

func (m *Manager) currentFetchTimeout() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	timeout := m.baseCfg.SubscriptionRefresh.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if m.baseCfg.SourceSync.RequestTimeout > timeout {
		timeout = m.baseCfg.SourceSync.RequestTimeout
	}
	return timeout
}

// fetchSubscription fetches and parses a single subscription URL.
func (m *Manager) fetchSubscription(subURL string, timeout time.Duration) ([]config.NodeConfig, error) {
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", subURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "*/*")

	// Use custom HTTP client with connection pooling
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	// Limit read size to prevent memory exhaustion
	const maxBodySize = 10 * 1024 * 1024 // 10MB
	limitedReader := io.LimitReader(resp.Body, maxBodySize)

	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return parseSubscriptionContent(string(body))
}

// createNewConfig creates a new config with runtime-generated nodes while
// preserving local inline/file/manual nodes.
func (m *Manager) createNewConfig(ephemeralNodes []config.NodeConfig) *config.Config {
	// Deep copy base config (uses Clone to avoid copying the mutex)
	m.mu.RLock()
	baseCfg := m.baseCfg
	m.mu.RUnlock()

	newCfg := baseCfg.Clone()

	// Start with persistent local nodes only.
	var allNodes []config.NodeConfig
	for _, node := range baseCfg.Nodes {
		if node.Source == config.NodeSourceInline || node.Source == config.NodeSourceFile {
			allNodes = append(allNodes, node)
		}
	}

	// Append runtime-generated subscription/manifest/fallback nodes.
	for idx := range ephemeralNodes {
		ephemeralNodes[idx].Name = strings.TrimSpace(ephemeralNodes[idx].Name)
		ephemeralNodes[idx].URI = strings.TrimSpace(ephemeralNodes[idx].URI)
		if ephemeralNodes[idx].Name == "" {
			ephemeralNodes[idx].Name = buildNodeName(ephemeralNodes[idx].URI, fmt.Sprintf("runtime-node-%d", idx+1))
		}
	}
	allNodes = append(allNodes, ephemeralNodes...)

	// Load manual nodes from Store
	if m.store != nil {
		storeManualNodes, err := m.store.ListNodes(m.ctx, store.NodeFilter{Source: store.NodeSourceManual})
		if err != nil {
			m.logger.Warnf("failed to load manual nodes from store: %v", err)
		} else if len(storeManualNodes) > 0 {
			for _, sn := range storeManualNodes {
				name := strings.TrimSpace(sn.Name)
				uri := strings.TrimSpace(sn.URI)
				if name == "" {
					if parsed, err := url.Parse(uri); err == nil && parsed.Fragment != "" {
						if decoded, err := url.QueryUnescape(parsed.Fragment); err == nil {
							name = decoded
						} else {
							name = parsed.Fragment
						}
					}
				}
				if name == "" {
					name = fmt.Sprintf("manual-%d", sn.ID)
				}
				allNodes = append(allNodes, config.NodeConfig{
					Name:     name,
					URI:      uri,
					Port:     sn.Port,
					Username: sn.Username,
					Password: sn.Password,
					Source:   config.NodeSourceManual,
				})
			}
			m.logger.Infof("preserved %d manual nodes from store during subscription refresh", len(storeManualNodes))
		}
	}

	// Assign port numbers to all nodes in multi-port mode
	if newCfg.Mode == "multi-port" {
		portCursor := newCfg.MultiPort.BasePort
		for i := range allNodes {
			allNodes[i].Port = portCursor
			portCursor++
			// Apply default credentials
			if allNodes[i].Username == "" {
				allNodes[i].Username = newCfg.MultiPort.Username
				allNodes[i].Password = newCfg.MultiPort.Password
			}
		}
	}

	newCfg.Nodes = allNodes
	return newCfg
}

// parseSubscriptionContent parses subscription content in various formats.
// This is a simplified version - the full implementation is in config package.
func parseSubscriptionContent(content string) ([]config.NodeConfig, error) {
	content = strings.TrimSpace(content)

	// Check if it's base64 encoded
	if isBase64(content) {
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(content)
			if err != nil {
				return parseNodesFromContent(content)
			}
		}
		content = string(decoded)
	}

	// Parse as plain text (one URI per line)
	return parseNodesFromContent(content)
}

func isBase64(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return false
	}
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	if strings.Contains(s, "://") {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		_, err = base64.RawStdEncoding.DecodeString(s)
	}
	return err == nil
}

func parseNodesFromContent(content string) ([]config.NodeConfig, error) {
	var nodes []config.NodeConfig
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		normalizedLine := config.NormalizeProxyURIInput(line, "http")
		if config.IsProxyURI(normalizedLine) {
			nodes = append(nodes, config.NodeConfig{URI: normalizedLine})
		}
	}
	return nodes, nil
}

type defaultLogger struct{}

func (defaultLogger) Infof(format string, args ...any) {
	log.Printf("[subscription] "+format, args...)
}

func (defaultLogger) Warnf(format string, args ...any) {
	log.Printf("[subscription] WARN: "+format, args...)
}

func (defaultLogger) Errorf(format string, args ...any) {
	log.Printf("[subscription] ERROR: "+format, args...)
}
