package monitor

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// Config mirrors user settings needed by the monitoring server.
type Config struct {
	Enabled        bool
	Listen         string
	ProbeTarget    string
	ProbeTargets   []string
	Password       string
	ProxyUsername  string // 代理池的用户名（用于导出）
	ProxyPassword  string // 代理池的密码（用于导出）
	ExternalIP     string // 外部 IP 地址，用于导出时替换 0.0.0.0
	SkipCertVerify bool   // 全局跳过 SSL 证书验证

	// LongLivedMinUptime / LongLivedMinSuccessRate control when a node is
	// considered "long-lived" (stable enough for sticky/anti-ban use). Zero
	// values fall back to defaults (2h / 0.9).
	LongLivedMinUptime      time.Duration
	LongLivedMinSuccessRate float64
}

// Generation identifies one node-registration epoch across box reloads.
type Generation uint64

// ProbeSummary is the result of a synchronous health round for one generation.
type ProbeSummary struct {
	Generation Generation
	Total      int
	Completed  int
	Available  int
	Failed     int
}

// ErrStaleGeneration reports a probe request or completion superseded by a
// newer reload generation.
var ErrStaleGeneration = errors.New("stale monitor generation")

type ProbeTargetSpec struct {
	Original string
	Scheme   string
	Host     string
	Port     uint16
	Path     string
	HostHdr  string
	Dst      M.Socksaddr
}

