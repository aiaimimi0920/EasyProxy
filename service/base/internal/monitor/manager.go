package monitor

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"sort"
	"strconv"
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
func (m *Manager) StartPeriodicHealthCheck(interval, timeout time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Hour
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	m.healthMu.Lock()
	if m.healthIntervalC == nil {
		m.healthIntervalC = make(chan time.Duration, 1)
	}
	alreadyStarted := m.healthStarted
	m.healthInterval = interval
	m.healthTimeout = timeout
	intervalC := m.healthIntervalC
	if alreadyStarted {
		m.healthMu.Unlock()
		select {
		case intervalC <- interval:
		default:
		}
		m.mu.RLock()
		probeReady := m.probeReady
		m.mu.RUnlock()
		if probeReady {
			m.RequestProbeAllOnce(timeout)
		}
		return
	}
	if m.healthTicker != nil {
		m.healthTicker.Stop()
	}
	m.healthTicker = time.NewTicker(interval)
	ticker := m.healthTicker
	m.healthStarted = true
	m.healthMu.Unlock()

	m.mu.RLock()
	probeReady := m.probeReady
	m.mu.RUnlock()

	// 启动阶段先同步完成首轮 probe，避免 compat checkout 或 available-only
	// 视图在 effective 节点尚未建立时抢先拿到空结果。
	if probeReady {
		m.probeAllNodes(timeout)
	} else if m.logger != nil {
		m.logger.Warn("probe target not configured, periodic health check is waiting")
	}

	go func() {
		for {
			select {
			case <-m.ctx.Done():
				return
			case newInterval := <-intervalC:
				if newInterval <= 0 {
					newInterval = 2 * time.Hour
				}
				// 重置 ticker
				m.healthMu.Lock()
				m.healthInterval = newInterval
				if m.healthTicker != nil {
					m.healthTicker.Stop()
				}
				m.healthTicker = time.NewTicker(newInterval)
				ticker = m.healthTicker
				m.healthMu.Unlock()
				if m.logger != nil {
					m.logger.Info("periodic health check interval updated: ", newInterval)
				}
			case <-ticker.C:
				m.RequestProbeAllOnce(timeout)
			}
		}
	}()

	if m.logger != nil {
		m.logger.Info("periodic health check started, interval: ", interval)
	}
}

// SetHealthCheckInterval updates the periodic health check interval at runtime.
// It is safe to call before StartPeriodicHealthCheck; it will be applied on start.
func (m *Manager) SetHealthCheckInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	m.healthMu.Lock()
	m.healthInterval = d
	intervalC := m.healthIntervalC
	m.healthMu.Unlock()

	if intervalC != nil {
		select {
		case intervalC <- d:
		default:
			// drop if a newer update is already queued
		}
	}
}

