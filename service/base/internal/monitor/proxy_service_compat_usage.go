package monitor

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func (state *proxyCompatState) storeLease(leaseState *proxyCompatLeaseState) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.leases[leaseState.Lease.ID] = leaseState
	if leaseState.SelectedNodeTag != "" {
		state.nodeReservations[leaseState.SelectedNodeTag]++
	}
}

func (state *proxyCompatState) readLease(leaseID string) (proxyCompatLease, bool) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	leaseState, ok := state.leases[leaseID]
	if !ok {
		return proxyCompatLease{}, false
	}
	return cloneProxyCompatLease(leaseState.Lease), true
}

func (state *proxyCompatState) releaseLease(leaseID string) error {
	state.mu.Lock()
	defer state.mu.Unlock()

	leaseState, ok := state.leases[leaseID]
	if !ok {
		return fmt.Errorf("lease %q not found", leaseID)
	}
	if leaseState.Lease.Status != "active" {
		return nil
	}
	leaseState.Lease.Status = "released"
	leaseState.Lease.ReleasedAt = time.Now().Format(time.RFC3339)
	state.releaseReservationLocked(leaseState.SelectedNodeTag)
	return nil
}

func (state *proxyCompatState) recordUsage(report proxyCompatUsageReport) (proxyCompatUsageRecord, string, string, error) {
	state.mu.Lock()
	defer state.mu.Unlock()

	leaseState, ok := state.leases[report.LeaseID]
	if !ok {
		return proxyCompatUsageRecord{}, "", "", fmt.Errorf("lease %q not found", report.LeaseID)
	}

	reportedAt := time.Now().Format(time.RFC3339)
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(report.ReportedAt)); err == nil {
		reportedAt = parsed.Format(time.RFC3339)
	}

	record := proxyCompatUsageRecord{
		ID:                 mustGenerateCompatID("usage"),
		LeaseID:            report.LeaseID,
		ProviderInstanceID: leaseState.Lease.ProviderInstanceID,
		SelectedNodeTag:    leaseState.SelectedNodeTag,
		ReportedAt:         reportedAt,
		Success:            report.Success,
	}
	if report.LatencyMs > 0 {
		record.LatencyMs = report.LatencyMs
	}
	if trimmed := strings.TrimSpace(report.ErrorCode); trimmed != "" {
		record.ErrorCode = trimmed
	}
	record.ServiceKey = normalizeProxyCompatServiceKey(
		firstNonEmptyCompatValue(
			report.ServiceKey,
			leaseState.Lease.Metadata["serviceKey"],
			leaseState.Lease.HostID,
		),
		leaseState.Lease.HostID,
	)
	record.Stage = normalizeProxyCompatUsageStage(
		firstNonEmptyCompatValue(
			report.Stage,
			leaseState.Lease.Metadata["stage"],
			leaseState.Lease.Metadata["purpose"],
		),
	)
	record.FailureClass = normalizeProxyCompatFailureClass(report.FailureClass)
	record.RouteConfidence = normalizeProxyCompatRouteConfidence(report.RouteConfidence)
	if !record.Success {
		inferredClass, inferredConfidence := inferProxyCompatFailureSemantics(record.ErrorCode)
		if record.FailureClass == "" {
			record.FailureClass = inferredClass
		}
		if record.RouteConfidence == "" {
			record.RouteConfidence = inferredConfidence
		}
	} else {
		if record.FailureClass == "" {
			record.FailureClass = proxyCompatFailureClassNone
		}
		if record.RouteConfidence == "" {
			record.RouteConfidence = proxyCompatRouteConfidenceLow
		}
	}

	state.usageRecords = append(state.usageRecords, record)
	if len(state.usageRecords) > 2048 {
		state.usageRecords = append([]proxyCompatUsageRecord(nil), state.usageRecords[len(state.usageRecords)-2048:]...)
	}
	return record, leaseState.SelectedNodeTag, leaseState.Lease.HostID, nil
}

