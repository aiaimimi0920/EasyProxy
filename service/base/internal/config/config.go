package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Config describes the high level settings for the proxy pool server.
type Config struct {
	mu sync.RWMutex `yaml:"-"` // protects all fields for concurrent access

	Mode                string                    `yaml:"mode"`
	Listener            ListenerConfig            `yaml:"listener"`
	MultiPort           MultiPortConfig           `yaml:"multi_port"`
	Pool                PoolConfig                `yaml:"pool"`
	Management          ManagementConfig          `yaml:"management"`
	SubscriptionRefresh SubscriptionRefreshConfig `yaml:"subscription_refresh"`
	SourceSync          SourceSyncConfig          `yaml:"source_sync"`
	GeoIP               GeoIPConfig               `yaml:"geoip"`
	Nodes               []NodeConfig              `yaml:"nodes"`
	Connectors          []ConnectorSourceConfig   `yaml:"connectors"`
	NodesFile           string                    `yaml:"nodes_file"`    // 节点文件路径，每行一个 URI
	Subscriptions       []string                  `yaml:"subscriptions"` // 订阅链接列表
	ExternalIP          string                    `yaml:"external_ip"`   // 外部 IP 地址，用于导出时替换 0.0.0.0
	LogLevel            string                    `yaml:"log_level"`
	SkipCertVerify      bool                      `yaml:"skip_cert_verify"` // 全局跳过 SSL 证书验证
	DatabasePath        string                    `yaml:"database_path"`    // SQLite 数据库路径，默认 data/data.db
	ExtraListeners      []ExtraListenerConfig     `yaml:"extra_listeners"`  // 额外监听端口（不同选择策略）

	filePath string `yaml:"-"` // 配置文件路径，用于保存
}

