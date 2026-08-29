package monitor

import (
	"errors"
	"strings"
	"time"
)

func (s *Server) applyProxyCompatUsageFeedback(selectedNodeTag, hostID string, record proxyCompatUsageRecord) {
	trimmedTag := strings.TrimSpace(selectedNodeTag)
	if trimmedTag == "" {
		return
	}

	nodeEntry, err := s.mgr.entry(trimmedTag)
	if err != nil {
		return
	}

	destination := strings.TrimSpace(hostID)
	if destination == "" {
		destination = "compat-report"
	}

	feedbackSubjectKeys := proxyCompatFeedbackSubjectKeys(hostID, record.ServiceKey)
	if record.Success {
		nodeEntry.recordSuccess(destination)
		nodeEntry.applyUsageReportSuccess()
		snapshot := nodeEntry.snapshot()
		for _, subjectKey := range feedbackSubjectKeys {
			s.compatState().recordServiceSuccessForSnapshot(subjectKey, snapshot)
		}
		return
	}

	errMessage := strings.TrimSpace(record.ErrorCode)
	if errMessage == "" {
		errMessage = "task reported proxy route failure"
	}
	failureClass := normalizeProxyCompatFailureClass(record.FailureClass)
	routeConfidence := normalizeProxyCompatRouteConfidence(record.RouteConfidence)
	if failureClass == "" {
		failureClass = proxyCompatFailureClassUnknown
	}
	if routeConfidence == "" {
		routeConfidence = proxyCompatRouteConfidenceLow
	}

	switch failureClass {
	case proxyCompatFailureClassAccountAuth:
		nodeEntry.recordObservationFailure(errors.New(errMessage), destination)
		nodeEntry.applyUsageReportFailure(0, false)
	case proxyCompatFailureClassBusinessRisk:
		nodeEntry.recordObservationFailure(errors.New(errMessage), destination)
		nodeEntry.applyUsageReportFailure(0, false)
		snapshot := nodeEntry.snapshot()
		localDecision := proxyCompatLocalBusinessRiskDecision(errMessage, routeConfidence)
		s.compatState().recordServiceFailureForSnapshot(hostID, snapshot, errMessage, localDecision)

		serviceKey := normalizeProxyCompatServiceKey(record.ServiceKey, hostID)
		stats := s.compatState().businessRiskStats(serviceKey, record.Stage, trimmedTag)
		if proxyCompatShouldCooldownSentinelBusinessRisk(serviceKey, record.Stage, errMessage, stats, routeConfidence) {
			serviceDecision := proxyCompatSentinelBusinessRiskDecision(errMessage, routeConfidence)
			s.compatState().recordServiceFailureForSnapshot(serviceKey, snapshot, errMessage, serviceDecision)
		} else if proxyCompatShouldCooldownBusinessRisk(stats, routeConfidence) {
			serviceDecision := proxyCompatServiceBusinessRiskDecision(errMessage, routeConfidence)
			s.compatState().recordServiceFailureForSnapshot(serviceKey, snapshot, errMessage, serviceDecision)
		}
	case proxyCompatFailureClassRouteFailure:
		serviceKey := normalizeProxyCompatServiceKey(record.ServiceKey, hostID)
		if proxyCompatShouldServiceCooldownLoginBlockedRouteFailure(serviceKey, record.Stage, errMessage, routeConfidence) {
			nodeEntry.recordObservationFailure(errors.New(errMessage), destination)
			nodeEntry.applyUsageReportFailure(0, false)
			serviceDecision := proxyCompatLoginBlockedServiceRouteFailureDecision(errMessage, routeConfidence)
			s.compatState().recordServiceFailure(serviceKey, trimmedTag, errMessage, serviceDecision)
			return
		}

		decision := proxyCompatRouteFailureDecision(errMessage, routeConfidence)
		nodeEntry.recordFailure(errors.New(errMessage), destination)
		nodeEntry.applyUsageReportFailure(decision.Penalty, true)
		reportFailures := nodeEntry.snapshot().ConsecutiveReportFailures
		cooldown := decision.CooldownBase
		if decision.EscalateAfterCount > 0 &&
			reportFailures >= decision.EscalateAfterCount &&
			decision.CooldownEscalated > 0 {
			cooldown = decision.CooldownEscalated
		}
		if cooldown > 0 {
			nodeEntry.blacklistUntil(time.Now().Add(cooldown))
		}
		if proxyCompatShouldDirectServiceCooldownRouteFailure(serviceKey, record.Stage, errMessage, routeConfidence) {
			snapshot := nodeEntry.snapshot()
			serviceDecision := proxyCompatDirectServiceRouteFailureDecision(errMessage, routeConfidence)
			s.compatState().recordServiceFailureForSnapshot(serviceKey, snapshot, errMessage, serviceDecision)
		}
	default:
		nodeEntry.recordObservationFailure(errors.New(errMessage), destination)
		nodeEntry.applyUsageReportFailure(0, false)
	}
}

