package pool

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

const (
	// Type is the outbound type name exposed to sing-box.
	Type = "pool"
	// Tag is the default outbound tag used by builder.
	Tag = "proxy-pool"

	modeAuto       = "auto"
	modeSequential = "sequential"
	modeRandom     = "random"
	modeBalance    = "balance"
)

// Options controls pool outbound behaviour.
type Options struct {
	Mode              string
	Members           []string
	FailureThreshold  int
	BlacklistDuration time.Duration
	Metadata          map[string]MemberMeta
	MaxRetries        int // max retry attempts on connection failure (default 2)
}

// MemberMeta carries optional descriptive information for monitoring UI.
type MemberMeta struct {
	Name           string
	URI            string
	Mode           string
	ListenAddress  string
	Port           uint16
	Region         string // GeoIP region code: "jp", "kr", "us", "hk", "tw", "other"
	Country        string // Full country name from GeoIP
	SourceKind     string
	SourceName     string
	SourceRef      string
	ProtocolFamily string
	NodeMode       string
	DomainFamily   string
}

// Register wires the pool outbound into the registry.
func Register(registry *outbound.Registry) {
	outbound.Register[Options](registry, Type, newPool)
}

type memberState struct {
	outbound adapter.Outbound
	tag      string
	entry    *monitor.EntryHandle
	shared   *sharedMemberState
}

type poolOutbound struct {
	outbound.Adapter
	ctx            context.Context
	logger         log.ContextLogger
	manager        adapter.OutboundManager
	options        Options
	mode           string
	members        []*memberState
	mu             sync.Mutex
	rrCounter      atomic.Uint32
	rng            *rand.Rand
	rngMu          sync.Mutex // protects rng for random mode
	monitor        *monitor.Manager
	candidatesPool sync.Pool
}

func newPool(ctx context.Context, _ adapter.Router, logger log.ContextLogger, tag string, options Options) (adapter.Outbound, error) {
	if len(options.Members) == 0 {
		return nil, E.New("pool requires at least one member")
	}
	manager := service.FromContext[adapter.OutboundManager](ctx)
	if manager == nil {
		return nil, E.New("missing outbound manager in context")
	}
	monitorMgr := monitor.FromContext(ctx)
	normalized := normalizeOptions(options)
	memberCount := len(normalized.Members)
	p := &poolOutbound{
		Adapter: outbound.NewAdapter(Type, tag, []string{N.NetworkTCP, N.NetworkUDP}, normalized.Members),
		ctx:     ctx,
		logger:  logger,
		manager: manager,
		options: normalized,
		mode:    normalized.Mode,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
		monitor: monitorMgr,
		candidatesPool: sync.Pool{
			New: func() any {
				return make([]*memberState, 0, memberCount)
			},
		},
	}

	// Register nodes immediately if monitor is available
	if monitorMgr != nil {
		logger.Info("registering ", len(normalized.Members), " nodes to monitor")
		for _, memberTag := range normalized.Members {
			// Acquire shared state for this tag (creates if not exists)
			state := acquireSharedState(memberTag)

			meta := normalized.Metadata[memberTag]
			info := monitor.NodeInfo{
				Tag:            memberTag,
				Name:           meta.Name,
				URI:            meta.URI,
				Mode:           meta.Mode,
				ListenAddress:  meta.ListenAddress,
				Port:           meta.Port,
				Region:         meta.Region,
				Country:        meta.Country,
				SourceKind:     meta.SourceKind,
				SourceName:     meta.SourceName,
				SourceRef:      meta.SourceRef,
				ProtocolFamily: meta.ProtocolFamily,
				NodeMode:       meta.NodeMode,
				DomainFamily:   meta.DomainFamily,
			}
			entry := monitorMgr.Register(info)
			if entry != nil {
				// Attach entry to shared state so all pool instances share it
				state.attachEntry(entry)
				logger.Info("registered node: ", memberTag)
				// Set probe and release functions immediately
				entry.SetRelease(p.makeReleaseByTagFunc(memberTag))
				if probeFn := p.makeProbeByTagFunc(memberTag); probeFn != nil {
					entry.SetProbe(probeFn)
				}
			} else {
				logger.Warn("failed to register node: ", memberTag)
			}
		}
	} else {
		logger.Warn("monitor manager is nil, skipping node registration")
	}

	return p, nil
}