// ExtraListenerConfig defines an additional listener with its own pool selection mode.
type ExtraListenerConfig struct {
	Address  string `yaml:"address"`
	Port     uint16 `yaml:"port"`
	Protocol string `yaml:"protocol"`  // http, socks5, mixed
	PoolMode string `yaml:"pool_mode"` // auto, random, balance, sequential
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// GeoIPConfig controls GeoIP-based region routing.
type GeoIPConfig struct {
	Enabled            bool          `yaml:"enabled"`              // 是否启用 GeoIP 地域分区
	DatabasePath       string        `yaml:"database_path"`        // GeoLite2-Country.mmdb 文件路径
	Listen             string        `yaml:"listen"`               // GeoIP 路由监听地址，默认使用 listener 配置
	Port               uint16        `yaml:"port"`                 // GeoIP 路由监听端口，默认 22323
	AutoUpdateEnabled  bool          `yaml:"auto_update_enabled"`  // 是否启用自动更新数据库
	AutoUpdateInterval time.Duration `yaml:"auto_update_interval"` // 自动更新间隔，默认 24 小时
}

// ListenerConfig defines how the proxy should listen for clients.
type ListenerConfig struct {
	Address  string `yaml:"address"`
	Port     uint16 `yaml:"port"`
	Protocol string `yaml:"protocol"` // http, socks5, mixed
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// PoolConfig configures scheduling + failure handling.
type PoolConfig struct {
	Mode              string        `yaml:"mode"`
	FailureThreshold  int           `yaml:"failure_threshold"`
	BlacklistDuration time.Duration `yaml:"blacklist_duration"`
	MaxRetries        int           `yaml:"max_retries"` // max retry attempts on connection failure (default 2, 0 = no retry)
}

// MultiPortConfig defines address/credential defaults for multi-port mode.
type MultiPortConfig struct {
	Address  string `yaml:"address"`
	BasePort uint16 `yaml:"base_port"`
	Protocol string `yaml:"protocol"` // http, socks5, mixed
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// ManagementConfig controls the monitoring HTTP endpoint.
type ManagementConfig struct {
	Enabled             *bool         `yaml:"enabled"`
	Listen              string        `yaml:"listen"`
	ProbeTarget         string        `yaml:"probe_target"`
	ProbeTargets        []string      `yaml:"probe_targets"`
	Password            string        `yaml:"password"` // WebUI 访问密码，为空则不需要密码
	HealthCheckInterval time.Duration `yaml:"health_check_interval"`
}

// Use TCP probe targets so health checks validate raw proxy reachability
// without EasyProxy's own TLS or HTTP client behavior skewing the result.
var defaultManagementProbeTargets = []string{
	"tcp://www.google.com:443",
	"tcp://connectivitycheck.gstatic.com:443",
	"tcp://www.msftconnecttest.com:443",
	"tcp://cp.cloudflare.com:443",
}

// DefaultManagementProbeTargets returns the default probe targets used by the
// health checker when the config does not explicitly provide any.
func DefaultManagementProbeTargets() []string {
	return append([]string(nil), defaultManagementProbeTargets...)
}

// SubscriptionRefreshConfig controls subscription auto-refresh and reload settings.
type SubscriptionRefreshConfig struct {
	Enabled            bool          `yaml:"enabled"`              // 是否启用定时刷新
	Interval           time.Duration `yaml:"interval"`             // 刷新间隔，默认 1 小时
	Timeout            time.Duration `yaml:"timeout"`              // 获取订阅的超时时间
	HealthCheckTimeout time.Duration `yaml:"health_check_timeout"` // 新节点健康检查超时
	DrainTimeout       time.Duration `yaml:"drain_timeout"`        // 旧实例排空超时时间
	MinAvailableNodes  int           `yaml:"min_available_nodes"`  // 最少可用节点数，低于此值不切换
}

// SourceSyncConfig controls MiSub manifest sync and aggregator fallback.
type SourceSyncConfig struct {
	Enabled                  bool                   `yaml:"enabled"`
	ManifestURL              string                 `yaml:"manifest_url"`
	ManifestToken            string                 `yaml:"manifest_token"`
	RefreshInterval          time.Duration          `yaml:"refresh_interval"`
	RequestTimeout           time.Duration          `yaml:"request_timeout"`
	FallbackSubscriptions    []string               `yaml:"fallback_subscriptions"`
	DefaultDirectProxyScheme string                 `yaml:"default_direct_proxy_scheme"`
	ConnectorRuntime         ConnectorRuntimeConfig `yaml:"connector_runtime"`
}

// ConnectorRuntimeConfig controls local execution of manifest connectors such as ech-workers.
type ConnectorRuntimeConfig struct {
	Enabled          *bool                      `yaml:"enabled"`
	BinaryPath       string                     `yaml:"binary_path"`
	WorkingDirectory string                     `yaml:"working_directory"`
	ListenHost       string                     `yaml:"listen_host"`
	ListenStartPort  uint16                     `yaml:"listen_start_port"`
	StartupTimeout   time.Duration              `yaml:"startup_timeout"`
	PreferredIP      PreferredIPGeneratorConfig `yaml:"preferred_ip"`
}

// PreferredIPGeneratorConfig controls optional local generation of preferred
// Cloudflare entry IPs for connector templates.
type PreferredIPGeneratorConfig struct {
	BinaryPath       string        `yaml:"binary_path"`
	IPFilePath       string        `yaml:"ip_file_path"`
	WorkingDirectory string        `yaml:"working_directory"`
	Timeout          time.Duration `yaml:"timeout"`
}

// NodeSource indicates where a node configuration originated from.
type NodeSource string

const (
	NodeSourceInline       NodeSource = "inline"       // Defined directly in config.yaml nodes array
	NodeSourceFile         NodeSource = "nodes_file"   // Loaded from external nodes file
	NodeSourceSubscription NodeSource = "subscription" // Fetched from subscription URL
	NodeSourceManual       NodeSource = "manual"       // Added manually via WebUI
	NodeSourceManifest     NodeSource = "manifest"     // Materialized from remote MiSub manifest
	NodeSourceFallback     NodeSource = "fallback"     // Materialized from aggregator fallback subscription
)

const (
	InboundProtocolHTTP   = "http"
	InboundProtocolSOCKS5 = "socks5"
	InboundProtocolMixed  = "mixed"
)

var supportedProxyURISchemes = []string{
	"vmess://",
	"vless://",
	"trojan://",
	"ss://",
	"ssr://",
	"hysteria://",
	"hysteria2://",
	"hy2://",
	"anytls://",
	"http://",
	"socks5://",
}

// NormalizeProxyURIInput converts bare residential inputs such as
// user:pass@host:port into explicit proxy URIs.
func NormalizeProxyURIInput(value string, defaultScheme string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "://") {
		return trimmed
	}
	if !isBareProxyInput(trimmed) {
		return trimmed
	}
	scheme := strings.TrimSpace(defaultScheme)
	if scheme == "" {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s", scheme, trimmed)
}

// NormalizeV2RayTransport canonicalizes V2Ray transport aliases into the
// transport names understood by sing-box.
//
// The boolean return value reports whether the input is recognized. Raw/tcp
// are both treated as the absence of an extra transport layer.
func NormalizeV2RayTransport(value string) (string, bool) {
	transport := strings.ToLower(strings.TrimSpace(value))
	switch transport {
	case "", "tcp", "raw":
		return "", true
	case "ws", "websocket":
		return "ws", true
	case "http", "h2", "h2c":
		return "http", true
	case "grpc":
		return "grpc", true
	case "httpupgrade", "http-upgrade":
		return "httpupgrade", true
	case "xhttp":
		log.Printf("⚠️  XHTTP transport not supported by sing-box, falling back to HTTPUpgrade")
		return "httpupgrade", true
	default:
		return "", false
	}
}

func isBareProxyInput(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, "://") || strings.ContainsAny(trimmed, "/?#") {
		return false
	}
	hostPort := trimmed
	if at := strings.LastIndex(hostPort, "@"); at >= 0 && at < len(hostPort)-1 {
		hostPort = hostPort[at+1:]
	}
	host, port, err := net.SplitHostPort(hostPort)
	return err == nil && host != "" && port != ""
}

// NormalizeInboundProtocol normalizes inbound protocol aliases and validates the value.
func NormalizeInboundProtocol(value string) (string, error) {
	protocol := strings.ToLower(strings.TrimSpace(value))
	switch protocol {
	case "socks":
		protocol = InboundProtocolSOCKS5
	}
	switch protocol {
	case InboundProtocolHTTP, InboundProtocolSOCKS5, InboundProtocolMixed:
		return protocol, nil
	default:
		return "", fmt.Errorf("不支持的监听协议: %q（仅支持 http/socks5/mixed）", value)
	}
}

// NodeConfig describes a single upstream proxy endpoint expressed as URI.
type NodeConfig struct {
	Name       string     `yaml:"name" json:"name"`
	URI        string     `yaml:"uri" json:"uri"`
	Port       uint16     `yaml:"port,omitempty" json:"port,omitempty"`
	Username   string     `yaml:"username,omitempty" json:"username,omitempty"`
	Password   string     `yaml:"password,omitempty" json:"password,omitempty"`
	Source     NodeSource `yaml:"-" json:"source,omitempty"`   // Runtime only, not persisted in YAML
	Disabled   bool       `yaml:"-" json:"disabled,omitempty"` // Runtime only, not persisted in YAML; true = node is disabled
	SourceKind string     `yaml:"-" json:"source_kind,omitempty"`
	SourceName string     `yaml:"-" json:"source_name,omitempty"`
	SourceRef  string     `yaml:"-" json:"source_ref,omitempty"`
}

// NodeKey returns a unique identifier for the node based on its URI.
// This is used to preserve port assignments across reloads.
func (n *NodeConfig) NodeKey() string {
	return n.URI
}

// ConnectorSourceConfig describes a locally managed connector source.
type ConnectorSourceConfig struct {
	Name            string         `yaml:"name" json:"name"`
	Input           string         `yaml:"input" json:"input"`
	Enabled         bool           `yaml:"enabled" json:"enabled"`
	TemplateOnly    bool           `yaml:"template_only,omitempty" json:"template_only,omitempty"`
	Group           string         `yaml:"group,omitempty" json:"group,omitempty"`
	Notes           string         `yaml:"notes,omitempty" json:"notes,omitempty"`
	ConnectorType   string         `yaml:"connector_type" json:"connector_type"`
	ConnectorConfig map[string]any `yaml:"connector_config,omitempty" json:"connector_config,omitempty"`
}

// Load reads YAML config from disk and applies defaults/validation.
// This is used for the initial startup and will fetch subscription URLs.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	cfg.filePath = path

	// Resolve relative paths to be relative to the config file directory
	configDir := filepath.Dir(path)
	if cfg.NodesFile != "" && !filepath.IsAbs(cfg.NodesFile) {
		cfg.NodesFile = filepath.Join(configDir, cfg.NodesFile)
	}
	if cfg.GeoIP.DatabasePath != "" && !filepath.IsAbs(cfg.GeoIP.DatabasePath) {
		cfg.GeoIP.DatabasePath = filepath.Join(configDir, cfg.GeoIP.DatabasePath)
	}

	if err := cfg.normalizeInternal(false); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadForReload reads YAML config from disk for a reload operation.
// Unlike Load, it does NOT re-fetch subscription URLs.
// Only inline nodes from config.yaml are loaded; subscription and manual
// nodes are loaded from the SQLite Store by the caller.
func LoadForReload(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	cfg.filePath = path

	// Resolve relative paths to be relative to the config file directory
	configDir := filepath.Dir(path)
	if cfg.NodesFile != "" && !filepath.IsAbs(cfg.NodesFile) {
		cfg.NodesFile = filepath.Join(configDir, cfg.NodesFile)
	}
	if cfg.GeoIP.DatabasePath != "" && !filepath.IsAbs(cfg.GeoIP.DatabasePath) {
		cfg.GeoIP.DatabasePath = filepath.Join(configDir, cfg.GeoIP.DatabasePath)
	}

	if err := cfg.normalizeInternal(true); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) normalize() error {
	return c.normalizeInternal(false)
}

// applyDefaults sets default values for all config fields.
// This is the single source of truth for defaults, used by both
// normalizeInternal and NormalizeWithPortMap to avoid code duplication.
func (c *Config) applyDefaults() error {
	if c.Mode == "" {
		c.Mode = "pool"
	}
	// Normalize mode name: support both multi-port and multi_port
	if c.Mode == "multi_port" {
		c.Mode = "multi-port"
	}
	switch c.Mode {
	case "pool", "multi-port", "hybrid":
	default:
		return fmt.Errorf("unsupported mode %q (use 'pool', 'multi-port', or 'hybrid')", c.Mode)
	}
	if c.Listener.Address == "" {
		c.Listener.Address = "0.0.0.0"
	}
	if c.Listener.Port == 0 {
		c.Listener.Port = 22323
	}
	if c.Listener.Protocol == "" {
		c.Listener.Protocol = InboundProtocolHTTP
	}
	listenerProtocol, err := NormalizeInboundProtocol(c.Listener.Protocol)
	if err != nil {
		return err
	}
	c.Listener.Protocol = listenerProtocol
	if c.Pool.Mode == "" {
		c.Pool.Mode = "auto"
	}
	if c.Pool.FailureThreshold <= 0 {
		c.Pool.FailureThreshold = 3
	}
	if c.Pool.BlacklistDuration <= 0 {
		c.Pool.BlacklistDuration = 24 * time.Hour
	}
	if c.MultiPort.Address == "" {
		c.MultiPort.Address = "0.0.0.0"
	}
	if c.MultiPort.BasePort == 0 {
		c.MultiPort.BasePort = 25000
	}
	if c.MultiPort.Protocol == "" {
		c.MultiPort.Protocol = InboundProtocolHTTP
	}
	multiPortProtocol, err := NormalizeInboundProtocol(c.MultiPort.Protocol)
	if err != nil {
		return err
	}
	c.MultiPort.Protocol = multiPortProtocol
	if c.Management.Listen == "" {
		c.Management.Listen = "0.0.0.0:29888"
	}
	if len(c.Management.ProbeTargets) == 0 && c.Management.ProbeTarget == "" {
		c.Management.ProbeTargets = DefaultManagementProbeTargets()
	}
	if c.Management.Enabled == nil {
		defaultEnabled := true
		c.Management.Enabled = &defaultEnabled
	}
	if c.Management.HealthCheckInterval <= 0 {
		c.Management.HealthCheckInterval = 2 * time.Hour
	}

	// Subscription refresh defaults
	if c.SubscriptionRefresh.Interval <= 0 {
		c.SubscriptionRefresh.Interval = 1 * time.Hour
	}
	if c.SubscriptionRefresh.Timeout <= 0 {
		c.SubscriptionRefresh.Timeout = 30 * time.Second
	}
	if c.SubscriptionRefresh.HealthCheckTimeout <= 0 {
		c.SubscriptionRefresh.HealthCheckTimeout = 60 * time.Second
	}
	if c.SubscriptionRefresh.DrainTimeout <= 0 {
		c.SubscriptionRefresh.DrainTimeout = 30 * time.Second
	}
	if c.SubscriptionRefresh.MinAvailableNodes <= 0 {
		c.SubscriptionRefresh.MinAvailableNodes = 1
	}
	if c.SourceSync.RefreshInterval <= 0 {
		c.SourceSync.RefreshInterval = 5 * time.Minute
	}
	if c.SourceSync.RequestTimeout <= 0 {
		c.SourceSync.RequestTimeout = 15 * time.Second
	}
	if strings.TrimSpace(c.SourceSync.DefaultDirectProxyScheme) == "" {
		c.SourceSync.DefaultDirectProxyScheme = "http"
	}
	if c.SourceSync.ConnectorRuntime.Enabled == nil {
		defaultEnabled := true
		c.SourceSync.ConnectorRuntime.Enabled = &defaultEnabled
	}
	if strings.TrimSpace(c.SourceSync.ConnectorRuntime.ListenHost) == "" {
		c.SourceSync.ConnectorRuntime.ListenHost = "127.0.0.1"
	}
	if c.SourceSync.ConnectorRuntime.ListenStartPort == 0 {
		c.SourceSync.ConnectorRuntime.ListenStartPort = 30000
	}
	if c.SourceSync.ConnectorRuntime.StartupTimeout <= 0 {
		c.SourceSync.ConnectorRuntime.StartupTimeout = 10 * time.Second
	}
	if strings.TrimSpace(c.SourceSync.ConnectorRuntime.PreferredIP.BinaryPath) == "" {
		c.SourceSync.ConnectorRuntime.PreferredIP.BinaryPath = "cfst"
	}
	if c.SourceSync.ConnectorRuntime.PreferredIP.Timeout <= 0 {
		c.SourceSync.ConnectorRuntime.PreferredIP.Timeout = 5 * time.Minute
	}
	if strings.TrimSpace(c.SourceSync.ConnectorRuntime.PreferredIP.WorkingDirectory) == "" {
		baseDir := "."
		if strings.TrimSpace(c.filePath) != "" {
			baseDir = filepath.Dir(c.filePath)
		}
		c.SourceSync.ConnectorRuntime.PreferredIP.WorkingDirectory = filepath.Join(baseDir, "data", "connectors", "preferred-ip")
	}
	if strings.TrimSpace(c.SourceSync.ConnectorRuntime.PreferredIP.IPFilePath) == "" {
		c.SourceSync.ConnectorRuntime.PreferredIP.IPFilePath = "/usr/local/share/cfst/ip.txt"
	}

	if c.LogLevel == "" {
		c.LogLevel = "info"
	}

	return nil
}

// normalizeInternal applies defaults, loads external nodes, and validates config.
// If skipSubscriptionFetch is true (reload/refresh scenario), only inline nodes
// from config.yaml are loaded. Subscription and manual nodes are managed by the
// SQLite Store and loaded by the caller (app.go / boxmgr).
func (c *Config) normalizeInternal(skipSubscriptionFetch bool) error {
	if err := c.applyDefaults(); err != nil {
		return err
	}

	// Mark inline nodes with source
	for idx := range c.Nodes {
		c.Nodes[idx].Source = NodeSourceInline
	}

	if skipSubscriptionFetch {
		// ---- Reload mode ----
		// Nodes will be loaded from SQLite Store by the caller (app.go / boxmgr).
		// Only inline nodes from config.yaml are included here.
		log.Printf("[config] reload mode: %d inline nodes from config.yaml", len(c.Nodes))
	} else {
		// ---- Initial load mode ----

		// Load nodes from file if specified (but NOT if subscriptions exist - subscription takes priority)
		if c.NodesFile != "" && len(c.Subscriptions) == 0 {
			fileNodes, err := loadNodesFromFile(c.NodesFile)
			if err != nil {
				return fmt.Errorf("load nodes from file %q: %w", c.NodesFile, err)
			}
			for idx := range fileNodes {
				fileNodes[idx].Source = NodeSourceFile
			}
			c.Nodes = append(c.Nodes, fileNodes...)
		}

		// Load nodes from subscriptions (fetched into memory, persisted to Store by app.go)
		if len(c.Subscriptions) > 0 {
			var subNodes []NodeConfig
			subTimeout := c.SubscriptionRefresh.Timeout
			for _, subURL := range c.Subscriptions {
				nodes, err := loadNodesFromSubscription(subURL, subTimeout)
				if err != nil {
					log.Printf("⚠️ Failed to load subscription %q: %v (skipping)", subURL, err)
					continue
				}
				log.Printf("✅ Loaded %d nodes from subscription", len(nodes))
				subNodes = append(subNodes, nodes...)
			}
			for idx := range subNodes {
				subNodes[idx].Source = NodeSourceSubscription
			}
			c.Nodes = append(c.Nodes, subNodes...)
		}
	}

	// Note: Manual nodes are loaded from SQLite Store by app.go, not from files.

	if len(c.Nodes) == 0 {
		// Not fatal — Store may have nodes from a previous run.
		log.Printf("⚠️ No nodes in config (inline/subscription/file). Will check Store for existing nodes.")
	}
	portCursor := c.MultiPort.BasePort
	for idx := range c.Nodes {
		c.Nodes[idx].Name = strings.TrimSpace(c.Nodes[idx].Name)
		c.Nodes[idx].URI = NormalizeProxyURIInput(strings.TrimSpace(c.Nodes[idx].URI), c.SourceSync.DefaultDirectProxyScheme)

		if c.Nodes[idx].URI == "" {
			return fmt.Errorf("node %d is missing uri", idx)
		}

		// Auto-extract name from URI fragment (#name) if not provided
		if c.Nodes[idx].Name == "" {
			if parsed, err := url.Parse(c.Nodes[idx].URI); err == nil && parsed.Fragment != "" {
				// URL decode the fragment to handle encoded characters
				if decoded, err := url.QueryUnescape(parsed.Fragment); err == nil {
					c.Nodes[idx].Name = decoded
				} else {
					c.Nodes[idx].Name = parsed.Fragment
				}
			}
		}

		// Fallback to default name if still empty
		if c.Nodes[idx].Name == "" {
			c.Nodes[idx].Name = fmt.Sprintf("node-%d", idx)
		}

		// Auto-assign port in multi-port/hybrid mode, skip occupied ports
		if c.Nodes[idx].Port == 0 && (c.Mode == "multi-port" || c.Mode == "hybrid") {
			for !isPortAvailable(c.MultiPort.Address, portCursor) {
				log.Printf("⚠️  Port %d is in use, trying next port", portCursor)
				portCursor++
				if portCursor > 65535 {
					return fmt.Errorf("no available ports found starting from %d", c.MultiPort.BasePort)
				}
			}
			c.Nodes[idx].Port = portCursor
			portCursor++
		} else if c.Nodes[idx].Port == 0 {
			c.Nodes[idx].Port = portCursor
			portCursor++
		}

		if c.Mode == "multi-port" || c.Mode == "hybrid" {
			if c.Nodes[idx].Username == "" {
				c.Nodes[idx].Username = c.MultiPort.Username
				c.Nodes[idx].Password = c.MultiPort.Password
			}
		}
	}
	if c.DatabasePath == "" {
		c.DatabasePath = "data/data.db"
	}
	// Resolve database path relative to config file directory
	if c.filePath != "" && !filepath.IsAbs(c.DatabasePath) {
		c.DatabasePath = filepath.Join(filepath.Dir(c.filePath), c.DatabasePath)
	}

	// Auto-fix port conflicts in hybrid mode (pool port vs multi-port)
	if c.Mode == "hybrid" {
		poolPort := c.Listener.Port
		usedPorts := make(map[uint16]bool)
		usedPorts[poolPort] = true
		for idx := range c.Nodes {
			usedPorts[c.Nodes[idx].Port] = true
		}
		for idx := range c.Nodes {
			if c.Nodes[idx].Port == poolPort {
				// Find next available port
				newPort := c.Nodes[idx].Port + 1
				for usedPorts[newPort] || !isPortAvailable(c.MultiPort.Address, newPort) {
					newPort++
					if newPort > 65535 {
						return fmt.Errorf("no available port for node %q after conflict with pool port %d", c.Nodes[idx].Name, poolPort)
					}
				}
				log.Printf("⚠️  Node %q port %d conflicts with pool port, reassigned to %d", c.Nodes[idx].Name, poolPort, newPort)
				usedPorts[newPort] = true
				c.Nodes[idx].Port = newPort
			}
		}
	}

	return nil
}

// BuildPortMap creates a mapping from node URI to port for existing nodes.
// This is used to preserve port assignments when reloading configuration.
func (c *Config) BuildPortMap() map[string]uint16 {
	portMap := make(map[string]uint16)
	for _, node := range c.Nodes {
		if node.Port > 0 {
			portMap[node.NodeKey()] = node.Port
		}
	}
	return portMap
}

// NormalizeWithPortMap applies defaults and validation, preserving port assignments
// for nodes that exist in the provided port map.
func (c *Config) NormalizeWithPortMap(portMap map[string]uint16) error {
	if err := c.applyDefaults(); err != nil {
		return err
	}

	if len(c.Nodes) == 0 {
		return errors.New("config.nodes cannot be empty (no inline, subscription, or manual nodes available)")
	}

	// Build set of ports already assigned from portMap
	usedPorts := make(map[uint16]bool)
	if c.Mode == "hybrid" {
		usedPorts[c.Listener.Port] = true
	}

	// First pass: assign ports from portMap for existing nodes, and track all pre-existing ports
	for idx := range c.Nodes {
		c.Nodes[idx].Name = strings.TrimSpace(c.Nodes[idx].Name)
		c.Nodes[idx].URI = NormalizeProxyURIInput(strings.TrimSpace(c.Nodes[idx].URI), c.SourceSync.DefaultDirectProxyScheme)
		if c.Nodes[idx].URI == "" {
			return fmt.Errorf("node %d is missing uri", idx)
		}

		// Extract name from URI fragment if not provided
		if c.Nodes[idx].Name == "" {
			if parsed, err := url.Parse(c.Nodes[idx].URI); err == nil && parsed.Fragment != "" {
				if decoded, err := url.QueryUnescape(parsed.Fragment); err == nil {
					c.Nodes[idx].Name = decoded
				} else {
					c.Nodes[idx].Name = parsed.Fragment
				}
			}
		}
		if c.Nodes[idx].Name == "" {
			c.Nodes[idx].Name = fmt.Sprintf("node-%d", idx)
		}

		// Check if this node has a preserved port from portMap
		if c.Mode == "multi-port" || c.Mode == "hybrid" {
			nodeKey := c.Nodes[idx].NodeKey()
			if existingPort, ok := portMap[nodeKey]; ok && existingPort > 0 {
				c.Nodes[idx].Port = existingPort
				usedPorts[existingPort] = true
				log.Printf("✅ Preserved port %d for node %q", existingPort, c.Nodes[idx].Name)
			} else if c.Nodes[idx].Port > 0 {
				// Track pre-existing ports (e.g. from store) to avoid conflicts
				usedPorts[c.Nodes[idx].Port] = true
			}
		}
	}

	// Second pass: assign new ports for nodes without preserved ports
	portCursor := c.MultiPort.BasePort
	for idx := range c.Nodes {
		if c.Nodes[idx].Port == 0 && (c.Mode == "multi-port" || c.Mode == "hybrid") {
			// Find next available port that's not used
			for usedPorts[portCursor] || !isPortAvailable(c.MultiPort.Address, portCursor) {
				portCursor++
				if portCursor > 65535 {
					return fmt.Errorf("no available ports found starting from %d", c.MultiPort.BasePort)
				}
			}
			c.Nodes[idx].Port = portCursor
			usedPorts[portCursor] = true
			log.Printf("📌 Assigned new port %d for node %q", portCursor, c.Nodes[idx].Name)
			portCursor++
		} else if c.Nodes[idx].Port == 0 {
			c.Nodes[idx].Port = portCursor
			portCursor++
		}

		// Apply default credentials
		if c.Mode == "multi-port" || c.Mode == "hybrid" {
			if c.Nodes[idx].Username == "" {
				c.Nodes[idx].Username = c.MultiPort.Username
				c.Nodes[idx].Password = c.MultiPort.Password
			}
		}
	}

	return nil
}

// ManagementEnabled reports whether the monitoring endpoint should run.
func (c *Config) ManagementEnabled() bool {
	if c.Management.Enabled == nil {
		return true
	}
	return *c.Management.Enabled
}

// ConnectorRuntimeEnabled reports whether manifest connectors should be executed locally.
func (c *Config) ConnectorRuntimeEnabled() bool {
	if c == nil || c.SourceSync.ConnectorRuntime.Enabled == nil {
		return true
	}
	return *c.SourceSync.ConnectorRuntime.Enabled
}

// loadNodesFromFile reads a nodes file where each line is a proxy URI
// Lines starting with # are comments, empty lines are ignored
func loadNodesFromFile(path string) ([]NodeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseNodesFromContent(string(data))
}

// loadNodesFromSubscription fetches and parses nodes from a subscription URL
// Supports multiple formats: base64 encoded, plain text, clash yaml, etc.
func loadNodesFromSubscription(subURL string, timeout time.Duration) ([]NodeConfig, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
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
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}

	req, err := http.NewRequest("GET", subURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Set common headers to avoid being blocked
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch subscription: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	content := string(body)

	// Try to detect and parse different formats
	return ParseSubscriptionContent(content)
}

// ParseSubscriptionContent tries to parse subscription content in various
// formats. It is shared by both config-time subscription loading and runtime
// source-sync/bootstrap refresh paths so manifest/fallback subscriptions are
// interpreted exactly the same way as local subscription URLs.
func ParseSubscriptionContent(content string) ([]NodeConfig, error) {
	content = strings.TrimSpace(content)

	// Clash subscriptions often include metadata headers before the actual
	// `proxies:` section, so checking only the first few bytes is too brittle.
	// Try the YAML parser whenever the document contains a proxies block.
	if looksLikeClashYAML(content) {
		nodes, err := parseClashYAML(content)
		if err == nil {
			return nodes, nil
		}
	}

	// Check if it's base64 encoded (common for v2ray subscriptions)
	if isBase64(content) {
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			// Try URL-safe base64
			decoded, err = base64.RawStdEncoding.DecodeString(content)
			if err != nil {
				// Not base64, try as plain text
				return parseNodesFromContent(content)
			}
		}
		content = string(decoded)
	}

	// Parse as plain text (one URI per line)
	return parseNodesFromContent(content)
}

func parseSubscriptionContent(content string) ([]NodeConfig, error) {
	return ParseSubscriptionContent(content)
}

func looksLikeClashYAML(content string) bool {
	normalized := strings.ToLower(strings.TrimSpace(content))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "\nproxies:") || strings.HasPrefix(normalized, "proxies:")
}