// RequestProbeAllOnce triggers a full periodic probe round. Requests for the
// same generation and probe topology are de-duplicated; a newer generation or
// probe replacement supersedes and drains the older coordinator first.
func (m *Manager) RequestProbeAllOnce(timeout time.Duration) {
	m.probeRoundMu.Lock()
	defer m.probeRoundMu.Unlock()
	if exclusive := m.exclusive; exclusive != nil {
		select {
		case <-exclusive.done:
			m.exclusive = nil
			m.clearActiveRoundLocked(exclusive)
		default:
			// A synchronous generation probe owns the scheduler. The caller will
			// run its own fresh round, so a periodic tick must not compete with it.
			return
		}
	}

	m.mu.RLock()
	ready := m.probeReady
	generation := m.reloadGen
	m.mu.RUnlock()
	if !ready {
		return
	}
	probeEpoch := m.probeEpoch.Load()

	if active := m.probeRound; active != nil {
		select {
		case <-active.done:
			m.probeRound = nil
			m.clearActiveRoundLocked(active)
		default:
			if active.generation == generation && active.probeEpoch == probeEpoch {
				return
			}
			m.invalidateActiveRoundLocked(active)
			active.cancel()
			<-active.done
			if m.probeRound == active {
				m.probeRound = nil
			}
		}
	}

	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	roundCtx, cancel := context.WithCancel(parent)
	round := &periodicProbeRound{
		id:         m.nextRoundID.Add(1),
		generation: generation,
		probeEpoch: probeEpoch,
		probeSeq:   m.nextProbeSeq.Add(1),
		gateRev:    m.currentInitialProbeRev(),
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	m.setActiveRoundLocked(round)
	m.probeRound = round
	go m.runPeriodicProbeRound(roundCtx, round, timeout)
}

// probeAllNodes checks all registered nodes concurrently.
func (m *Manager) probeAllNodes(timeout time.Duration) {
	m.mu.RLock()
	generation := m.reloadGen
	m.mu.RUnlock()
	summary, err := m.ProbeGeneration(m.ctx, generation, timeout)
	if err != nil && !errors.Is(err, ErrStaleGeneration) && m.logger != nil {
		m.logger.Warn("health check failed: ", err)
	}
	if m.logger != nil && !errors.Is(err, ErrStaleGeneration) {
		m.logger.Info("health check completed: ", summary.Available, " available, ", summary.Failed, " failed")
	}
}

func (m *Manager) runPeriodicProbeRound(
	ctx context.Context,
	round *periodicProbeRound,
	timeout time.Duration,
) {
	defer func() {
		round.cancel()
		close(round.done)
		m.probeRoundMu.Lock()
		if m.probeRound == round {
			m.probeRound = nil
		}
		m.clearActiveRoundLocked(round)
		m.probeRoundMu.Unlock()
	}()
	m.probeAllNodesForGeneration(
		ctx,
		round.generation,
		round.id,
		round.probeEpoch,
		round.probeSeq,
		round.gateRev,
		timeout,
	)
}

func (m *Manager) setActiveRoundLocked(round *periodicProbeRound) {
	if round == nil {
		return
	}
	m.mu.Lock()
	m.activeRound = round.id
	m.mu.Unlock()
}

func (m *Manager) invalidateActiveRoundLocked(round *periodicProbeRound) {
	if round == nil {
		return
	}
	m.mu.Lock()
	if m.activeRound == round.id {
		m.activeRound = 0
	}
	m.mu.Unlock()
}

func (m *Manager) clearActiveRoundLocked(round *periodicProbeRound) {
	m.invalidateActiveRoundLocked(round)
}

func (m *Manager) probeAllNodesForGeneration(
	ctx context.Context,
	generation Generation,
	roundID uint64,
	probeEpoch uint64,
	probeSeq uint64,
	gateRev uint64,
	timeout time.Duration,
) {
	summary, err := m.probeGeneration(ctx, generation, roundID, probeEpoch, probeSeq, timeout)
	m.completeInitialProbeGateForRound(generation, roundID, probeEpoch, gateRev, err)
	if err != nil && !errors.Is(err, ErrStaleGeneration) && m.logger != nil {
		m.logger.Warn("health check failed: ", err)
	}
	if m.logger != nil && !errors.Is(err, ErrStaleGeneration) {
		m.logger.Info("health check completed: ", summary.Available, " available, ", summary.Failed, " failed")
	}
}

type generationProbeTask struct {
	tag           string
	entry         *entry
	probe         probeFunc
	probeRevision uint64
}

// ProbeGeneration synchronously probes only entries registered in generation.
// It owns the full-probe scheduler while running: any periodic round is first
// invalidated, cancelled, and drained, and new periodic ticks are ignored until
// this authoritative generation probe completes.
func (m *Manager) ProbeGeneration(
	ctx context.Context,
	generation Generation,
	timeout time.Duration,
) (ProbeSummary, error) {
	m.exclusiveMu.Lock()
	defer m.exclusiveMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	m.probeRoundMu.Lock()
	if active := m.probeRound; active != nil {
		select {
		case <-active.done:
			m.probeRound = nil
			m.clearActiveRoundLocked(active)
		default:
			m.invalidateActiveRoundLocked(active)
			active.cancel()
			<-active.done
			if m.probeRound == active {
				m.probeRound = nil
			}
		}
	}
	roundCtx, cancel := context.WithCancel(ctx)
	round := &periodicProbeRound{
		id:         m.nextRoundID.Add(1),
		generation: generation,
		probeEpoch: m.probeEpoch.Load(),
		probeSeq:   m.nextProbeSeq.Add(1),
		gateRev:    m.currentInitialProbeRev(),
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	m.exclusive = round
	m.setActiveRoundLocked(round)
	m.probeRoundMu.Unlock()

	defer func() {
		cancel()
		close(round.done)
		m.probeRoundMu.Lock()
		if m.exclusive == round {
			m.exclusive = nil
		}
		m.clearActiveRoundLocked(round)
		m.probeRoundMu.Unlock()
	}()
	summary, err := m.probeGeneration(
		roundCtx,
		generation,
		round.id,
		round.probeEpoch,
		round.probeSeq,
		timeout,
	)
	m.completeInitialProbeGateForRound(
		generation,
		round.id,
		round.probeEpoch,
		round.gateRev,
		err,
	)
	return summary, err
}

// cancelProbeRoundsLocked invalidates and drains any automatic probe
// coordinators. The caller must hold probeRoundMu. Results that were already
// in flight are rejected by activeRound before their network goroutines can
// publish health state.
func (m *Manager) cancelProbeRoundsLocked() {
	if active := m.probeRound; active != nil {
		active.cancel()
		<-active.done
		if m.probeRound == active {
			m.probeRound = nil
		}
	}
	if exclusive := m.exclusive; exclusive != nil {
		exclusive.cancel()
		<-exclusive.done
		if m.exclusive == exclusive {
			m.exclusive = nil
		}
	}
}

func (m *Manager) probeGeneration(
	ctx context.Context,
	generation Generation,
	roundID uint64,
	probeEpoch uint64,
	probeSeq uint64,
	timeout time.Duration,
) (ProbeSummary, error) {
	summary := ProbeSummary{Generation: generation}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	m.mu.RLock()
	if generation != m.reloadGen {
		m.mu.RUnlock()
		return summary, ErrStaleGeneration
	}
	tasks := make([]generationProbeTask, 0, len(m.nodes))
	for tag, entry := range m.nodes {
		entry.mu.RLock()
		if entry.reloadGen == generation {
			tasks = append(tasks, generationProbeTask{
				tag:           tag,
				entry:         entry,
				probe:         entry.probe,
				probeRevision: entry.probeRevision,
			})
		}
		entry.mu.RUnlock()
	}
	m.mu.RUnlock()
	summary.Total = len(tasks)
	if len(tasks) == 0 {
		return summary, nil
	}

	workerLimit := probeAllWorkerLimit(len(tasks))
	sem := make(chan struct{}, workerLimit)
	var wg sync.WaitGroup
	var completed atomic.Int32
	var available atomic.Int32
	var failed atomic.Int32
	for _, task := range tasks {
		sem <- struct{}{}
		wg.Add(1)
		go func(task generationProbeTask) {
			defer wg.Done()
			defer func() { <-sem }()
			var latency time.Duration
			var probeErr error
			if task.probe == nil {
				probeErr = errors.New("probe not available for this node")
			} else {
				latency, probeErr = runProbeWithTimeout(ctx, timeout, task.probe)
			}
			if !m.applyProbeResult(
				generation,
				task.tag,
				task.entry,
				roundID,
				probeEpoch,
				probeSeq,
				task.probeRevision,
				latency,
				probeErr,
			) {
				return
			}
			completed.Add(1)
			if probeErr != nil {
				failed.Add(1)
				if m.logger != nil {
					m.logger.Warn("probe failed for ", task.tag, ": ", probeErr)
				}
			} else {
				available.Add(1)
			}
		}(task)
	}
	wg.Wait()
	summary.Completed = int(completed.Load())
	summary.Available = int(available.Load())
	summary.Failed = int(failed.Load())

	m.mu.RLock()
	stale := generation != m.reloadGen
	m.mu.RUnlock()
	if stale {
		return summary, ErrStaleGeneration
	}
	return summary, nil
}

func (m *Manager) applyProbeResult(
	generation Generation,
	tag string,
	entry *entry,
	roundID uint64,
	probeEpoch uint64,
	probeSeq uint64,
	probeRevision uint64,
	latency time.Duration,
	probeErr error,
) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if generation != m.reloadGen || probeEpoch != m.probeEpoch.Load() || m.nodes[tag] != entry {
		return false
	}
	if roundID != 0 && m.activeRound != roundID {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	// Invocation order, not network completion order, decides which probe is
	// authoritative across manual, periodic, and exclusive generation probes.
	if entry.reloadGen != generation || entry.probeRevision != probeRevision || probeSeq <= entry.lastProbeSeq {
		return false
	}

	now := time.Now()
	entry.lastProbeSeq = probeSeq
	entry.healthGen = generation
	entry.lastProbeAt = now
	entry.initialCheckDone = true
	entry.eventSeq++
	if probeErr != nil {
		errText := probeErr.Error()
		entry.failure++
		entry.lastFailureSeq = entry.eventSeq
		entry.lastError = errText
		entry.lastFail = now
		entry.lastProbe = 0
		entry.available = false
		entry.appendTimelineLocked(false, 0, errText, "health-probe")
		return true
	}

	entry.success++
	entry.lastError = ""
	entry.lastOK = now
	entry.lastProbeOK = now
	entry.lastProbe = latency
	entry.available = true
	latencyMs := latency.Milliseconds()
	if latencyMs == 0 && latency > 0 {
		latencyMs = 1
	}
	entry.appendTimelineLocked(true, latencyMs, "", "health-probe")
	return true
}

func probeAllWorkerLimit(totalEntries int) int {
	if totalEntries <= 0 {
		return 0
	}

	workerLimit := runtime.NumCPU() * 4
	if workerLimit < 8 {
		workerLimit = 8
	}
	// Keep the full automatic probe round below the DNS / connector burst that
	// has repeatedly produced false zero-available snapshots on production-sized
	// node sets. The manual probe-all API already succeeds with this lower
	// ceiling, so startup and periodic probes should use the same safer range.
	if workerLimit > 16 {
		workerLimit = 16
	}
	if totalEntries < workerLimit {
		workerLimit = totalEntries
	}
	return workerLimit
}

func runProbeWithTimeout(parent context.Context, timeout time.Duration, probe probeFunc) (time.Duration, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	type probeResult struct {
		latency time.Duration
		err     error
	}
	resultCh := make(chan probeResult, 1)
	go func() {
		latency, err := probe(ctx)
		select {
		case resultCh <- probeResult{latency: latency, err: err}:
		case <-ctx.Done():
		}
	}()

	select {
	case result := <-resultCh:
		return result.latency, result.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Stop stops the periodic health check.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.healthMu.Lock()
	if m.healthTicker != nil {
		m.healthTicker.Stop()
		m.healthTicker = nil
	}
	m.healthStarted = false
	m.healthMu.Unlock()
	m.probeRoundMu.Lock()
	m.mu.Lock()
	m.activeRound = 0
	m.probeEpoch.Add(1)
	m.mu.Unlock()
	m.cancelProbeRoundsLocked()
	m.probeRoundMu.Unlock()
}

// WaitForInitialProbe waits until the first full probe round completes.
// A zero or negative timeout falls back to the active health timeout or a
// conservative default.
func (m *Manager) WaitForInitialProbe(timeout time.Duration) error {
	if timeout <= 0 {
		m.healthMu.Lock()
		timeout = m.healthTimeout
		m.healthMu.Unlock()
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		m.mu.RLock()
		probeReady := m.probeReady
		m.mu.RUnlock()
		if !probeReady {
			return nil
		}

		m.initialProbeMu.Lock()
		if m.initialProbeDone {
			m.initialProbeMu.Unlock()
			return nil
		}
		ch := m.initialProbeCh
		m.initialProbeMu.Unlock()

		// A target can be cleared between the first readiness check and the
		// gate snapshot. Re-check so a disabled monitor does not wait on a
		// newly-created gate that can never be completed.
		m.mu.RLock()
		probeReady = m.probeReady
		m.mu.RUnlock()
		if !probeReady {
			return nil
		}

		select {
		case <-ch:
			// The channel may have been closed by a gate reset. Loop back to
			// observe the current revision/done state instead of treating every
			// close as successful completion.
			continue
		case <-timer.C:
			return fmt.Errorf("timeout waiting for initial probe completion after %s", timeout)
		case <-m.ctx.Done():
			return m.ctx.Err()
		}
	}
}

func (m *Manager) startTrafficSpeedSampler() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-ticker.C:
			m.sampleTrafficSpeeds(now)
		}
	}
}