func normalizeOptions(options Options) Options {
	if options.FailureThreshold <= 0 {
		options.FailureThreshold = 3
	}
	if options.BlacklistDuration <= 0 {
		options.BlacklistDuration = 24 * time.Hour
	}
	if options.Metadata == nil {
		options.Metadata = make(map[string]MemberMeta)
	}
	if options.MaxRetries < 0 {
		options.MaxRetries = 0
	} else if options.MaxRetries == 0 {
		options.MaxRetries = 2 // default: up to 3 total attempts
	}
	switch strings.ToLower(options.Mode) {
	case modeAuto:
		options.Mode = modeAuto
	case modeRandom:
		options.Mode = modeRandom
	case modeBalance:
		options.Mode = modeBalance
	default:
		options.Mode = modeAuto
	}
	return options
}

func (p *poolOutbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	p.mu.Lock()
	err := p.initializeMembersLocked()
	p.mu.Unlock()
	if err != nil {
		return err
	}
	// 在初始化完成后，立即在后台触发健康检查
	if p.monitor != nil {
		go p.probeAllMembersOnStartup()
	}
	return nil
}

// initializeMembersLocked must be called with p.mu held
func (p *poolOutbound) initializeMembersLocked() error {
	if len(p.members) > 0 {
		return nil // Already initialized
	}

	members := make([]*memberState, 0, len(p.options.Members))
	for _, tag := range p.options.Members {
		detour, loaded := p.manager.Outbound(tag)
		if !loaded {
			return E.New("pool member not found: ", tag)
		}

		// Acquire shared state (creates if not exists, reuses if already created)
		state := acquireSharedState(tag)

		member := &memberState{
			outbound: detour,
			tag:      tag,
			shared:   state,
			entry:    state.entryHandle(),
		}

		// Connect to existing monitor entry if available
		if p.monitor != nil {
			meta := p.options.Metadata[tag]
			info := monitor.NodeInfo{
				Tag:            tag,
				Name:           meta.Name,
				URI:            meta.URI,
				Mode:           meta.Mode,
				ListenAddress:  meta.ListenAddress,
				Port:           meta.Port,
				Region:         meta.Region,
				Country:        meta.Country,
				SourceKind:     meta.SourceKind,
				SourceName:     meta.SourceName,
				SourceRef:      meta.SourceRef,
				ProtocolFamily: meta.ProtocolFamily,
				NodeMode:       meta.NodeMode,
				DomainFamily:   meta.DomainFamily,
			}
			entry := p.monitor.Register(info)
			if entry != nil {
				state.attachEntry(entry)
				member.entry = entry
				entry.SetRelease(p.makeReleaseFunc(member))
				if probe := p.makeProbeFunc(member); probe != nil {
					entry.SetProbe(probe)
				}
			}
		}
		members = append(members, member)
	}
	p.members = members
	p.logger.Info("pool initialized with ", len(members), " members")

	return nil
}

// probeAllMembersOnStartup performs initial health checks on all members
func (p *poolOutbound) probeAllMembersOnStartup() {
	targets, ok := p.monitor.ProbeTargets()
	if !ok {
		p.logger.Warn("probe target not configured, skipping initial health check")
		// 没有配置探测目标时，标记所有节点为可用
		p.mu.Lock()
		for _, member := range p.members {
			if member.entry != nil {
				member.entry.MarkInitialCheckDone(true)
			}
		}
		p.mu.Unlock()
		return
	}

	p.logger.Info("starting initial health check for all nodes")

	p.mu.Lock()
	members := make([]*memberState, len(p.members))
	copy(members, p.members)
	p.mu.Unlock()

	availableCount := 0
	failedCount := 0

	for _, member := range members {
		// Create a timeout context for each probe
		ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)

		latency, err := p.runProbeTargetsForMember(ctx, member, targets)
		if err != nil {
			p.logger.Warn("initial probe failed for ", member.tag, ": ", err)
			failedCount++
			if member.entry != nil {
				member.entry.RecordFailure(err, strings.Join(probeTargetLabels(targets), ","))
				member.entry.MarkInitialCheckDone(false) // 标记为不可用
			}
			cancel()
			continue
		}
		latencyMs := latency.Milliseconds()
		p.logger.Info("initial probe success for ", member.tag, ", latency: ", latencyMs, "ms")
		availableCount++
		if member.entry != nil {
			member.entry.RecordSuccessWithLatency(latency)
			member.entry.MarkInitialCheckDone(true)
		}

		cancel()
	}

	p.logger.Info("initial health check completed: ", availableCount, " available, ", failedCount, " failed")
}

