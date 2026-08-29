package monitor

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

func cloneProxyCompatLease(lease proxyCompatLease) proxyCompatLease {
	cloned := lease
	if lease.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(lease.Metadata))
		for key, value := range lease.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return cloned
}

func (s *Server) respondProxyCompatResolveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errProxyCompatUnsupportedProvider):
		writeProxyCompatError(w, http.StatusServiceUnavailable, "PROVIDER_INSTANCE_UNAVAILABLE", err.Error())
	case errors.Is(err, errProxyCompatNoNodes):
		writeProxyCompatError(w, http.StatusServiceUnavailable, "NO_PROXY_PROVIDER_ROUTE", err.Error())
	default:
		writeProxyCompatError(w, http.StatusInternalServerError, "PROXY_COMPAT_RESOLVE_FAILED", err.Error())
	}
}

func (s *Server) resolveProxyCompatCandidate(r *http.Request, request proxyCompatCheckoutRequest) (proxyCompatCandidate, proxyCompatRuntime, error) {
	if providerType := strings.TrimSpace(strings.ToLower(request.ProviderTypeKey)); providerType != "" && providerType != "easy-proxies" {
		return proxyCompatCandidate{}, proxyCompatRuntime{}, fmt.Errorf("%w: %s", errProxyCompatUnsupportedProvider, providerType)
	}

	runtimeCfg := s.resolveProxyCompatRuntime(r)
	snapshots := s.mgr.Snapshot()
	sourceStates := s.mgr.SourceSelectionStates()
	secondaryStates := s.mgr.SecondarySelectionStates()
	nodes, selectionTier := selectProxyCompatCandidateSnapshotsWithSelection(snapshots, sourceStates, secondaryStates)
	if len(nodes) == 0 {
		return proxyCompatCandidate{}, runtimeCfg, errProxyCompatNoNodes
	}
	serviceKey := normalizeProxyCompatServiceKey(
		firstNonEmptyCompatValue(
			request.Metadata["serviceKey"],
			request.Metadata["service"],
		),
		request.HostID,
	)
	stage := normalizeProxyCompatUsageStage(
		firstNonEmptyCompatValue(
			request.Metadata["stage"],
			request.Metadata["purpose"],
		),
	)
	feedbackSubjectKeys := proxyCompatFeedbackSubjectKeys(
		request.HostID,
		serviceKey,
	)
	recentSuccessPreference := proxyCompatRecentSuccessReusePreferenceFromRequest(request)

	candidates := make([]proxyCompatCandidate, 0, len(nodes))
	preferHistoricalStats := proxyCompatShouldPreferHistoricalSuccessRouting(serviceKey, stage)
	for _, snap := range nodes {
		endpointHost := normalizeCompatEndpointHost(snap.ListenAddress, runtimeCfg.SharedHost)
		endpointPort := int(snap.Port)
		endpointMode := "dedicated-node"
		protocol := runtimeCfg.NodeProtocol
		username := runtimeCfg.NodeUsername
		password := runtimeCfg.NodePassword
		if endpointPort <= 0 {
			if !runtimeCfg.AllowSharedPoolFallback {
				continue
			}
			endpointHost = runtimeCfg.SharedHost
			endpointPort = runtimeCfg.SharedPort
			endpointMode = "shared-pool"
			protocol = runtimeCfg.SharedProtocol
			username = runtimeCfg.SharedUsername
			password = runtimeCfg.SharedPassword
		}
		if s.localServerCompatEnabled() {
			username = proxyUsernameForHost(username, request.HostID)
		}
		servicePenalty, serviceCooling := s.compatState().serviceFeedbackAggregateForSnapshot(feedbackSubjectKeys, snap)
		usageStats := proxyCompatUsageStats{}
		if preferHistoricalStats {
			usageStats = s.compatState().businessRiskStats(serviceKey, stage, snap.Tag)
		}
		recentSuccessCount := 0
		recentSuccessStreak := 0
		recentSuccessPenalty := 0
		if recentSuccessPreference.Enabled {
			recentSuccessCount, recentSuccessStreak = s.compatState().recentServiceSuccessStats(
				serviceKey,
				stage,
				snap.Tag,
				recentSuccessPreference.Window,
			)
			recentSuccessPenalty = proxyCompatRecentSuccessReusePenalty(
				recentSuccessCount,
				recentSuccessStreak,
				recentSuccessPreference.Threshold,
			)
		}
		candidates = append(candidates, proxyCompatCandidate{
			Snapshot:             snap,
			ReservationCount:     s.compatState().reservationCount(snap.Tag),
			ServiceLeaseCount:    s.compatState().activeServiceLeaseCount(serviceKey, snap.Tag),
			ServicePenalty:       servicePenalty,
			ServiceCooling:       serviceCooling,
			UsageStats:           usageStats,
			RecentSuccessCount:   recentSuccessCount,
			RecentSuccessStreak:  recentSuccessStreak,
			RecentSuccessPenalty: recentSuccessPenalty,
			SelectionTier:        selectionTier,
			EndpointHost:         endpointHost,
			EndpointPort:         endpointPort,
			Protocol:             protocol,
			Username:             username,
			Password:             password,
			EndpointMode:         endpointMode,
		})
	}
	if len(candidates) == 0 {
		return proxyCompatCandidate{}, runtimeCfg, fmt.Errorf(
			"%w: no EasyProxy candidates expose dedicated listener ports for the active runtime mode",
			errProxyCompatNoNodes,
		)
	}

	eligible := make([]proxyCompatCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ServiceCooling {
			continue
		}
		eligible = append(eligible, candidate)
	}
	if len(eligible) == 0 {
		if proxyCompatRequiresStrictDegradedServiceCooldown(serviceKey, stage) {
			return proxyCompatCandidate{}, runtimeCfg, fmt.Errorf(
				"%w: no EasyProxy nodes are currently available for service %s after service cooldown",
				errProxyCompatNoNodes,
				serviceKey,
			)
		}
		eligible = append(eligible, candidates...)
		for idx := range eligible {
			tier := strings.TrimSpace(eligible[idx].SelectionTier)
			if tier == "" {
				tier = "effective"
			}
			if !strings.Contains(tier, "cooldown-fallback") {
				tier += "-cooldown-fallback"
			}
			eligible[idx].SelectionTier = tier
		}
	}
	if selectionTier == "degraded" {
		spreadEligible := make([]proxyCompatCandidate, 0, len(eligible))
		hadServiceOverlap := false
		for _, candidate := range eligible {
			if candidate.ServiceLeaseCount > 0 {
				hadServiceOverlap = true
				continue
			}
			spreadEligible = append(spreadEligible, candidate)
		}
		if hadServiceOverlap && len(spreadEligible) > 0 {
			eligible = spreadEligible
			for idx := range eligible {
				tier := strings.TrimSpace(eligible[idx].SelectionTier)
				if tier == "" {
					tier = "degraded"
				}
				if !strings.Contains(tier, "service-spread") {
					tier += "-service-spread"
				}
				eligible[idx].SelectionTier = tier
			}
		}
	}

	sort.SliceStable(eligible, func(i, j int) bool {
		left := eligible[i]
		right := eligible[j]
		if left.ServiceLeaseCount != right.ServiceLeaseCount {
			return left.ServiceLeaseCount < right.ServiceLeaseCount
		}
		if left.ServicePenalty != right.ServicePenalty {
			return left.ServicePenalty < right.ServicePenalty
		}
		if recentSuccessPreference.Enabled && left.RecentSuccessPenalty != right.RecentSuccessPenalty {
			return left.RecentSuccessPenalty < right.RecentSuccessPenalty
		}
		if recentSuccessPreference.Enabled && left.RecentSuccessStreak != right.RecentSuccessStreak {
			return left.RecentSuccessStreak < right.RecentSuccessStreak
		}
		if recentSuccessPreference.Enabled && left.RecentSuccessCount != right.RecentSuccessCount {
			return left.RecentSuccessCount < right.RecentSuccessCount
		}
		if preferHistoricalStats {
			leftHistoricalPenalty := proxyCompatHistoricalRegistrationPenalty(left.UsageStats)
			rightHistoricalPenalty := proxyCompatHistoricalRegistrationPenalty(right.UsageStats)
			if leftHistoricalPenalty != rightHistoricalPenalty {
				return leftHistoricalPenalty < rightHistoricalPenalty
			}
			if left.UsageStats.NodeSuccessRate != right.UsageStats.NodeSuccessRate {
				return left.UsageStats.NodeSuccessRate > right.UsageStats.NodeSuccessRate
			}
			if left.UsageStats.NodeSentinelFailureRate != right.UsageStats.NodeSentinelFailureRate {
				return left.UsageStats.NodeSentinelFailureRate < right.UsageStats.NodeSentinelFailureRate
			}
		}
		if left.ReservationCount != right.ReservationCount {
			return left.ReservationCount < right.ReservationCount
		}
		if left.Snapshot.AvailabilityScore != right.Snapshot.AvailabilityScore {
			return left.Snapshot.AvailabilityScore > right.Snapshot.AvailabilityScore
		}
		if left.Snapshot.ActiveConnections != right.Snapshot.ActiveConnections {
			return left.Snapshot.ActiveConnections < right.Snapshot.ActiveConnections
		}
		leftLatency := normalizeCompatLatency(left.Snapshot.LastLatencyMs)
		rightLatency := normalizeCompatLatency(right.Snapshot.LastLatencyMs)
		if leftLatency != rightLatency {
			return leftLatency < rightLatency
		}
		if left.Snapshot.SuccessCount != right.Snapshot.SuccessCount {
			return left.Snapshot.SuccessCount > right.Snapshot.SuccessCount
		}
		if left.Snapshot.FailureCount != right.Snapshot.FailureCount {
			return left.Snapshot.FailureCount < right.Snapshot.FailureCount
		}
		return left.Snapshot.Tag < right.Snapshot.Tag
	})

	return eligible[0], runtimeCfg, nil
}

