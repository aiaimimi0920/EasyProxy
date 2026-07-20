package monitor

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/store"

	"golang.org/x/sync/semaphore"
)

//go:embed assets/*
var embeddedFS embed.FS

// Session represents a user session with expiration.
type Session struct {
	Token     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// NodeManager exposes config node CRUD and reload operations.
type NodeManager interface {
	ListConfigNodes(ctx context.Context) ([]config.NodeConfig, error)
	CreateNode(ctx context.Context, node config.NodeConfig) (config.NodeConfig, error)
	UpdateNode(ctx context.Context, ref string, node config.NodeConfig) (config.NodeConfig, error)
	DeleteNode(ctx context.Context, ref string) error
	SetNodeEnabled(ctx context.Context, ref string, enabled bool) error
	TriggerReload(ctx context.Context) error
}

// Sentinel errors for node operations.
var (
	ErrNodeNotFound = errors.New("节点不存在")
	ErrNodeConflict = errors.New("节点名称或端口已存在")
	ErrInvalidNode  = errors.New("无效的节点配置")
)

// SubscriptionRefresher interface for subscription manager.
type SubscriptionRefresher interface {
	RefreshNow() error
	Status() SubscriptionStatus
}

type SourceSyncReporter interface {
	SourceSyncStatus() SourceSyncStatus
}

// RoutingReporter exposes read-only smart-dispatch routing state for the
// management API. Implemented by the routing controller when routing is active.
type RoutingReporter interface {
	RoutingStatus() RoutingStatus
}

// RoutingController is the management surface for the smart dispatch entry. It
// extends the read-only reporter with a hot-apply hook: after the routing
// config is edited and persisted, ApplyHot pushes rule/strategy changes onto
// the running engine without a full sing-box reload. It returns false when the
// change could not be hot-applied (e.g. routing is not currently running, or an
// enable/listen edit needs a full reload), in which case the caller signals
// need_reload to the client.
type RoutingController interface {
	RoutingReporter
	ApplyHot(cfg *config.Config) bool
}

// RoutingStatus is the observability view of the smart dispatch entry.
type RoutingStatus struct {
	Enabled         bool              `json:"enabled"`
	DispatcherReady bool              `json:"dispatcher_ready"`
	SharedEnabled   bool              `json:"shared_enabled"`
	SharedRevision  int64             `json:"shared_revision,omitempty"`
	ProfileScope    string            `json:"profile_scope,omitempty"`
	Listen          string            `json:"listen,omitempty"`
	DefaultStrategy string            `json:"default_strategy,omitempty"`
	FinalPolicy     string            `json:"final_policy,omitempty"`
	RuleCount       int               `json:"rule_count"`
	StickyBuckets   map[string]string `json:"sticky_buckets,omitempty"`  // filter bucket → pinned node tag
	StickySessions  map[string]string `json:"sticky_sessions,omitempty"` // session key → bound node tag
}

// SubscriptionStatus represents subscription refresh status.
type SubscriptionStatus struct {
	Enabled          bool      `json:"enabled"`           // Whether auto-refresh is enabled in config
	HasSubscriptions bool      `json:"has_subscriptions"` // Whether subscription URLs are configured
	LastRefresh      time.Time `json:"last_refresh"`
	NextRefresh      time.Time `json:"next_refresh"`
	NodeCount        int       `json:"node_count"`
	LastError        string    `json:"last_error,omitempty"`
	RefreshCount     int       `json:"refresh_count"`
	IsRefreshing     bool      `json:"is_refreshing"`
	NodesModified    bool      `json:"nodes_modified"` // True if nodes were modified since last refresh
}

// Server exposes HTTP endpoints for monitoring.
type Server struct {
	cfg               Config
	cfgMu             sync.RWMutex   // protects cfgSrc pointer assignment and local cfg fields
	cfgSrc            *config.Config // 可持久化的配置对象; fields protected by cfgSrc.mu
	configUpdateMu    sync.Mutex     // serializes cfgSrc swaps with persisted config edits
	reloadWindowCount int            // nested reload intents that reject persisted edits
	mgr               *Manager
	handler           http.Handler

	lifecycleMu sync.Mutex
	srv         *http.Server
	listener    net.Listener
	listen      string
	shutdown    bool
	watchOnce   sync.Once
	doneOnce    sync.Once
	logger      *log.Logger

	depsMu   sync.RWMutex
	store    store.Store // 数据存储
	profiles *profile.Manager

	// Session management
	sessionMu  sync.RWMutex
	sessions   map[string]*Session
	sessionTTL time.Duration

	// Concurrency control
	probeSem *semaphore.Weighted

	// Lifecycle
	done chan struct{} // closed on Shutdown to stop background goroutines

	subRefresher SubscriptionRefresher
	nodeMgr      NodeManager
	connectorMgr ConnectorManager
	sourceSync   SourceSyncReporter
	routing      RoutingController
	proxyCompat  *proxyCompatState

	// Serializes compatibility checkout selection/store so concurrent callers
	// do not race into the same degraded node before reservations are visible.
	proxyCompatCheckoutMu sync.Mutex
}

var errReloadInProgress = errors.New("configuration update deferred while reload is in progress")

// NewServer constructs the reusable monitor runtime. A disabled config creates
// a dormant server so integrations can be wired before management is enabled.
func NewServer(cfg Config, mgr *Manager, logger *log.Logger) *Server {
	if mgr == nil {
		return nil
	}
	if logger == nil {
		logger = log.Default()
	}

	// Calculate max concurrent probes
	maxConcurrentProbes := int64(runtime.NumCPU() * 4)
	if maxConcurrentProbes < 10 {
		maxConcurrentProbes = 10
	}

	s := &Server{
		cfg:         cfg,
		mgr:         mgr,
		logger:      logger,
		sessions:    make(map[string]*Session),
		sessionTTL:  24 * time.Hour,
		probeSem:    semaphore.NewWeighted(maxConcurrentProbes),
		done:        make(chan struct{}),
		proxyCompat: newProxyCompatState(),
	}

	// Start session cleanup goroutine
	go s.cleanupExpiredSessions()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth", s.handleAuth)
	mux.HandleFunc("/api/settings", s.withAuth(s.handleSettings))
	mux.HandleFunc("/api/nodes", s.withAuth(s.handleNodes))
	mux.HandleFunc("/api/nodes/config", s.withAuth(s.handleConfigNodes))
	mux.HandleFunc("/api/nodes/config/batch-toggle", s.withAuth(s.handleConfigNodesBatchToggle))
	mux.HandleFunc("/api/nodes/config/batch-delete", s.withAuth(s.handleConfigNodesBatchDelete))
	mux.HandleFunc("/api/nodes/config/", s.withAuth(s.handleConfigNodeItem))
	mux.HandleFunc("/api/connectors/config", s.withAuth(s.handleConfigConnectors))
	mux.HandleFunc("/api/connectors/config/", s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/preferred-ips/refresh") {
			s.handleConnectorPreferredIPRefresh(w, r)
			return
		}
		s.handleConfigConnectorItem(w, r)
	}))
	mux.HandleFunc("/api/nodes/probe-all", s.withAuth(s.handleProbeAll))
	mux.HandleFunc("/api/nodes/traffic/stream", s.withAuth(s.handleTrafficStream))
	mux.HandleFunc("/api/nodes/", s.withAuth(s.handleNodeAction))
	mux.HandleFunc("/api/debug", s.withAuth(s.handleDebug))
	mux.HandleFunc("/api/best-proxy", s.withAuth(s.handleBestProxy))
	mux.HandleFunc("/api/export", s.withAuth(s.handleExport))
	mux.HandleFunc("/api/import", s.withAuth(s.handleImport))
	mux.HandleFunc("/api/subscription/status", s.withAuth(s.handleSubscriptionStatus))
	mux.HandleFunc("/api/subscription/refresh", s.withAuth(s.handleSubscriptionRefresh))
	mux.HandleFunc("/api/source-sync/status", s.withAuth(s.handleSourceSyncStatus))
	mux.HandleFunc("/api/source-sync/source-health", s.withAuth(s.handleSourceSyncSourceHealth))
	mux.HandleFunc("/api/routing/status", s.withAuth(s.handleRoutingStatus))
	mux.HandleFunc("/api/routing/config", s.withAuth(s.handleRoutingConfig))
	mux.HandleFunc("/api/reload", s.withAuth(s.handleReload))
	mux.HandleFunc("/proxy/catalog", s.withAuth(s.handleProxyCatalog))
	mux.HandleFunc("/proxy/snapshot", s.withAuth(s.handleProxySnapshot))
	mux.HandleFunc("/proxy/leases/plan", s.withAuth(s.handleProxyPlanCheckout))
	mux.HandleFunc("/proxy/leases/checkout", s.withAuth(s.handleProxyCheckout))
	mux.HandleFunc("/proxy/leases/report", s.withAuth(s.handleProxyReportUsage))
	mux.HandleFunc("/proxy/leases/", s.withAuth(s.handleProxyLeaseItem))
	mux.HandleFunc("/proxy/maintenance/run", s.withAuth(s.handleProxyMaintenanceRun))

	// Default handler for static assets (React App)
	mux.HandleFunc("/", s.handleIndex)
	s.handler = mux
	return s
}

const listenerShutdownTimeout = 2 * time.Second

// ListenerTransition holds a pre-bound listener change. The old listener keeps
// serving until Finalize, which makes activation rollback-safe and prevents a
// reload handler from synchronously shutting down the server handling itself.
type ListenerTransition struct {
	server *Server

	oldServer   *http.Server
	oldListener net.Listener
	oldListen   string

	newServer   *http.Server
	newListener net.Listener
	newListen   string
	enabled     bool
	noChange    bool

	oldConfigSource  *config.Config
	oldRuntimeConfig Config
	targetConfig     *config.Config
	targetRuntime    persistedServerConfig
	hasTargetConfig  bool
	configApplied    bool

	mu               sync.Mutex
	activated        bool
	done             bool
	configUpdateHeld bool
}

func (t *ListenerTransition) releaseConfigUpdateLocked() {
	if t == nil || !t.configUpdateHeld || t.server == nil {
		return
	}
	t.configUpdateHeld = false
	t.server.configUpdateMu.Unlock()
}

