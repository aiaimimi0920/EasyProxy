package monitor

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func classifyProxyCompatUsageFailure(errorCode string) proxyCompatUsageFeedbackDecision {
	normalized := strings.ToLower(strings.TrimSpace(errorCode))
	if normalized == "" {
		return proxyCompatUsageFeedbackDecision{
			Scope:      proxyCompatUsageFailureGlobal,
			ErrorClass: "route:unknown",
			Penalty:    15,
		}
	}

	authMarkers := []string{
		"401",
		"403",
		"502",
		"not_login",
		"not login",
		"auth not pass",
		"auth failed",
		"invalid token",
		"quota",
	}
	for _, marker := range authMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatUsageFeedbackDecision{
				Scope:      proxyCompatUsageFailureNone,
				ErrorClass: "application:auth",
			}
		}
	}

	type markerRule struct {
		marker   string
		decision proxyCompatUsageFeedbackDecision
	}
	riskMarkers := []markerRule{
		{
			marker: "eudf5",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:eudf5",
				Penalty:            70,
				CooldownBase:       20 * time.Minute,
				CooldownEscalated:  2 * time.Hour,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "eudf",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:eudf",
				Penalty:            65,
				CooldownBase:       20 * time.Minute,
				CooldownEscalated:  90 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "unusual traffic",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:unusual_traffic",
				Penalty:            60,
				CooldownBase:       20 * time.Minute,
				CooldownEscalated:  90 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "sentinel rate limit",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:sentinel_rate_limit",
				Penalty:            60,
				CooldownBase:       20 * time.Minute,
				CooldownEscalated:  90 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "slider_failed",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:slider_failed",
				Penalty:            55,
				CooldownBase:       12 * time.Minute,
				CooldownEscalated:  45 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "email_submit_not_accepted",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:email_submit_not_accepted",
				Penalty:            50,
				CooldownBase:       12 * time.Minute,
				CooldownEscalated:  45 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "captcha",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:captcha",
				Penalty:            50,
				CooldownBase:       10 * time.Minute,
				CooldownEscalated:  30 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
	}
	for _, rule := range riskMarkers {
		if strings.Contains(normalized, rule.marker) {
			return rule.decision
		}
	}

	networkMarkers := []string{
		"timeout",
		"tls",
		"connection reset",
		"proxy route failure",
		"connection refused",
		"econnreset",
		"network unreachable",
		"dial tcp",
		"i/o timeout",
	}
	for _, marker := range networkMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureGlobal,
				ErrorClass:         "route:network",
				Penalty:            25,
				CooldownBase:       5 * time.Minute,
				CooldownEscalated:  30 * time.Minute,
				EscalateAfterCount: 3,
			}
		}
	}

	return proxyCompatUsageFeedbackDecision{
		Scope:      proxyCompatUsageFailureGlobal,
		ErrorClass: "route:generic",
		Penalty:    15,
	}
}

func proxyCompatAverageAvailabilityScore(nodes []Snapshot) int {
	if len(nodes) == 0 {
		return 0
	}
	total := 0
	for _, node := range nodes {
		total += node.AvailabilityScore
	}
	return total / len(nodes)
}

