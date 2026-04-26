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

// Manager owns the lifecycle of the active sing-box instance.
type Manager struct {
	mu sync.RWMutex

	currentBox    *box.Box
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
	idle               bool // true when manager was started but stopped due to 0 enabled nodes
	ephemeralNodes     []config.NodeConfig

	// lastAppliedMode and lastAppliedBasePort track the mode/BasePort from the
	// last successful Start/Reload. Used by TriggerReload to detect changes,
	// since m.cfg may have been mutated by updateAllSettings before reload.
	lastAppliedMode     string
	lastAppliedBasePort uint16
}

// New creates a BoxManager with the given config.
func New(cfg *config.Config, monitorCfg monitor.Config, opts ...Option) *Manager {
	m := &Manager{
		cfg:        cfg,
		monitorCfg: monitorCfg,
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
	if ctx == nil {
		ctx = context.Background()
	}
	if err := m.ensureMonitor(ctx); err != nil {
		return err
	}

	m.mu.Lock()
	if m.cfg == nil {
		m.mu.Unlock()
		return errors.New("box manager requires config")
	}
	if m.currentBox != nil {
		m.mu.Unlock()
		return errors.New("sing-box already running")
	}
	m.applyConfigSettings(m.cfg)
	m.baseCtx = ctx
	cfg := m.cfg
	m.mu.Unlock()

	// Try to start, with automatic port conflict resolution
	var instance *box.Box
	maxRetries := 10
	for retry := 0; retry < maxRetries; retry++ {
		var err error
		instance, err = m.createBox(ctx, cfg)
		if err != nil {
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
	m.lastAppliedMode = cfg.Mode
	m.lastAppliedBasePort = cfg.MultiPort.BasePort
	m.mu.Unlock()

	// Start periodic health check after nodes are registered
	m.mu.Lock()
	if m.monitorMgr != nil && !m.healthCheckStarted {
		interval := cfg.Management.HealthCheckInterval
		m.monitorMgr.StartPeriodicHealthCheck(interval, periodicHealthTimeout)
		m.healthCheckStarted = true
	}
	m.mu.Unlock()

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

// Reload gracefully switches to a new configuration.
// For multi-port mode, we must stop the old instance first to release ports.
// Supports transitioning from idle state (0 nodes → has nodes).
func (m *Manager) Reload(newCfg *config.Config) error {
	if newCfg == nil {
		return errors.New("new config is nil")
	}

	m.mu.Lock()
	if m.currentBox == nil && !m.idle {
		m.mu.Unlock()
		return errors.New("manager not started")
	}
	ctx := m.baseCtx
	oldBox := m.currentBox
	oldCfg := m.cfg
	prevMonitorCfg := m.monitorCfg
	m.currentBox = nil // Mark as reloading
	m.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}

	m.logger.Infof("reloading with %d nodes", len(newCfg.Nodes))

	// For multi-port mode, we must close old instance first to release ports
	// This causes a brief interruption but avoids port conflicts
	if oldBox != nil {
		m.logger.Infof("stopping old instance to release ports...")
		if err := oldBox.Close(); err != nil {
			m.logger.Warnf("error closing old instance: %v", err)
		}
		// Give OS time to release ports
		time.Sleep(500 * time.Millisecond)
	}

	// Begin a new reload generation. Nodes re-registered during createBox will
	// be marked with the new generation; stale (disabled/removed) nodes will be
	// swept after the new box is successfully started.
	if m.monitorMgr != nil {
		m.monitorMgr.BeginReload()
	}

	// Reset shared state store to ensure clean state for new config
	pool.ResetSharedStateStore()

	// Create and start new box instance with automatic port conflict resolution
	var instance *box.Box
	maxRetries := 10
	for retry := 0; retry < maxRetries; retry++ {
		var err error
		instance, err = m.createBox(ctx, newCfg)
		if err != nil {
			m.rollbackToOldConfig(ctx, oldCfg)
			return fmt.Errorf("create new box: %w", err)
		}
		if err = instance.Start(); err != nil {
			_ = instance.Close()
			// Check if it's a port conflict error
			if conflictPort := extractPortFromBindError(err); conflictPort > 0 {
				m.logger.Warnf("port %d is in use, reassigning and retrying...", conflictPort)
				if reassigned := reassignConflictingPort(newCfg, conflictPort); reassigned {
					pool.ResetSharedStateStore()
					continue
				}
			}
			m.rollbackToOldConfig(ctx, oldCfg)
			return fmt.Errorf("start new box: %w", err)
		}
		break // Success
	}

	// Sweep stale monitor entries (disabled/removed nodes) now that the new box
	// has successfully registered all active nodes with the current generation.
	if m.monitorMgr != nil {
		m.monitorMgr.SweepStaleNodes()
	}

	m.applyConfigSettings(newCfg)

	m.mu.Lock()
	m.currentBox = instance
	m.cfg = newCfg
	m.idle = false // Clear idle state on successful reload
	m.lastAppliedMode = newCfg.Mode
	m.lastAppliedBasePort = newCfg.MultiPort.BasePort
	// Update monitor server's config reference so settings API reads the latest config
	if m.monitorServer != nil {
		m.monitorServer.SetConfig(newCfg)
	}
	// Notify config update listeners (e.g., subscription manager)
	listeners := make([]ConfigUpdateListener, len(m.configListeners))
	copy(listeners, m.configListeners)
	m.mu.Unlock()

	for _, l := range listeners {
		l.OnConfigUpdate(newCfg)
	}

	// Reload 成功后立即触发 1 次全量探测（内部去重，避免多次 Reload 造成突发）。
	// 与启动阶段不同，订阅刷新后的新节点集如果健康度达不到最小阈值，
	// 应该视为坏池子并回滚，而不是继续覆盖旧的可用池。
	if m.monitorMgr != nil {
		m.monitorMgr.RequestProbeAllOnce(periodicHealthTimeout)
	}
	if newCfg.SubscriptionRefresh.MinAvailableNodes > 0 {
		timeout := newCfg.SubscriptionRefresh.HealthCheckTimeout
		if timeout <= 0 {
			timeout = defaultHealthCheckTimeout
		}
		if err := m.waitForHealthCheck(timeout); err != nil {
			m.logger.Warnf("reload health check failed: %v", err)
			m.rollbackToOldConfig(ctx, oldCfg)
			return fmt.Errorf("reload health check failed: %w", err)
		}
	}
	m.syncMonitorServerLifecycle(ctx, prevMonitorCfg, newCfg)
	m.logger.Infof("reload completed successfully with %d nodes", len(newCfg.Nodes))
	return nil
}

// AddConfigListener registers a listener to be notified when config changes after reload.
func (m *Manager) AddConfigListener(l ConfigUpdateListener) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configListeners = append(m.configListeners, l)
}

// rollbackToOldConfig attempts to restart with the previous configuration.
func (m *Manager) rollbackToOldConfig(ctx context.Context, oldCfg *config.Config) {
	if oldCfg == nil {
		return
	}
	m.logger.Warnf("attempting rollback to previous config...")
	instance, err := m.createBox(ctx, oldCfg)
	if err != nil {
		m.logger.Errorf("rollback failed to create box: %v", err)
		return
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		m.logger.Errorf("rollback failed to start box: %v", err)
		return
	}
	m.mu.Lock()
	m.currentBox = instance
	m.cfg = oldCfg
	listeners := make([]ConfigUpdateListener, len(m.configListeners))
	copy(listeners, m.configListeners)
	m.mu.Unlock()

	for _, l := range listeners {
		l.OnConfigUpdate(oldCfg)
	}
	m.logger.Infof("rollback successful")
}

// Close terminates the active instance and auxiliary components.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var err error
	if m.currentBox != nil {
		err = m.currentBox.Close()
		m.currentBox = nil
	}
	if m.monitorServer != nil {
		m.monitorServer.Shutdown(context.Background())
		m.monitorServer = nil
	}
	if m.monitorMgr != nil {
		m.monitorMgr.Stop()
		m.monitorMgr = nil
		m.healthCheckStarted = false
	}
	m.baseCtx = nil
	return err
}

// MonitorManager returns the shared monitor manager.
func (m *Manager) MonitorManager() *monitor.Manager {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.monitorMgr
}

// MonitorServer returns the monitor HTTP server.
func (m *Manager) MonitorServer() *monitor.Server {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.monitorServer
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

// gracefulSwitch swaps the current box with a new one.
func (m *Manager) gracefulSwitch(newBox *box.Box) error {
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
func (m *Manager) drainOldBox(oldBox *box.Box, timeout time.Duration) {
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
	if m.monitorMgr == nil || m.minAvailableNodes <= 0 {
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
		if available >= m.minAvailableNodes {
			m.logger.Infof("health check passed: %d/%d nodes available", available, total)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout: %d/%d nodes available (need >= %d)", available, total, m.minAvailableNodes)
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
	if m.monitorMgr != nil {
		m.mu.Unlock()
		return nil
	}

	monitorMgr, err := monitor.NewManager(m.monitorCfg)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("init monitor manager: %w", err)
	}
	monitorMgr.SetLogger(monitorLoggerAdapter{logger: m.logger})
	m.monitorMgr = monitorMgr

	var serverToStart *monitor.Server
	if m.monitorCfg.Enabled {
		if m.monitorServer == nil {
			serverToStart = monitor.NewServer(m.monitorCfg, monitorMgr, log.Default())
			m.monitorServer = serverToStart
		}
		// Set NodeManager for config CRUD endpoints
		if m.monitorServer != nil {
			m.monitorServer.SetNodeManager(m)
		}
		// Note: StartPeriodicHealthCheck is called after nodes are registered in Start()
	}
	m.mu.Unlock()

	if serverToStart != nil {
		serverToStart.Start(ctx)
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
	m.monitorCfg.ProbeTarget = cfg.Management.ProbeTarget
	m.monitorCfg.ProbeTargets = append([]string(nil), cfg.Management.ProbeTargets...)
	m.monitorCfg.SkipCertVerify = cfg.SkipCertVerify
	if m.monitorMgr != nil {
		m.monitorMgr.SetSkipCertVerify(cfg.SkipCertVerify)
		if err := m.monitorMgr.UpdateProbeTargets(cfg.Management.ProbeTargets, cfg.Management.ProbeTarget); err != nil {
			m.logger.Warnf("failed to update probe targets from config: %v", err)
		}
	}
}

func (m *Manager) syncMonitorServerLifecycle(ctx context.Context, prev monitor.Config, activeCfg *config.Config) {
	if activeCfg == nil {
		return
	}

	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.RLock()
	currentCfg := m.monitorCfg
	currentServer := m.monitorServer
	currentMgr := m.monitorMgr
	currentStore := m.store
	m.mu.RUnlock()

	if currentMgr == nil {
		return
	}

	needsRestart := prev.Enabled != currentCfg.Enabled || prev.Listen != currentCfg.Listen

	if !currentCfg.Enabled {
		if currentServer != nil {
			currentServer.Shutdown(context.Background())
			m.mu.Lock()
			if m.monitorServer == currentServer {
				m.monitorServer = nil
			}
			m.mu.Unlock()
		}
		return
	}

	if currentServer != nil && !needsRestart {
		currentServer.SetConfig(activeCfg)
		return
	}

	if currentServer != nil {
		currentServer.Shutdown(context.Background())
	}

	server := monitor.NewServer(currentCfg, currentMgr, log.Default())
	if server == nil {
		return
	}
	server.SetNodeManager(m)
	server.SetStore(currentStore)
	server.SetConfig(activeCfg)
	server.Start(ctx)

	m.mu.Lock()
	if m.monitorServer == nil || m.monitorServer == currentServer {
		m.monitorServer = server
	}
	m.mu.Unlock()
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

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return config.NodeConfig{}, errConfigUnavailable
	}

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

	ref = strings.TrimSpace(ref)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return config.NodeConfig{}, errConfigUnavailable
	}

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

	ref = strings.TrimSpace(ref)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return errConfigUnavailable
	}

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

	ref = strings.TrimSpace(ref)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return errConfigUnavailable
	}

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
		var err error
		newCfg, err = config.LoadForReload(cfgPath)
		if err != nil {
			m.logger.Warnf("failed to reload config from disk: %v, falling back to in-memory copy", err)
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

	m.mu.RLock()
	ephemeralNodes := cloneNodes(m.ephemeralNodes)
	m.mu.RUnlock()
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
		return m.enterIdle(newCfg)
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

	return m.ReloadWithPortMap(newCfg, portMap)
}

// ReloadWithPortMap gracefully switches to a new configuration, preserving port assignments.
func (m *Manager) ReloadWithPortMap(newCfg *config.Config, portMap map[string]uint16) error {
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

	return m.Reload(newCfg)
}

// enterIdle stops the running sing-box instance when there are 0 enabled nodes.
// The manager enters an idle state and can be resumed by TriggerReload when
// nodes are re-enabled.
func (m *Manager) enterIdle(newCfg *config.Config) error {
	m.mu.Lock()
	oldBox := m.currentBox
	wasIdle := m.idle
	m.currentBox = nil
	m.idle = true
	m.cfg = newCfg
	ctx := m.baseCtx
	// Update monitor server's config reference
	if m.monitorServer != nil {
		m.monitorServer.SetConfig(newCfg)
	}
	listeners := make([]ConfigUpdateListener, len(m.configListeners))
	copy(listeners, m.configListeners)
	m.mu.Unlock()

	if wasIdle {
		m.logger.Infof("already idle, updated config (still 0 enabled nodes)")
		return nil
	}

	// Stop the running instance
	if oldBox != nil {
		m.logger.Infof("stopping instance (all nodes disabled)...")
		if err := oldBox.Close(); err != nil {
			m.logger.Warnf("error closing instance during idle transition: %v", err)
		}
	}

	// Clean up monitor and shared state
	if m.monitorMgr != nil {
		m.monitorMgr.BeginReload()
		m.monitorMgr.SweepStaleNodes()
	}
	pool.ResetSharedStateStore()

	_ = ctx // baseCtx preserved for future resume

	for _, l := range listeners {
		l.OnConfigUpdate(newCfg)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ephemeralNodes = cloneNodes(nodes)
}

func (m *Manager) copyConfigLocked() *config.Config {
	if m.cfg == nil {
		return nil
	}
	return m.cfg.Clone()
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