// PrepareListener synchronously binds a replacement address without disturbing
// the active listener. Bind failures therefore leave the old endpoint intact.
func (s *Server) PrepareListener(enabled bool, listen string, targetConfigs ...*config.Config) (*ListenerTransition, error) {
	if s == nil {
		return nil, errors.New("monitor server is nil")
	}
	listen = strings.TrimSpace(listen)
	if enabled && listen == "" {
		return nil, errors.New("monitor listen address is empty")
	}
	s.configUpdateMu.Lock()
	var targetConfig *config.Config
	if len(targetConfigs) > 0 {
		targetConfig = targetConfigs[0]
	}
	targetRuntime, hasTargetConfig := snapshotPersistedServerConfig(targetConfig)
	s.cfgMu.RLock()
	oldConfigSource := s.cfgSrc
	oldRuntimeConfig := cloneRuntimeConfig(s.cfg)
	s.cfgMu.RUnlock()

	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.shutdown {
		s.configUpdateMu.Unlock()
		return nil, errors.New("monitor server is shut down")
	}
	transition := &ListenerTransition{
		server:           s,
		oldServer:        s.srv,
		oldListener:      s.listener,
		oldListen:        s.listen,
		enabled:          enabled,
		newListen:        listen,
		oldConfigSource:  oldConfigSource,
		oldRuntimeConfig: oldRuntimeConfig,
		targetConfig:     targetConfig,
		targetRuntime:    targetRuntime,
		hasTargetConfig:  hasTargetConfig,
		configUpdateHeld: true,
	}
	if enabled && s.srv != nil && s.listen == listen {
		transition.noChange = true
		return transition, nil
	}
	if !enabled {
		return transition, nil
	}

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		s.configUpdateMu.Unlock()
		return nil, fmt.Errorf("bind monitor listener %s: %w", listen, err)
	}
	transition.newListener = listener
	transition.newServer = &http.Server{Addr: listen, Handler: s.handler}
	return transition, nil
}

// Activate publishes the prepared listener while deliberately leaving the old
// listener alive. Call Finalize after the surrounding reload commits, or
// Rollback if a later transaction step fails.
func (t *ListenerTransition) Activate(ctx context.Context) error {
	if t == nil || t.server == nil {
		return errors.New("monitor listener transition is nil")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return errors.New("monitor listener transition already completed")
	}
	if t.activated {
		return nil
	}

	s := t.server
	s.lifecycleMu.Lock()
	if s.shutdown {
		s.lifecycleMu.Unlock()
		t.releaseConfigUpdateLocked()
		return errors.New("monitor server is shut down")
	}
	if s.srv != t.oldServer || s.listener != t.oldListener || s.listen != t.oldListen {
		s.lifecycleMu.Unlock()
		t.releaseConfigUpdateLocked()
		return errors.New("monitor listener changed after transition preparation")
	}
	if t.hasTargetConfig {
		s.applyPreparedConfigLocked(t.targetConfig, t.targetRuntime)
		t.configApplied = true
	}
	if !t.noChange {
		if t.enabled {
			s.srv = t.newServer
			s.listener = t.newListener
			s.listen = t.newListen
			s.serveLocked(t.newServer, t.newListener, t.newListen)
		} else {
			s.srv = nil
			s.listener = nil
			s.listen = ""
		}
	}
	s.watchContextLocked(ctx)
	s.lifecycleMu.Unlock()
	t.activated = true
	return nil
}

// Finalize commits the transition and drains the old HTTP server asynchronously
// with a bounded timeout, allowing a request served by the old listener to
// finish after triggering its own reload.
func (t *ListenerTransition) Finalize() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.done {
		t.mu.Unlock()
		return
	}
	if !t.activated {
		listener := t.newListener
		t.done = true
		t.releaseConfigUpdateLocked()
		t.mu.Unlock()
		if listener != nil {
			_ = listener.Close()
		}
		return
	}
	if t.noChange {
		t.done = true
		t.releaseConfigUpdateLocked()
		t.mu.Unlock()
		return
	}
	oldServer := t.oldServer
	newServer := t.newServer
	t.done = true
	t.releaseConfigUpdateLocked()
	t.mu.Unlock()
	if oldServer != nil && oldServer != newServer {
		shutdownHTTPServerAsync(oldServer)
	}
}

// Rollback restores the still-serving old listener and closes the prepared or
// activated candidate listener.
func (t *ListenerTransition) Rollback() error {
	if t == nil || t.server == nil {
		return errors.New("monitor listener transition is nil")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return errors.New("monitor listener transition already completed")
	}

	s := t.server
	if !t.activated {
		t.done = true
		t.releaseConfigUpdateLocked()
		if t.newListener != nil {
			_ = t.newListener.Close()
		}
		return nil
	}
	if t.activated {
		s.lifecycleMu.Lock()
		if !t.noChange {
			currentMatches := false
			if t.enabled {
				currentMatches = s.srv == t.newServer && s.listener == t.newListener && s.listen == t.newListen
			} else {
				currentMatches = s.srv == nil && s.listener == nil && s.listen == ""
			}
			if !currentMatches {
				s.lifecycleMu.Unlock()
				t.releaseConfigUpdateLocked()
				return errors.New("monitor listener changed after transition activation")
			}
			s.srv = t.oldServer
			s.listener = t.oldListener
			s.listen = t.oldListen
		}
		if t.configApplied {
			s.restorePreparedConfigLocked(t.oldConfigSource, t.oldRuntimeConfig)
		}
		s.lifecycleMu.Unlock()
	}
	t.done = true
	t.releaseConfigUpdateLocked()
	if t.newServer != nil && t.newServer != t.oldServer {
		shutdownHTTPServerAsync(t.newServer)
	} else if t.newListener != nil {
		_ = t.newListener.Close()
	}
	return nil
}

// Abort releases a prepared transition. It is safe before or after Activate.
func (t *ListenerTransition) Abort() {
	if t != nil {
		_ = t.Rollback()
	}
}

func (s *Server) serveLocked(server *http.Server, listener net.Listener, listen string) {
	if server == nil || listener == nil {
		return
	}
	s.logger.Printf("Starting monitor server on %s", listen)
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.logger.Printf("❌ Monitor server error: %v", err)
		}
	}()
	s.logger.Printf("✅ Monitor server started on http://%s", listen)
}

func (s *Server) watchContextLocked(ctx context.Context) {
	if ctx == nil || ctx.Done() == nil {
		return
	}
	s.watchOnce.Do(func() {
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), listenerShutdownTimeout)
			defer cancel()
			s.Shutdown(shutdownCtx)
		}()
	})
}

func shutdownHTTPServerAsync(server *http.Server) {
	if server == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), listenerShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
	}()
}

// SetSubscriptionRefresher sets the subscription refresher for API endpoints.
func (s *Server) SetSubscriptionRefresher(sr SubscriptionRefresher) {
	if s != nil {
		s.depsMu.Lock()
		s.subRefresher = sr
		s.depsMu.Unlock()
	}
}

func (s *Server) SetSourceSyncReporter(sr SourceSyncReporter) {
	if s != nil {
		s.depsMu.Lock()
		s.sourceSync = sr
		s.depsMu.Unlock()
	}
}

// SetRoutingController enables the /api/routing/* endpoints (status + config
// read/write). The controller provides both the read-only status and the
// hot-apply hook used after a routing config edit.
func (s *Server) SetRoutingController(rc RoutingController) {
	if s != nil {
		s.depsMu.Lock()
		s.routing = rc
		s.depsMu.Unlock()
	}
}

// SetNodeManager enables config-node CRUD endpoints.
func (s *Server) SetNodeManager(nm NodeManager) {
	if s != nil {
		s.depsMu.Lock()
		s.nodeMgr = nm
		s.depsMu.Unlock()
	}
}

// SetStore sets the data store for session persistence and other operations.
func (s *Server) SetStore(st store.Store) {
	if s != nil {
		s.depsMu.Lock()
		s.store = st
		s.depsMu.Unlock()
	}
}

// SetProfileManager publishes the Local Server profile/auth dependency used by
// management authentication and profile APIs.
func (s *Server) SetProfileManager(manager *profile.Manager) {
	if s == nil {
		return
	}
	s.depsMu.Lock()
	s.profiles = manager
	s.depsMu.Unlock()
}

func (s *Server) profileManagerSnapshot() *profile.Manager {
	if s == nil {
		return nil
	}
	s.depsMu.RLock()
	defer s.depsMu.RUnlock()
	return s.profiles
}

func (s *Server) subscriptionRefresherSnapshot() SubscriptionRefresher {
	if s == nil {
		return nil
	}
	s.depsMu.RLock()
	defer s.depsMu.RUnlock()
	return s.subRefresher
}

func (s *Server) sourceSyncSnapshot() SourceSyncReporter {
	if s == nil {
		return nil
	}
	s.depsMu.RLock()
	defer s.depsMu.RUnlock()
	return s.sourceSync
}

func (s *Server) routingSnapshot() RoutingController {
	if s == nil {
		return nil
	}
	s.depsMu.RLock()
	defer s.depsMu.RUnlock()
	return s.routing
}

func (s *Server) nodeManagerSnapshot() NodeManager {
	if s == nil {
		return nil
	}
	s.depsMu.RLock()
	defer s.depsMu.RUnlock()
	return s.nodeMgr
}

func (s *Server) storeSnapshot() store.Store {
	if s == nil {
		return nil
	}
	s.depsMu.RLock()
	defer s.depsMu.RUnlock()
	return s.store
}

// SetConfig binds the persistable config object for settings API.
func (s *Server) SetConfig(cfg *config.Config) {
	if s == nil {
		return
	}
	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	runtimeCfg, hasConfig := snapshotPersistedServerConfig(cfg)
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfgSrc = cfg
	if hasConfig {
		applyPersistedServerConfig(&s.cfg, runtimeCfg)
	}
}

// BeginReloadWindow rejects persisted edits while a reload intent is capturing
// and committing a target configuration. Windows are nestable because a
// subscription refresh may hold an outer intent while calling ReloadWithPortMap.
func (s *Server) BeginReloadWindow() {
	if s == nil {
		return
	}
	s.configUpdateMu.Lock()
	s.reloadWindowCount++
	s.configUpdateMu.Unlock()
}

// EndReloadWindow releases one nested reload window.
func (s *Server) EndReloadWindow() {
	if s == nil {
		return
	}
	s.configUpdateMu.Lock()
	if s.reloadWindowCount > 0 {
		s.reloadWindowCount--
	}
	s.configUpdateMu.Unlock()
}

type persistedServerConfig struct {
	enabled        bool
	listen         string
	externalIP     string
	probeTarget    string
	probeTargets   []string
	password       string
	proxyUsername  string
	proxyPassword  string
	skipCertVerify bool
}

