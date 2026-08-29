package config

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const maxSubscriptionBodyBytes = 10 * 1024 * 1024

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
	DNS                 DNSConfig                 `yaml:"dns"`              // sing-box DNS 路由，避免使用受污染的系统解析
	DatabasePath        string                    `yaml:"database_path"`    // SQLite 数据库路径，默认 data/data.db
	ExtraListeners      []ExtraListenerConfig     `yaml:"extra_listeners"`  // 额外监听端口（不同选择策略）
	LocalServer         LocalServerConfig         `yaml:"local_server"`     // 局域网本地服务器统一入口
	Routing             RoutingConfig             `yaml:"routing"`          // 智能分流 + 多策略选节点入口
	Gateway             GatewayConfig             `yaml:"gateway"`          // 原生透明网关入口

	filePath string `yaml:"-"` // 配置文件路径，用于保存
}

// RoutingConfig controls the smart dispatch entry: rule-based traffic splitting
// (DIRECT vs PROXY) plus strategy-based node selection (stable / session /
// attribute filters). When disabled the runtime behaves exactly as before: the
// plain pool inbound serves all traffic with no splitting.
type RoutingConfig struct {
	Enabled         bool                    `yaml:"enabled"`           // 是否启用智能分流入口（默认关闭，保持旧行为）
	Listen          string                  `yaml:"listen"`            // 智能入口监听地址，默认接管 listener 的 host:port
	DefaultStrategy string                  `yaml:"default_strategy"`  // 默认入口策略：stable / session / auto（默认 stable）
	UseDefaultRules *bool                   `yaml:"use_default_rules"` // 是否附加内置“中国直连”默认规则集（默认 true）
	FinalPolicy     string                  `yaml:"final_policy"`      // 兜底策略：DIRECT / PROXY（默认 PROXY）
	Rules           []string                `yaml:"rules"`             // 自定义分流规则，按顺序优先于默认集
	RuleFiles       []string                `yaml:"rule_files"`        // 本地规则文件，按声明顺序加载
	RuleProviders   []RuleProvider          `yaml:"rule_providers"`    // 远程规则集
	NodeFilter      RoutingNodeFilterConfig `yaml:"node_filter"`       // 节点筛选条件
	LongLived       LongLivedConfig         `yaml:"long_lived"`        // 长效节点判定阈值
	Session         SessionConfig           `yaml:"session"`           // 会话粘性参数
}

// GatewayConfig controls the provider-neutral transparent ingress. Overlay
// products only contribute interfaces, routes, and source CIDRs; EasyProxy
// does not depend on their APIs or names.
type GatewayConfig struct {
	Enabled bool                           `yaml:"enabled"`
	Mode    string                         `yaml:"mode"`
	Listen  string                         `yaml:"listen"`
	Ingress GatewayIngressConfig           `yaml:"ingress"`
	Capture GatewayCaptureConfig           `yaml:"capture"`
	Routing GatewayRoutingConfig           `yaml:"routing"`
	DNS     GatewayDNSConfig               `yaml:"dns"`
	Tun     GatewayTunConfig               `yaml:"tun"`
	Devices map[string]GatewayDeviceConfig `yaml:"devices"`
}

type GatewayIngressConfig struct {
	Interfaces        []string `yaml:"interfaces"`
	InterfacePatterns []string `yaml:"interface_patterns"`
	TrustedCIDRs      []string `yaml:"trusted_cidrs"`
}

type GatewayCaptureConfig struct {
	TCP                         string `yaml:"tcp"`
	UDP                         string `yaml:"udp"`
	PreserveOriginalDestination bool   `yaml:"preserve_original_destination"`
}

type GatewayRoutingConfig struct {
	FinalPolicy            string `yaml:"final_policy"`
	NoAvailableProxyPolicy string `yaml:"no_available_proxy_policy"`
}

type GatewayDNSConfig struct {
	Enabled bool   `yaml:"enabled"`
	Listen  string `yaml:"listen"`
}

type GatewayTunConfig struct {
	InterfaceName string   `yaml:"interface_name"`
	Addresses     []string `yaml:"addresses"`
	Stack         string   `yaml:"stack"`
	MTU           uint32   `yaml:"mtu"`
	IPv4          bool     `yaml:"ipv4"`
	IPv6          bool     `yaml:"ipv6"`
	UDP           bool     `yaml:"udp"`
	StrictRoute   bool     `yaml:"strict_route"`
	DNSHijack     bool     `yaml:"dns_hijack"`
	FakeIP        bool     `yaml:"fake_ip"`
	FakeIPv4Range string   `yaml:"fake_ipv4_range"`
	FakeIPv6Range string   `yaml:"fake_ipv6_range"`

	ipv4Set        bool `yaml:"-"`
	udpSet         bool `yaml:"-"`
	strictRouteSet bool `yaml:"-"`
}

