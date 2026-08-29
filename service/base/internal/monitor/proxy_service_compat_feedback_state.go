package monitor

import (
	"strings"
	"time"
)

func (state *proxyCompatState) reservationCount(nodeTag string) int {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.nodeReservations[nodeTag]
}

func (state *proxyCompatState) activeServiceLeaseCount(serviceKey, nodeTag string) int {
	normalizedServiceKey := normalizeProxyCompatServiceKey(serviceKey, "")
	normalizedNodeTag := strings.TrimSpace(nodeTag)
	if normalizedServiceKey == "" || normalizedNodeTag == "" {
		return 0
	}

	state.mu.RLock()
	defer state.mu.RUnlock()

	count := 0
	for _, leaseState := range state.leases {
		if leaseState == nil || leaseState.Lease.Status != "active" {
			continue
		}
		if strings.TrimSpace(leaseState.SelectedNodeTag) != normalizedNodeTag {
			continue
		}
		leaseServiceKey := normalizeProxyCompatServiceKey(
			firstNonEmptyCompatValue(
				leaseState.Lease.Metadata["serviceKey"],
				leaseState.Lease.Metadata["service"],
				leaseState.Lease.HostID,
			),
			leaseState.Lease.HostID,
		)
		if leaseServiceKey == normalizedServiceKey {
			count++
		}
	}
	return count
}

func (state *proxyCompatState) recentServiceSuccessStats(serviceKey, stage, nodeTag string, window time.Duration) (int, int) {
	normalizedServiceKey := normalizeProxyCompatServiceKey(serviceKey, "")
	normalizedStage := normalizeProxyCompatUsageStage(stage)
	normalizedNodeTag := strings.TrimSpace(nodeTag)
	if normalizedServiceKey == "" || normalizedStage == "" || normalizedNodeTag == "" || window <= 0 {
		return 0, 0
	}

	cutoff := time.Now().Add(-window)
	state.mu.RLock()
	defer state.mu.RUnlock()

	successCount := 0
	successStreak := 0
	streakOpen := true
	for idx := len(state.usageRecords) - 1; idx >= 0; idx-- {
		record := state.usageRecords[idx]
		if strings.TrimSpace(record.SelectedNodeTag) != normalizedNodeTag {
			continue
		}
		if normalizeProxyCompatServiceKey(record.ServiceKey, "") != normalizedServiceKey {
			continue
		}
		if normalizeProxyCompatUsageStage(record.Stage) != normalizedStage {
			continue
		}

		reportedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(record.ReportedAt))
		if err == nil && !reportedAt.IsZero() && reportedAt.Before(cutoff) {
			break
		}

		if record.Success {
			successCount++
			if streakOpen {
				successStreak++
			}
			continue
		}
		streakOpen = false
	}

	return successCount, successStreak
}

func (state *proxyCompatState) releaseReservationLocked(nodeTag string) {
	if nodeTag == "" {
		return
	}
	current := state.nodeReservations[nodeTag]
	if current <= 1 {
		delete(state.nodeReservations, nodeTag)
		return
	}
	state.nodeReservations[nodeTag] = current - 1
}

func (state *proxyCompatState) serviceFeedbackForNode(hostID, nodeTag string) (proxyCompatServiceFeedback, bool) {
	return state.serviceFeedbackForRef(hostID, proxyCompatServiceFeedbackRef{
		Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeNode, nodeTag),
		ScopeKind:  proxyCompatFeedbackScopeNode,
		ScopeValue: strings.TrimSpace(nodeTag),
	})
}

func (state *proxyCompatState) serviceFeedbackForRef(hostID string, ref proxyCompatServiceFeedbackRef) (proxyCompatServiceFeedback, bool) {
	normalizedHostID := normalizeProxyCompatHostID(hostID)
	if normalizedHostID == "" || strings.TrimSpace(ref.Key) == "" {
		return proxyCompatServiceFeedback{}, false
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	state.clearExpiredServiceCooldownsLocked(time.Now())

	byNode, ok := state.serviceFeedback[normalizedHostID]
	if !ok {
		return proxyCompatServiceFeedback{}, false
	}
	feedback, ok := byNode[ref.Key]
	if !ok || feedback == nil {
		return proxyCompatServiceFeedback{}, false
	}
	return *feedback, true
}

func (state *proxyCompatState) recordServiceSuccess(hostID, nodeTag string) {
	state.recordServiceSuccessForRefs(hostID, []proxyCompatServiceFeedbackRef{{
		Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeNode, nodeTag),
		ScopeKind:  proxyCompatFeedbackScopeNode,
		ScopeValue: strings.TrimSpace(nodeTag),
	}})
}