func proxyCompatFeedbackSubjectKeys(hostID, serviceKey string) []string {
	keys := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, raw := range []string{
		normalizeProxyCompatHostID(hostID),
		normalizeProxyCompatServiceKey(serviceKey, hostID),
	} {
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		keys = append(keys, raw)
	}
	return keys
}

func proxyCompatRouteFailureDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	decision := classifyProxyCompatUsageFailure(errorCode)
	if decision.Scope != proxyCompatUsageFailureGlobal {
		decision = proxyCompatUsageFeedbackDecision{
			Scope:              proxyCompatUsageFailureGlobal,
			ErrorClass:         "route:network",
			Penalty:            22,
			CooldownBase:       5 * time.Minute,
			CooldownEscalated:  30 * time.Minute,
			EscalateAfterCount: 3,
		}
	}

	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceLow:
		decision.Penalty = max(8, decision.Penalty-10)
		decision.CooldownBase = minDuration(decision.CooldownBase, 3*time.Minute)
		decision.CooldownEscalated = minDuration(decision.CooldownEscalated, 12*time.Minute)
	case proxyCompatRouteConfidenceMedium:
		decision.Penalty = max(12, decision.Penalty-4)
		decision.CooldownBase = minDuration(decision.CooldownBase, 4*time.Minute)
		decision.CooldownEscalated = minDuration(decision.CooldownEscalated, 20*time.Minute)
	}
	return decision
}

func proxyCompatLocalBusinessRiskDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	penalty := 10
	baseCooldown := 2 * time.Minute
	escalatedCooldown := 8 * time.Minute
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceMedium:
		penalty = 14
		baseCooldown = 3 * time.Minute
		escalatedCooldown = 10 * time.Minute
	case proxyCompatRouteConfidenceHigh:
		penalty = 18
		baseCooldown = 4 * time.Minute
		escalatedCooldown = 12 * time.Minute
	}
	return proxyCompatUsageFeedbackDecision{
		Scope:              proxyCompatUsageFailureService,
		ErrorClass:         proxyCompatBusinessRiskErrorClass(errorCode),
		Penalty:            penalty,
		CooldownBase:       baseCooldown,
		CooldownEscalated:  escalatedCooldown,
		EscalateAfterCount: 3,
	}
}

func proxyCompatServiceBusinessRiskDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	penalty := 18
	baseCooldown := 8 * time.Minute
	escalatedCooldown := 25 * time.Minute
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceMedium:
		penalty = 24
		baseCooldown = 10 * time.Minute
		escalatedCooldown = 35 * time.Minute
	case proxyCompatRouteConfidenceHigh:
		penalty = 30
		baseCooldown = 12 * time.Minute
		escalatedCooldown = 45 * time.Minute
	}
	return proxyCompatUsageFeedbackDecision{
		Scope:              proxyCompatUsageFailureService,
		ErrorClass:         proxyCompatBusinessRiskErrorClass(errorCode),
		Penalty:            penalty,
		CooldownBase:       baseCooldown,
		CooldownEscalated:  escalatedCooldown,
		EscalateAfterCount: 2,
	}
}

func proxyCompatBusinessRiskErrorClass(errorCode string) string {
	decision := classifyProxyCompatUsageFailure(errorCode)
	if decision.Scope == proxyCompatUsageFailureService && strings.TrimSpace(decision.ErrorClass) != "" {
		return decision.ErrorClass
	}
	return "application:business_risk"
}