func (state *proxyCompatState) snapshot() ([]proxyCompatLease, []proxyCompatUsageRecord, []proxyCompatServiceFeedback, []proxyCompatUsageStats) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.clearExpiredServiceCooldownsLocked(time.Now())

	leases := make([]proxyCompatLease, 0, len(state.leases))
	for _, leaseState := range state.leases {
		leases = append(leases, cloneProxyCompatLease(leaseState.Lease))
	}
	sort.SliceStable(leases, func(i, j int) bool {
		return leases[i].CreatedAt > leases[j].CreatedAt
	})

	usage := append([]proxyCompatUsageRecord(nil), state.usageRecords...)
	sort.SliceStable(usage, func(i, j int) bool {
		return usage[i].ReportedAt > usage[j].ReportedAt
	})

	serviceFeedback := make([]proxyCompatServiceFeedback, 0, len(state.serviceFeedback))
	for hostID, byNode := range state.serviceFeedback {
		for _, feedback := range byNode {
			if feedback == nil {
				continue
			}
			serviceFeedback = append(serviceFeedback, proxyCompatServiceFeedback{
				HostID:              hostID,
				NodeTag:             feedback.NodeTag,
				FeedbackKey:         feedback.FeedbackKey,
				ScopeKind:           feedback.ScopeKind,
				ScopeValue:          feedback.ScopeValue,
				Penalty:             feedback.Penalty,
				ConsecutiveFailures: feedback.ConsecutiveFailures,
				CooldownUntil:       feedback.CooldownUntil,
				LastErrorClass:      feedback.LastErrorClass,
				LastErrorCode:       feedback.LastErrorCode,
				LastReportedAt:      feedback.LastReportedAt,
			})
		}
	}
	sort.SliceStable(serviceFeedback, func(i, j int) bool {
		if serviceFeedback[i].HostID != serviceFeedback[j].HostID {
			return serviceFeedback[i].HostID < serviceFeedback[j].HostID
		}
		if serviceFeedback[i].ScopeKind != serviceFeedback[j].ScopeKind {
			return serviceFeedback[i].ScopeKind < serviceFeedback[j].ScopeKind
		}
		return serviceFeedback[i].ScopeValue < serviceFeedback[j].ScopeValue
	})
	return leases, usage, serviceFeedback, buildProxyCompatUsageStats(state.usageRecords)
}

func (state *proxyCompatState) businessRiskStats(serviceKey, stage, nodeTag string) proxyCompatUsageStats {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return computeProxyCompatUsageStats(state.usageRecords, serviceKey, stage, nodeTag)
}

func buildProxyCompatUsageStats(records []proxyCompatUsageRecord) []proxyCompatUsageStats {
	type serviceStageKey struct {
		serviceKey string
		stage      string
	}
	type nodeKey struct {
		serviceKey string
		stage      string
		nodeTag    string
	}

	serviceTotals := make(map[serviceStageKey]*proxyCompatUsageStats)
	nodeTotals := make(map[nodeKey]*proxyCompatUsageStats)

	for _, record := range records {
		serviceKey := normalizeProxyCompatServiceKey(record.ServiceKey, "")
		stage := normalizeProxyCompatUsageStage(record.Stage)
		if serviceKey == "" || stage == "" || !proxyCompatCountsTowardBusinessBaseline(record) {
			continue
		}

		svcKey := serviceStageKey{serviceKey: serviceKey, stage: stage}
		svcStats, ok := serviceTotals[svcKey]
		if !ok {
			svcStats = &proxyCompatUsageStats{
				ServiceKey: serviceKey,
				Stage:      stage,
			}
			serviceTotals[svcKey] = svcStats
		}
		svcStats.ServiceTotal++
		if !record.Success {
			svcStats.ServiceFailures++
			if proxyCompatCountsAsSentinelRateLimit(record.ErrorCode) {
				svcStats.ServiceSentinelFailures++
			}
		}

		if strings.TrimSpace(record.SelectedNodeTag) == "" {
			continue
		}
		nKey := nodeKey{
			serviceKey: serviceKey,
			stage:      stage,
			nodeTag:    strings.TrimSpace(record.SelectedNodeTag),
		}
		nodeStats, ok := nodeTotals[nKey]
		if !ok {
			nodeStats = &proxyCompatUsageStats{
				ServiceKey: serviceKey,
				Stage:      stage,
				NodeTag:    strings.TrimSpace(record.SelectedNodeTag),
			}
			nodeTotals[nKey] = nodeStats
		}
		nodeStats.NodeTotal++
		if record.Success {
			nodeStats.NodeSuccesses++
		} else {
			nodeStats.NodeFailures++
			if proxyCompatCountsAsSentinelRateLimit(record.ErrorCode) {
				nodeStats.NodeSentinelFailures++
			}
		}
	}

	result := make([]proxyCompatUsageStats, 0, len(nodeTotals))
	for key, nodeStats := range nodeTotals {
		svcStats := serviceTotals[serviceStageKey{serviceKey: key.serviceKey, stage: key.stage}]
		if svcStats != nil {
			nodeStats.ServiceTotal = svcStats.ServiceTotal
			nodeStats.ServiceFailures = svcStats.ServiceFailures
			nodeStats.ServiceFailureRate = proxyCompatFailureRate(svcStats.ServiceFailures, svcStats.ServiceTotal)
			nodeStats.ServiceSentinelFailures = svcStats.ServiceSentinelFailures
			nodeStats.ServiceSentinelFailureRate = proxyCompatFailureRate(svcStats.ServiceSentinelFailures, svcStats.ServiceTotal)
		}
		nodeStats.NodeSuccessRate = proxyCompatRatio(nodeStats.NodeSuccesses, nodeStats.NodeTotal)
		nodeStats.NodeFailureRate = proxyCompatFailureRate(nodeStats.NodeFailures, nodeStats.NodeTotal)
		nodeStats.NodeSentinelFailureRate = proxyCompatFailureRate(nodeStats.NodeSentinelFailures, nodeStats.NodeTotal)
		result = append(result, *nodeStats)
	}

	sort.SliceStable(result, func(i, j int) bool {
		if result[i].ServiceKey != result[j].ServiceKey {
			return result[i].ServiceKey < result[j].ServiceKey
		}
		if result[i].Stage != result[j].Stage {
			return result[i].Stage < result[j].Stage
		}
		return result[i].NodeTag < result[j].NodeTag
	})
	return result
}