func inferCompatRequestScheme(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		first := strings.ToLower(strings.TrimSpace(strings.Split(forwarded, ",")[0]))
		if first == "https" {
			return "https"
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func inferCompatRequestHost(r *http.Request, externalIP string) string {
	host := strings.TrimSpace(r.Host)
	if host != "" {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			host = parsedHost
		}
	}
	if host == "" || isCompatLoopbackOrWildcard(host) {
		if trimmedExternal := strings.TrimSpace(externalIP); trimmedExternal != "" {
			host = trimmedExternal
		}
	}
	if host == "" || isCompatLoopbackOrWildcard(host) {
		host = "127.0.0.1"
	}
	return host
}

func isCompatLoopbackOrWildcard(host string) bool {
	switch strings.Trim(strings.TrimSpace(host), "[]") {
	case "", "0.0.0.0", "127.0.0.1", "::", "::1", "localhost":
		return true
	default:
		return false
	}
}

func parseCompatPort(listen string) int {
	trimmed := strings.TrimSpace(listen)
	if trimmed == "" {
		return 0
	}
	if _, port, err := net.SplitHostPort(trimmed); err == nil {
		if parsed, parseErr := strconv.Atoi(port); parseErr == nil && parsed > 0 {
			return parsed
		}
	}
	if parsed, err := strconv.Atoi(trimmed); err == nil && parsed > 0 {
		return parsed
	}
	return 0
}

func normalizeProxyCompatHostID(hostID string) string {
	return strings.ToLower(strings.TrimSpace(hostID))
}

func normalizeProxyCompatServiceKey(serviceKey string, hostID string) string {
	normalized := strings.ToLower(strings.TrimSpace(serviceKey))
	if normalized != "" {
		return normalized
	}
	return normalizeProxyCompatHostID(hostID)
}

func normalizeProxyCompatUsageStage(stage string) string {
	normalized := strings.ToLower(strings.TrimSpace(stage))
	if normalized == "" {
		return "request"
	}
	return normalized
}

func normalizeProxyCompatFailureClass(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case proxyCompatFailureClassNone:
		return proxyCompatFailureClassNone
	case proxyCompatFailureClassRouteFailure:
		return proxyCompatFailureClassRouteFailure
	case proxyCompatFailureClassBusinessRisk:
		return proxyCompatFailureClassBusinessRisk
	case proxyCompatFailureClassAccountAuth:
		return proxyCompatFailureClassAccountAuth
	case proxyCompatFailureClassUnknown:
		return proxyCompatFailureClassUnknown
	default:
		return ""
	}
}

func normalizeProxyCompatRouteConfidence(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case proxyCompatRouteConfidenceLow:
		return proxyCompatRouteConfidenceLow
	case proxyCompatRouteConfidenceMedium:
		return proxyCompatRouteConfidenceMedium
	case proxyCompatRouteConfidenceHigh:
		return proxyCompatRouteConfidenceHigh
	default:
		return ""
	}
}

func proxyCompatMetadataBool(metadata map[string]string, keys ...string) bool {
	for _, key := range keys {
		value := strings.ToLower(strings.TrimSpace(metadata[key]))
		switch value {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return false
}

func proxyCompatMetadataPositiveInt(metadata map[string]string, fallback int, keys ...string) int {
	for _, key := range keys {
		value := strings.TrimSpace(metadata[key])
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			continue
		}
		return parsed
	}
	return fallback
}

func inferProxyCompatFailureSemantics(errorCode string) (string, string) {
	normalized := strings.ToLower(strings.TrimSpace(errorCode))
	if normalized == "" {
		return proxyCompatFailureClassUnknown, proxyCompatRouteConfidenceLow
	}

	authMarkers := []string{
		"401",
		"403",
		"502",
		"not_login",
		"not login",
		"auth not pass",
		"auth failed",
		"invalid token",
		"quota_empty",
		"quota empty",
		"quota=0",
		"0/520",
	}
	for _, marker := range authMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatFailureClassAccountAuth, proxyCompatRouteConfidenceLow
		}
	}

	mediumRiskMarkers := []string{
		"eudf5",
		"eudf",
		"unusual traffic",
		"sentinel rate limit",
	}
	for _, marker := range mediumRiskMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatFailureClassBusinessRisk, proxyCompatRouteConfidenceMedium
		}
	}

	lowRiskMarkers := []string{
		"slider_failed",
		"email_submit_not_accepted",
		"captcha",
		"otp_timeout",
		"import_activation_failed",
		"import_not_login",
		"import_quota_unavailable",
		"import_callback_failed",
		"timeoutexception",
	}
	for _, marker := range lowRiskMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatFailureClassBusinessRisk, proxyCompatRouteConfidenceLow
		}
	}

	networkMarkers := []string{
		"net::err_connection_closed",
		"err_connection_closed",
		"net::err_proxy_connection_failed",
		"err_proxy_connection_failed",
		"net::err_tunnel_connection_failed",
		"err_tunnel_connection_failed",
		"timeout",
		"tls",
		"connection reset",
		"proxy route failure",
		"connection refused",
		"econnreset",
		"network unreachable",
		"dial tcp",
		"i/o timeout",
		"unable to connect to proxy",
		"remote end closed",
		"unexpected eof",
		"read timed out",
	}
	for _, marker := range networkMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatFailureClassRouteFailure, proxyCompatRouteConfidenceHigh
		}
	}

	return proxyCompatFailureClassUnknown, proxyCompatRouteConfidenceLow
}

func firstNonEmptyCompatValue(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func writeProxyCompatError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]any{
		"error":   code,
		"message": message,
	})
}