func (state *proxyCompatState) recordServiceSuccessForRefs(hostID string, refs []proxyCompatServiceFeedbackRef) {
	normalizedHostID := normalizeProxyCompatHostID(hostID)
	if normalizedHostID == "" || len(refs) == 0 {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	byNode, ok := state.serviceFeedback[normalizedHostID]
	if !ok {
		return
	}
	for _, ref := range refs {
		if strings.TrimSpace(ref.Key) == "" {
			continue
		}
		delete(byNode, ref.Key)
	}
	if len(byNode) == 0 {
		delete(state.serviceFeedback, normalizedHostID)
	}
}

func (state *proxyCompatState) recordServiceFailure(hostID, nodeTag, errorCode string, decision proxyCompatUsageFeedbackDecision) proxyCompatServiceFeedback {
	return state.recordServiceFailureForRef(hostID, proxyCompatServiceFeedbackRef{
		Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeNode, nodeTag),
		ScopeKind:  proxyCompatFeedbackScopeNode,
		ScopeValue: strings.TrimSpace(nodeTag),
	}, errorCode, decision)
}

func (state *proxyCompatState) recordServiceFailureForRef(
	hostID string,
	ref proxyCompatServiceFeedbackRef,
	errorCode string,
	decision proxyCompatUsageFeedbackDecision,
) proxyCompatServiceFeedback {
	normalizedHostID := normalizeProxyCompatHostID(hostID)
	if normalizedHostID == "" || strings.TrimSpace(ref.Key) == "" {
		return proxyCompatServiceFeedback{}
	}

	now := time.Now()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.clearExpiredServiceCooldownsLocked(now)

	byNode, ok := state.serviceFeedback[normalizedHostID]
	if !ok {
		byNode = make(map[string]*proxyCompatServiceFeedback)
		state.serviceFeedback[normalizedHostID] = byNode
	}
	feedback, ok := byNode[ref.Key]
	if !ok || feedback == nil {
		feedback = &proxyCompatServiceFeedback{
			HostID:      normalizedHostID,
			NodeTag:     strings.TrimSpace(ref.ScopeValue),
			FeedbackKey: strings.TrimSpace(ref.Key),
			ScopeKind:   strings.TrimSpace(ref.ScopeKind),
			ScopeValue:  strings.TrimSpace(ref.ScopeValue),
		}
		if feedback.ScopeKind != proxyCompatFeedbackScopeNode {
			feedback.NodeTag = ""
		}
		byNode[ref.Key] = feedback
	}

	consecutiveFailures := 1
	lastReportedAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(feedback.LastReportedAt))
	if feedback.ConsecutiveFailures > 0 &&
		feedback.LastErrorClass == decision.ErrorClass &&
		!lastReportedAt.IsZero() &&
		now.Sub(lastReportedAt) <= 24*time.Hour {
		consecutiveFailures = feedback.ConsecutiveFailures + 1
	}

	feedback.ConsecutiveFailures = consecutiveFailures
	feedback.Penalty = decision.Penalty + max(0, consecutiveFailures-1)*5
	if feedback.Penalty > 95 {
		feedback.Penalty = 95
	}
	feedback.LastErrorClass = decision.ErrorClass
	feedback.LastErrorCode = strings.TrimSpace(errorCode)
	feedback.LastReportedAt = now.Format(time.RFC3339)

	cooldown := decision.CooldownBase
	if decision.EscalateAfterCount > 0 &&
		consecutiveFailures >= decision.EscalateAfterCount &&
		decision.CooldownEscalated > 0 {
		cooldown = decision.CooldownEscalated
	}
	if cooldown > 0 {
		feedback.CooldownUntil = now.Add(cooldown).Format(time.RFC3339)
	} else {
		feedback.CooldownUntil = ""
	}

	return *feedback
}

func (state *proxyCompatState) clearExpiredServiceCooldownsLocked(now time.Time) {
	for hostID, byNode := range state.serviceFeedback {
		for nodeTag, feedback := range byNode {
			if feedback == nil {
				delete(byNode, nodeTag)
				continue
			}
			cooldownUntil, err := time.Parse(time.RFC3339, strings.TrimSpace(feedback.CooldownUntil))
			if err != nil || cooldownUntil.IsZero() || now.After(cooldownUntil) {
				feedback.CooldownUntil = ""
			}
		}
		if len(byNode) == 0 {
			delete(state.serviceFeedback, hostID)
		}
	}
}

func (state *proxyCompatState) serviceFeedbackAggregateForSnapshot(subjectKeys []string, snap Snapshot) (int, bool) {
	refs := proxyCompatServiceFeedbackRefsForSnapshot(snap)
	if len(refs) == 0 {
		return 0, false
	}
	totalPenalty := 0
	anyCooling := false
	now := time.Now()
	seenSubjects := make(map[string]struct{}, len(subjectKeys))
	for _, rawSubjectKey := range subjectKeys {
		subjectKey := normalizeProxyCompatHostID(rawSubjectKey)
		if subjectKey == "" {
			continue
		}
		if _, exists := seenSubjects[subjectKey]; exists {
			continue
		}
		seenSubjects[subjectKey] = struct{}{}
		for _, ref := range refs {
			feedback, ok := state.serviceFeedbackForRef(subjectKey, ref)
			if !ok {
				continue
			}
			totalPenalty += feedback.Penalty
			if cooldownUntil, _ := time.Parse(time.RFC3339, strings.TrimSpace(feedback.CooldownUntil)); !cooldownUntil.IsZero() && cooldownUntil.After(now) {
				anyCooling = true
			}
		}
	}
	if totalPenalty > 95 {
		totalPenalty = 95
	}
	return totalPenalty, anyCooling
}