// NodeInfo is static metadata about a proxy entry.
type NodeInfo struct {
	Tag            string `json:"tag"`
	Name           string `json:"name"`
	URI            string `json:"uri"`
	Mode           string `json:"mode"`
	ListenAddress  string `json:"listen_address,omitempty"`
	Port           uint16 `json:"port,omitempty"`
	Region         string `json:"region,omitempty"`  // GeoIP region code: "jp", "kr", "us", "hk", "tw", "other"
	Country        string `json:"country,omitempty"` // Full country name from GeoIP
	SourceKind     string `json:"source_kind,omitempty"`
	SourceName     string `json:"source_name,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
	ProtocolFamily string `json:"protocol_family,omitempty"`
	NodeMode       string `json:"node_mode,omitempty"`
	DomainFamily   string `json:"domain_family,omitempty"`
}

// TimelineEvent represents a single usage event for debug tracking.
type TimelineEvent struct {
	Time        time.Time `json:"time"`
	Success     bool      `json:"success"`
	LatencyMs   int64     `json:"latency_ms"`
	Error       string    `json:"error,omitempty"`
	Destination string    `json:"destination,omitempty"`
}

const maxTimelineSize = 20

// Long-lived (长效) judgement defaults. A node is considered long-lived when it
// has been continuously known for at least defaultLongLivedMinUptime and its
// reported success rate is at least defaultLongLivedMinSuccessRate, while it is
// currently effectively available.
const (
	defaultLongLivedMinUptime      = 2 * time.Hour
	defaultLongLivedMinSuccessRate = 0.9
	// minimum reported samples before success-rate gating applies, avoids a
	// single early success marking a node long-lived prematurely.
	longLivedMinSamples = 5
)

// Snapshot is a runtime view of a proxy node.
type Snapshot struct {
	NodeInfo
	FailureCount              int             `json:"failure_count"`
	SuccessCount              int64           `json:"success_count"`
	TrafficSuccessCount       int64           `json:"traffic_success_count"`
	Blacklisted               bool            `json:"blacklisted"`
	BlacklistedUntil          time.Time       `json:"blacklisted_until"`
	AvailabilityScore         int             `json:"availability_score"`
	ReportedSuccessCount      int64           `json:"reported_success_count"`
	ReportedFailureCount      int64           `json:"reported_failure_count"`
	ConsecutiveReportFailures int             `json:"consecutive_report_failures"`
	ActiveConnections         int32           `json:"active_connections"`
	LastError                 string          `json:"last_error,omitempty"`
	LastFailure               time.Time       `json:"last_failure,omitempty"`
	LastSuccess               time.Time       `json:"last_success,omitempty"`
	LastTrafficSuccessAt      time.Time       `json:"last_traffic_success_at,omitempty"`
	LastReportedAt            time.Time       `json:"last_reported_at,omitempty"`
	LastReportedSuccess       bool            `json:"last_reported_success"`
	LastProbeAt               time.Time       `json:"last_probe_at,omitempty"`
	LastProbeSuccessAt        time.Time       `json:"last_probe_success_at,omitempty"`
	LastProbeLatency          time.Duration   `json:"last_probe_latency,omitempty"`
	LastLatencyMs             int64           `json:"last_latency_ms"`
	Available                 bool            `json:"available"`
	InitialCheckDone          bool            `json:"initial_check_done"`
	TrafficProvenUsable       bool            `json:"traffic_proven_usable"`
	EffectiveAvailable        bool            `json:"effective_available"`
	AvailabilitySource        string          `json:"availability_source,omitempty"`
	LongLived                 bool            `json:"long_lived"`
	Uptime                    time.Duration   `json:"-"`
	UptimeSeconds             int64           `json:"uptime_seconds"`
	TotalUpload               int64           `json:"total_upload"`
	TotalDownload             int64           `json:"total_download"`
	UploadSpeed               int64           `json:"upload_speed"`   // bytes/sec
	DownloadSpeed             int64           `json:"download_speed"` // bytes/sec
	TrafficSuccessSeq         int64           `json:"-"`
	FailureSeq                int64           `json:"-"`
	Timeline                  []TimelineEvent `json:"timeline,omitempty"`
}

// PersistedState carries node runtime state restored from durable storage.
type PersistedState struct {
	FailureCount         int
	SuccessCount         int64
	TrafficSuccessCount  int64
	Blacklisted          bool
	BlacklistedUntil     time.Time
	LastError            string
	LastFailureAt        time.Time
	LastSuccessAt        time.Time
	LastTrafficSuccessAt time.Time
	LastProbeAt          time.Time
	LastProbeSuccessAt   time.Time
	LastLatencyMs        int64
	Available            bool
	InitialCheckDone     bool
	TotalUpload          int64
	TotalDownload        int64
}

type NodeTrafficSpeed struct {
	Tag           string `json:"tag"`
	UploadSpeed   int64  `json:"upload_speed"`   // bytes/sec
	DownloadSpeed int64  `json:"download_speed"` // bytes/sec
	TotalUpload   int64  `json:"total_upload"`
	TotalDownload int64  `json:"total_download"`
}

type TrafficSummary struct {
	NodeCount     int                `json:"node_count"`
	TotalUpload   int64              `json:"total_upload"`
	TotalDownload int64              `json:"total_download"`
	UploadSpeed   int64              `json:"upload_speed"`   // bytes/sec
	DownloadSpeed int64              `json:"download_speed"` // bytes/sec
	Nodes         []NodeTrafficSpeed `json:"nodes,omitempty"`
	SampledAt     time.Time          `json:"sampled_at"`
}

type SourceSelectionState struct {
	Ref                string `json:"ref"`
	Name               string `json:"name,omitempty"`
	Kind               string `json:"kind,omitempty"`
	TotalNodes         int    `json:"total_nodes"`
	HealthyNodes       int    `json:"healthy_nodes"`
	StructuralFailures int    `json:"structural_failures"`
	Penalty            int    `json:"penalty"`
	Excluded           bool   `json:"excluded"`
	Reason             string `json:"reason,omitempty"`
}

const (
	SelectionDimensionProtocolFamily = "protocol_family"
	SelectionDimensionNodeMode       = "node_mode"
	SelectionDimensionDomainFamily   = "domain_family"
)

type SecondarySelectionState struct {
	Key                string `json:"key"`
	SourceRef          string `json:"source_ref"`
	SourceName         string `json:"source_name,omitempty"`
	SourceKind         string `json:"source_kind,omitempty"`
	Dimension          string `json:"dimension"`
	Value              string `json:"value"`
	TotalNodes         int    `json:"total_nodes"`
	HealthyNodes       int    `json:"healthy_nodes"`
	StructuralFailures int    `json:"structural_failures"`
	Penalty            int    `json:"penalty"`
	Excluded           bool   `json:"excluded"`
	Reason             string `json:"reason,omitempty"`
}

type SourceHealthState struct {
	Ref                     string `json:"ref"`
	Name                    string `json:"name,omitempty"`
	Kind                    string `json:"kind,omitempty"`
	TotalNodes              int    `json:"total_nodes"`
	EffectiveAvailableNodes int    `json:"effective_available_nodes"`
	ProbeAvailableNodes     int    `json:"probe_available_nodes"`
	TrafficProvenNodes      int    `json:"traffic_proven_nodes"`
	BlacklistedNodes        int    `json:"blacklisted_nodes"`
	PendingNodes            int    `json:"pending_nodes"`
	UnavailableNodes        int    `json:"unavailable_nodes"`
	StructuralFailures      int    `json:"structural_failures"`
	SelectionPenalty        int    `json:"selection_penalty"`
	SelectionExcluded       bool   `json:"selection_excluded"`
	SelectionReason         string `json:"selection_reason,omitempty"`
}

func SecondarySelectionStateKey(sourceRef, dimension, value string) string {
	return strings.TrimSpace(sourceRef) + "|" + strings.TrimSpace(dimension) + "|" + strings.TrimSpace(value)
}

type probeFunc func(ctx context.Context) (time.Duration, error)
type releaseFunc func()

type EntryHandle struct {
	ref *entry
}

type entry struct {
	owner              *Manager
	info               NodeInfo
	failure            int
	success            int64
	reportSuccess      int64
	reportFailure      int64
	reportFailures     int
	feedbackPenalty    int
	lastReportedAt     time.Time
	lastReportOK       bool
	timeline           []TimelineEvent
	blacklist          bool
	until              time.Time
	lastError          string
	lastFail           time.Time
	lastOK             time.Time
	lastTrafficOK      time.Time
	lastTrafficSeq     int64
	lastFailureSeq     int64
	eventSeq           int64
	lastProbeAt        time.Time
	lastProbeOK        time.Time
	lastProbe          time.Duration
	lastProbeSeq       uint64
	trafficSuccess     int64
	active             atomic.Int32
	totalUpload        atomic.Int64
	totalDownload      atomic.Int64
	uploadSpeed        int64
	downloadSpeed      int64
	lastSpeedUpload    int64
	lastSpeedDown      int64
	lastSpeedAt        time.Time
	probe              probeFunc
	probeRevision      uint64
	release            releaseFunc
	initialCheckDone   bool
	available          bool
	firstSeenAt        time.Time     // first registration time, used for long-lived uptime
	longLivedMinUptime time.Duration // resolved long-lived min uptime threshold
	longLivedMinRate   float64       // resolved long-lived min success-rate threshold
	reloadGen          Generation    // generation counter to track active registrations
	healthGen          Generation    // generation that produced current probe availability
	mu                 sync.RWMutex
}

type periodicProbeRound struct {
	id         uint64
	generation Generation
	probeEpoch uint64
	probeSeq   uint64
	gateRev    uint64
	cancel     context.CancelFunc
	done       chan struct{}
}

// Manager aggregates all node states for the UI/API.
type Manager struct {
	cfg        Config
	reloadGen  Generation // current reload generation
	probeDst   M.Socksaddr
	probeSpecs []ProbeTargetSpec
	probeReady bool
	mu         sync.RWMutex
	nodes      map[string]*entry
	ctx        context.Context
	cancel     context.CancelFunc
	logger     Logger

	initialProbeMu   sync.Mutex
	initialProbeDone bool
	initialProbeCh   chan struct{}
	initialProbeRev  uint64

	// periodic health check control
	healthMu        sync.Mutex
	healthInterval  time.Duration
	healthTimeout   time.Duration
	healthTicker    *time.Ticker
	healthIntervalC chan time.Duration
	healthStarted   bool

	probeEpoch   atomic.Uint64
	nextProbeSeq atomic.Uint64
	probeRoundMu sync.Mutex
	probeRound   *periodicProbeRound
	exclusive    *periodicProbeRound
	exclusiveMu  sync.Mutex
	nextRoundID  atomic.Uint64
	activeRound  uint64 // protected by mu
}

// Logger interface for logging
type Logger interface {
	Info(args ...any)
	Warn(args ...any)
}

// NewManager constructs a manager and pre-validates the probe target.
func NewManager(cfg Config) (*Manager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		cfg:              cfg,
		nodes:            make(map[string]*entry),
		ctx:              ctx,
		cancel:           cancel,
		initialProbeCh:   make(chan struct{}),
		initialProbeDone: false,
		initialProbeRev:  1,
	}
	if specs, err := parseProbeTargets(cfg.ProbeTargets, cfg.ProbeTarget); err == nil && len(specs) > 0 {
		m.probeSpecs = specs
		m.probeDst = specs[0].Dst
		m.probeReady = true
	}
	go m.startTrafficSpeedSampler()
	return m, nil
}

func parseProbeTargets(targets []string, single string) ([]ProbeTargetSpec, error) {
	rawTargets := make([]string, 0, len(targets)+1)
	for _, target := range targets {
		for _, part := range strings.FieldsFunc(target, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				rawTargets = append(rawTargets, trimmed)
			}
		}
	}
	if len(rawTargets) == 0 {
		for _, part := range strings.FieldsFunc(single, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				rawTargets = append(rawTargets, trimmed)
			}
		}
	}

	specs := make([]ProbeTargetSpec, 0, len(rawTargets))
	seen := make(map[string]struct{}, len(rawTargets))
	for _, raw := range rawTargets {
		spec, err := parseProbeTarget(raw)
		if err != nil {
			return nil, err
		}
		key := spec.Scheme + "://" + spec.HostHdr + spec.Path
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		specs = append(specs, spec)
	}
	return specs, nil
}

func parseProbeTarget(raw string) (ProbeTargetSpec, error) {
	original := strings.TrimSpace(raw)
	if original == "" {
		return ProbeTargetSpec{}, errors.New("empty probe target")
	}
	if !strings.Contains(original, "://") {
		original = "https://" + original
	}
	parsed, err := url.Parse(original)
	if err != nil {
		return ProbeTargetSpec{}, fmt.Errorf("parse probe target %q: %w", raw, err)
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme == "" {
		scheme = "https"
	}
	if scheme != "http" && scheme != "https" && scheme != "tcp" {
		return ProbeTargetSpec{}, fmt.Errorf("unsupported probe target scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return ProbeTargetSpec{}, fmt.Errorf("probe target missing host: %q", raw)
	}
	port := parsed.Port()
	if port == "" {
		if scheme == "https" || scheme == "tcp" {
			port = "443"
		} else {
			port = "80"
		}
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	return ProbeTargetSpec{
		Original: strings.TrimSpace(raw),
		Scheme:   scheme,
		Host:     host,
		Port:     parsePort(port),
		Path:     path,
		HostHdr:  parsed.Host,
		Dst:      M.ParseSocksaddrHostPort(host, parsePort(port)),
	}, nil
}

// SetLogger sets the logger for the manager.
func (m *Manager) SetLogger(logger Logger) {
	m.logger = logger
}

// StartPeriodicHealthCheck starts a background goroutine that periodically checks all nodes.
// interval: how often to check (e.g., 30 * time.Second)
// timeout: timeout for each probe (e.g., 10 * time.Second)