func (p *poolOutbound) memberName(member *memberState) string {
	if meta, ok := p.options.Metadata[member.tag]; ok && meta.Name != "" {
		return meta.Name
	}
	return member.tag
}

func (p *poolOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	maxRetries := p.options.MaxRetries
	var lastErr error
	excluded := make(map[string]struct{})
	dst := destination.String()

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			break
		}
		member, err := p.pickMember(network, excluded)
		if err != nil {
			break
		}
		excluded[member.tag] = struct{}{}

		if attempt > 0 {
			p.logger.Info("→ ", dst, " ⇒ ", p.memberName(member), " [", network, "] (retry ", attempt, "/", maxRetries, ")")
		} else {
			p.logger.Info("→ ", dst, " ⇒ ", p.memberName(member), " [", network, "]")
		}

		p.incActive(member)
		conn, err := member.outbound.DialContext(ctx, network, destination)
		if err != nil {
			p.decActive(member)
			p.recordFailure(member, err, dst)
			lastErr = err
			continue
		}
		return p.wrapConn(conn, member, dst), nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, E.New("no healthy proxy available")
}

func (p *poolOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	maxRetries := p.options.MaxRetries
	var lastErr error
	excluded := make(map[string]struct{})
	dst := destination.String()

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			break
		}
		member, err := p.pickMember(N.NetworkUDP, excluded)
		if err != nil {
			break
		}
		excluded[member.tag] = struct{}{}

		if attempt > 0 {
			p.logger.Info("→ ", dst, " ⇒ ", p.memberName(member), " [udp] (retry ", attempt, "/", maxRetries, ")")
		} else {
			p.logger.Info("→ ", dst, " ⇒ ", p.memberName(member), " [udp]")
		}

		p.incActive(member)
		conn, err := member.outbound.ListenPacket(ctx, destination)
		if err != nil {
			p.decActive(member)
			p.recordFailure(member, err, dst)
			lastErr = err
			continue
		}
		return p.wrapPacketConn(conn, member, dst), nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, E.New("no healthy proxy available")
}

func (p *poolOutbound) pickMember(network string, excluded map[string]struct{}) (*memberState, error) {
	now := time.Now()
	candidates := p.getCandidateBuffer()
	sourceStates := p.sourceSelectionStates()
	secondaryStates := p.secondarySelectionStates()

	p.mu.Lock()
	if len(p.members) == 0 {
		if err := p.initializeMembersLocked(); err != nil {
			p.mu.Unlock()
			p.putCandidateBuffer(candidates)
			return nil, err
		}
	}
	candidates = p.availableMembersLocked(now, network, candidates, sourceStates, secondaryStates, true, true, excluded)
	p.mu.Unlock()

	if len(candidates) == 0 {
		p.mu.Lock()
		candidates = p.availableMembersLocked(now, network, candidates, sourceStates, secondaryStates, true, false, excluded)
		p.mu.Unlock()
	}

	if len(candidates) == 0 {
		p.mu.Lock()
		candidates = p.availableMembersLocked(now, network, candidates, sourceStates, secondaryStates, false, false, excluded)
		p.mu.Unlock()
	}

	if len(candidates) == 0 && len(excluded) == 0 {
		p.mu.Lock()
		if p.releaseIfAllBlacklistedLocked(now) {
			candidates = p.availableMembersLocked(now, network, candidates, sourceStates, secondaryStates, false, false, excluded)
		}
		p.mu.Unlock()
	}

	if len(candidates) == 0 {
		p.putCandidateBuffer(candidates)
		return nil, E.New("no healthy proxy available")
	}

	member := p.selectMember(candidates, sourceStates, secondaryStates)
	p.putCandidateBuffer(candidates)
	return member, nil
}

func (p *poolOutbound) availableMembersLocked(
	now time.Time,
	network string,
	buf []*memberState,
	sourceStates map[string]monitor.SourceSelectionState,
	secondaryStates map[string]monitor.SecondarySelectionState,
	enforceSourceExclusion bool,
	enforceSecondaryExclusion bool,
	excluded map[string]struct{},
) []*memberState {
	result := buf[:0]
	for _, member := range p.members {
		if _, skip := excluded[member.tag]; skip {
			continue
		}
		// Check blacklist via shared state (auto-clears if expired)
		if member.shared != nil && member.shared.isBlacklisted(now) {
			continue
		}
		if network != "" && !common.Contains(member.outbound.Network(), network) {
			continue
		}
		if enforceSourceExclusion {
			if state, ok := sourceStates[p.sourceRefForMember(member)]; ok && state.Excluded {
				continue
			}
		}
		if enforceSecondaryExclusion && p.secondaryExcludedForMember(member, secondaryStates) {
			continue
		}
		result = append(result, member)
	}
	return result
}

