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
	MaxRetries        int           // max retry attempts on connection failure (default 2)
	SessionTTL        time.Duration // idle TTL for session-strategy stickiness (default 10m)
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
	CountryISO     string // ISO country code from GeoIP (e.g. "US", "JP")
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
	sticky         *stickyState
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
		sticky: newStickyState(normalized.SessionTTL),
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
				entry.SetRelease(releaseSharedState(state))
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

		// The constructor registers and binds the monitor entry before sing-box
		// starts the outbound. Reuse that entry during lazy initialization so the
		// first probe does not replace its callback/revision while it is running.
		if member.entry != nil {
			member.entry.SetRelease(p.makeReleaseFunc(member))
			members = append(members, member)
			continue
		}

		// Register a monitor entry when this pool belongs to a fresh reload
		// generation and no constructor-time entry is available.
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

func (p *poolOutbound) memberName(member *memberState) string {
	if meta, ok := p.options.Metadata[member.tag]; ok && meta.Name != "" {
		return meta.Name
	}
	return member.tag
}

// StickySnapshot exposes the current stable-bucket and session affinity state
// for observability. Returns an empty snapshot when stickiness is not in use.
func (p *poolOutbound) StickySnapshot() StickySnapshot {
	if p == nil || p.sticky == nil {
		return StickySnapshot{Buckets: map[string]string{}, Sessions: map[string]string{}}
	}
	return p.sticky.snapshot()
}

func (p *poolOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	maxRetries := p.options.MaxRetries
	var lastErr error
	excluded := make(map[string]struct{})
	dst := destination.String()
	directive := DirectiveFrom(ctx)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			break
		}
		member, err := p.pickMember(network, excluded, directive)
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
	directive := DirectiveFrom(ctx)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			break
		}
		member, err := p.pickMember(N.NetworkUDP, excluded, directive)
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

func (p *poolOutbound) pickMember(network string, excluded map[string]struct{}, directive *SelectionDirective) (*memberState, error) {
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
	candidates = p.availableMembersLocked(now, network, candidates, sourceStates, secondaryStates, true, true, excluded, directive)
	p.mu.Unlock()

	if len(candidates) == 0 {
		p.mu.Lock()
		candidates = p.availableMembersLocked(now, network, candidates, sourceStates, secondaryStates, true, false, excluded, directive)
		p.mu.Unlock()
	}

	if len(candidates) == 0 {
		p.mu.Lock()
		candidates = p.availableMembersLocked(now, network, candidates, sourceStates, secondaryStates, false, false, excluded, directive)
		p.mu.Unlock()
	}

	if len(candidates) == 0 && len(excluded) == 0 {
		p.mu.Lock()
		if p.releaseIfAllBlacklistedLocked(now) {
			candidates = p.availableMembersLocked(now, network, candidates, sourceStates, secondaryStates, false, false, excluded, directive)
		}
		p.mu.Unlock()
	}

	if len(candidates) == 0 {
		p.putCandidateBuffer(candidates)
		return nil, E.New("no healthy proxy available")
	}

	member := p.selectMemberWithDirective(candidates, sourceStates, secondaryStates, directive)
	p.putCandidateBuffer(candidates)
	return member, nil
}

// selectMemberWithDirective applies stable/session stickiness when a directive
// requests it, otherwise falls back to the pool's configured Mode selection.
// candidates is the already filtered, healthy candidate set.
func (p *poolOutbound) selectMemberWithDirective(
	candidates []*memberState,
	sourceStates map[string]monitor.SourceSelectionState,
	secondaryStates map[string]monitor.SecondarySelectionState,
	directive *SelectionDirective,
) *memberState {
	if directive == nil || p.sticky == nil {
		return p.selectMember(candidates, sourceStates, secondaryStates)
	}

	selectionCandidates := candidates
	var preferredCandidates []*memberState
	if directive.Strategy == StrategyStable && directive.Filter.LongLived == nil {
		preferredCandidates = p.getCandidateBuffer()
		for _, member := range candidates {
			if member.entry != nil && member.entry.Snapshot().LongLived {
				preferredCandidates = append(preferredCandidates, member)
			}
		}
		if len(preferredCandidates) > 0 {
			selectionCandidates = preferredCandidates
		}
	}
	if preferredCandidates != nil {
		defer p.putCandidateBuffer(preferredCandidates)
	}

	// A manually pinned tag wins whenever it is still a healthy candidate.
	// Otherwise we fall through to sticky promotion, which auto-fails-over to
	// the next best node in the same bucket/session.
	if directive.PinnedTag != "" {
		if m := candidateByTag(candidates, directive.PinnedTag); m != nil {
			return m
		}
	}

	// fallback is the best candidate per the pool's configured Mode; sticky
	// selection reuses it only when no healthy pinned member already exists.
	fallback := p.selectMember(selectionCandidates, sourceStates, secondaryStates)

	switch directive.Strategy {
	case StrategyStable:
		// Existing stable bindings remain valid while their node is still in the
		// full healthy/filtered set. The long-lived preference only influences a
		// new binding or promotion after the previous node disappears.
		return p.sticky.pickStable(directive.Filter.bucketKey(), candidates, fallback)
	case StrategySession:
		// pickSession treats an empty key as "no stickiness" and just returns
		// the fallback, so keyless callers never collapse onto one node.
		return p.sticky.pickSession(directive.SessionKey, selectionCandidates, fallback)
	default:
		return fallback
	}
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
	directive *SelectionDirective,
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
		if directive != nil && !p.memberMatchesFilter(member, directive.Filter) {
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

// memberMatchesFilter reports whether a member satisfies the directive's
// attribute filter (country / region / long-lived). An empty filter matches
// every member. Country matching accepts either the ISO code or the full
// country name so callers can pass either form.
func (p *poolOutbound) memberMatchesFilter(member *memberState, filter NodeFilter) bool {
	if filter.IsZero() {
		return true
	}
	meta := p.options.Metadata[member.tag]

	if len(filter.Countries) > 0 {
		iso := strings.ToUpper(strings.TrimSpace(meta.CountryISO))
		name := strings.ToUpper(strings.TrimSpace(meta.Country))
		matched := false
		for _, want := range filter.Countries {
			if want == iso || want == name {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if len(filter.Regions) > 0 {
		region := strings.ToLower(strings.TrimSpace(meta.Region))
		matched := false
		for _, want := range filter.Regions {
			if want == region {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if filter.LongLived != nil {
		if member.entry == nil {
			return !*filter.LongLived
		}
		if member.entry.Snapshot().LongLived != *filter.LongLived {
			return false
		}
	}

	return true
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
	return func(ctx context.Context) (time.Duration, error) {
		// 每次执行时动态获取最新的探测目标
		targets, ok := p.monitor.ProbeTargets()
		if !ok {
			return 0, E.New("probe target not configured")
		}

		duration, err := p.runProbeTargetsForMember(ctx, member, targets)
		return duration, err
	}
}

// makeProbeByTagFunc creates a probe function that works before member initialization
func (p *poolOutbound) makeProbeByTagFunc(tag string) func(ctx context.Context) (time.Duration, error) {
	if p.monitor == nil {
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
		return duration, err
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