func snapshotPersistedServerConfig(cfg *config.Config) (persistedServerConfig, bool) {
	if cfg == nil {
		return persistedServerConfig{}, false
	}
	cfg.RLock()
	defer cfg.RUnlock()
	snapshot := persistedServerConfig{
		enabled:        cfg.ManagementEnabled(),
		listen:         cfg.Management.Listen,
		externalIP:     cfg.ExternalIP,
		probeTarget:    cfg.Management.ProbeTarget,
		probeTargets:   append([]string(nil), cfg.Management.ProbeTargets...),
		password:       cfg.Management.Password,
		skipCertVerify: cfg.SkipCertVerify,
	}
	if cfg.Mode == "hybrid" || cfg.Mode == "multi-port" {
		snapshot.proxyUsername = cfg.MultiPort.Username
		snapshot.proxyPassword = cfg.MultiPort.Password
	} else {
		snapshot.proxyUsername = cfg.Listener.Username
		snapshot.proxyPassword = cfg.Listener.Password
	}
	return snapshot, true
}

func applyPersistedServerConfig(dst *Config, snapshot persistedServerConfig) {
	dst.Enabled = snapshot.enabled
	dst.Listen = snapshot.listen
	dst.ExternalIP = snapshot.externalIP
	dst.ProbeTarget = snapshot.probeTarget
	dst.ProbeTargets = append([]string(nil), snapshot.probeTargets...)
	dst.Password = snapshot.password
	dst.ProxyUsername = snapshot.proxyUsername
	dst.ProxyPassword = snapshot.proxyPassword
	dst.SkipCertVerify = snapshot.skipCertVerify
}

func cloneRuntimeConfig(cfg Config) Config {
	cfg.ProbeTargets = append([]string(nil), cfg.ProbeTargets...)
	return cfg
}

func (s *Server) applyPreparedConfig(source *config.Config, snapshot persistedServerConfig) {
	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	s.applyPreparedConfigLocked(source, snapshot)
}

func (s *Server) applyPreparedConfigLocked(source *config.Config, snapshot persistedServerConfig) {
	s.cfgMu.Lock()
	s.cfgSrc = source
	applyPersistedServerConfig(&s.cfg, snapshot)
	s.cfgMu.Unlock()
}

func (s *Server) restorePreparedConfig(source *config.Config, runtimeCfg Config) {
	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	s.restorePreparedConfigLocked(source, runtimeCfg)
}

func (s *Server) restorePreparedConfigLocked(source *config.Config, runtimeCfg Config) {
	s.cfgMu.Lock()
	s.cfgSrc = source
	s.cfg = cloneRuntimeConfig(runtimeCfg)
	s.cfgMu.Unlock()
}

func settingsChangeRequiresReload(c *config.Config, req allSettingsRequest) bool {
	if c == nil {
		return true
	}

	normalizedListenerProtocol := req.ListenerProtocol
	if p, err := config.NormalizeInboundProtocol(req.ListenerProtocol); err == nil {
		normalizedListenerProtocol = p
	}
	normalizedMultiPortProtocol := req.MultiPortProtocol
	if p, err := config.NormalizeInboundProtocol(req.MultiPortProtocol); err == nil {
		normalizedMultiPortProtocol = p
	}
	probeTarget := strings.TrimSpace(req.ManagementProbeTarget)
	sourceManifestURL := strings.TrimSpace(req.SourceSyncManifestURL)
	sourceManifestToken := strings.TrimSpace(req.SourceSyncManifestToken)
	defaultDirectProxyScheme := strings.TrimSpace(req.SourceSyncDefaultDirectProxyScheme)
	if defaultDirectProxyScheme == "" {
		defaultDirectProxyScheme = "http"
	}

	if c.Mode != req.Mode ||
		c.LogLevel != req.LogLevel ||
		c.SkipCertVerify != req.SkipCertVerify ||
		c.Listener.Address != req.ListenerAddress ||
		c.Listener.Port != req.ListenerPort ||
		c.Listener.Protocol != normalizedListenerProtocol ||
		c.Listener.Username != req.ListenerUsername ||
		c.Listener.Password != req.ListenerPassword ||
		c.MultiPort.Address != req.MultiPortAddress ||
		c.MultiPort.BasePort != req.MultiPortBasePort ||
		c.MultiPort.Protocol != normalizedMultiPortProtocol ||
		c.MultiPort.Username != req.MultiPortUsername ||
		c.MultiPort.Password != req.MultiPortPassword ||
		c.Pool.Mode != req.PoolMode ||
		c.Pool.FailureThreshold != req.PoolFailureThreshold ||
		c.Management.Listen != req.ManagementListen ||
		c.Management.Password != req.ManagementPassword ||
		c.SourceSync.Enabled != req.SourceSyncEnabled ||
		c.SourceSync.ManifestURL != sourceManifestURL ||
		c.SourceSync.ManifestToken != sourceManifestToken ||
		c.SourceSync.DefaultDirectProxyScheme != defaultDirectProxyScheme ||
		!reflect.DeepEqual(c.SourceSync.FallbackSubscriptions, req.SourceSyncFallbackSubscriptions) ||
		!reflect.DeepEqual(c.Subscriptions, req.Subscriptions) ||
		c.SubscriptionRefresh.Enabled != req.SubRefreshEnabled ||
		c.SubscriptionRefresh.MinAvailableNodes != req.SubRefreshMinAvailableNodes ||
		c.GeoIP.Enabled != req.GeoIPEnabled ||
		strings.TrimSpace(c.GeoIP.DatabasePath) != strings.TrimSpace(req.GeoIPDatabasePath) ||
		c.GeoIP.AutoUpdateEnabled != req.GeoIPAutoUpdateEnabled {
		return true
	}

	if req.ManagementEnabled != nil {
		currentEnabled := c.ManagementEnabled()
		if currentEnabled != *req.ManagementEnabled {
			return true
		}
	}

	if c.Management.ProbeTarget != probeTarget {
		return true
	}

	if d, err := time.ParseDuration(req.PoolBlacklistDuration); err == nil && d > 0 && c.Pool.BlacklistDuration != d {
		return true
	}
	if d, err := time.ParseDuration(req.SubRefreshDrainTimeout); err == nil && d > 0 && c.SubscriptionRefresh.DrainTimeout != d {
		return true
	}
	if d, err := time.ParseDuration(req.SubRefreshInterval); err == nil && d > 0 && c.SubscriptionRefresh.Interval != d {
		return true
	}
	if d, err := time.ParseDuration(req.SubRefreshTimeout); err == nil && d > 0 && c.SubscriptionRefresh.Timeout != d {
		return true
	}
	if d, err := time.ParseDuration(req.SubRefreshHealthCheckTimeout); err == nil && d > 0 && c.SubscriptionRefresh.HealthCheckTimeout != d {
		return true
	}
	if d, err := time.ParseDuration(req.SourceSyncRefreshInterval); err == nil && d > 0 && c.SourceSync.RefreshInterval != d {
		return true
	}
	if d, err := time.ParseDuration(req.SourceSyncRequestTimeout); err == nil && d > 0 && c.SourceSync.RequestTimeout != d {
		return true
	}
	if d, err := time.ParseDuration(req.GeoIPAutoUpdateInterval); err == nil && d > 0 && c.GeoIP.AutoUpdateInterval != d {
		return true
	}
	return false
}

// allSettingsResponse is the JSON structure for GET /api/settings.
type allSettingsResponse struct {
	// Global
	Mode           string `json:"mode"`
	LogLevel       string `json:"log_level"`
	ExternalIP     string `json:"external_ip"`
	SkipCertVerify bool   `json:"skip_cert_verify"`

	// Listener
	ListenerAddress  string `json:"listener_address"`
	ListenerPort     uint16 `json:"listener_port"`
	ListenerProtocol string `json:"listener_protocol"`
	ListenerUsername string `json:"listener_username"`
	ListenerPassword string `json:"listener_password"`

	// Multi-port
	MultiPortAddress  string `json:"multi_port_address"`
	MultiPortBasePort uint16 `json:"multi_port_base_port"`
	MultiPortProtocol string `json:"multi_port_protocol"`
	MultiPortUsername string `json:"multi_port_username"`
	MultiPortPassword string `json:"multi_port_password"`

	// Pool
	PoolMode              string `json:"pool_mode"`
	PoolFailureThreshold  int    `json:"pool_failure_threshold"`
	PoolBlacklistDuration string `json:"pool_blacklist_duration"`

	// Management
	ManagementEnabled             bool   `json:"management_enabled"`
	ManagementListen              string `json:"management_listen"`
	ManagementProbeTarget         string `json:"management_probe_target"`
	ManagementPassword            string `json:"management_password"`
	ManagementHealthCheckInterval string `json:"management_health_check_interval"`

	// Subscription refresh
	SubRefreshEnabled            bool   `json:"sub_refresh_enabled"`
	SubRefreshInterval           string `json:"sub_refresh_interval"`
	SubRefreshTimeout            string `json:"sub_refresh_timeout"`
	SubRefreshHealthCheckTimeout string `json:"sub_refresh_health_check_timeout"`
	SubRefreshDrainTimeout       string `json:"sub_refresh_drain_timeout"`
	SubRefreshMinAvailableNodes  int    `json:"sub_refresh_min_available_nodes"`

	// Source sync
	SourceSyncEnabled                  bool     `json:"source_sync_enabled"`
	SourceSyncManifestURL              string   `json:"source_sync_manifest_url"`
	SourceSyncManifestToken            string   `json:"source_sync_manifest_token"`
	SourceSyncRefreshInterval          string   `json:"source_sync_refresh_interval"`
	SourceSyncRequestTimeout           string   `json:"source_sync_request_timeout"`
	SourceSyncFallbackSubscriptions    []string `json:"source_sync_fallback_subscriptions"`
	SourceSyncDefaultDirectProxyScheme string   `json:"source_sync_default_direct_proxy_scheme"`

	// GeoIP
	GeoIPEnabled            bool   `json:"geoip_enabled"`
	GeoIPDatabasePath       string `json:"geoip_database_path"`
	GeoIPAutoUpdateEnabled  bool   `json:"geoip_auto_update_enabled"`
	GeoIPAutoUpdateInterval string `json:"geoip_auto_update_interval"`

	// Subscriptions
	Subscriptions []string `json:"subscriptions"`
}

