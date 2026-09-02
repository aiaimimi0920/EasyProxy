package pool

import (
	"strings"
	"time"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
)

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
	enforceTCPHealth := network != N.NetworkUDP
	candidates = p.availableMembersLocked(now, network, candidates, sourceStates, secondaryStates, enforceTCPHealth, enforceTCPHealth, excluded, directive)
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
		if p.releaseIfAllBlacklistedLocked(now, network) {
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
	selectionCandidates, protocolCandidates := p.preferProtocolCandidates(selectionCandidates, directive)
	if protocolCandidates != nil {
		defer p.putCandidateBuffer(protocolCandidates)
	}
	var preferredCandidates []*memberState
	if directive.Strategy == StrategyStable && directive.Filter.LongLived == nil {
		preferredCandidates = p.getCandidateBuffer()
		for _, member := range selectionCandidates {
			if p.memberMeetsLongLivedPolicy(member, directive) {
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
		return p.sticky.pickStable(directive.stableBucketKey(), candidates, fallback)
	case StrategySession:
		// pickSession treats an empty key as "no stickiness" and just returns
		// the fallback, so keyless callers never collapse onto one node.
		return p.sticky.pickSession(directive.namespacedSessionKey(), directive.SessionTTL, selectionCandidates, fallback)
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
		if member.shared != nil && member.shared.isTransportBlacklisted(network, now) {
			continue
		}
		if network != "" && !common.Contains(member.outbound.Network(), network) {
			continue
		}
		if directive != nil && !p.memberMatchesFilter(member, directive) {
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
func (p *poolOutbound) memberMatchesFilter(member *memberState, directive *SelectionDirective) bool {
	if directive == nil {
		return true
	}
	filter := directive.Filter
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
		if p.memberMeetsLongLivedPolicy(member, directive) != *filter.LongLived {
			return false
		}
	}

	return true
}

func (p *poolOutbound) memberMeetsLongLivedPolicy(member *memberState, directive *SelectionDirective) bool {
	if member == nil || member.entry == nil {
		return false
	}
	snapshot := member.entry.Snapshot()
	if directive != nil && (directive.LongLived.MinUptime > 0 || directive.LongLived.MinSuccessRate > 0) {
		return monitor.MeetsLongLivedPolicy(snapshot, directive.LongLived.MinUptime, directive.LongLived.MinSuccessRate)
	}
	return snapshot.LongLived
}

func (p *poolOutbound) releaseIfAllBlacklistedLocked(now time.Time, network string) bool {
	if len(p.members) == 0 {
		return false
	}
	// Check if all members are blacklisted
	for _, member := range p.members {
		if member.shared == nil || !member.shared.isTransportBlacklisted(network, now) {
			return false
		}
	}
	if network == N.NetworkUDP {
		return false
	}
	// All blacklisted, force release all
	for _, member := range p.members {
		if member.shared != nil {
			member.shared.forceReleaseTransport(network)
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