// parseNodesFromContent parses nodes from plain text content (one URI per line)
func parseNodesFromContent(content string) ([]NodeConfig, error) {
	var nodes []NodeConfig
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check if it's a valid proxy URI
		normalizedLine := NormalizeProxyURIInput(line, "http")
		if IsProxyURI(normalizedLine) {
			nodes = append(nodes, NodeConfig{
				URI: normalizedLine,
			})
		}
	}

	return nodes, nil
}

// isBase64 checks if a string looks like base64 encoded content (optimized version)
func isBase64(s string) bool {
	// Remove whitespace
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return false
	}

	// Remove newlines for checking
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")

	// Quick check: if it contains proxy URI schemes, it's not base64
	if strings.Contains(s, "://") {
		return false
	}

	// Check character set - base64 only contains A-Za-z0-9+/=
	// This is much faster than trying to decode
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=') {
			return false
		}
	}

	// Length must be multiple of 4 (with padding)
	return len(s)%4 == 0
}

// IsProxyURI checks if a string is a valid proxy URI
func IsProxyURI(s string) bool {
	normalized := strings.TrimSpace(NormalizeProxyURIInput(s, "http"))
	lower := strings.ToLower(normalized)
	if normalized == "" {
		return false
	}
	for _, scheme := range supportedProxyURISchemes {
		if strings.HasPrefix(lower, scheme) {
			return looksLikeProxyURI(normalized)
		}
	}
	return false
}