func (m *Manager) sampleTrafficSpeeds(now time.Time) {
	m.mu.RLock()
	entries := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		entries = append(entries, e)
	}
	m.mu.RUnlock()

	for _, e := range entries {
		e.updateTrafficSpeed(now)
	}
}

func parsePort(value string) uint16 {
	p, err := strconv.Atoi(value)
	if err != nil || p <= 0 || p > 65535 {
		return 80
	}
	return uint16(p)
}

// BeginReload bumps the generation counter. Nodes registered after this call
// will be marked with the new generation. Call SweepStaleNodes after reload
// to remove nodes that were not re-registered (disabled/deleted nodes).
func (m *Manager) BeginReload() Generation {
	m.probeRoundMu.Lock()
	defer m.probeRoundMu.Unlock()

	m.mu.Lock()
	m.reloadGen++
	generation := m.reloadGen
	m.activeRound = 0
	m.resetInitialProbeGate()
	m.mu.Unlock()
	m.cancelProbeRoundsLocked()
	return generation
}

// SweepStaleNodes removes nodes that were not re-registered during the current
// reload cycle. This preserves monitoring data (latency, success/failure counts)
// for nodes that are still active, while cleaning up disabled/removed nodes.
func (m *Manager) SweepStaleNodes() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for tag, e := range m.nodes {
		if e.reloadGen != m.reloadGen {
			delete(m.nodes, tag)
		}
	}
}

// ClearNodes removes all registered nodes. Use BeginReload + SweepStaleNodes
// for reload scenarios to preserve data for active nodes.
func (m *Manager) ClearNodes() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes = make(map[string]*entry)
}

// Register ensures a node is tracked and returns its entry.
// If the node already exists, its info is updated but monitoring stats
// (latency, success/failure counts, etc.) are preserved.
func (m *Manager) Register(info NodeInfo) *EntryHandle {
	m.mu.Lock()
	defer m.mu.Unlock()
	minUptime, minRate := m.longLivedThresholds()
	e, ok := m.nodes[info.Tag]
	if !ok {
		e = &entry{
			owner:              m,
			info:               info,
			timeline:           make([]TimelineEvent, 0, maxTimelineSize),
			reloadGen:          m.reloadGen,
			firstSeenAt:        time.Now(),
			longLivedMinUptime: minUptime,
			longLivedMinRate:   minRate,
		}
		m.nodes[info.Tag] = e
	} else {
		e.mu.Lock()
		e.owner = m
		e.info = info
		if e.reloadGen != m.reloadGen {
			// A probe closure belongs to the box generation that installed it.
			// Clear it before exposing the reused entry as part of the candidate
			// generation; SetProbe will publish the candidate closure separately.
			e.probe = nil
			e.probeRevision++
		}
		e.reloadGen = m.reloadGen
		if e.firstSeenAt.IsZero() {
			e.firstSeenAt = time.Now()
		}
		e.longLivedMinUptime = minUptime
		e.longLivedMinRate = minRate
		e.mu.Unlock()
	}
	m.probeEpoch.Add(1)
	return &EntryHandle{ref: e}
}

// longLivedThresholds resolves configured long-lived thresholds, falling back
// to defaults when unset.
func (m *Manager) longLivedThresholds() (time.Duration, float64) {
	return normalizeLongLivedThresholds(m.cfg.LongLivedMinUptime, m.cfg.LongLivedMinSuccessRate)
}

func normalizeLongLivedThresholds(minUptime time.Duration, minRate float64) (time.Duration, float64) {
	if minUptime <= 0 {
		minUptime = defaultLongLivedMinUptime
	}
	if minRate <= 0 || minRate > 1 {
		minRate = defaultLongLivedMinSuccessRate
	}
	return minUptime, minRate
}

// MeetsLongLivedPolicy reports whether a raw snapshot satisfies the supplied
// uptime / success-rate thresholds. Callers should pass the directive-specific
// thresholds they want to enforce; zero or invalid values fall back to the
// manager defaults.
func MeetsLongLivedPolicy(snapshot Snapshot, minUptime time.Duration, minRate float64) bool {
	if !snapshot.EffectiveAvailable {
		return false
	}
	minUptime, minRate = normalizeLongLivedThresholds(minUptime, minRate)
	rate := reportedSuccessRate(snapshot.ReportedSuccessCount, snapshot.ReportedFailureCount)
	uptime := snapshot.Uptime
	if uptime <= 0 {
		uptime = time.Duration(snapshot.UptimeSeconds) * time.Second
	}
	return uptime >= minUptime && rate >= minRate
}