func (p *poolOutbound) releaseIfAllBlacklistedLocked(now time.Time) bool {
	if len(p.members) == 0 {
		return false
	}
	// Check if all members are blacklisted
	for _, member := range p.members {
		if member.shared == nil || !member.shared.isBlacklisted(now) {
			return false
		}
	}
	// All blacklisted, force release all
	for _, member := range p.members {
		if member.shared != nil {
			member.shared.forceRelease()
		}
	}
	p.logger.Warn("all upstream proxies were blacklisted, releasing them for retry")
	return true
}

func (p *poolOutbound) selectMember(
	candidates []*memberState,
	sourceStates map[string]monitor.SourceSelectionState,
	secondaryStates map[string]monitor.SecondarySelectionState,
) *memberState {
	switch p.mode {
	case modeRandom:
		p.rngMu.Lock()
		idx := p.rng.Intn(len(candidates))
		p.rngMu.Unlock()
		return candidates[idx]
	case modeBalance:
		var selected *memberState
		var minActive int32
		for _, member := range candidates {
			var active int32
			if member.shared != nil {
				active = member.shared.activeCount()
			}
			if selected == nil || active < minActive {
				selected = member
				minActive = active
				continue
			}
			if active == minActive && p.compareMembersByHealth(member, selected, sourceStates, secondaryStates) {
				selected = member
			}
		}
		return selected
	case modeSequential:
		idx := int(p.rrCounter.Add(1)-1) % len(candidates)
		return candidates[idx]
	default:
		best := candidates[0]
		for _, candidate := range candidates[1:] {
			if p.compareMembersByHealth(candidate, best, sourceStates, secondaryStates) {
				best = candidate
			}
		}
		return best
	}
}

func (p *poolOutbound) compareMembersByHealth(
	left, right *memberState,
	sourceStates map[string]monitor.SourceSelectionState,
	secondaryStates map[string]monitor.SecondarySelectionState,
) bool {
	leftSnap := memberSelectionSnapshot(left)
	rightSnap := memberSelectionSnapshot(right)

	leftSourcePenalty := p.sourcePenaltyForMember(left, sourceStates)
	rightSourcePenalty := p.sourcePenaltyForMember(right, sourceStates)
	leftSecondaryPenalty := p.secondaryPenaltyForMember(left, secondaryStates)
	rightSecondaryPenalty := p.secondaryPenaltyForMember(right, secondaryStates)
	leftScore := adjustedAvailabilityScore(leftSnap.AvailabilityScore, leftSourcePenalty+leftSecondaryPenalty)
	rightScore := adjustedAvailabilityScore(rightSnap.AvailabilityScore, rightSourcePenalty+rightSecondaryPenalty)
	if leftScore != rightScore {
		return leftScore > rightScore
	}

	leftActive := int32(0)
	if left.shared != nil {
		leftActive = left.shared.activeCount()
	}
	rightActive := int32(0)
	if right.shared != nil {
		rightActive = right.shared.activeCount()
	}
	if leftActive != rightActive {
		return leftActive < rightActive
	}

	leftLatency := normalizeLatencyForSelection(leftSnap.LastLatencyMs)
	rightLatency := normalizeLatencyForSelection(rightSnap.LastLatencyMs)
	if leftLatency != rightLatency {
		return leftLatency < rightLatency
	}

	if leftSnap.ReportedFailureCount != rightSnap.ReportedFailureCount {
		return leftSnap.ReportedFailureCount < rightSnap.ReportedFailureCount
	}
	if leftSnap.ReportedSuccessCount != rightSnap.ReportedSuccessCount {
		return leftSnap.ReportedSuccessCount > rightSnap.ReportedSuccessCount
	}
	if leftSnap.FailureCount != rightSnap.FailureCount {
		return leftSnap.FailureCount < rightSnap.FailureCount
	}
	if leftSnap.SuccessCount != rightSnap.SuccessCount {
		return leftSnap.SuccessCount > rightSnap.SuccessCount
	}
	return left.tag < right.tag
}