func looksLikeProxyURI(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "socks5":
		if strings.ContainsAny(raw, "<> \t\r\n") {
			return false
		}
		host := strings.TrimSpace(parsed.Hostname())
		if host == "" || strings.ContainsAny(host, "<> \t\r\n") {
			return false
		}
		if port := strings.TrimSpace(parsed.Port()); port != "" {
			if _, err := strconv.Atoi(port); err != nil {
				return false
			}
		}
		return true
	case "vmess", "vless", "trojan", "ss", "ssr", "hysteria", "hysteria2", "hy2", "anytls":
		return true
	default:
		return false
	}
}

// clashConfig represents a minimal Clash configuration for parsing proxies
type clashConfig struct {
	Proxies []clashProxy `yaml:"proxies"`
}

type clashProxy struct {
	Name              string                 `yaml:"name"`
	Type              string                 `yaml:"type"`
	Server            string                 `yaml:"server"`
	Port              int                    `yaml:"port"`
	UUID              string                 `yaml:"uuid"`
	Password          string                 `yaml:"password"`
	Cipher            string                 `yaml:"cipher"`
	AlterId           int                    `yaml:"alterId"`
	Network           string                 `yaml:"network"`
	TLS               bool                   `yaml:"tls"`
	SkipCertVerify    bool                   `yaml:"skip-cert-verify"`
	ServerName        string                 `yaml:"servername"`
	SNI               string                 `yaml:"sni"`
	Flow              string                 `yaml:"flow"`
	UDP               bool                   `yaml:"udp"`
	WSOpts            *clashWSOptions        `yaml:"ws-opts"`
	GrpcOpts          *clashGrpcOptions      `yaml:"grpc-opts"`
	RealityOpts       *clashRealityOptions   `yaml:"reality-opts"`
	ClientFingerprint string                 `yaml:"client-fingerprint"`
	Plugin            string                 `yaml:"plugin"`
	PluginOpts        map[string]interface{} `yaml:"plugin-opts"`
}