func selectProxyCompatCandidateSnapshots(nodes []Snapshot) ([]Snapshot, string) {
	effective := filterEffectiveSnapshots(nodes)
	if len(effective) > 0 {
		return effective, "effective"
	}

	degraded := filterCompatFallbackSnapshots(nodes)
	if len(degraded) > 0 {
		return degraded, "degraded"
	}
	return nil, ""
}

func selectProxyCompatCandidateSnapshotsWithSelection(
	nodes []Snapshot,
	sourceStates map[string]SourceSelectionState,
	secondaryStates map[string]SecondarySelectionState,
) ([]Snapshot, string) {
	effective := filterEffectiveSnapshots(nodes)
	if len(effective) > 0 {
		return effective, "effective"
	}

	degraded := filterCompatFallbackSnapshotsWithSelection(nodes, sourceStates, secondaryStates)
	if len(degraded) > 0 {
		return degraded, "degraded"
	}
	return nil, ""
}

func filterCompatFallbackSnapshots(nodes []Snapshot) []Snapshot {
	if len(nodes) == 0 {
		return nil
	}

	nonBlacklisted := make([]Snapshot, 0, len(nodes))
	for _, snap := range nodes {
		if snap.Blacklisted {
			continue
		}
		nonBlacklisted = append(nonBlacklisted, snap)
	}
	if len(nonBlacklisted) > 0 {
		return nonBlacklisted
	}
	return append([]Snapshot(nil), nodes...)
}