// SetLongLivedThresholds updates both future registrations and all currently
// registered entries without rebuilding sing-box.
func (m *Manager) SetLongLivedThresholds(minUptime time.Duration, minRate float64) {
	minUptime, minRate = normalizeLongLivedThresholds(minUptime, minRate)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.LongLivedMinUptime = minUptime
	m.cfg.LongLivedMinSuccessRate = minRate
	for _, e := range m.nodes {
		e.mu.Lock()
		e.longLivedMinUptime = minUptime
		e.longLivedMinRate = minRate
		e.mu.Unlock()
	}
}

func (m *Manager) resetInitialProbeGate() {
	m.initialProbeMu.Lock()
	defer m.initialProbeMu.Unlock()
	m.initialProbeRev++
	if !m.initialProbeDone && m.initialProbeCh != nil {
		close(m.initialProbeCh)
	}
	m.initialProbeDone = false
	m.initialProbeCh = make(chan struct{})
}

func (m *Manager) currentInitialProbeRev() uint64 {
	m.initialProbeMu.Lock()
	defer m.initialProbeMu.Unlock()
	return m.initialProbeRev
}

func (m *Manager) completeInitialProbeGate(revision uint64) {
	m.initialProbeMu.Lock()
	defer m.initialProbeMu.Unlock()
	if m.initialProbeDone || revision != m.initialProbeRev {
		return
	}
	m.initialProbeDone = true
	if m.initialProbeCh != nil {
		close(m.initialProbeCh)
	}
}

func (m *Manager) completeInitialProbeGateForRound(
	generation Generation,
	roundID uint64,
	probeEpoch uint64,
	gateRev uint64,
	err error,
) {
	if err != nil {
		return
	}
	// A reset publishes a new gate revision before cancelling the old round.
	// Re-check round ownership so a late completion cannot close the new gate.
	m.mu.RLock()
	authoritative := generation == m.reloadGen &&
		probeEpoch == m.probeEpoch.Load() &&
		m.activeRound == roundID
	m.mu.RUnlock()
	if !authoritative {
		return
	}
	m.completeInitialProbeGate(gateRev)
}

// RestorePersistedState hydrates a registered node with runtime stats loaded
// from durable storage. Matching prefers URI and falls back to name.
func (m *Manager) RestorePersistedState(uri, name string, state PersistedState) bool {
	normalizedURI := strings.TrimSpace(uri)
	normalizedName := strings.TrimSpace(name)
	if normalizedURI == "" && normalizedName == "" {
		return false
	}

	m.mu.RLock()
	var target *entry
	if normalizedURI != "" {
		for _, candidate := range m.nodes {
			if strings.TrimSpace(candidate.info.URI) == normalizedURI {
				target = candidate
				break
			}
		}
	}
	if target == nil && normalizedName != "" {
		for _, candidate := range m.nodes {
			if strings.TrimSpace(candidate.info.Name) == normalizedName {
				target = candidate
				break
			}
		}
	}
	m.mu.RUnlock()

	if target == nil {
		return false
	}
	target.restorePersistedState(state)
	return true
}

// DestinationForProbe exposes the configured destination for health checks.
func (m *Manager) DestinationForProbe() (M.Socksaddr, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.probeReady {
		return M.Socksaddr{}, false
	}
	return m.probeDst, true
}

func (m *Manager) ProbeTargets() ([]ProbeTargetSpec, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.probeReady || len(m.probeSpecs) == 0 {
		return nil, false
	}
	specs := make([]ProbeTargetSpec, len(m.probeSpecs))
	copy(specs, m.probeSpecs)
	return specs, true
}

// SkipCertVerify reports whether HTTPS probe TLS verification is disabled.
func (m *Manager) SkipCertVerify() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.SkipCertVerify
}

// SetSkipCertVerify updates the HTTPS probe TLS verification policy at runtime.
func (m *Manager) SetSkipCertVerify(skip bool) {
	m.probeRoundMu.Lock()
	m.mu.Lock()
	if m.cfg.SkipCertVerify == skip {
		m.mu.Unlock()
		m.probeRoundMu.Unlock()
		return
	}
	m.cfg.SkipCertVerify = skip
	m.activeRound = 0
	m.probeEpoch.Add(1)
	m.resetInitialProbeGate()
	m.mu.Unlock()
	m.cancelProbeRoundsLocked()
	m.probeRoundMu.Unlock()
}

// UpdateProbeTarget dynamically updates the probe destination at runtime.
func (m *Manager) UpdateProbeTarget(target string) error {
	return m.UpdateProbeTargets(nil, target)
}

func (m *Manager) UpdateProbeTargets(targets []string, single string) error {
	specs, err := parseProbeTargets(targets, single)
	if err != nil {
		return err
	}
	m.probeRoundMu.Lock()
	m.mu.Lock()
	if stringSlicesEqual(m.cfg.ProbeTargets, targets) && strings.TrimSpace(m.cfg.ProbeTarget) == strings.TrimSpace(single) {
		m.mu.Unlock()
		m.probeRoundMu.Unlock()
		return nil
	}
	probeReady := len(specs) > 0
	if !probeReady {
		m.probeSpecs = nil
		m.probeDst = M.Socksaddr{}
		m.probeReady = false
	} else {
		m.probeSpecs = specs
		m.probeDst = specs[0].Dst
		m.probeReady = true
	}
	m.cfg.ProbeTargets = append([]string(nil), targets...)
	m.cfg.ProbeTarget = strings.TrimSpace(single)
	m.activeRound = 0
	m.probeEpoch.Add(1)
	m.resetInitialProbeGate()
	m.mu.Unlock()
	m.cancelProbeRoundsLocked()
	m.probeRoundMu.Unlock()
	if probeReady {
		m.healthMu.Lock()
		healthStarted := m.healthStarted
		healthTimeout := m.healthTimeout
		m.healthMu.Unlock()
		if healthStarted {
			m.RequestProbeAllOnce(healthTimeout)
		}
	}
	return nil
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// Snapshot returns a sorted copy of current node states.
func (m *Manager) Snapshot() []Snapshot {
	return m.SnapshotFiltered(false)
}

// SnapshotFiltered returns a sorted copy of current node states.
// If onlyAvailable is true, it keeps only nodes that are effectively usable:
// either probe-confirmed available or recently proven by successful traffic.
func (m *Manager) SnapshotFiltered(onlyAvailable bool) []Snapshot {
	m.mu.RLock()
	list := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		list = append(list, e)
	}
	m.mu.RUnlock()
	snapshots := make([]Snapshot, 0, len(list))
	for _, e := range list {
		snap := e.snapshot()
		if onlyAvailable && !isEffectiveSnapshot(snap) {
			continue
		}
		snapshots = append(snapshots, snap)
	}
	// 按延迟排序（延迟小的在前面，未测试的排在最后）
	sort.Slice(snapshots, func(i, j int) bool {
		latencyI := snapshots[i].LastLatencyMs
		latencyJ := snapshots[j].LastLatencyMs
		// -1 表示未测试，排在最后
		if latencyI < 0 && latencyJ < 0 {
			return snapshots[i].Name < snapshots[j].Name // 都未测试时按名称排序
		}
		if latencyI < 0 {
			return false // i 未测试，排在后面
		}
		if latencyJ < 0 {
			return true // j 未测试，i 排在前面
		}
		if latencyI == latencyJ {
			return snapshots[i].Name < snapshots[j].Name // 延迟相同时按名称排序
		}
		return latencyI < latencyJ
	})
	return snapshots
}