type clashWSOptions struct {
	Path    string            `yaml:"path"`
	Headers map[string]string `yaml:"headers"`
}

type clashGrpcOptions struct {
	GrpcServiceName string `yaml:"grpc-service-name"`
}

type clashRealityOptions struct {
	PublicKey string `yaml:"public-key"`
	ShortID   string `yaml:"short-id"`
}

// parseClashYAML parses Clash YAML format and converts to NodeConfig
func parseClashYAML(content string) ([]NodeConfig, error) {
	var clash clashConfig
	if err := yaml.Unmarshal([]byte(content), &clash); err != nil {
		return nil, fmt.Errorf("parse clash yaml: %w", err)
	}

	var nodes []NodeConfig
	for _, proxy := range clash.Proxies {
		uri := convertClashProxyToURI(proxy)
		if uri != "" {
			nodes = append(nodes, NodeConfig{
				Name: proxy.Name,
				URI:  uri,
			})
		}
	}

	return nodes, nil
}

// convertClashProxyToURI converts a Clash proxy config to a standard URI
func convertClashProxyToURI(p clashProxy) string {
	switch strings.ToLower(p.Type) {
	case "vmess":
		return buildVMessURI(p)
	case "vless":
		return buildVLESSURI(p)
	case "trojan":
		return buildTrojanURI(p)
	case "ss", "shadowsocks":
		return buildShadowsocksURI(p)
	case "hysteria2", "hy2":
		return buildHysteria2URI(p)
	case "anytls":
		return buildAnyTLSURI(p)
	default:
		return ""
	}
}