func (c *GatewayTunConfig) UnmarshalYAML(value *yaml.Node) error {
	type plain GatewayTunConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*c = GatewayTunConfig(decoded)
	for idx := 0; idx+1 < len(value.Content); idx += 2 {
		switch value.Content[idx].Value {
		case "ipv4":
			c.ipv4Set = true
		case "udp":
			c.udpSet = true
		case "strict_route":
			c.strictRouteSet = true
		}
	}
	return nil
}

type GatewayDeviceConfig struct {
	Addresses []string `yaml:"addresses"`
}

// DNSConfig controls the DNS router used by sing-box outbounds. Proxy
// destinations use encrypted DNS through the proxy pool, while upstream node
// server names are explicitly routed to the local resolver to avoid a DNS
// dependency cycle.
type DNSConfig struct {
	Enabled       *bool    `yaml:"enabled"`
	RemoteServers []string `yaml:"remote_servers"`
	Detour        string   `yaml:"detour"`
	Strategy      string   `yaml:"strategy"`
}

type LocalServerConfig struct {
	Enabled              bool                  `yaml:"enabled"`
	Listen               string                `yaml:"listen"`
	Auth                 LocalServerAuthConfig `yaml:"auth"`
	SharedRevision       int64                 `yaml:"shared_revision"`
	CredentialGeneration uint64                `yaml:"credential_generation"`
}

type LocalServerAuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type RoutingNodeFilterConfig struct {
	Countries []string `yaml:"countries"`
	Regions   []string `yaml:"regions"`
	LongLived *bool    `yaml:"long_lived"`
}

// RuleProvider describes a remote rule list applied with a single policy.
type RuleProvider struct {
	URL      string        `yaml:"url"`
	Policy   string        `yaml:"policy"`   // DIRECT / PROXY
	Behavior string        `yaml:"behavior"` // domain / ipcidr / classical（默认 domain）
	Interval time.Duration `yaml:"interval"` // 刷新间隔（默认 24h）
}

// LongLivedConfig sets when a node is considered "long-lived" (stable enough for
// anti-ban stable strategy). Zero values fall back to defaults (2h / 0.9).
type LongLivedConfig struct {
	MinUptime      time.Duration `yaml:"min_uptime"`       // 最小在线时长（默认 2h）
	MinSuccessRate float64       `yaml:"min_success_rate"` // 最小成功率 0-1（默认 0.9）
}

// SessionConfig controls session stickiness for the session strategy.
type SessionConfig struct {
	TTL time.Duration `yaml:"ttl"` // 会话空闲过期时间（默认 10m）
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

func defaultConfigForDecode() Config {
	return Config{
		GeoIP: GeoIPConfig{
			Enabled:            true,
			DatabasePath:       "./GeoLite2-Country.mmdb",
			AutoUpdateEnabled:  true,
			AutoUpdateInterval: 24 * time.Hour,
		},
	}
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

// Align runtime health checks with Mihomo URL-Test and sing-box URLTest style
// HTTP probes so upstream layers only hand off lines that can pass a real
// external request, not just a raw TCP connect.
var defaultManagementProbeTargets = []string{
	"https://connectivitycheck.gstatic.com/generate_204",
	"https://cp.cloudflare.com/generate_204",
	"https://www.msftconnecttest.com/connecttest.txt",
	"https://www.google.com/generate_204",
	"https://www.google.com/robots.txt",
	"https://www.youtube.com/robots.txt",
}

// DefaultManagementProbeTargets returns the default probe targets used by the
// health checker when the config does not explicitly provide any.
func DefaultManagementProbeTargets() []string {
	return append([]string(nil), defaultManagementProbeTargets...)
}

// SubscriptionRefreshConfig controls subscription auto-refresh and reload settings.
type SubscriptionRefreshConfig struct {
	Enabled            bool          `yaml:"enabled"`              // 是否启用定时刷新
	Interval           time.Duration `yaml:"interval"`             // 刷新间隔，默认 24 小时
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
	FanoutCount      int           `yaml:"fanout_count"`
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

// NormalizeVLESSFlow canonicalizes historical flow values into the subset
// accepted by the current sing-box runtime.
func NormalizeVLESSFlow(value string) string {
	flow := strings.TrimSpace(value)
	normalized := strings.ToLower(flow)
	canonicalCandidate := normalized
	for strings.HasSuffix(canonicalCandidate, "-udp443") {
		canonicalCandidate = strings.TrimSuffix(canonicalCandidate, "-udp443")
	}
	switch normalized {
	case "", "none":
		return ""
	}
	switch canonicalCandidate {
	case "xtls-rprx-vision":
		return "xtls-rprx-vision"
	default:
		return flow
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

type subscriptionCacheEntry struct {
	URL       string       `json:"url"`
	FetchedAt time.Time    `json:"fetched_at"`
	LastError string       `json:"last_error,omitempty"`
	Nodes     []NodeConfig `json:"nodes"`
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