// SourceSelectionStates aggregates recent node health into source-level
// selection hints so pool schedulers can avoid sources with systemic
// handshake/auth failures without permanently removing every node.
func (m *Manager) SourceSelectionStates() map[string]SourceSelectionState {
	m.mu.RLock()
	list := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		list = append(list, e)
	}
	m.mu.RUnlock()

	grouped := make(map[string]*SourceSelectionState)
	for _, e := range list {
		snap := e.snapshot()
		sourceRef := strings.TrimSpace(snap.SourceRef)
		if sourceRef == "" {
			continue
		}
		state, ok := grouped[sourceRef]
		if !ok {
			state = &SourceSelectionState{
				Ref:  sourceRef,
				Name: strings.TrimSpace(snap.SourceName),
				Kind: strings.TrimSpace(snap.SourceKind),
			}
			grouped[sourceRef] = state
		}
		state.TotalNodes++
		if isEffectiveSnapshot(snap) {
			state.HealthyNodes++
		}
		if structural, reason := classifySourceStructuralFailure(snap); structural {
			state.StructuralFailures++
			if state.Reason == "" {
				state.Reason = reason
			}
		}
	}

	result := make(map[string]SourceSelectionState, len(grouped))
	for ref, state := range grouped {
		applySourceSelectionPenalty(state)
		result[ref] = *state
	}
	return result
}

// SourceHealthStates aggregates runtime availability by source_ref so operator
// tooling can answer questions like total/effective/blacklisted/pending counts
// for manifest-fed providers such as ZenProxy.
func (m *Manager) SourceHealthStates() map[string]SourceHealthState {
	m.mu.RLock()
	list := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		list = append(list, e)
	}
	m.mu.RUnlock()

	grouped := make(map[string]*SourceHealthState)
	for _, e := range list {
		snap := e.snapshot()
		sourceRef := strings.TrimSpace(snap.SourceRef)
		if sourceRef == "" {
			continue
		}
		state, ok := grouped[sourceRef]
		if !ok {
			state = &SourceHealthState{
				Ref:  sourceRef,
				Name: strings.TrimSpace(snap.SourceName),
				Kind: strings.TrimSpace(snap.SourceKind),
			}
			grouped[sourceRef] = state
		}

		state.TotalNodes++
		if snap.EffectiveAvailable {
			state.EffectiveAvailableNodes++
		}
		if isProbeAvailable(snap) {
			state.ProbeAvailableNodes++
		}
		if snap.TrafficProvenUsable {
			state.TrafficProvenNodes++
		}
		if snap.Blacklisted {
			state.BlacklistedNodes++
		}
		if !snap.InitialCheckDone {
			state.PendingNodes++
		} else if !snap.EffectiveAvailable && !snap.Blacklisted {
			state.UnavailableNodes++
		}
		if structural, reason := classifySourceStructuralFailure(snap); structural {
			state.StructuralFailures++
			if state.SelectionReason == "" {
				state.SelectionReason = reason
			}
		}
	}

	result := make(map[string]SourceHealthState, len(grouped))
	for ref, state := range grouped {
		selection := &SourceSelectionState{
			Ref:                state.Ref,
			Name:               state.Name,
			Kind:               state.Kind,
			TotalNodes:         state.TotalNodes,
			HealthyNodes:       state.EffectiveAvailableNodes,
			StructuralFailures: state.StructuralFailures,
			Reason:             state.SelectionReason,
		}
		applySourceSelectionPenalty(selection)
		state.SelectionPenalty = selection.Penalty
		state.SelectionExcluded = selection.Excluded
		state.SelectionReason = selection.Reason
		result[ref] = *state
	}
	return result
}

// SecondarySelectionStates aggregates source-internal node features such as
// protocol family, node mode, and domain family so the pool can degrade
// recurrently bad clusters without excluding an otherwise healthy source.
func (m *Manager) SecondarySelectionStates() map[string]SecondarySelectionState {
	m.mu.RLock()
	list := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		list = append(list, e)
	}
	m.mu.RUnlock()

	grouped := make(map[string]*SecondarySelectionState)
	for _, e := range list {
		snap := e.snapshot()
		sourceRef := strings.TrimSpace(snap.SourceRef)
		if sourceRef == "" {
			continue
		}
		structural, reason := classifySourceStructuralFailure(snap)
		dimensions := []struct {
			name  string
			value string
		}{
			{name: SelectionDimensionProtocolFamily, value: strings.TrimSpace(snap.ProtocolFamily)},
			{name: SelectionDimensionNodeMode, value: strings.TrimSpace(snap.NodeMode)},
			{name: SelectionDimensionDomainFamily, value: strings.TrimSpace(snap.DomainFamily)},
		}
		for _, dimension := range dimensions {
			if dimension.value == "" {
				continue
			}
			key := SecondarySelectionStateKey(sourceRef, dimension.name, dimension.value)
			state, ok := grouped[key]
			if !ok {
				state = &SecondarySelectionState{
					Key:        key,
					SourceRef:  sourceRef,
					SourceName: strings.TrimSpace(snap.SourceName),
					SourceKind: strings.TrimSpace(snap.SourceKind),
					Dimension:  dimension.name,
					Value:      dimension.value,
				}
				grouped[key] = state
			}
			state.TotalNodes++
			if isEffectiveSnapshot(snap) {
				state.HealthyNodes++
			}
			if structural {
				state.StructuralFailures++
				if state.Reason == "" {
					state.Reason = reason
				}
			}
		}
	}

	result := make(map[string]SecondarySelectionState, len(grouped))
	for key, state := range grouped {
		applySecondarySelectionPenalty(state)
		result[key] = *state
	}
	return result
}

// TrafficSummary returns aggregated traffic totals/speeds and per-node speeds.
// includeNodes controls whether per-node details are returned.
func (m *Manager) TrafficSummary(includeNodes bool) TrafficSummary {
	m.mu.RLock()
	list := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		list = append(list, e)
	}
	m.mu.RUnlock()

	summary := TrafficSummary{
		NodeCount: len(list),
		SampledAt: time.Now(),
	}
	if includeNodes {
		summary.Nodes = make([]NodeTrafficSpeed, 0, len(list))
	}

	for _, e := range list {
		totalUp := e.totalUpload.Load()
		totalDown := e.totalDownload.Load()

		e.mu.RLock()
		upSpeed := e.uploadSpeed
		downSpeed := e.downloadSpeed
		tag := e.info.Tag
		e.mu.RUnlock()

		summary.TotalUpload += totalUp
		summary.TotalDownload += totalDown
		summary.UploadSpeed += upSpeed
		summary.DownloadSpeed += downSpeed

		if includeNodes {
			summary.Nodes = append(summary.Nodes, NodeTrafficSpeed{
				Tag:           tag,
				UploadSpeed:   upSpeed,
				DownloadSpeed: downSpeed,
				TotalUpload:   totalUp,
				TotalDownload: totalDown,
			})
		}
	}

	return summary
}