func buildVMessURI(p clashProxy) string {
	params := url.Values{}
	if transport, ok := NormalizeV2RayTransport(p.Network); ok {
		if transport != "" {
			params.Set("type", transport)
		}
	} else {
		return ""
	}
	if p.TLS {
		params.Set("security", "tls")
		if p.ServerName != "" {
			params.Set("sni", p.ServerName)
		} else if p.SNI != "" {
			params.Set("sni", p.SNI)
		}
	}
	if p.WSOpts != nil {
		if p.WSOpts.Path != "" {
			params.Set("path", p.WSOpts.Path)
		}
		if host, ok := p.WSOpts.Headers["Host"]; ok {
			params.Set("host", host)
		}
	}
	if p.ClientFingerprint != "" {
		params.Set("fp", p.ClientFingerprint)
	}

	query := ""
	if len(params) > 0 {
		query = "?" + params.Encode()
	}

	return fmt.Sprintf("vmess://%s@%s:%d%s#%s", p.UUID, p.Server, p.Port, query, url.QueryEscape(p.Name))
}

func buildVLESSURI(p clashProxy) string {
	params := url.Values{}
	params.Set("encryption", "none")

	if transport, ok := NormalizeV2RayTransport(p.Network); ok {
		if transport != "" {
			params.Set("type", transport)
		}
	} else {
		return ""
	}
	if p.Flow != "" {
		params.Set("flow", p.Flow)
	}
	if p.TLS {
		params.Set("security", "tls")
		if p.ServerName != "" {
			params.Set("sni", p.ServerName)
		} else if p.SNI != "" {
			params.Set("sni", p.SNI)
		}
	}
	if p.RealityOpts != nil {
		params.Set("security", "reality")
		if p.RealityOpts.PublicKey != "" {
			params.Set("pbk", p.RealityOpts.PublicKey)
		}
		if p.RealityOpts.ShortID != "" {
			params.Set("sid", p.RealityOpts.ShortID)
		}
		if p.ServerName != "" {
			params.Set("sni", p.ServerName)
		}
	}
	if p.WSOpts != nil {
		if p.WSOpts.Path != "" {
			params.Set("path", p.WSOpts.Path)
		}
		if host, ok := p.WSOpts.Headers["Host"]; ok {
			params.Set("host", host)
		}
	}
	if p.GrpcOpts != nil && p.GrpcOpts.GrpcServiceName != "" {
		params.Set("serviceName", p.GrpcOpts.GrpcServiceName)
	}
	if p.ClientFingerprint != "" {
		params.Set("fp", p.ClientFingerprint)
	}

	return fmt.Sprintf("vless://%s@%s:%d?%s#%s", p.UUID, p.Server, p.Port, params.Encode(), url.QueryEscape(p.Name))
}