func filterCompatFallbackSnapshotsWithSelection(
	nodes []Snapshot,
	sourceStates map[string]SourceSelectionState,
	secondaryStates map[string]SecondarySelectionState,
) []Snapshot {
	if len(nodes) == 0 {
		return nil
	}

	nonBlacklisted := make([]Snapshot, 0, len(nodes))
	for _, snap := range nodes {
		if snap.Blacklisted {
			continue
		}
		nonBlacklisted = append(nonBlacklisted, snap)
	}

	eligible := make([]Snapshot, 0, len(nonBlacklisted))
	for _, snap := range nonBlacklisted {
		if compatSourceExcluded(snap, sourceStates) {
			continue
		}
		if compatSecondaryExcluded(snap, secondaryStates) {
			continue
		}
		eligible = append(eligible, snap)
	}
	if len(eligible) > 0 {
		return eligible
	}

	if len(sourceStates) == 0 && len(secondaryStates) == 0 {
		if len(nonBlacklisted) > 0 {
			return nonBlacklisted
		}
		return append([]Snapshot(nil), nodes...)
	}
	return nil
}

func compatSourceExcluded(snap Snapshot, sourceStates map[string]SourceSelectionState) bool {
	if len(sourceStates) == 0 {
		return false
	}
	sourceRef := strings.TrimSpace(snap.SourceRef)
	if sourceRef == "" {
		return false
	}
	state, ok := sourceStates[sourceRef]
	return ok && state.Excluded
}

func compatSecondaryExcluded(snap Snapshot, secondaryStates map[string]SecondarySelectionState) bool {
	if len(secondaryStates) == 0 {
		return false
	}
	sourceRef := strings.TrimSpace(snap.SourceRef)
	if sourceRef == "" {
		return false
	}

	keys := make([]string, 0, 3)
	if value := strings.TrimSpace(snap.ProtocolFamily); value != "" {
		keys = append(keys, SecondarySelectionStateKey(sourceRef, SelectionDimensionProtocolFamily, value))
	}
	if value := strings.TrimSpace(snap.NodeMode); value != "" {
		keys = append(keys, SecondarySelectionStateKey(sourceRef, SelectionDimensionNodeMode, value))
	}
	if value := strings.TrimSpace(snap.DomainFamily); value != "" {
		keys = append(keys, SecondarySelectionStateKey(sourceRef, SelectionDimensionDomainFamily, value))
	}
	for _, key := range keys {
		state, ok := secondaryStates[key]
		if ok && state.Excluded {
			return true
		}
	}
	return false
}

func normalizeCompatLatency(value int64) int64 {
	if value <= 0 {
		return 1<<62 - 1
	}
	return value
}

func normalizeCompatEndpointHost(candidate string, fallback string) string {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return fallback
	}
	switch trimmed {
	case "0.0.0.0", "::", "[::]", "127.0.0.1", "localhost":
		return fallback
	default:
		return trimmed
	}
}
