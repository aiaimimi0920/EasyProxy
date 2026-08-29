package monitor

import (
	"context"
	"embed"
	"errors"
	"log"
	"net"
	"net/http"
	"runtime"
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
	Token                string
	CreatedAt            time.Time
	ExpiresAt            time.Time
	CredentialGeneration uint64
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

// GatewayReporter exposes the transparent gateway runtime without coupling
// the monitor package to Linux networking implementation types.
type GatewayReporter interface {
	GatewayStatus() any
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
	cfg                      Config
	cfgMu                    sync.RWMutex   // protects cfgSrc pointer assignment and local cfg fields
	cfgSrc                   *config.Config // 可持久化的配置对象; fields protected by cfgSrc.mu
	configUpdateMu           sync.Mutex     // serializes cfgSrc swaps with persisted config edits
	reloadWindowCount        int            // nested reload intents that reject persisted edits
	localServerReloadPending bool           // persisted Local Server edit awaits reload publication
	mgr                      *Manager
	handler                  http.Handler

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
	gateway      GatewayReporter
	proxyCompat  *proxyCompatState

	// Serializes compatibility checkout selection/store so concurrent callers
	// do not race into the same degraded node before reservations are visible.
	proxyCompatCheckoutMu sync.Mutex
}

var (
	errReloadInProgress              = errors.New("configuration update deferred while reload is in progress")
	errInvalidLocalServerConfig      = errors.New("invalid local server config")
	errLocalServerCredentialConflict = errors.New("local server credential conflict")
)

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
	if maxConcurrentProbes > 16 {
		maxConcurrentProbes = 16
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
	mux.HandleFunc("/api/gateway/status", s.withAuth(s.handleGatewayStatus))
	s.registerLocalServerRoutes(mux)
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

func (s *Server) SetGatewayReporter(reporter GatewayReporter) {
	if s != nil {
		s.depsMu.Lock()
		s.gateway = reporter
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

func (s *Server) gatewaySnapshot() GatewayReporter {
	if s == nil {
		return nil
	}
	s.depsMu.RLock()
	defer s.depsMu.RUnlock()
	return s.gateway
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
	s.localServerReloadPending = false
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