func proxyCompatIsRegistrationService(serviceKey string) bool {
	normalized := normalizeProxyCompatServiceKey(serviceKey, "")
	if normalized == "" {
		return false
	}
	for _, prefix := range []string{
		"accio-register",
		"register-service",
		"register-orchestration",
	} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func proxyCompatShouldPreferHistoricalSuccessRouting(serviceKey, stage string) bool {
	stage = normalizeProxyCompatUsageStage(stage)
	return stage == "registration" && proxyCompatIsRegistrationService(serviceKey)
}

func proxyCompatHistoricalRegistrationPenalty(stats proxyCompatUsageStats) float64 {
	if stats.NodeTotal <= 0 {
		return 5
	}
	penalty := (1 - stats.NodeSuccessRate) * 25
	penalty += stats.NodeSentinelFailureRate * 60
	if stats.NodeSentinelFailures >= 2 {
		penalty += 15
	}
	delta := stats.NodeSentinelFailureRate - stats.ServiceSentinelFailureRate
	if delta > 0 {
		penalty += delta * 50
	}
	if stats.NodeTotal >= 3 && stats.NodeSuccessRate <= 0.20 {
		penalty += 12
	}
	return penalty
}

func proxyCompatRecentSuccessReusePreferenceFromRequest(request proxyCompatCheckoutRequest) proxyCompatRecentSuccessReusePreference {
	enabled := proxyCompatMetadataBool(
		request.Metadata,
		"avoidRecentSuccessReuse",
		"preferFreshNodeAfterSuccess",
	)
	threshold := proxyCompatMetadataPositiveInt(
		request.Metadata,
		2,
		"recentSuccessReuseThreshold",
		"recentSuccessThreshold",
	)
	windowMinutes := proxyCompatMetadataPositiveInt(
		request.Metadata,
		20,
		"recentSuccessReuseWindowMinutes",
		"recentSuccessWindowMinutes",
	)
	return proxyCompatRecentSuccessReusePreference{
		Enabled:   enabled,
		Threshold: threshold,
		Window:    time.Duration(windowMinutes) * time.Minute,
	}
}

func proxyCompatRecentSuccessReusePenalty(successCount, successStreak, threshold int) int {
	if successCount <= 0 || successStreak <= 0 {
		return 0
	}
	if threshold <= 0 {
		threshold = 1
	}

	penalty := min(successCount, 3) * 6
	if successStreak >= threshold {
		penalty += 24 + (successStreak-threshold)*14
	}
	return penalty
}

func proxyCompatShouldCooldownBusinessRisk(stats proxyCompatUsageStats, routeConfidence string) bool {
	if stats.ServiceTotal < 10 || stats.NodeTotal < 5 || stats.NodeFailures < 4 {
		return false
	}
	delta := stats.NodeFailureRate - stats.ServiceFailureRate
	if stats.ServiceFailureRate <= 0.25 && stats.NodeFailures >= 5 && stats.NodeSuccesses == 0 {
		return true
	}

	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceHigh:
		return stats.NodeFailureRate >= 0.60 && delta >= 0.25
	case proxyCompatRouteConfidenceMedium:
		return stats.NodeFailureRate >= 0.70 && delta >= 0.35
	default:
		return stats.NodeFailureRate >= 0.85 && delta >= 0.45
	}
}

func proxyCompatShouldCooldownSentinelBusinessRisk(serviceKey, stage, errorCode string, stats proxyCompatUsageStats, routeConfidence string) bool {
	if !proxyCompatShouldPreferHistoricalSuccessRouting(serviceKey, stage) {
		return false
	}
	if !proxyCompatCountsAsSentinelRateLimit(errorCode) {
		return false
	}
	if stats.ServiceTotal < 6 || stats.NodeTotal < 3 || stats.NodeSentinelFailures < 2 {
		return false
	}
	delta := stats.NodeSentinelFailureRate - stats.ServiceSentinelFailureRate
	if stats.ServiceSentinelFailureRate <= 0.15 && stats.NodeSentinelFailures >= 2 && stats.NodeSuccesses == 0 {
		return true
	}
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceHigh:
		return stats.NodeSentinelFailureRate >= 0.40 && delta >= 0.12
	case proxyCompatRouteConfidenceMedium:
		return stats.NodeSentinelFailureRate >= 0.45 && delta >= 0.15
	default:
		return stats.NodeSentinelFailureRate >= 0.55 && delta >= 0.20
	}
}

func proxyCompatSentinelBusinessRiskDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	penalty := 30
	baseCooldown := 12 * time.Minute
	escalatedCooldown := 40 * time.Minute
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceMedium:
		penalty = 34
		baseCooldown = 15 * time.Minute
		escalatedCooldown = 50 * time.Minute
	case proxyCompatRouteConfidenceHigh:
		penalty = 40
		baseCooldown = 18 * time.Minute
		escalatedCooldown = 60 * time.Minute
	}
	return proxyCompatUsageFeedbackDecision{
		Scope:              proxyCompatUsageFailureService,
		ErrorClass:         "application:sentinel_hotspot",
		Penalty:            penalty,
		CooldownBase:       baseCooldown,
		CooldownEscalated:  escalatedCooldown,
		EscalateAfterCount: 2,
	}
}

func proxyCompatRequiresStrictDegradedServiceCooldown(serviceKey, stage string) bool {
	stage = normalizeProxyCompatUsageStage(stage)
	return stage == "registration" && proxyCompatIsRegistrationService(serviceKey)
}

func proxyCompatShouldDirectServiceCooldownRouteFailure(serviceKey, stage, errorCode, routeConfidence string) bool {
	if !proxyCompatRequiresStrictDegradedServiceCooldown(serviceKey, stage) {
		return false
	}
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceHigh, proxyCompatRouteConfidenceMedium:
	default:
		return false
	}

	normalized := strings.ToLower(strings.TrimSpace(errorCode))
	if normalized == "" {
		return false
	}
	tripMarkers := []string{
		"net::err_connection_closed",
		"err_connection_closed",
		"remote end closed",
		"unexpected eof",
		"connection reset",
		"econnreset",
	}
	for _, marker := range tripMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func proxyCompatDirectServiceRouteFailureDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	penalty := 36
	baseCooldown := 10 * time.Minute
	escalatedCooldown := 30 * time.Minute
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceMedium:
		penalty = 28
		baseCooldown = 8 * time.Minute
		escalatedCooldown = 20 * time.Minute
	}
	return proxyCompatUsageFeedbackDecision{
		Scope:              proxyCompatUsageFailureService,
		ErrorClass:         "route:registration_close",
		Penalty:            penalty,
		CooldownBase:       baseCooldown,
		CooldownEscalated:  escalatedCooldown,
		EscalateAfterCount: 2,
	}
}

func proxyCompatShouldServiceCooldownLoginBlockedRouteFailure(serviceKey, stage, errorCode, routeConfidence string) bool {
	if !proxyCompatRequiresStrictDegradedServiceCooldown(serviceKey, stage) {
		return false
	}
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceHigh, proxyCompatRouteConfidenceMedium:
	default:
		return false
	}

	normalized := strings.ToLower(strings.TrimSpace(errorCode))
	if normalized == "" {
		return false
	}
	loginHosts := []string{
		"platform.openai.com/login",
		"auth.openai.com",
		"chatgpt.com/auth/login_with",
		"chatgpt.com/auth/login",
	}
	matchedHost := false
	for _, host := range loginHosts {
		if strings.Contains(normalized, host) {
			matchedHost = true
			break
		}
	}
	if !matchedHost {
		return false
	}

	blockMarkers := []string{
		"proxy route failure blocked",
		"easy_proxy_probe_failed",
		"just a moment",
		"challenge=yes",
		"status=403",
	}
	for _, marker := range blockMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func proxyCompatLoginBlockedServiceRouteFailureDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	penalty := 30
	baseCooldown := 8 * time.Minute
	escalatedCooldown := 25 * time.Minute
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceMedium:
		penalty = 24
		baseCooldown = 6 * time.Minute
		escalatedCooldown = 18 * time.Minute
	}
	return proxyCompatUsageFeedbackDecision{
		Scope:              proxyCompatUsageFailureService,
		ErrorClass:         "route:login_blocked",
		Penalty:            penalty,
		CooldownBase:       baseCooldown,
		CooldownEscalated:  escalatedCooldown,
		EscalateAfterCount: 2,
	}
}