func computeProxyCompatUsageStats(records []proxyCompatUsageRecord, serviceKey, stage, nodeTag string) proxyCompatUsageStats {
	serviceKey = normalizeProxyCompatServiceKey(serviceKey, "")
	stage = normalizeProxyCompatUsageStage(stage)
	nodeTag = strings.TrimSpace(nodeTag)
	stats := proxyCompatUsageStats{
		ServiceKey: serviceKey,
		Stage:      stage,
		NodeTag:    nodeTag,
	}
	if serviceKey == "" || stage == "" || nodeTag == "" {
		return stats
	}

	for _, record := range records {
		if normalizeProxyCompatServiceKey(record.ServiceKey, "") != serviceKey {
			continue
		}
		if normalizeProxyCompatUsageStage(record.Stage) != stage {
			continue
		}
		if !proxyCompatCountsTowardBusinessBaseline(record) {
			continue
		}
		stats.ServiceTotal++
		if !record.Success {
			stats.ServiceFailures++
			if proxyCompatCountsAsSentinelRateLimit(record.ErrorCode) {
				stats.ServiceSentinelFailures++
			}
		}
		if strings.TrimSpace(record.SelectedNodeTag) != nodeTag {
			continue
		}
		stats.NodeTotal++
		if record.Success {
			stats.NodeSuccesses++
		} else {
			stats.NodeFailures++
			if proxyCompatCountsAsSentinelRateLimit(record.ErrorCode) {
				stats.NodeSentinelFailures++
			}
		}
	}

	stats.ServiceFailureRate = proxyCompatFailureRate(stats.ServiceFailures, stats.ServiceTotal)
	stats.ServiceSentinelFailureRate = proxyCompatFailureRate(stats.ServiceSentinelFailures, stats.ServiceTotal)
	stats.NodeSuccessRate = proxyCompatRatio(stats.NodeSuccesses, stats.NodeTotal)
	stats.NodeFailureRate = proxyCompatFailureRate(stats.NodeFailures, stats.NodeTotal)
	stats.NodeSentinelFailureRate = proxyCompatFailureRate(stats.NodeSentinelFailures, stats.NodeTotal)
	return stats
}

func proxyCompatCountsAsSentinelRateLimit(errorCode string) bool {
	normalized := strings.ToLower(strings.TrimSpace(errorCode))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "sentinel rate limit") ||
		strings.Contains(normalized, "blocked by sentinel") ||
		strings.Contains(normalized, "code\": \"555\"") ||
		(strings.Contains(normalized, "sentinel") && strings.Contains(normalized, "555"))
}

func proxyCompatCountsTowardBusinessBaseline(record proxyCompatUsageRecord) bool {
	if record.Success {
		return true
	}
	return normalizeProxyCompatFailureClass(record.FailureClass) == proxyCompatFailureClassBusinessRisk
}

func proxyCompatFailureRate(failures, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(failures) / float64(total)
}

func proxyCompatRatio(value, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(value) / float64(total)
}