func (state *proxyCompatState) recordServiceSuccessForSnapshot(hostID string, snap Snapshot) {
	state.recordServiceSuccessForRefs(hostID, proxyCompatServiceFeedbackRefsForSnapshot(snap))
}

func (state *proxyCompatState) recordServiceFailureForSnapshot(
	hostID string,
	snap Snapshot,
	errorCode string,
	decision proxyCompatUsageFeedbackDecision,
) []proxyCompatServiceFeedback {
	refs := proxyCompatServiceFeedbackRefsForSnapshot(snap)
	results := make([]proxyCompatServiceFeedback, 0, len(refs))
	for _, ref := range refs {
		scopedDecision := proxyCompatScopedUsageDecision(ref.ScopeKind, decision)
		if scopedDecision.Scope == proxyCompatUsageFailureNone || scopedDecision.Penalty <= 0 {
			continue
		}
		feedback := state.recordServiceFailureForRef(hostID, ref, errorCode, scopedDecision)
		if strings.TrimSpace(feedback.FeedbackKey) != "" {
			results = append(results, feedback)
		}
	}
	return results
}

func proxyCompatServiceFeedbackRefsForSnapshot(snap Snapshot) []proxyCompatServiceFeedbackRef {
	refs := make([]proxyCompatServiceFeedbackRef, 0, 4)
	if tag := strings.TrimSpace(snap.Tag); tag != "" {
		refs = append(refs, proxyCompatServiceFeedbackRef{
			Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeNode, tag),
			ScopeKind:  proxyCompatFeedbackScopeNode,
			ScopeValue: tag,
		})
	}
	if value := strings.TrimSpace(snap.ProtocolFamily); value != "" {
		refs = append(refs, proxyCompatServiceFeedbackRef{
			Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeProtocolFamily, value),
			ScopeKind:  proxyCompatFeedbackScopeProtocolFamily,
			ScopeValue: value,
		})
	}
	if value := strings.TrimSpace(snap.NodeMode); value != "" {
		refs = append(refs, proxyCompatServiceFeedbackRef{
			Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeNodeMode, value),
			ScopeKind:  proxyCompatFeedbackScopeNodeMode,
			ScopeValue: value,
		})
	}
	if value := strings.TrimSpace(snap.DomainFamily); value != "" {
		refs = append(refs, proxyCompatServiceFeedbackRef{
			Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeDomainFamily, value),
			ScopeKind:  proxyCompatFeedbackScopeDomainFamily,
			ScopeValue: value,
		})
	}
	return refs
}

func proxyCompatServiceFeedbackKey(scopeKind string, scopeValue string) string {
	return strings.TrimSpace(scopeKind) + "::" + strings.TrimSpace(scopeValue)
}

func proxyCompatScopedUsageDecision(scopeKind string, decision proxyCompatUsageFeedbackDecision) proxyCompatUsageFeedbackDecision {
	scoped := decision
	routeFailure := strings.HasPrefix(strings.TrimSpace(decision.ErrorClass), "route:")
	switch strings.TrimSpace(scopeKind) {
	case proxyCompatFeedbackScopeNode:
		return scoped
	case proxyCompatFeedbackScopeNodeMode:
		scoped.Penalty = min(55, max(15, decision.Penalty-18))
		scoped.CooldownBase = minDuration(decision.CooldownBase, 8*time.Minute)
		scoped.CooldownEscalated = minDuration(decision.CooldownEscalated, 30*time.Minute)
	case proxyCompatFeedbackScopeDomainFamily:
		scoped.Penalty = min(45, max(12, decision.Penalty-24))
		scoped.CooldownBase = minDuration(decision.CooldownBase, 6*time.Minute)
		scoped.CooldownEscalated = minDuration(decision.CooldownEscalated, 20*time.Minute)
	case proxyCompatFeedbackScopeProtocolFamily:
		scoped.Penalty = min(18, max(4, decision.Penalty/4))
		scoped.CooldownBase = 0
		scoped.CooldownEscalated = 0
	default:
		scoped.Penalty = min(20, max(4, decision.Penalty/4))
		scoped.CooldownBase = 0
		scoped.CooldownEscalated = 0
	}
	if routeFailure {
		scoped.CooldownBase = 0
		scoped.CooldownEscalated = 0
	}
	return scoped
}

func minDuration(left, right time.Duration) time.Duration {
	switch {
	case left <= 0:
		return right
	case right <= 0:
		return left
	case left < right:
		return left
	default:
		return right
	}
}