func buildTrojanURI(p clashProxy) string {
	params := url.Values{}
	if transport, ok := NormalizeV2RayTransport(p.Network); ok {
		if transport != "" {
			params.Set("type", transport)
		}
	} else {
		return ""
	}
	if p.ServerName != "" {
		params.Set("sni", p.ServerName)
	} else if p.SNI != "" {
		params.Set("sni", p.SNI)
	}
	if p.SkipCertVerify {
		params.Set("allowInsecure", "1")
	}
	if p.WSOpts != nil {
		if p.WSOpts.Path != "" {
			params.Set("path", p.WSOpts.Path)
		}
		if host, ok := p.WSOpts.Headers["Host"]; ok {
			params.Set("host", host)
		}
	}
	if p.ClientFingerprint != "" {
		params.Set("fp", p.ClientFingerprint)
	}

	query := ""
	if len(params) > 0 {
		query = "?" + params.Encode()
	}

	return fmt.Sprintf("trojan://%s@%s:%d%s#%s", p.Password, p.Server, p.Port, query, url.QueryEscape(p.Name))
}

func buildShadowsocksURI(p clashProxy) string {
	// Encode method:password in base64
	userInfo := base64.StdEncoding.EncodeToString([]byte(p.Cipher + ":" + p.Password))
	return fmt.Sprintf("ss://%s@%s:%d#%s", userInfo, p.Server, p.Port, url.QueryEscape(p.Name))
}

func buildHysteria2URI(p clashProxy) string {
	params := url.Values{}
	if p.ServerName != "" {
		params.Set("sni", p.ServerName)
	} else if p.SNI != "" {
		params.Set("sni", p.SNI)
	}
	if p.SkipCertVerify {
		params.Set("insecure", "1")
	}

	query := ""
	if len(params) > 0 {
		query = "?" + params.Encode()
	}

	return fmt.Sprintf("hysteria2://%s@%s:%d%s#%s", p.Password, p.Server, p.Port, query, url.QueryEscape(p.Name))
}

func buildAnyTLSURI(p clashProxy) string {
	params := url.Values{}
	if p.ServerName != "" {
		params.Set("sni", p.ServerName)
	} else if p.SNI != "" {
		params.Set("sni", p.SNI)
	}
	if p.SkipCertVerify {
		params.Set("insecure", "1")
	}

	query := ""
	if len(params) > 0 {
		query = "?" + params.Encode()
	}

	return fmt.Sprintf("anytls://%s@%s:%d%s#%s", p.Password, p.Server, p.Port, query, url.QueryEscape(p.Name))
}

// RLock acquires a read lock on the config.
func (c *Config) RLock() {
	if c != nil {
		c.mu.RLock()
	}
}

// RUnlock releases the read lock on the config.
func (c *Config) RUnlock() {
	if c != nil {
		c.mu.RUnlock()
	}
}

// Lock acquires a write lock on the config.
func (c *Config) Lock() {
	if c != nil {
		c.mu.Lock()
	}
}

// Unlock releases the write lock on the config.
func (c *Config) Unlock() {
	if c != nil {
		c.mu.Unlock()
	}
}

// Clone creates a deep copy of the Config (without the mutex).
// The caller must hold at least a read lock if the config may be modified concurrently.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	cloned := *c
	cloned.mu = sync.RWMutex{} // fresh mutex for the clone

	// Deep copy slices
	if c.Nodes != nil {
		cloned.Nodes = make([]NodeConfig, len(c.Nodes))
		copy(cloned.Nodes, c.Nodes)
	}
	if c.Connectors != nil {
		cloned.Connectors = make([]ConnectorSourceConfig, len(c.Connectors))
		for idx, connector := range c.Connectors {
			cloned.Connectors[idx] = connector
			if connector.ConnectorConfig != nil {
				cloned.Connectors[idx].ConnectorConfig = make(map[string]any, len(connector.ConnectorConfig))
				for key, value := range connector.ConnectorConfig {
					cloned.Connectors[idx].ConnectorConfig[key] = value
				}
			}
		}
	}
	if c.Subscriptions != nil {
		cloned.Subscriptions = make([]string, len(c.Subscriptions))
		copy(cloned.Subscriptions, c.Subscriptions)
	}
	if c.SourceSync.FallbackSubscriptions != nil {
		cloned.SourceSync.FallbackSubscriptions = make([]string, len(c.SourceSync.FallbackSubscriptions))
		copy(cloned.SourceSync.FallbackSubscriptions, c.SourceSync.FallbackSubscriptions)
	}
	return &cloned
}

