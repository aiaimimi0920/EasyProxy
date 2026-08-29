package subscription

import (
	"fmt"
	"net/url"
	"strings"
)

func parseZenProxyConnectorConfig(options map[string]any) (zenProxyConnectorConfig, error) {
	cfg := zenProxyConnectorConfig{
		APIKey:      strings.TrimSpace(extractStringOption(options, "api_key")),
		FetchPath:   strings.TrimSpace(extractStringOption(options, "fetch_path")),
		Count:       extractIntOption(options, "count", 10),
		Country:     strings.TrimSpace(extractStringOption(options, "country")),
		ProxyType:   strings.TrimSpace(extractStringOption(options, "type")),
		ProxyID:     strings.TrimSpace(extractStringOption(options, "proxy_id")),
		ChatGPT:     extractBoolOption(options, "chatgpt"),
		Google:      extractBoolOption(options, "google"),
		Residential: extractBoolOption(options, "residential"),
		RiskMax:     extractFloat64Option(options, "risk_max"),
		AuthInQuery: extractBoolOption(options, "auth_in_query"),
	}
	if cfg.APIKey == "" {
		return zenProxyConnectorConfig{}, fmt.Errorf("missing api_key")
	}
	if cfg.Count <= 0 {
		cfg.Count = 10
	}
	return cfg, nil
}

func buildZenProxyFetchURL(rawInput string, fetchPath string) (string, error) {
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return "", fmt.Errorf("missing zenproxy input url")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid zenproxy input url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("zenproxy input must be http(s)")
	}

	path := strings.TrimSpace(fetchPath)
	if path == "" {
		path = "/api/client/fetch"
	}
	if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/api/client/fetch") {
		return parsed.String(), nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	return parsed.String(), nil
}

func buildZenProxyRuntimeDisplayName(sourceName string, proxyName string, server string, port int, index int) string {
	remote := strings.TrimSpace(proxyName)
	if remote == "" && strings.TrimSpace(server) != "" && port > 0 {
		remote = fmt.Sprintf("%s:%d", strings.TrimSpace(server), port)
	}
	if remote == "" {
		remote = fmt.Sprintf("proxy-%d", index+1)
	}
	base := strings.TrimSpace(sourceName)
	if base == "" {
		return remote
	}
	return fmt.Sprintf("%s | %s", base, remote)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func isKilledProcessError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "killed") || strings.Contains(text, "terminated")
}