// allSettingsRequest is the JSON structure for PUT /api/settings.
type allSettingsRequest struct {
	// Global
	Mode           string `json:"mode"`
	LogLevel       string `json:"log_level"`
	ExternalIP     string `json:"external_ip"`
	SkipCertVerify bool   `json:"skip_cert_verify"`

	// Listener
	ListenerAddress  string `json:"listener_address"`
	ListenerPort     uint16 `json:"listener_port"`
	ListenerProtocol string `json:"listener_protocol"`
	ListenerUsername string `json:"listener_username"`
	ListenerPassword string `json:"listener_password"`

	// Multi-port
	MultiPortAddress  string `json:"multi_port_address"`
	MultiPortBasePort uint16 `json:"multi_port_base_port"`
	MultiPortProtocol string `json:"multi_port_protocol"`
	MultiPortUsername string `json:"multi_port_username"`
	MultiPortPassword string `json:"multi_port_password"`

	// Pool
	PoolMode              string `json:"pool_mode"`
	PoolFailureThreshold  int    `json:"pool_failure_threshold"`
	PoolBlacklistDuration string `json:"pool_blacklist_duration"`

	// Management
	ManagementEnabled             *bool  `json:"management_enabled"`
	ManagementListen              string `json:"management_listen"`
	ManagementProbeTarget         string `json:"management_probe_target"`
	ManagementPassword            string `json:"management_password"`
	ManagementHealthCheckInterval string `json:"management_health_check_interval"`

	// Subscription refresh
	SubRefreshEnabled            bool   `json:"sub_refresh_enabled"`
	SubRefreshInterval           string `json:"sub_refresh_interval"`
	SubRefreshTimeout            string `json:"sub_refresh_timeout"`
	SubRefreshHealthCheckTimeout string `json:"sub_refresh_health_check_timeout"`
	SubRefreshDrainTimeout       string `json:"sub_refresh_drain_timeout"`
	SubRefreshMinAvailableNodes  int    `json:"sub_refresh_min_available_nodes"`

	// Source sync
	SourceSyncEnabled                  bool     `json:"source_sync_enabled"`
	SourceSyncManifestURL              string   `json:"source_sync_manifest_url"`
	SourceSyncManifestToken            string   `json:"source_sync_manifest_token"`
	SourceSyncRefreshInterval          string   `json:"source_sync_refresh_interval"`
	SourceSyncRequestTimeout           string   `json:"source_sync_request_timeout"`
	SourceSyncFallbackSubscriptions    []string `json:"source_sync_fallback_subscriptions"`
	SourceSyncDefaultDirectProxyScheme string   `json:"source_sync_default_direct_proxy_scheme"`

	// GeoIP
	GeoIPEnabled            bool   `json:"geoip_enabled"`
	GeoIPDatabasePath       string `json:"geoip_database_path"`
	GeoIPAutoUpdateEnabled  bool   `json:"geoip_auto_update_enabled"`
	GeoIPAutoUpdateInterval string `json:"geoip_auto_update_interval"`

	// Subscriptions
	Subscriptions []string `json:"subscriptions"`
}

// getAllSettings reads all config fields into a flat response (thread-safe).
func (s *Server) getAllSettings() allSettingsResponse {
	s.cfgMu.RLock()
	c := s.cfgSrc
	s.cfgMu.RUnlock()

	if c == nil {
		return allSettingsResponse{}
	}

	c.RLock()
	defer c.RUnlock()
	mgmtEnabled := true
	if c.Management.Enabled != nil {
		mgmtEnabled = *c.Management.Enabled
	}

	return allSettingsResponse{
		Mode:           c.Mode,
		LogLevel:       c.LogLevel,
		ExternalIP:     c.ExternalIP,
		SkipCertVerify: c.SkipCertVerify,

		ListenerAddress:  c.Listener.Address,
		ListenerPort:     c.Listener.Port,
		ListenerProtocol: c.Listener.Protocol,
		ListenerUsername: c.Listener.Username,
		ListenerPassword: c.Listener.Password,

		MultiPortAddress:  c.MultiPort.Address,
		MultiPortBasePort: c.MultiPort.BasePort,
		MultiPortProtocol: c.MultiPort.Protocol,
		MultiPortUsername: c.MultiPort.Username,
		MultiPortPassword: c.MultiPort.Password,

		PoolMode:              c.Pool.Mode,
		PoolFailureThreshold:  c.Pool.FailureThreshold,
		PoolBlacklistDuration: c.Pool.BlacklistDuration.String(),

		ManagementEnabled:             mgmtEnabled,
		ManagementListen:              c.Management.Listen,
		ManagementProbeTarget:         c.Management.ProbeTarget,
		ManagementPassword:            c.Management.Password,
		ManagementHealthCheckInterval: c.Management.HealthCheckInterval.String(),

		SubRefreshEnabled:            c.SubscriptionRefresh.Enabled,
		SubRefreshInterval:           c.SubscriptionRefresh.Interval.String(),
		SubRefreshTimeout:            c.SubscriptionRefresh.Timeout.String(),
		SubRefreshHealthCheckTimeout: c.SubscriptionRefresh.HealthCheckTimeout.String(),
		SubRefreshDrainTimeout:       c.SubscriptionRefresh.DrainTimeout.String(),
		SubRefreshMinAvailableNodes:  c.SubscriptionRefresh.MinAvailableNodes,

		SourceSyncEnabled:                  c.SourceSync.Enabled,
		SourceSyncManifestURL:              c.SourceSync.ManifestURL,
		SourceSyncManifestToken:            c.SourceSync.ManifestToken,
		SourceSyncRefreshInterval:          c.SourceSync.RefreshInterval.String(),
		SourceSyncRequestTimeout:           c.SourceSync.RequestTimeout.String(),
		SourceSyncFallbackSubscriptions:    c.SourceSync.FallbackSubscriptions,
		SourceSyncDefaultDirectProxyScheme: c.SourceSync.DefaultDirectProxyScheme,

		GeoIPEnabled:            c.GeoIP.Enabled,
		GeoIPDatabasePath:       c.GeoIP.DatabasePath,
		GeoIPAutoUpdateEnabled:  c.GeoIP.AutoUpdateEnabled,
		GeoIPAutoUpdateInterval: c.GeoIP.AutoUpdateInterval.String(),

		Subscriptions: c.Subscriptions,
	}
}

func applyAllSettingsRequest(c *config.Config, req allSettingsRequest) {
	if c == nil {
		return
	}
	c.Mode = req.Mode
	c.LogLevel = req.LogLevel
	c.ExternalIP = strings.TrimSpace(req.ExternalIP)
	c.SkipCertVerify = req.SkipCertVerify

	c.Listener.Address = req.ListenerAddress
	c.Listener.Port = req.ListenerPort
	if protocol, err := config.NormalizeInboundProtocol(req.ListenerProtocol); err == nil {
		c.Listener.Protocol = protocol
	}
	c.Listener.Username = req.ListenerUsername
	c.Listener.Password = req.ListenerPassword

	c.MultiPort.Address = req.MultiPortAddress
	c.MultiPort.BasePort = req.MultiPortBasePort
	if protocol, err := config.NormalizeInboundProtocol(req.MultiPortProtocol); err == nil {
		c.MultiPort.Protocol = protocol
	}
	c.MultiPort.Username = req.MultiPortUsername
	c.MultiPort.Password = req.MultiPortPassword

	c.Pool.Mode = req.PoolMode
	c.Pool.FailureThreshold = req.PoolFailureThreshold
	if duration, err := time.ParseDuration(req.PoolBlacklistDuration); err == nil && duration > 0 {
		c.Pool.BlacklistDuration = duration
	}

	if req.ManagementEnabled != nil {
		enabled := *req.ManagementEnabled
		c.Management.Enabled = &enabled
	}
	c.Management.Listen = req.ManagementListen
	c.Management.ProbeTarget = strings.TrimSpace(req.ManagementProbeTarget)
	if c.Management.ProbeTarget != "" {
		c.Management.ProbeTargets = nil
	}
	c.Management.Password = req.ManagementPassword
	if duration, err := time.ParseDuration(req.ManagementHealthCheckInterval); err == nil && duration > 0 {
		c.Management.HealthCheckInterval = duration
	}

	c.SubscriptionRefresh.Enabled = req.SubRefreshEnabled
	if duration, err := time.ParseDuration(req.SubRefreshInterval); err == nil && duration > 0 {
		c.SubscriptionRefresh.Interval = duration
	}
	if duration, err := time.ParseDuration(req.SubRefreshTimeout); err == nil && duration > 0 {
		c.SubscriptionRefresh.Timeout = duration
	}
	if duration, err := time.ParseDuration(req.SubRefreshHealthCheckTimeout); err == nil && duration > 0 {
		c.SubscriptionRefresh.HealthCheckTimeout = duration
	}
	if duration, err := time.ParseDuration(req.SubRefreshDrainTimeout); err == nil && duration > 0 {
		c.SubscriptionRefresh.DrainTimeout = duration
	}
	c.SubscriptionRefresh.MinAvailableNodes = req.SubRefreshMinAvailableNodes

	c.SourceSync.Enabled = req.SourceSyncEnabled
	c.SourceSync.ManifestURL = strings.TrimSpace(req.SourceSyncManifestURL)
	c.SourceSync.ManifestToken = strings.TrimSpace(req.SourceSyncManifestToken)
	if duration, err := time.ParseDuration(req.SourceSyncRefreshInterval); err == nil && duration > 0 {
		c.SourceSync.RefreshInterval = duration
	}
	if duration, err := time.ParseDuration(req.SourceSyncRequestTimeout); err == nil && duration > 0 {
		c.SourceSync.RequestTimeout = duration
	}
	c.SourceSync.FallbackSubscriptions = append([]string(nil), req.SourceSyncFallbackSubscriptions...)
	c.SourceSync.DefaultDirectProxyScheme = strings.TrimSpace(req.SourceSyncDefaultDirectProxyScheme)
	if c.SourceSync.DefaultDirectProxyScheme == "" {
		c.SourceSync.DefaultDirectProxyScheme = "http"
	}

	c.GeoIP.Enabled = req.GeoIPEnabled
	c.GeoIP.DatabasePath = req.GeoIPDatabasePath
	c.GeoIP.AutoUpdateEnabled = req.GeoIPAutoUpdateEnabled
	if duration, err := time.ParseDuration(req.GeoIPAutoUpdateInterval); err == nil && duration > 0 {
		c.GeoIP.AutoUpdateInterval = duration
	}
	c.Subscriptions = append([]string(nil), req.Subscriptions...)
}