func classifySourceStructuralFailure(snap Snapshot) (bool, string) {
	if !snap.InitialCheckDone || isEffectiveSnapshot(snap) {
		return false, ""
	}

	errText := strings.ToLower(strings.TrimSpace(snap.LastError))
	if errText == "" {
		return false, ""
	}

	switch {
	case strings.Contains(errText, "reality verification failed"):
		return true, "reality_verification_failed"
	case strings.Contains(errText, "authentication failed, status code: 200"):
		return true, "authentication_failed_status_200"
	case strings.Contains(errText, "unexpected http response status: 500"),
		strings.Contains(errText, "unexpected http response status: 530"):
		return true, "unexpected_http_status"
	case strings.Contains(errText, "tls handshake: eof"),
		strings.Contains(errText, "tls: first record does not look like a tls handshake"):
		return true, "tls_handshake_eof"
	case errText == "eof", strings.Contains(errText, "unexpected eof"):
		return true, "protocol_eof"
	case strings.Contains(errText, "i/o timeout"),
		strings.Contains(errText, "context deadline exceeded"),
		strings.Contains(errText, "timeout: no recent network activity"):
		return true, "probe_timeout"
	default:
		return false, ""
	}
}

func applySecondarySelectionPenalty(state *SecondarySelectionState) {
	if state == nil || state.StructuralFailures == 0 {
		return
	}
	switch state.Dimension {
	case SelectionDimensionNodeMode:
		applyDimensionPenalty(state, 12, 28, 58, 72)
	case SelectionDimensionDomainFamily:
		applyDimensionPenalty(state, 8, 22, 50, 65)
	case SelectionDimensionProtocolFamily:
		applyDimensionPenalty(state, 4, 12, 24, 38)
	default:
		applyDimensionPenalty(state, 4, 10, 20, 30)
	}
}

func applySourceSelectionPenalty(state *SourceSelectionState) {
	if state == nil {
		return
	}
	switch {
	case state.StructuralFailures == 0:
		state.Penalty = 0
	case state.HealthyNodes == 0 && state.StructuralFailures >= 2:
		state.Penalty = 85
		state.Excluded = true
	case state.HealthyNodes == 0 && state.TotalNodes == 1 && state.StructuralFailures == 1:
		state.Penalty = 70
		state.Excluded = true
	case state.StructuralFailures*2 >= state.TotalNodes:
		state.Penalty = 45
	case state.StructuralFailures >= 1:
		state.Penalty = 20
	}
}

func applyDimensionPenalty(
	state *SecondarySelectionState,
	minorPenalty int,
	mixedPenalty int,
	excludedPenalty int,
	saturatedPenalty int,
) {
	switch {
	case state.StructuralFailures == 0:
		state.Penalty = 0
	case state.HealthyNodes == 0:
		state.Penalty = excludedPenalty
		state.Excluded = true
		if state.StructuralFailures >= 2 {
			state.Penalty = saturatedPenalty
		}
		if state.TotalNodes == state.StructuralFailures && state.TotalNodes >= 2 && state.Penalty < saturatedPenalty {
			state.Penalty = saturatedPenalty
		}
	case state.StructuralFailures >= 2:
		state.Penalty = mixedPenalty
	default:
		state.Penalty = minorPenalty
	}
}

// Probe triggers a manual health check.
func (m *Manager) Probe(ctx context.Context, tag string) (time.Duration, error) {
	m.mu.RLock()
	e, ok := m.nodes[tag]
	generation := m.reloadGen
	probeEpoch := m.probeEpoch.Load()
	if !ok {
		m.mu.RUnlock()
		return 0, fmt.Errorf("node not found: %s", tag)
	}
	e.mu.RLock()
	probe := e.probe
	probeRevision := e.probeRevision
	entryGeneration := e.reloadGen
	e.mu.RUnlock()
	m.mu.RUnlock()
	if probe == nil {
		return 0, errors.New("probe not available for this node")
	}
	if entryGeneration != generation {
		return 0, ErrStaleGeneration
	}
	probeSeq := m.nextProbeSeq.Add(1)
	latency, probeErr := probe(ctx)
	if !m.applyProbeResult(generation, tag, e, 0, probeEpoch, probeSeq, probeRevision, latency, probeErr) {
		return 0, ErrStaleGeneration
	}
	return latency, probeErr
}

// Release clears blacklist state for the given node.
func (m *Manager) Release(tag string) error {
	e, err := m.entry(tag)
	if err != nil {
		return err
	}
	if e.release == nil {
		return errors.New("release not available for this node")
	}
	e.release()
	return nil
}

func (m *Manager) entry(tag string) (*entry, error) {
	m.mu.RLock()
	e, ok := m.nodes[tag]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("node %s not found", tag)
	}
	return e, nil
}

func (e *entry) snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.clearExpiredBlacklistLocked(now)

	latencyMs := int64(-1)
	if e.lastProbe > 0 {
		latencyMs = e.lastProbe.Milliseconds()
		if latencyMs == 0 {
			latencyMs = 1
		}
	}

	var timelineCopy []TimelineEvent
	if len(e.timeline) > 0 {
		timelineCopy = make([]TimelineEvent, len(e.timeline))
		copy(timelineCopy, e.timeline)
	}

	snap := Snapshot{
		NodeInfo:                  e.info,
		FailureCount:              e.failure,
		SuccessCount:              e.success,
		TrafficSuccessCount:       e.trafficSuccess,
		Blacklisted:               e.blacklist,
		BlacklistedUntil:          e.until,
		AvailabilityScore:         e.availabilityScoreLocked(),
		ReportedSuccessCount:      e.reportSuccess,
		ReportedFailureCount:      e.reportFailure,
		ConsecutiveReportFailures: e.reportFailures,
		ActiveConnections:         e.active.Load(),
		LastError:                 e.lastError,
		LastFailure:               e.lastFail,
		LastSuccess:               e.lastOK,
		LastTrafficSuccessAt:      e.lastTrafficOK,
		LastReportedAt:            e.lastReportedAt,
		LastReportedSuccess:       e.lastReportOK,
		LastProbeAt:               e.lastProbeAt,
		LastProbeSuccessAt:        e.lastProbeOK,
		LastProbeLatency:          e.lastProbe,
		LastLatencyMs:             latencyMs,
		Available:                 e.healthGen == e.reloadGen && e.available,
		InitialCheckDone:          e.healthGen == e.reloadGen && e.initialCheckDone,
		TotalUpload:               e.totalUpload.Load(),
		TotalDownload:             e.totalDownload.Load(),
		UploadSpeed:               e.uploadSpeed,
		DownloadSpeed:             e.downloadSpeed,
		TrafficSuccessSeq:         e.lastTrafficSeq,
		FailureSeq:                e.lastFailureSeq,
		Timeline:                  timelineCopy,
	}

	effectiveAvailable, trafficProvenUsable, availabilitySource := effectiveAvailabilityDetailsAt(snap, now)
	snap.EffectiveAvailable = effectiveAvailable
	snap.TrafficProvenUsable = trafficProvenUsable
	snap.AvailabilitySource = availabilitySource

	if !e.firstSeenAt.IsZero() {
		uptime := now.Sub(e.firstSeenAt)
		if uptime > 0 {
			snap.Uptime = uptime
			snap.UptimeSeconds = int64(uptime / time.Second)
		}
		minUptime := e.longLivedMinUptime
		if minUptime <= 0 {
			minUptime = defaultLongLivedMinUptime
		}
		minRate := e.longLivedMinRate
		if minRate <= 0 {
			minRate = defaultLongLivedMinSuccessRate
		}
		snap.LongLived = MeetsLongLivedPolicy(snap, minUptime, minRate)
	}
	return snap
}