func adjustedAvailabilityScore(base int, penalty int) int {
	score := base - penalty
	if score < 1 {
		return 1
	}
	return score
}

func memberSelectionSnapshot(member *memberState) monitor.Snapshot {
	if member == nil || member.entry == nil {
		return monitor.Snapshot{AvailabilityScore: 100, LastLatencyMs: -1}
	}
	return member.entry.Snapshot()
}

func (p *poolOutbound) sourceSelectionStates() map[string]monitor.SourceSelectionState {
	if p == nil || p.monitor == nil {
		return nil
	}
	return p.monitor.SourceSelectionStates()
}

func (p *poolOutbound) secondarySelectionStates() map[string]monitor.SecondarySelectionState {
	if p == nil || p.monitor == nil {
		return nil
	}
	return p.monitor.SecondarySelectionStates()
}

func (p *poolOutbound) sourceRefForMember(member *memberState) string {
	if p == nil || member == nil {
		return ""
	}
	meta, ok := p.options.Metadata[member.tag]
	if !ok {
		return ""
	}
	return strings.TrimSpace(meta.SourceRef)
}

func (p *poolOutbound) sourcePenaltyForMember(
	member *memberState,
	sourceStates map[string]monitor.SourceSelectionState,
) int {
	if len(sourceStates) == 0 {
		return 0
	}
	ref := p.sourceRefForMember(member)
	if ref == "" {
		return 0
	}
	state, ok := sourceStates[ref]
	if !ok {
		return 0
	}
	return state.Penalty
}

func (p *poolOutbound) secondaryPenaltyForMember(
	member *memberState,
	secondaryStates map[string]monitor.SecondarySelectionState,
) int {
	if len(secondaryStates) == 0 {
		return 0
	}
	total := 0
	for _, key := range p.secondarySelectionKeysForMember(member) {
		state, ok := secondaryStates[key]
		if !ok {
			continue
		}
		total += state.Penalty
	}
	if total > 80 {
		return 80
	}
	return total
}

func (p *poolOutbound) secondaryExcludedForMember(
	member *memberState,
	secondaryStates map[string]monitor.SecondarySelectionState,
) bool {
	if len(secondaryStates) == 0 {
		return false
	}
	for _, key := range p.secondarySelectionKeysForMember(member) {
		state, ok := secondaryStates[key]
		if ok && state.Excluded {
			return true
		}
	}
	return false
}

func (p *poolOutbound) secondarySelectionKeysForMember(member *memberState) []string {
	if p == nil || member == nil {
		return nil
	}
	meta, ok := p.options.Metadata[member.tag]
	if !ok {
		return nil
	}
	sourceRef := strings.TrimSpace(meta.SourceRef)
	if sourceRef == "" {
		return nil
	}
	keys := make([]string, 0, 3)
	if value := strings.TrimSpace(meta.ProtocolFamily); value != "" {
		keys = append(keys, monitor.SecondarySelectionStateKey(sourceRef, monitor.SelectionDimensionProtocolFamily, value))
	}
	if value := strings.TrimSpace(meta.NodeMode); value != "" {
		keys = append(keys, monitor.SecondarySelectionStateKey(sourceRef, monitor.SelectionDimensionNodeMode, value))
	}
	if value := strings.TrimSpace(meta.DomainFamily); value != "" {
		keys = append(keys, monitor.SecondarySelectionStateKey(sourceRef, monitor.SelectionDimensionDomainFamily, value))
	}
	return keys
}

func normalizeLatencyForSelection(value int64) int64 {
	if value <= 0 {
		return 1<<62 - 1
	}
	return value
}

func (p *poolOutbound) shouldSkipProbeTLSVerify() bool {
	return p != nil && p.monitor != nil && p.monitor.SkipCertVerify()
}

func (p *poolOutbound) recordFailure(member *memberState, cause error, destination string) {
	if member.shared == nil {
		p.logger.Warn("proxy ", member.tag, " failure (no shared state): ", cause)
		return
	}
	failures, blacklisted, _ := member.shared.recordFailure(cause, p.options.FailureThreshold, p.options.BlacklistDuration, destination)
	if blacklisted {
		p.logger.Warn("proxy ", member.tag, " blacklisted for ", p.options.BlacklistDuration, ": ", cause)
	} else {
		p.logger.Warn("proxy ", member.tag, " failure ", failures, "/", p.options.FailureThreshold, ": ", cause)
	}
}