// FilePath returns the config file path.
func (c *Config) FilePath() string {
	if c == nil {
		return ""
	}
	return c.filePath
}

// SetFilePath sets the config file path (used when creating config programmatically).
func (c *Config) SetFilePath(path string) {
	if c != nil {
		c.filePath = path
	}
}

// NOTE: ManualNodesFilePath, LoadManualNodes, SaveManualNodes, writeNodesToFile,
// SaveNodes, Save have been removed. All node persistence is now handled by
// the SQLite Store (internal/store package).

// SaveSettings persists all editable settings to config.yaml.
// Node data is managed by the SQLite Store, not config.yaml.
// The caller must hold the config lock (c.mu) before calling this method.
func (c *Config) SaveSettings() error {
	if c == nil {
		return errors.New("config is nil")
	}
	if c.filePath == "" {
		return errors.New("config file path is unknown")
	}

	// Build a clean config struct for serialization.
	// We read the existing YAML to preserve fields not managed by the settings API
	// (e.g., nodes, nodes_file, database_path).
	data, err := os.ReadFile(c.filePath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var saveCfg Config
	if err := yaml.Unmarshal(data, &saveCfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	// Sync all editable fields from runtime config
	saveCfg.Mode = c.Mode
	saveCfg.LogLevel = c.LogLevel
	saveCfg.ExternalIP = c.ExternalIP
	saveCfg.SkipCertVerify = c.SkipCertVerify

	// Listener
	saveCfg.Listener = c.Listener

	// Multi-port
	saveCfg.MultiPort = c.MultiPort

	// Pool
	saveCfg.Pool = c.Pool

	// Management
	saveCfg.Management = c.Management

	// Subscription refresh
	saveCfg.SubscriptionRefresh = c.SubscriptionRefresh

	// Source sync
	saveCfg.SourceSync = c.SourceSync

	// GeoIP
	saveCfg.GeoIP = c.GeoIP

	// Connectors
	saveCfg.Connectors = c.Connectors

	// Subscriptions
	saveCfg.Subscriptions = c.Subscriptions

	newData, err := yaml.Marshal(&saveCfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	// Use atomic write to prevent data loss from concurrent/interrupted writes
	if err := writeFileAtomic(c.filePath, newData, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// ValidateSettingsRequest validates the settings request fields and returns
// a descriptive error for any invalid values, instead of silently ignoring them.
func ValidateSettingsRequest(mode string, listenerPort, multiPortBasePort uint16,
	listenerProtocol, multiPortProtocol, poolMode,
	poolBlacklistDuration, subRefreshInterval, subRefreshTimeout,
	subRefreshHealthCheckTimeout, subRefreshDrainTimeout,
	sourceSyncRefreshInterval, sourceSyncRequestTimeout,
	geoIPAutoUpdateInterval, managementHealthCheckInterval string) error {

	// Validate mode
	switch mode {
	case "pool", "multi-port", "hybrid":
	default:
		return fmt.Errorf("不支持的运行模式: %q", mode)
	}

	switch strings.ToLower(strings.TrimSpace(poolMode)) {
	case "auto", "sequential", "random", "balance":
	default:
		return fmt.Errorf("不支持的代理池调度模式: %q", poolMode)
	}

	// Validate ports
	if listenerPort == 0 {
		return fmt.Errorf("监听端口不能为 0")
	}
	if multiPortBasePort == 0 {
		return fmt.Errorf("多端口起始端口不能为 0")
	}

	// Validate inbound protocols
	if _, err := NormalizeInboundProtocol(listenerProtocol); err != nil {
		return fmt.Errorf("监听入口协议无效: %w", err)
	}
	if _, err := NormalizeInboundProtocol(multiPortProtocol); err != nil {
		return fmt.Errorf("多端口入口协议无效: %w", err)
	}

	// Validate duration fields
	durationFields := []struct {
		name  string
		value string
	}{
		{"黑名单持续时间", poolBlacklistDuration},
		{"订阅刷新间隔", subRefreshInterval},
		{"订阅获取超时", subRefreshTimeout},
		{"健康检查超时", subRefreshHealthCheckTimeout},
		{"排空超时", subRefreshDrainTimeout},
		{"Source Sync 刷新间隔", sourceSyncRefreshInterval},
		{"Source Sync 请求超时", sourceSyncRequestTimeout},
		{"GeoIP 更新间隔", geoIPAutoUpdateInterval},
		{"周期健康检查间隔", managementHealthCheckInterval},
	}

	for _, field := range durationFields {
		if field.value != "" {
			d, err := time.ParseDuration(field.value)
			if err != nil {
				return fmt.Errorf("%s 格式无效: %q (%v)", field.name, field.value, err)
			}
			if d <= 0 {
				return fmt.Errorf("%s 必须大于 0: %q", field.name, field.value)
			}
		}
	}

	return nil
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

// writeFileAtomic writes data to a file atomically by writing to a temporary
// file first and then renaming it. This prevents data loss if the process is
// interrupted or if concurrent reads occur during the write.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	// Create temp file in same directory (required for atomic rename on same filesystem)
	tmpFile, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Clean up temp file on error
	success := false
	defer func() {
		if !success {
			tmpFile.Close()
			os.Remove(tmpPath)
		}
	}()

	// Write data to temp file
	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	// Sync to disk before rename
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}

	// Close before rename (required on Windows)
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Set permissions
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		// 当 config.yaml 通过 bind-mount 映射到宿主机时，部分宿主机文件系统/挂载模式会导致
		// 对“正在被挂载使用的目标文件”执行 rename 返回 EBUSY（device or resource busy）。
		// 这种情况下回退为“直接覆盖写入”（非原子，但可用），以保证 WebUI 能保存配置。
		if errors.Is(err, syscall.EBUSY) {
			data, rerr := os.ReadFile(tmpPath)
			if rerr != nil {
				return fmt.Errorf("rename temp file: %w", err)
			}
			if werr := os.WriteFile(path, data, perm); werr != nil {
				return fmt.Errorf("rename temp file: %w", err)
			}
			if cerr := os.Remove(tmpPath); cerr != nil {
				// best-effort cleanup
			}
			return nil
		}
		return fmt.Errorf("rename temp file: %w", err)
	}

	success = true
	return nil
}