// commitAllSettings copies only fields owned by PUT /api/settings. The target
// config remains the object shared by BoxManager and the monitor server.
func commitAllSettings(target, candidate *config.Config) {
	if target == nil || candidate == nil {
		return
	}
	target.Mode = candidate.Mode
	target.LogLevel = candidate.LogLevel
	target.ExternalIP = candidate.ExternalIP
	target.SkipCertVerify = candidate.SkipCertVerify
	target.Listener = candidate.Listener
	target.MultiPort = candidate.MultiPort
	target.Pool = candidate.Pool
	target.Management = candidate.Management
	target.SubscriptionRefresh = candidate.SubscriptionRefresh
	target.SourceSync = candidate.SourceSync
	target.GeoIP = candidate.GeoIP
	target.Subscriptions = candidate.Subscriptions
}

// updateAllSettings applies all settings from request and persists to config file.
func (s *Server) updateAllSettings(req allSettingsRequest) error {
	_, err := s.updateAllSettingsWithReload(req)
	return err
}

func (s *Server) updateAllSettingsWithReload(req allSettingsRequest) (bool, error) {
	// Validate request before applying
	if err := config.ValidateSettingsRequest(
		req.Mode, req.ListenerPort, req.MultiPortBasePort,
		req.ListenerProtocol, req.MultiPortProtocol, req.PoolMode,
		req.PoolBlacklistDuration, req.SubRefreshInterval, req.SubRefreshTimeout,
		req.SubRefreshHealthCheckTimeout, req.SubRefreshDrainTimeout,
		req.SourceSyncRefreshInterval, req.SourceSyncRequestTimeout,
		req.GeoIPAutoUpdateInterval, req.ManagementHealthCheckInterval,
	); err != nil {
		return false, fmt.Errorf("参数验证失败: %w", err)
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	s.cfgMu.RLock()
	c := s.cfgSrc
	s.cfgMu.RUnlock()

	if c == nil {
		return false, errors.New("配置存储未初始化")
	}
	if s.reloadWindowCount > 0 {
		return false, errReloadInProgress
	}

	// Build and persist an isolated candidate before changing the live config.
	// SaveSettings can fail (read-only/missing path); committing only afterward
	// keeps the in-memory source, monitor runtime, and YAML in one state.
	c.Lock()
	needReload := settingsChangeRequiresReload(c, req)
	candidate := c.Clone()
	applyAllSettingsRequest(candidate, req)
	candidate.Lock()
	err := candidate.SaveSettings()
	candidate.Unlock()
	if err != nil {
		c.Unlock()
		return false, fmt.Errorf("保存配置失败: %w", err)
	}
	commitAllSettings(c, candidate)
	c.Unlock()

	// Sync ALL monitor-level config fields only after persistence succeeds.
	runtimeCfg, _ := snapshotPersistedServerConfig(candidate)
	s.cfgMu.Lock()
	applyPersistedServerConfig(&s.cfg, runtimeCfg)
	s.cfgMu.Unlock()

	// 动态更新 Manager 的探测目标，使其立即生效
	if (candidate.Management.ProbeTarget != "" || len(candidate.Management.ProbeTargets) > 0) && s.mgr != nil {
		if err := s.mgr.UpdateProbeTargets(candidate.Management.ProbeTargets, candidate.Management.ProbeTarget); err != nil {
			s.logger.Printf("更新探测目标失败: %v", err)
		}
	}
	if s.mgr != nil {
		s.mgr.SetSkipCertVerify(candidate.SkipCertVerify)
	}
	// 动态更新周期健康检查间隔，使其立即生效
	if candidate.Management.HealthCheckInterval > 0 && s.mgr != nil {
		s.mgr.SetHealthCheckInterval(candidate.Management.HealthCheckInterval)
	}

	s.logger.Printf("✅ 设置已保存并同步到运行时")
	return needReload, nil
}

// Start synchronously binds and activates the configured HTTP listener.
func (s *Server) Start(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.cfgMu.RLock()
	enabled := s.cfg.Enabled
	listen := s.cfg.Listen
	s.cfgMu.RUnlock()
	if !enabled {
		return nil
	}
	transition, err := s.PrepareListener(true, listen)
	if err != nil {
		return err
	}
	if err := transition.Activate(ctx); err != nil {
		transition.Abort()
		return err
	}
	transition.Finalize()
	return nil
}

// Shutdown stops the server gracefully.
func (s *Server) Shutdown(ctx context.Context) {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	if s.shutdown {
		s.lifecycleMu.Unlock()
		return
	}
	s.shutdown = true
	server := s.srv
	s.srv = nil
	s.listener = nil
	s.listen = ""
	s.doneOnce.Do(func() { close(s.done) })
	s.lifecycleMu.Unlock()
	if server == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, listenerShutdownTimeout)
		defer cancel()
	}
	if err := server.Shutdown(ctx); err != nil {
		_ = server.Close()
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// First check if this is an API request that wasn't matched
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Try to serve static file
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		// Clean the path to avoid directory traversal
		cleanPath := "assets" + r.URL.Path
		_, err := embeddedFS.Open(cleanPath)
		if err == nil {
			// If file exists, serve it
			r.URL.Path = cleanPath // rewrite path for FileServer
			http.FileServer(http.FS(embeddedFS)).ServeHTTP(w, r)
			return
		}
	}

	// For root or non-existent files (SPA routing), serve index.html
	data, err := embeddedFS.ReadFile("assets/index.html")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func isTruthyQueryValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func isEffectiveSnapshot(snap Snapshot) bool {
	effective, _, _ := effectiveAvailabilityDetails(snap)
	return effective
}

func filterEffectiveSnapshots(nodes []Snapshot) []Snapshot {
	filtered := make([]Snapshot, 0, len(nodes))
	for _, snap := range nodes {
		if isEffectiveSnapshot(snap) {
			filtered = append(filtered, snap)
		}
	}
	return filtered
}

func preferEffectiveSnapshots(nodes []Snapshot) []Snapshot {
	reordered := append([]Snapshot(nil), nodes...)
	sort.SliceStable(reordered, func(i, j int) bool {
		leftEffective := isEffectiveSnapshot(reordered[i])
		rightEffective := isEffectiveSnapshot(reordered[j])
		if leftEffective == rightEffective {
			return false
		}
		return leftEffective && !rightEffective
	})
	return reordered
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	onlyAvailable := isTruthyQueryValue(r.URL.Query().Get("only_available")) ||
		isTruthyQueryValue(r.URL.Query().Get("available_only"))
	preferAvailable := onlyAvailable || isTruthyQueryValue(r.URL.Query().Get("prefer_available"))
	if (onlyAvailable || preferAvailable) && s.mgr != nil {
		if err := s.mgr.WaitForInitialProbe(0); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			writeJSON(w, map[string]any{
				"error":   "INITIAL_PROXY_PROBE_PENDING",
				"message": err.Error(),
			})
			return
		}
	}

	allNodes := s.mgr.Snapshot()
	availableNodes := filterEffectiveSnapshots(allNodes)
	probeAvailableNodes := 0
	trafficProvenNodes := 0
	for _, snap := range allNodes {
		if snap.InitialCheckDone && snap.Available && !snap.Blacklisted {
			probeAvailableNodes++
		}
		if snap.TrafficProvenUsable {
			trafficProvenNodes++
		}
	}
	nodes := allNodes
	if onlyAvailable {
		nodes = availableNodes
	} else if preferAvailable {
		nodes = preferEffectiveSnapshots(allNodes)
	}

	totalNodes := len(nodes)

	// Calculate region statistics and traffic totals
	regionStats := make(map[string]int)
	regionHealthy := make(map[string]int)
	for _, snap := range nodes {
		region := snap.Region
		if region == "" {
			region = "other"
		}
		regionStats[region]++
		if isEffectiveSnapshot(snap) {
			regionHealthy[region]++
		}
	}

	traffic := s.mgr.TrafficSummary(false)

	payload := map[string]any{
		"nodes":                 nodes,
		"total_nodes":           totalNodes,
		"all_total_nodes":       len(allNodes),
		"available_nodes":       len(availableNodes),
		"probe_available_nodes": probeAvailableNodes,
		"traffic_proven_nodes":  trafficProvenNodes,
		"total_upload":          traffic.TotalUpload,
		"total_download":        traffic.TotalDownload,
		"upload_speed":          traffic.UploadSpeed,
		"download_speed":        traffic.DownloadSpeed,
		"traffic_sampled":       traffic.SampledAt,
		"region_stats":          regionStats,
		"region_healthy":        regionHealthy,
		"only_available":        onlyAvailable,
		"prefer_available":      preferAvailable,
	}
	writeJSON(w, payload)
}

func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	snapshots := s.mgr.Snapshot()
	var totalCalls, totalSuccess int64
	debugNodes := make([]map[string]any, 0, len(snapshots))
	for _, snap := range snapshots {
		totalCalls += snap.SuccessCount + int64(snap.FailureCount)
		totalSuccess += snap.SuccessCount
		debugNodes = append(debugNodes, map[string]any{
			"tag":                   snap.Tag,
			"name":                  snap.Name,
			"mode":                  snap.Mode,
			"port":                  snap.Port,
			"source_kind":           snap.SourceKind,
			"source_name":           snap.SourceName,
			"source_ref":            snap.SourceRef,
			"availability_score":    snap.AvailabilityScore,
			"failure_count":         snap.FailureCount,
			"reported_success":      snap.ReportedSuccessCount,
			"reported_failure":      snap.ReportedFailureCount,
			"success_count":         snap.SuccessCount,
			"active_connections":    snap.ActiveConnections,
			"initial_check_done":    snap.InitialCheckDone,
			"available":             snap.Available,
			"effective_available":   snap.EffectiveAvailable,
			"traffic_proven_usable": snap.TrafficProvenUsable,
			"availability_source":   snap.AvailabilitySource,
			"last_latency_ms":       snap.LastLatencyMs,
			"last_success":          snap.LastSuccess,
			"last_failure":          snap.LastFailure,
			"last_error":            snap.LastError,
			"blacklisted":           snap.Blacklisted,
			"total_upload":          snap.TotalUpload,
			"total_download":        snap.TotalDownload,
			"timeline":              snap.Timeline,
		})
	}
	var successRate float64
	if totalCalls > 0 {
		successRate = float64(totalSuccess) / float64(totalCalls) * 100
	}
	writeJSON(w, map[string]any{
		"nodes":         debugNodes,
		"total_calls":   totalCalls,
		"total_success": totalSuccess,
		"success_rate":  successRate,
	})
}