// reportedSuccessRate returns the success ratio over reported probe/feedback
// outcomes. With no samples it returns 1.0 so a freshly-seen node is not
// penalized before any probe has run (it still fails the uptime gate anyway).
func reportedSuccessRate(success, failure int64) float64 {
	total := success + failure
	if total <= 0 {
		return 1.0
	}
	return float64(success) / float64(total)
}

func (e *entry) trafficProvenUsableLocked(now time.Time) bool {
	if e.blacklist || e.lastTrafficOK.IsZero() {
		return false
	}
	if e.lastFailureSeq > 0 && e.lastTrafficSeq > 0 {
		if e.lastFailureSeq > e.lastTrafficSeq {
			return false
		}
	} else if !e.lastFail.IsZero() && !e.lastFail.Before(e.lastTrafficOK) {
		return false
	}
	if now.Before(e.lastTrafficOK) {
		return true
	}
	return now.Sub(e.lastTrafficOK) <= trafficProvenSuccessWindow
}

func (e *entry) availabilityScoreLocked() int {
	const (
		baseScore            = 100
		maxPenalty           = 80
		unhealthyScoreCap    = 20
		blacklistedScoreCap  = 5
		minAvailabilityScore = 1
	)

	penalty := e.feedbackPenalty
	if penalty < 0 {
		penalty = 0
	} else if penalty > maxPenalty {
		penalty = maxPenalty
	}

	score := baseScore - penalty
	if e.initialCheckDone && !e.available && !e.trafficProvenUsableLocked(time.Now()) && score > unhealthyScoreCap {
		score = unhealthyScoreCap
	}
	if e.blacklist && score > blacklistedScoreCap {
		score = blacklistedScoreCap
	}
	if score < minAvailabilityScore {
		score = minAvailabilityScore
	}
	return score
}

func (e *entry) recordFailure(err error, destination string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	errStr := err.Error()
	e.eventSeq++
	e.lastFailureSeq = e.eventSeq
	e.failure++
	e.lastError = errStr
	e.lastFail = time.Now()
	// 注意：不修改 available/initialCheckDone
	// 流量失败不代表节点不可用（可能是目标网站的问题）
	// available 只由探测操作控制
	e.appendTimelineLocked(false, 0, errStr, destination)
}

func (e *entry) recordObservationFailure(err error, destination string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	errStr := err.Error()
	e.eventSeq++
	e.lastFailureSeq = e.eventSeq
	e.lastFail = time.Now()
	e.lastError = errStr
	e.appendTimelineLocked(false, 0, errStr, destination)
}

func (e *entry) recordSuccess(destination string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.eventSeq++
	e.lastTrafficSeq = e.eventSeq
	e.success++
	e.trafficSuccess++
	e.lastOK = now
	e.lastTrafficOK = now
	// 注意：不修改 available/initialCheckDone
	// 流量成功不代表需要更新探测状态
	// available 只由探测操作控制
	e.appendTimelineLocked(true, 0, "", destination)
}

func (e *entry) recordSuccessWithLatency(latency time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.success++
	e.lastError = ""
	e.lastOK = now
	e.lastProbeAt = now
	e.lastProbeOK = now
	e.lastProbe = latency
	e.available = true
	e.initialCheckDone = true
	e.healthGen = e.reloadGen
	latencyMs := latency.Milliseconds()
	if latencyMs == 0 && latency > 0 {
		latencyMs = 1
	}
	e.appendTimelineLocked(true, latencyMs, "", "")
}

func (e *entry) appendTimelineLocked(success bool, latencyMs int64, errStr string, destination string) {
	evt := TimelineEvent{
		Time:        time.Now(),
		Success:     success,
		LatencyMs:   latencyMs,
		Error:       errStr,
		Destination: destination,
	}
	if len(e.timeline) >= maxTimelineSize {
		copy(e.timeline, e.timeline[1:])
		e.timeline[len(e.timeline)-1] = evt
	} else {
		e.timeline = append(e.timeline, evt)
	}
}

func (e *entry) blacklistUntil(until time.Time) {
	e.mu.Lock()
	e.blacklist = true
	e.until = until
	e.mu.Unlock()
}

func (e *entry) clearExpiredBlacklistLocked(now time.Time) bool {
	if e.blacklist && !e.until.IsZero() && now.After(e.until) {
		e.blacklist = false
		e.until = time.Time{}
		return true
	}
	return false
}

func (e *entry) clearBlacklist() {
	e.mu.Lock()
	e.blacklist = false
	e.until = time.Time{}
	e.mu.Unlock()
}

func (e *entry) incActive() {
	e.active.Add(1)
}

func (e *entry) decActive() {
	e.active.Add(-1)
}

func (e *entry) setActiveConnections(active int32) {
	e.active.Store(active)
}

func (e *entry) setProbe(fn probeFunc) {
	e.mu.Lock()
	e.probe = fn
	e.probeRevision++
	owner := e.owner
	e.mu.Unlock()
	if owner != nil {
		owner.probeEpoch.Add(1)
	}
}

func (e *entry) setRelease(fn releaseFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.release = fn
}

func (e *entry) recordProbeLatency(d time.Duration) {
	e.mu.Lock()
	now := time.Now()
	e.lastProbeAt = now
	e.lastProbeOK = now
	e.lastProbe = d
	e.mu.Unlock()
}

func (e *entry) applyUsageReportSuccess() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.reportSuccess++
	e.reportFailures = 0
	e.lastReportedAt = time.Now()
	e.lastReportOK = true
	if e.feedbackPenalty >= 5 {
		e.feedbackPenalty -= 5
	} else {
		e.feedbackPenalty = 0
	}
}

func (e *entry) applyUsageReportFailure(penalty int, countAsRouteFailure bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if countAsRouteFailure {
		e.reportFailure++
		e.reportFailures++
	}
	e.lastReportedAt = time.Now()
	e.lastReportOK = false
	e.feedbackPenalty += penalty
	if e.feedbackPenalty > 80 {
		e.feedbackPenalty = 80
	}
}