func (p *poolOutbound) recordSuccess(member *memberState, destination string) {
	if member.shared != nil {
		member.shared.recordSuccess(destination)
	}
}

func (p *poolOutbound) wrapConn(conn net.Conn, member *memberState, destination string) net.Conn {
	return &trackedConn{
		Conn: conn,
		release: func() {
			p.decActive(member)
		},
		onTraffic: func(upload, download int64) {
			if member.shared != nil {
				member.shared.addTraffic(upload, download)
			}
		},
		onConfirmedSuccess: func() {
			p.recordSuccess(member, destination)
		},
	}
}

func (p *poolOutbound) wrapPacketConn(conn net.PacketConn, member *memberState, destination string) net.PacketConn {
	return &trackedPacketConn{
		PacketConn: conn,
		release: func() {
			p.decActive(member)
		},
		onTraffic: func(upload, download int64) {
			if member.shared != nil {
				member.shared.addTraffic(upload, download)
			}
		},
		onConfirmedSuccess: func() {
			p.recordSuccess(member, destination)
		},
	}
}

func (p *poolOutbound) makeReleaseFunc(member *memberState) func() {
	return func() {
		if member.shared != nil {
			member.shared.forceRelease()
		}
	}
}

// httpProbe performs an HTTP probe through the connection and measures TTFB.
// It sends a minimal HTTP request and waits for the first byte of response.
func httpProbe(conn net.Conn, destination M.Socksaddr, skipCertVerify ...bool) (time.Duration, error) {
	return httpProbeTarget(conn, monitor.ProbeTargetSpec{
		Scheme:  map[bool]string{true: "https", false: "http"}[destination.Port == 443],
		Host:    destination.AddrString(),
		Port:    destination.Port,
		Path:    "/generate_204",
		HostHdr: destination.AddrString(),
		Dst:     destination,
	}, skipCertVerify...)
}