func (s *Server) handleBestProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	topN := 1
	if v := r.URL.Query().Get("top"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			topN = n
		}
	}

	allNodes := s.mgr.Snapshot()
	available := filterEffectiveSnapshots(allNodes)

	if len(available) == 0 {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"error": "no available proxy nodes"})
		return
	}

	// Rank: AvailabilityScore desc → LastLatencyMs asc → ActiveConnections asc
	sort.SliceStable(available, func(i, j int) bool {
		if available[i].AvailabilityScore != available[j].AvailabilityScore {
			return available[i].AvailabilityScore > available[j].AvailabilityScore
		}
		li, lj := available[i].LastLatencyMs, available[j].LastLatencyMs
		if li <= 0 {
			li = math.MaxInt64
		}
		if lj <= 0 {
			lj = math.MaxInt64
		}
		if li != lj {
			return li < lj
		}
		return available[i].ActiveConnections < available[j].ActiveConnections
	})

	if topN > len(available) {
		topN = len(available)
	}

	// Build proxy URL prefix from multi-port config.
	proxyHost := "0.0.0.0"
	proxyProto := "http"
	s.cfgMu.RLock()
	c := s.cfgSrc
	s.cfgMu.RUnlock()
	if c != nil {
		c.RLock()
		if c.MultiPort.Address != "" {
			proxyHost = c.MultiPort.Address
		}
		if c.MultiPort.Protocol != "" {
			proxyProto = c.MultiPort.Protocol
		}
		c.RUnlock()
	}

	nodes := make([]map[string]any, 0, topN)
	for _, snap := range available[:topN] {
		nodes = append(nodes, map[string]any{
			"name":               snap.Name,
			"tag":                snap.Tag,
			"proxy_url":          fmt.Sprintf("%s://%s:%d", proxyProto, proxyHost, snap.Port),
			"port":               snap.Port,
			"availability_score": snap.AvailabilityScore,
			"last_latency_ms":    snap.LastLatencyMs,
			"active_connections": snap.ActiveConnections,
			"region":             snap.Region,
		})
	}

	writeJSON(w, map[string]any{
		"nodes":           nodes,
		"total_available": len(available),
	})
}

func (s *Server) handleNodeAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/nodes/"), "/")
	if len(parts) < 1 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	tag := parts[0]
	if tag == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch action {
	case "probe":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		latency, err := s.mgr.Probe(ctx, tag)
		if err != nil {
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		latencyMs := latency.Milliseconds()
		if latencyMs == 0 && latency > 0 {
			latencyMs = 1 // Round up sub-millisecond latencies to 1ms
		}
		writeJSON(w, map[string]any{"message": "探测成功", "latency_ms": latencyMs})
	case "release":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := s.mgr.Release(tag); err != nil {
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"message": "已解除拉黑"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// handleProbeAll probes all nodes in batches and returns results via SSE
func (s *Server) handleProbeAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Get all nodes
	snapshots := s.mgr.Snapshot()
	total := len(snapshots)
	if total == 0 {
		fmt.Fprintf(w, "data: %s\n\n", `{"type":"complete","total":0,"success":0,"failed":0}`)
		flusher.Flush()
		return
	}

	// Send start event
	fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(`{"type":"start","total":%d}`, total))
	flusher.Flush()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	// Probe all nodes with semaphore control
	type probeResult struct {
		tag     string
		name    string
		latency int64
		err     string
	}
	results := make(chan probeResult, total)
	var wg sync.WaitGroup

	// Launch probes with semaphore control
	for _, snap := range snapshots {
		wg.Add(1)
		go func(snap Snapshot) {
			defer wg.Done()

			// Acquire semaphore permit
			if err := s.probeSem.Acquire(ctx, 1); err != nil {
				results <- probeResult{
					tag:  snap.Tag,
					name: snap.Name,
					err:  "probe cancelled: " + err.Error(),
				}
				return
			}
			defer s.probeSem.Release(1)

			// Execute probe
			probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
			defer probeCancel()

			latency, err := s.mgr.Probe(probeCtx, snap.Tag)
			if err != nil {
				results <- probeResult{
					tag:     snap.Tag,
					name:    snap.Name,
					latency: -1,
					err:     err.Error(),
				}
			} else {
				results <- probeResult{
					tag:     snap.Tag,
					name:    snap.Name,
					latency: latency.Milliseconds(),
					err:     "",
				}
			}
		}(snap)
	}

	// Wait for all probes to complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	successCount := 0
	failedCount := 0
	count := 0

	for result := range results {
		count++
		if result.err != "" {
			failedCount++
		} else {
			successCount++
		}

		progress := float64(count) / float64(total) * 100
		status := "success"
		if result.err != "" {
			status = "error"
		}

		eventPayload := map[string]any{
			"type":     "progress",
			"tag":      result.tag,
			"name":     result.name,
			"latency":  result.latency,
			"status":   status,
			"error":    result.err,
			"current":  count,
			"total":    total,
			"progress": math.Round(progress*10) / 10,
		}
		eventData, _ := json.Marshal(eventPayload)
		fmt.Fprintf(w, "data: %s\n\n", eventData)
		flusher.Flush()
	}

	// Send complete event
	fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(`{"type":"complete","total":%d,"success":%d,"failed":%d}`, total, successCount, failedCount))
	flusher.Flush()
}

func (s *Server) handleTrafficStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	send := func(payload map[string]any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Initial snapshot
	initial := s.mgr.TrafficSummary(true)
	if !send(map[string]any{
		"type":           "traffic",
		"node_count":     initial.NodeCount,
		"total_upload":   initial.TotalUpload,
		"total_download": initial.TotalDownload,
		"upload_speed":   initial.UploadSpeed,
		"download_speed": initial.DownloadSpeed,
		"sampled_at":     initial.SampledAt,
		"nodes":          initial.Nodes,
	}) {
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		case <-ticker.C:
			summary := s.mgr.TrafficSummary(true)
			ok := send(map[string]any{
				"type":           "traffic",
				"node_count":     summary.NodeCount,
				"total_upload":   summary.TotalUpload,
				"total_download": summary.TotalDownload,
				"upload_speed":   summary.UploadSpeed,
				"download_speed": summary.DownloadSpeed,
				"sampled_at":     summary.SampledAt,
				"nodes":          summary.Nodes,
			})
			if !ok {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

// withAuth 认证中间件，如果配置了密码则需要验证
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		password := s.managementPassword()
		// 如果没有配置密码，直接放行
		if password == "" {
			next(w, r)
			return
		}

		// 检查 Cookie 中的 session token
		cookie, err := r.Cookie("session_token")
		if err == nil && s.validateSession(cookie.Value) {
			next(w, r)
			return
		}

		// 检查 Authorization header:
		// 1. Bearer session token (WebUI login flow)
		// 2. Raw management password / Bearer management password (service-to-service flow)
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		if authHeader != "" {
			if token, ok := bearerTokenFromHeader(authHeader); ok && s.validateSession(token) {
				next(w, r)
				return
			}
			if s.validateManagementPassword(authHeader) {
				next(w, r)
				return
			}
		}

		// 未授权
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"error": "未授权，请先登录"})
	}
}

func bearerTokenFromHeader(authHeader string) (string, bool) {
	const prefix = "Bearer "
	if len(authHeader) < len(prefix) || !strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(authHeader[len(prefix):]), true
}

func (s *Server) validateManagementPassword(authHeader string) bool {
	password := s.managementPassword()
	if password == "" {
		return false
	}

	if secureCompareStrings(authHeader, password) {
		return true
	}

	token, ok := bearerTokenFromHeader(authHeader)
	if !ok {
		return false
	}

	return secureCompareStrings(token, password)
}

func (s *Server) managementPassword() string {
	if s == nil {
		return ""
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.Password
}

func (s *Server) runtimeConfig() Config {
	if s == nil {
		return Config{}
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	cfg := s.cfg
	cfg.ProbeTargets = append([]string(nil), s.cfg.ProbeTargets...)
	return cfg
}

// handleAuth 处理登录认证
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	password := s.managementPassword()
	// 如果没有配置密码，直接返回成功（不需要token）
	if password == "" {
		writeJSON(w, map[string]any{"message": "无需密码", "no_password": true})
		return
	}

	// GET 请求用于检查是否需要密码（供前端初始化时使用）
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"message": "需要密码", "no_password": false})
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "请求格式错误"})
		return
	}

	// 使用 constant-time 比较防止时序攻击
	if !secureCompareStrings(req.Password, password) {
		// 添加随机延迟防止暴力破解
		time.Sleep(time.Duration(100+mathrand.Intn(200)) * time.Millisecond)
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"error": "密码错误"})
		return
	}

	// 创建新会话
	session, err := s.createSession()
	if err != nil {
		s.logger.Printf("Failed to create session: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"error": "服务器错误"})
		return
	}

	// 设置 HttpOnly Cookie
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    session.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.sessionTTL.Seconds()),
	})

	writeJSON(w, map[string]any{
		"message": "登录成功",
		"token":   session.Token,
	})
}

// handleExport 导出所有当前有效可用节点的原始代理 URI（如 trojan://、vless:// 等），每行一个。
// “有效可用”包括探测可用节点，以及最近被真实流量证明可用的节点。
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// 只导出当前有效可用的节点
	snapshots := s.mgr.SnapshotFiltered(true)
	var lines []string

	for _, snap := range snapshots {
		// 导出节点的原始 URI
		if snap.URI == "" {
			continue
		}
		lines = append(lines, snap.URI)
	}

	// 返回纯文本，每行一个 URI
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=nodes_export.txt")
	_, _ = w.Write([]byte(strings.Join(lines, "\n")))
}

// handleImport 导入节点 URI 列表（每行一个），支持与导出格式互通
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	var req struct {
		Content string `json:"content"` // 节点 URI 文本，每行一个
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 10<<20)).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "请求格式错误"})
		return
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "导入内容为空"})
		return
	}

	// 解析每行 URI
	lines := strings.Split(content, "\n")
	var imported int
	var errs []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 验证是否为合法代理 URI
		if !config.IsProxyURI(line) {
			errs = append(errs, fmt.Sprintf("无效的代理 URI: %s", truncateStr(line, 60)))
			continue
		}

		// 从 URI 中提取名称
		name := ""
		if parsed, err := url.Parse(line); err == nil && parsed.Fragment != "" {
			if decoded, decErr := url.QueryUnescape(parsed.Fragment); decErr == nil {
				name = decoded
			} else {
				name = parsed.Fragment
			}
		}
		if name == "" {
			name = fmt.Sprintf("imported-%d", imported+1)
		}

		node := config.NodeConfig{
			Name: name,
			URI:  line,
		}

		if _, err := nodeManager.CreateNode(r.Context(), node); err != nil {
			errs = append(errs, fmt.Sprintf("添加节点 %q 失败: %v", name, err))
			continue
		}
		imported++
	}

	result := map[string]any{
		"message":  fmt.Sprintf("成功导入 %d 个节点", imported),
		"imported": imported,
	}
	if len(errs) > 0 {
		result["errors"] = errs
	}
	writeJSON(w, result)
}