func (e *entry) updateTrafficSpeed(now time.Time) {
	curUp := e.totalUpload.Load()
	curDown := e.totalDownload.Load()

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.lastSpeedAt.IsZero() {
		e.lastSpeedAt = now
		e.lastSpeedUpload = curUp
		e.lastSpeedDown = curDown
		e.uploadSpeed = 0
		e.downloadSpeed = 0
		return
	}

	elapsed := now.Sub(e.lastSpeedAt).Seconds()
	if elapsed <= 0 {
		return
	}

	deltaUp := curUp - e.lastSpeedUpload
	deltaDown := curDown - e.lastSpeedDown
	if deltaUp < 0 {
		deltaUp = 0
	}
	if deltaDown < 0 {
		deltaDown = 0
	}

	e.uploadSpeed = int64(float64(deltaUp) / elapsed)
	e.downloadSpeed = int64(float64(deltaDown) / elapsed)
	e.lastSpeedUpload = curUp
	e.lastSpeedDown = curDown
	e.lastSpeedAt = now
}

func (e *entry) restorePersistedState(state PersistedState) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	blacklisted := state.Blacklisted
	until := state.BlacklistedUntil
	if blacklisted && !until.IsZero() && now.After(until) {
		blacklisted = false
		until = time.Time{}
	}

	lastTrafficOK := state.LastTrafficSuccessAt
	if lastTrafficOK.IsZero() && (state.TotalUpload > 0 || state.TotalDownload > 0) && !state.LastSuccessAt.IsZero() {
		lastTrafficOK = state.LastSuccessAt
	}

	e.failure = state.FailureCount
	e.success = state.SuccessCount
	e.trafficSuccess = state.TrafficSuccessCount
	e.blacklist = blacklisted
	e.until = until
	e.lastError = state.LastError
	e.lastFail = state.LastFailureAt
	e.lastOK = state.LastSuccessAt
	e.lastTrafficOK = lastTrafficOK
	e.eventSeq = 0
	e.lastFailureSeq = 0
	e.lastTrafficSeq = 0
	switch {
	case !e.lastFail.IsZero() && !e.lastTrafficOK.IsZero() && e.lastFail.After(e.lastTrafficOK):
		e.lastTrafficSeq = 1
		e.lastFailureSeq = 2
		e.eventSeq = 2
	case !e.lastFail.IsZero() && !e.lastTrafficOK.IsZero() && e.lastTrafficOK.After(e.lastFail):
		e.lastFailureSeq = 1
		e.lastTrafficSeq = 2
		e.eventSeq = 2
	case !e.lastTrafficOK.IsZero():
		e.lastTrafficSeq = 1
		e.eventSeq = 1
	case !e.lastFail.IsZero():
		e.lastFailureSeq = 1
		e.eventSeq = 1
	}
	e.lastProbeAt = state.LastProbeAt
	e.lastProbeOK = state.LastProbeSuccessAt
	e.available = state.Available
	e.initialCheckDone = state.InitialCheckDone
	e.healthGen = e.reloadGen
	if state.LastLatencyMs > 0 {
		e.lastProbe = time.Duration(state.LastLatencyMs) * time.Millisecond
	} else {
		e.lastProbe = 0
	}

	e.totalUpload.Store(state.TotalUpload)
	e.totalDownload.Store(state.TotalDownload)
	e.uploadSpeed = 0
	e.downloadSpeed = 0
	e.lastSpeedUpload = state.TotalUpload
	e.lastSpeedDown = state.TotalDownload
	e.lastSpeedAt = now
}

// RecordFailure updates failure counters with destination info.
func (h *EntryHandle) RecordFailure(err error, destination string) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.recordFailure(err, destination)
}

// RecordObservationFailure appends a failed timeline event without degrading
// hard route-failure counters. This is used for business failures that do not
// prove the proxy route itself is unhealthy.
func (h *EntryHandle) RecordObservationFailure(err error, destination string) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.recordObservationFailure(err, destination)
}

// RecordSuccess updates the last success timestamp with destination info.
func (h *EntryHandle) RecordSuccess(destination string) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.recordSuccess(destination)
}

// RecordSuccessWithLatency updates the last success timestamp and latency.
func (h *EntryHandle) RecordSuccessWithLatency(latency time.Duration) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.recordSuccessWithLatency(latency)
}

// Blacklist marks the node unavailable until the given deadline.
func (h *EntryHandle) Blacklist(until time.Time) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.blacklistUntil(until)
}

// ClearBlacklist removes the blacklist flag.
func (h *EntryHandle) ClearBlacklist() {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.clearBlacklist()
}

// IncActive increments the active connection counter.
func (h *EntryHandle) IncActive() {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.incActive()
}

// DecActive decrements the active connection counter.
func (h *EntryHandle) DecActive() {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.decActive()
}

// SetActiveConnections replaces the active connection counter with an exact
// shared-state snapshot during monitor-entry reattachment.
func (h *EntryHandle) SetActiveConnections(active int32) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.setActiveConnections(active)
}

// SetProbe assigns a probe function.
func (h *EntryHandle) SetProbe(fn func(ctx context.Context) (time.Duration, error)) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.setProbe(fn)
}

// SetRelease assigns a release function.
func (h *EntryHandle) SetRelease(fn func()) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.setRelease(fn)
}

// MarkInitialCheckDone marks the initial health check as completed.
func (h *EntryHandle) MarkInitialCheckDone(available bool) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.mu.Lock()
	h.ref.initialCheckDone = true
	h.ref.available = available
	h.ref.healthGen = h.ref.reloadGen
	h.ref.mu.Unlock()
}

// MarkAvailable updates the availability status.
func (h *EntryHandle) MarkAvailable(available bool) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.mu.Lock()
	h.ref.available = available
	h.ref.healthGen = h.ref.reloadGen
	h.ref.mu.Unlock()
}

// AddTraffic adds upload and download byte counts to the node's traffic counters.
func (h *EntryHandle) AddTraffic(upload, download int64) {
	if h == nil || h.ref == nil {
		return
	}
	if upload > 0 {
		h.ref.totalUpload.Add(upload)
	}
	if download > 0 {
		h.ref.totalDownload.Add(download)
	}
}

// SetTraffic sets the traffic counters to specific values (used for restoring from store).
func (h *EntryHandle) SetTraffic(upload, download int64) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.totalUpload.Store(upload)
	h.ref.totalDownload.Store(download)
	h.ref.mu.Lock()
	h.ref.uploadSpeed = 0
	h.ref.downloadSpeed = 0
	h.ref.lastSpeedUpload = upload
	h.ref.lastSpeedDown = download
	h.ref.lastSpeedAt = time.Now()
	h.ref.mu.Unlock()
}

// ApplyUsageReportSuccess updates the external task-feedback score after a
// successful business request routed through this node.
func (h *EntryHandle) ApplyUsageReportSuccess() {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.applyUsageReportSuccess()
}

// ApplyUsageReportFailure updates the external task-feedback score after a
// failed business request routed through this node.
func (h *EntryHandle) ApplyUsageReportFailure(penalty int, countAsRouteFailure bool) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.applyUsageReportFailure(penalty, countAsRouteFailure)
}

// Snapshot returns a point-in-time copy of the node state.
func (h *EntryHandle) Snapshot() Snapshot {
	if h == nil || h.ref == nil {
		return Snapshot{}
	}
	return h.ref.snapshot()
}