func httpProbeTarget(conn net.Conn, target monitor.ProbeTargetSpec, skipCertVerify ...bool) (time.Duration, error) {
	probeConn := conn
	host := target.Host
	hostHeader := target.HostHdr
	if hostHeader == "" {
		hostHeader = target.Host
	}
	if target.Scheme == "https" {
		serverName := target.Dst.Fqdn
		if serverName == "" {
			serverName = host
		}
		insecure := len(skipCertVerify) > 0 && skipCertVerify[0]
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: insecure,
		})
		if err := tlsConn.Handshake(); err != nil {
			return 0, fmt.Errorf("tls handshake: %w", err)
		}
		probeConn = tlsConn
	}

	path := target.Path
	if path == "" {
		path = "/"
	}
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: Mozilla/5.0\r\nAccept: */*\r\n\r\n", path, hostHeader)

	_ = probeConn.SetWriteDeadline(time.Now().Add(5 * time.Second))

	start := time.Now()

	if _, err := probeConn.Write([]byte(req)); err != nil {
		return 0, fmt.Errorf("write request: %w", err)
	}

	_ = probeConn.SetReadDeadline(time.Now().Add(10 * time.Second))

	reader := bufio.NewReader(probeConn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}
	parts := strings.Fields(strings.TrimSpace(statusLine))
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid status line: %q", strings.TrimSpace(statusLine))
	}
	var status int
	if _, err := fmt.Sscanf(parts[1], "%d", &status); err != nil {
		return 0, fmt.Errorf("parse status line %q: %w", strings.TrimSpace(statusLine), err)
	}
	if status < 200 || status >= 500 {
		return 0, fmt.Errorf("unexpected HTTP status %d from %s", status, target.Original)
	}

	ttfb := time.Since(start)
	return ttfb, nil
}

func (p *poolOutbound) makeProbeFunc(member *memberState) func(ctx context.Context) (time.Duration, error) {
	if p.monitor == nil {
		return nil
	}
	// 仅在创建时检查是否有探测目标，实际目标在执行时动态获取
	if _, ok := p.monitor.ProbeTargets(); !ok {
		return nil
	}
	return func(ctx context.Context) (time.Duration, error) {
		// 每次执行时动态获取最新的探测目标
		targets, ok := p.monitor.ProbeTargets()
		if !ok {
			return 0, E.New("probe target not configured")
		}

		duration, err := p.runProbeTargetsForMember(ctx, member, targets)
		if err != nil {
			if member.entry != nil {
				member.entry.RecordFailure(err, strings.Join(probeTargetLabels(targets), ","))
			}
			return 0, err
		}

		if member.entry != nil {
			member.entry.RecordSuccessWithLatency(duration)
		}
		return duration, nil
	}
}

// makeProbeByTagFunc creates a probe function that works before member initialization
func (p *poolOutbound) makeProbeByTagFunc(tag string) func(ctx context.Context) (time.Duration, error) {
	if p.monitor == nil {
		return nil
	}
	// 仅在创建时检查是否有探测目标，实际目标在执行时动态获取
	if _, ok := p.monitor.ProbeTargets(); !ok {
		return nil
	}
	return func(ctx context.Context) (time.Duration, error) {
		// 每次执行时动态获取最新的探测目标
		targets, ok := p.monitor.ProbeTargets()
		if !ok {
			return 0, E.New("probe target not configured")
		}

		// Ensure members are initialized
		p.mu.Lock()
		if len(p.members) == 0 {
			if err := p.initializeMembersLocked(); err != nil {
				p.mu.Unlock()
				return 0, err
			}
		}

		// Find the member by tag
		var member *memberState
		for _, m := range p.members {
			if m.tag == tag {
				member = m
				break
			}
		}
		p.mu.Unlock()

		if member == nil {
			return 0, E.New("member not found: ", tag)
		}

		duration, err := p.runProbeTargetsForMember(ctx, member, targets)
		if err != nil {
			if member.entry != nil {
				member.entry.RecordFailure(err, strings.Join(probeTargetLabels(targets), ","))
			}
			return 0, err
		}

		if member.entry != nil {
			member.entry.RecordSuccessWithLatency(duration)
		}
		return duration, nil
	}
}

func probeTargetLabels(targets []monitor.ProbeTargetSpec) []string {
	labels := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.Original != "" {
			labels = append(labels, target.Original)
			continue
		}
		labels = append(labels, target.Dst.String())
	}
	return labels
}

func normalizeLocalProbeHost(host string) string {
	trimmed := strings.TrimSpace(host)
	switch trimmed {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return trimmed
	}
}

func (p *poolOutbound) memberProbeProxyAddress(member *memberState) string {
	if member == nil {
		return ""
	}
	meta, ok := p.options.Metadata[member.tag]
	if !ok || meta.Port == 0 {
		return ""
	}
	host := normalizeLocalProbeHost(meta.ListenAddress)
	return net.JoinHostPort(host, strconv.Itoa(int(meta.Port)))
}

func (p *poolOutbound) runProbeTargetsForMember(ctx context.Context, member *memberState, targets []monitor.ProbeTargetSpec) (time.Duration, error) {
	var errs []string

	if proxyAddress := p.memberProbeProxyAddress(member); proxyAddress != "" {
		duration, err := p.runProbeTargetsViaHTTPProxy(ctx, proxyAddress, targets)
		if err == nil {
			return duration, nil
		}
		errs = append(errs, fmt.Sprintf("local proxy probe via %s: %v", proxyAddress, err))
	}

	if member != nil && member.outbound != nil {
		duration, err := p.runProbeTargets(ctx, member.outbound, targets)
		if err == nil {
			return duration, nil
		}
		errs = append(errs, fmt.Sprintf("raw outbound probe: %v", err))
	}

	if len(errs) == 0 {
		return 0, E.New("member probe failed: missing outbound and local proxy metadata")
	}
	return 0, E.New(strings.Join(errs, " | "))
}

func dialContextTCP(ctx context.Context, address string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return dialer.DialContext(ctx, "tcp", address)
}

func connectHTTPProxy(conn net.Conn, target monitor.ProbeTargetSpec) error {
	host := target.Host
	if host == "" {
		host = target.Dst.AddrString()
	}
	port := target.Port
	if port == 0 {
		port = target.Dst.Port
	}
	authority := net.JoinHostPort(host, strconv.Itoa(int(port)))
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\nUser-Agent: EasyProxy-Probe/1.0\r\n\r\n", authority, authority)

	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("write CONNECT request: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read CONNECT response: %w", err)
	}
	parts := strings.Fields(strings.TrimSpace(statusLine))
	if len(parts) < 2 {
		return fmt.Errorf("invalid CONNECT status line: %q", strings.TrimSpace(statusLine))
	}
	var status int
	if _, err := fmt.Sscanf(parts[1], "%d", &status); err != nil {
		return fmt.Errorf("parse CONNECT status %q: %w", strings.TrimSpace(statusLine), err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("unexpected CONNECT status %d for %s", status, authority)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read CONNECT headers: %w", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	_ = conn.SetDeadline(time.Time{})
	return nil
}

func (p *poolOutbound) runProbeTargetsViaHTTPProxy(ctx context.Context, proxyAddress string, targets []monitor.ProbeTargetSpec) (time.Duration, error) {
	var errs []string
	for _, target := range targets {
		start := time.Now()
		conn, err := dialContextTCP(ctx, proxyAddress)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s proxy dial: %v", target.Original, err))
			continue
		}
		err = connectHTTPProxy(conn, target)
		if err != nil {
			conn.Close()
			errs = append(errs, fmt.Sprintf("%s proxy connect: %v", target.Original, err))
			continue
		}
		if target.Scheme == "tcp" {
			conn.Close()
			return time.Since(start), nil
		}
		_, err = httpProbeTarget(conn, target, p.shouldSkipProbeTLSVerify())
		conn.Close()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s proxy probe: %v", target.Original, err))
			continue
		}
		return time.Since(start), nil
	}
	return 0, E.New("all proxy probe targets failed: ", strings.Join(errs, " | "))
}

func (p *poolOutbound) runProbeTargets(ctx context.Context, outbound adapter.Outbound, targets []monitor.ProbeTargetSpec) (time.Duration, error) {
	var errs []string
	for _, target := range targets {
		start := time.Now()
		conn, err := outbound.DialContext(ctx, N.NetworkTCP, target.Dst)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s dial: %v", target.Original, err))
			continue
		}
		if target.Scheme == "tcp" {
			conn.Close()
			return time.Since(start), nil
		}
		_, err = httpProbeTarget(conn, target, p.shouldSkipProbeTLSVerify())
		conn.Close()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s probe: %v", target.Original, err))
			continue
		}
		return time.Since(start), nil
	}
	return 0, E.New("all probe targets failed: ", strings.Join(errs, " | "))
}

// makeReleaseByTagFunc creates a release function that works before member initialization
func (p *poolOutbound) makeReleaseByTagFunc(tag string) func() {
	return func() {
		releaseSharedMember(tag)
	}
}

type trackedConn struct {
	net.Conn
	once               sync.Once
	successOnce        sync.Once
	release            func()
	onTraffic          func(upload, download int64)
	onConfirmedSuccess func()
}

func (c *trackedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 && c.onTraffic != nil {
		c.onTraffic(0, int64(n))
	}
	if n > 0 && c.onConfirmedSuccess != nil {
		c.successOnce.Do(c.onConfirmedSuccess)
	}
	return n, err
}

func (c *trackedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 && c.onTraffic != nil {
		c.onTraffic(int64(n), 0)
	}
	return n, err
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

type trackedPacketConn struct {
	net.PacketConn
	once               sync.Once
	successOnce        sync.Once
	release            func()
	onTraffic          func(upload, download int64)
	onConfirmedSuccess func()
}

func (c *trackedPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(b)
	if n > 0 && c.onTraffic != nil {
		c.onTraffic(0, int64(n))
	}
	if n > 0 && c.onConfirmedSuccess != nil {
		c.successOnce.Do(c.onConfirmedSuccess)
	}
	return n, addr, err
}

func (c *trackedPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(b, addr)
	if n > 0 && c.onTraffic != nil {
		c.onTraffic(int64(n), 0)
	}
	return n, err
}

func (c *trackedPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(c.release)
	return err
}

func (p *poolOutbound) incActive(member *memberState) {
	if member.shared != nil {
		member.shared.incActive()
	}
}

func (p *poolOutbound) decActive(member *memberState) {
	if member.shared != nil {
		member.shared.decActive()
	}
}

func (p *poolOutbound) getCandidateBuffer() []*memberState {
	if buf := p.candidatesPool.Get(); buf != nil {
		return buf.([]*memberState)
	}
	return make([]*memberState, 0, len(p.options.Members))
}

func (p *poolOutbound) putCandidateBuffer(buf []*memberState) {
	if buf == nil {
		return
	}
	const maxCached = 4096
	if cap(buf) > maxCached {
		return
	}
	p.candidatesPool.Put(buf[:0])
}