// truncateStr truncates a string to maxLen and appends "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// handleSettings handles GET/PUT for all system settings.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		resp := s.getAllSettings()
		writeJSON(w, resp)
	case http.MethodPut:
		var req allSettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}

		needReload, err := s.updateAllSettingsWithReload(req)
		if err != nil {
			if errors.Is(err, errReloadInProgress) {
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]any{"error": "配置正在重载，请稍后重试", "need_reload": true})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}

		writeJSON(w, map[string]any{
			"message":     "设置已保存",
			"need_reload": needReload,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleSubscriptionStatus returns the current subscription refresh status.
// Works even when subRefresher is nil by reading config directly.
func (s *Server) handleSubscriptionStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	subRefresher := s.subscriptionRefresherSnapshot()
	if subRefresher == nil {
		// No subscription manager — read config directly to provide accurate status
		s.cfgMu.RLock()
		c := s.cfgSrc
		s.cfgMu.RUnlock()

		resp := map[string]any{
			"enabled":           false,
			"has_subscriptions": false,
			"message":           "订阅管理器未初始化",
		}
		if c != nil {
			c.RLock()
			resp["enabled"] = c.SubscriptionRefresh.Enabled
			resp["has_subscriptions"] = len(c.Subscriptions) > 0
			c.RUnlock()
		}
		writeJSON(w, resp)
		return
	}

	status := subRefresher.Status()
	writeJSON(w, map[string]any{
		"enabled":           status.Enabled,
		"has_subscriptions": status.HasSubscriptions,
		"last_refresh":      status.LastRefresh,
		"next_refresh":      status.NextRefresh,
		"node_count":        status.NodeCount,
		"last_error":        status.LastError,
		"refresh_count":     status.RefreshCount,
		"is_refreshing":     status.IsRefreshing,
	})
}

// handleSubscriptionRefresh triggers an immediate subscription refresh.
func (s *Server) handleSubscriptionRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	subRefresher := s.subscriptionRefresherSnapshot()
	if subRefresher == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"error": "订阅管理器未初始化，请重启程序"})
		return
	}

	if err := subRefresher.RefreshNow(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}

	status := subRefresher.Status()
	writeJSON(w, map[string]any{
		"message":    "刷新成功",
		"node_count": status.NodeCount,
	})
}

func (s *Server) handleSourceSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sourceSync := s.sourceSyncSnapshot()
	if sourceSync == nil {
		writeJSON(w, SourceSyncStatus{})
		return
	}

	writeJSON(w, sourceSync.SourceSyncStatus())
}

func (s *Server) handleRoutingStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	routing := s.routingSnapshot()
	if routing == nil {
		writeJSON(w, RoutingStatus{Enabled: false})
		return
	}
	writeJSON(w, routing.RoutingStatus())
}

// routingConfigPayload is the editable smart-routing configuration exchanged by
// GET/PUT /api/routing/config. Durations are expressed as Go duration strings
// (e.g. "2h", "10m") for human-friendly editing in the UI.
type routingConfigPayload struct {
	Enabled            bool                    `json:"enabled"`
	Listen             string                  `json:"listen"`
	DefaultStrategy    string                  `json:"default_strategy"`
	UseDefaultRules    bool                    `json:"use_default_rules"`
	FinalPolicy        string                  `json:"final_policy"`
	Rules              []string                `json:"rules"`
	RuleProviders      []routingProviderConfig `json:"rule_providers"`
	LongLivedMinUptime string                  `json:"long_lived_min_uptime"`
	LongLivedMinRate   float64                 `json:"long_lived_min_success_rate"`
	SessionTTL         string                  `json:"session_ttl"`
}

type routingProviderConfig struct {
	URL      string `json:"url"`
	Policy   string `json:"policy"`
	Behavior string `json:"behavior"`
	Interval string `json:"interval"`
}

// handleRoutingConfig reads (GET) or updates (PUT) the smart-routing config.
//
// On PUT the config is validated, persisted to YAML, and then applied with the
// cheapest sufficient mechanism: rule/strategy/final-policy edits are hot-applied
// to the running engine; enable/disable or listen-address edits set need_reload
// so the client can trigger a full reload (which rebinds the entry port and
// rebuilds sing-box).
func (s *Server) handleRoutingConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.getRoutingConfig())
	case http.MethodPut:
		var req routingConfigPayload
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "无效的请求体: " + err.Error()})
			return
		}
		needReload, err := s.updateRoutingConfig(req)
		if err != nil {
			if errors.Is(err, errReloadInProgress) {
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]any{"error": "配置正在重载，请稍后重试", "need_reload": true})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{
			"message":     "分流配置已保存",
			"need_reload": needReload,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// getRoutingConfig snapshots the current routing config into the editable payload.
func (s *Server) getRoutingConfig() routingConfigPayload {
	s.cfgMu.RLock()
	c := s.cfgSrc
	s.cfgMu.RUnlock()
	if c == nil {
		return routingConfigPayload{}
	}
	c.RLock()
	defer c.RUnlock()

	providers := make([]routingProviderConfig, 0, len(c.Routing.RuleProviders))
	for _, p := range c.Routing.RuleProviders {
		providers = append(providers, routingProviderConfig{
			URL:      p.URL,
			Policy:   p.Policy,
			Behavior: p.Behavior,
			Interval: p.Interval.String(),
		})
	}
	return routingConfigPayload{
		Enabled:            c.Routing.Enabled,
		Listen:             c.Routing.Listen,
		DefaultStrategy:    c.Routing.DefaultStrategy,
		UseDefaultRules:    c.RoutingUseDefaultRules(),
		FinalPolicy:        c.Routing.FinalPolicy,
		Rules:              append([]string(nil), c.Routing.Rules...),
		RuleProviders:      providers,
		LongLivedMinUptime: c.Routing.LongLived.MinUptime.String(),
		LongLivedMinRate:   c.Routing.LongLived.MinSuccessRate,
		SessionTTL:         c.Routing.Session.TTL.String(),
	}
}

// updateRoutingConfig validates, persists, and applies a routing config edit.
// Returns whether a full reload is required for the change to fully take effect.
func (s *Server) updateRoutingConfig(req routingConfigPayload) (bool, error) {
	// Parse/validate durations up front so a bad value fails cleanly before we
	// mutate anything.
	var llUptime, sessionTTL time.Duration
	if v := strings.TrimSpace(req.LongLivedMinUptime); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return false, fmt.Errorf("长效判定时长无效: %q", req.LongLivedMinUptime)
		}
		llUptime = d
	}
	if v := strings.TrimSpace(req.SessionTTL); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return false, fmt.Errorf("会话 TTL 无效: %q", req.SessionTTL)
		}
		sessionTTL = d
	}
	if req.LongLivedMinRate < 0 || req.LongLivedMinRate > 1 {
		return false, fmt.Errorf("长效成功率需在 0~1 之间: %v", req.LongLivedMinRate)
	}
	providers := make([]config.RuleProvider, 0, len(req.RuleProviders))
	for _, p := range req.RuleProviders {
		if strings.TrimSpace(p.URL) == "" {
			continue
		}
		var interval time.Duration
		if v := strings.TrimSpace(p.Interval); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d < 0 {
				return false, fmt.Errorf("规则订阅刷新间隔无效: %q", p.Interval)
			}
			interval = d
		}
		providers = append(providers, config.RuleProvider{
			URL:      strings.TrimSpace(p.URL),
			Policy:   strings.TrimSpace(p.Policy),
			Behavior: strings.TrimSpace(p.Behavior),
			Interval: interval,
		})
	}
	if err := config.ValidateRuleProviders(providers); err != nil {
		return false, err
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	s.cfgMu.RLock()
	c := s.cfgSrc
	s.cfgMu.RUnlock()
	if c == nil {
		return false, errors.New("配置存储未初始化")
	}
	if s.reloadWindowCount > 0 {
		return false, errReloadInProgress
	}

	// Determine whether the change needs a full reload BEFORE mutating config:
	// enable/disable and listen-address edits change whether the pool inbound
	// binds the entry port, while session TTL changes rebuild sticky state.
	// Long-lived thresholds are propagated directly to existing monitor entries.
	c.Lock()
	reloadNeeded := c.Routing.Enabled != req.Enabled ||
		strings.TrimSpace(c.Routing.Listen) != strings.TrimSpace(req.Listen) ||
		c.Routing.Session.TTL != sessionTTL
	candidate := c.Clone()
	candidate.Routing.Enabled = req.Enabled
	candidate.Routing.Listen = strings.TrimSpace(req.Listen)
	candidate.Routing.DefaultStrategy = strings.TrimSpace(req.DefaultStrategy)
	useDefaults := req.UseDefaultRules
	candidate.Routing.UseDefaultRules = &useDefaults
	candidate.Routing.FinalPolicy = strings.TrimSpace(req.FinalPolicy)
	candidate.Routing.Rules = append([]string(nil), req.Rules...)
	candidate.Routing.RuleProviders = providers
	candidate.Routing.LongLived.MinUptime = llUptime
	candidate.Routing.LongLived.MinSuccessRate = req.LongLivedMinRate
	candidate.Routing.Session.TTL = sessionTTL
	candidate.Lock()
	err := candidate.SaveSettings()
	candidate.Unlock()
	if err != nil {
		c.Unlock()
		return false, fmt.Errorf("保存配置失败: %w", err)
	}
	c.Routing = candidate.Routing
	c.Unlock()

	// Hot-apply rules, strategy, final policy, providers, and long-lived
	// thresholds when no structural change forces a reload.
	routing := s.routingSnapshot()
	if !reloadNeeded && routing != nil {
		if applied := routing.ApplyHot(c); applied {
			return false, nil
		}
		// Not hot-appliable (e.g. routing running-state mismatch) → reload.
		return true, nil
	}
	return reloadNeeded, nil
}

func (s *Server) handleSourceSyncSourceHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if s.mgr == nil {
		writeJSON(w, map[string]any{
			"sources":       []SourceHealthState{},
			"total_sources": 0,
		})
		return
	}

	sourceRef := strings.TrimSpace(r.URL.Query().Get("source_ref"))
	if sourceRef == "" {
		sourceRef = strings.TrimSpace(r.URL.Query().Get("ref"))
	}

	grouped := s.mgr.SourceHealthStates()
	if sourceRef != "" {
		state, ok := grouped[sourceRef]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{
				"error":      "source_ref not found",
				"source_ref": sourceRef,
			})
			return
		}
		writeJSON(w, map[string]any{
			"sources":       []SourceHealthState{state},
			"total_sources": 1,
			"source_ref":    sourceRef,
		})
		return
	}

	sources := make([]SourceHealthState, 0, len(grouped))
	for _, state := range grouped {
		sources = append(sources, state)
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].SelectionExcluded != sources[j].SelectionExcluded {
			return !sources[i].SelectionExcluded && sources[j].SelectionExcluded
		}
		if sources[i].EffectiveAvailableNodes != sources[j].EffectiveAvailableNodes {
			return sources[i].EffectiveAvailableNodes > sources[j].EffectiveAvailableNodes
		}
		if sources[i].TotalNodes != sources[j].TotalNodes {
			return sources[i].TotalNodes > sources[j].TotalNodes
		}
		return sources[i].Ref < sources[j].Ref
	})

	writeJSON(w, map[string]any{
		"sources":       sources,
		"total_sources": len(sources),
	})
}

// nodePayload is the JSON request body for node CRUD operations.
type nodePayload struct {
	Name     string `json:"name"`
	URI      string `json:"uri"`
	Port     uint16 `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (p nodePayload) toConfig() config.NodeConfig {
	return config.NodeConfig{
		Name:     p.Name,
		URI:      p.URI,
		Port:     p.Port,
		Username: p.Username,
		Password: p.Password,
	}
}

// handleConfigNodes handles GET (list) and POST (create) for config nodes.
func (s *Server) handleConfigNodes(w http.ResponseWriter, r *http.Request) {
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	switch r.Method {
	case http.MethodGet:
		nodes, err := nodeManager.ListConfigNodes(r.Context())
		if err != nil {
			s.respondNodeError(w, err)
			return
		}
		writeJSON(w, map[string]any{"nodes": nodes})
	case http.MethodPost:
		var payload nodePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}
		node, err := nodeManager.CreateNode(r.Context(), payload.toConfig())
		if err != nil {
			s.respondNodeError(w, err)
			return
		}
		writeJSON(w, map[string]any{"node": node, "message": "节点已添加，请点击重载使配置生效"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleConfigNodeItem handles PUT (update) and DELETE for a specific config node.
func (s *Server) handleConfigNodeItem(w http.ResponseWriter, r *http.Request) {
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	namePart := strings.TrimPrefix(r.URL.Path, "/api/nodes/config/")
	nodeName, err := url.PathUnescape(namePart)
	if err != nil || nodeName == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "节点名称无效"})
		return
	}

	switch r.Method {
	case http.MethodPut:
		var payload nodePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}
		node, err := nodeManager.UpdateNode(r.Context(), nodeName, payload.toConfig())
		if err != nil {
			s.respondNodeError(w, err)
			return
		}
		writeJSON(w, map[string]any{"node": node, "message": "节点已更新，请点击重载使配置生效"})
	case http.MethodPatch:
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "请求格式错误"})
			return
		}
		if body.Enabled == nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "缺少 enabled 字段"})
			return
		}
		if err := nodeManager.SetNodeEnabled(r.Context(), nodeName, *body.Enabled); err != nil {
			s.respondNodeError(w, err)
			return
		}
		action := "已启用"
		if !*body.Enabled {
			action = "已禁用"
		}
		// Auto-reload after toggle
		reloadMsg := ""
		if err := nodeManager.TriggerReload(r.Context()); err != nil {
			s.logger.Printf("auto-reload after toggle failed: %v", err)
			reloadMsg = "（自动重载失败，请手动重载）"
		} else {
			reloadMsg = "（已自动重载）"
		}
		writeJSON(w, map[string]any{"message": fmt.Sprintf("节点 %s %s%s", nodeName, action, reloadMsg)})
	case http.MethodDelete:
		if err := nodeManager.DeleteNode(r.Context(), nodeName); err != nil {
			s.respondNodeError(w, err)
			return
		}
		writeJSON(w, map[string]any{"message": "节点已删除，请点击重载使配置生效"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleConfigNodesBatchToggle handles batch enable/disable for multiple nodes.
func (s *Server) handleConfigNodesBatchToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	var body struct {
		Names   []string `json:"names"`
		Enabled bool     `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "请求格式错误"})
		return
	}
	if len(body.Names) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "节点列表为空"})
		return
	}

	var errs []string
	successCount := 0
	for _, name := range body.Names {
		if err := nodeManager.SetNodeEnabled(r.Context(), name, body.Enabled); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		} else {
			successCount++
		}
	}

	action := "启用"
	if !body.Enabled {
		action = "禁用"
	}

	// Auto-reload after batch toggle
	reloadMsg := ""
	if successCount > 0 {
		if err := nodeManager.TriggerReload(r.Context()); err != nil {
			s.logger.Printf("auto-reload after batch toggle failed: %v", err)
			reloadMsg = "（自动重载失败，请手动重载）"
		} else {
			reloadMsg = "（已自动重载）"
		}
	}

	result := map[string]any{
		"message": fmt.Sprintf("成功%s %d 个节点%s", action, successCount, reloadMsg),
		"success": successCount,
		"total":   len(body.Names),
	}
	if len(errs) > 0 {
		result["errors"] = errs
	}
	writeJSON(w, result)
}

// handleConfigNodesBatchDelete handles batch deletion for multiple nodes.
func (s *Server) handleConfigNodesBatchDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	var body struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "请求格式错误"})
		return
	}
	if len(body.Names) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "节点列表为空"})
		return
	}

	var errs []string
	successCount := 0
	for _, name := range body.Names {
		if err := nodeManager.DeleteNode(r.Context(), name); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		} else {
			successCount++
		}
	}

	// Auto-reload after batch delete
	reloadMsg := ""
	if successCount > 0 {
		if err := nodeManager.TriggerReload(r.Context()); err != nil {
			s.logger.Printf("auto-reload after batch delete failed: %v", err)
			reloadMsg = "（自动重载失败，请手动重载）"
		} else {
			reloadMsg = "（已自动重载）"
		}
	}

	result := map[string]any{
		"message": fmt.Sprintf("成功删除 %d 个节点%s", successCount, reloadMsg),
		"success": successCount,
		"total":   len(body.Names),
	}
	if len(errs) > 0 {
		result["errors"] = errs
	}
	writeJSON(w, result)
}

// handleReload triggers a configuration reload.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nodeManager, ok := s.requireNodeManager(w)
	if !ok {
		return
	}

	if err := nodeManager.TriggerReload(r.Context()); err != nil {
		s.respondNodeError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"message": "重载成功，现有连接已被中断",
	})
}

func (s *Server) requireNodeManager(w http.ResponseWriter) (NodeManager, bool) {
	nodeManager := s.nodeManagerSnapshot()
	if nodeManager == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"error": "节点管理未启用"})
		return nil, false
	}
	return nodeManager, true
}

func (s *Server) respondNodeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNodeNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrNodeConflict), errors.Is(err, ErrInvalidNode):
		status = http.StatusBadRequest
	}
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"error": err.Error()})
}

// Session management functions

// generateSessionToken creates a cryptographically secure random token.
func (s *Server) generateSessionToken() (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate session token: %w", err)
	}
	return hex.EncodeToString(tokenBytes), nil
}

// createSession creates a new session with expiration.
func (s *Server) createSession() (*Session, error) {
	token, err := s.generateSessionToken()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	session := &Session{
		Token:     token,
		CreatedAt: now,
		ExpiresAt: now.Add(s.sessionTTL),
	}

	// Persist to Store if available
	storeRef := s.storeSnapshot()
	if storeRef != nil {
		storeSession := &store.Session{
			Token:     session.Token,
			CreatedAt: session.CreatedAt,
			ExpiresAt: session.ExpiresAt,
		}
		if err := storeRef.CreateSession(context.Background(), storeSession); err != nil {
			s.logger.Printf("Failed to persist session to store: %v", err)
		}
	}

	// Also keep in memory for fast lookups
	s.sessionMu.Lock()
	s.sessions[token] = session
	s.sessionMu.Unlock()

	return session, nil
}

// validateSession checks if a session token is valid and not expired.
func (s *Server) validateSession(token string) bool {
	storeRef := s.storeSnapshot()
	// Check in-memory cache first
	s.sessionMu.RLock()
	session, exists := s.sessions[token]
	s.sessionMu.RUnlock()

	if exists {
		if time.Now().After(session.ExpiresAt) {
			s.sessionMu.Lock()
			delete(s.sessions, token)
			s.sessionMu.Unlock()
			// Also delete from store
			if storeRef != nil {
				_ = storeRef.DeleteSession(context.Background(), token)
			}
			return false
		}
		return true
	}

	// Fallback: check Store (e.g., after restart)
	if storeRef != nil {
		storeSess, err := storeRef.GetSession(context.Background(), token)
		if err != nil || storeSess == nil {
			return false
		}
		if time.Now().After(storeSess.ExpiresAt) {
			_ = storeRef.DeleteSession(context.Background(), token)
			return false
		}
		// Restore to in-memory cache
		s.sessionMu.Lock()
		s.sessions[token] = &Session{
			Token:     storeSess.Token,
			CreatedAt: storeSess.CreatedAt,
			ExpiresAt: storeSess.ExpiresAt,
		}
		s.sessionMu.Unlock()
		return true
	}

	return false
}

// cleanupExpiredSessions periodically removes expired sessions.
func (s *Server) cleanupExpiredSessions() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			now := time.Now()
			s.sessionMu.Lock()
			for token, session := range s.sessions {
				if now.After(session.ExpiresAt) {
					delete(s.sessions, token)
				}
			}
			s.sessionMu.Unlock()

			// Also cleanup in Store
			if storeRef := s.storeSnapshot(); storeRef != nil {
				_ = storeRef.CleanupExpiredSessions(context.Background())
			}
		}
	}
}

// secureCompareStrings performs constant-time string comparison to prevent timing attacks.
func secureCompareStrings(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
