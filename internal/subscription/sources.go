package subscription

import (
	"fmt"
	"net/url"
	"strings"

	"easy_proxies/internal/config"
)

type SourceKind string

const (
	SourceKindSubscription SourceKind = "subscription"
	SourceKindProxyURI     SourceKind = "proxy_uri"
	SourceKindConnector    SourceKind = "connector"
)

type RuntimeSource struct {
	ID      string
	Kind    SourceKind
	Name    string
	Input   string
	Options map[string]any
	Origin  string
}

type manifestProfile struct {
	ID       string `json:"id"`
	CustomID string `json:"customId"`
	Name     string `json:"name"`
}

type manifestSource struct {
	ID      string         `json:"id"`
	Kind    SourceKind     `json:"kind"`
	Name    string         `json:"name"`
	Enabled bool           `json:"enabled"`
	Group   string         `json:"group"`
	Notes   string         `json:"notes"`
	Input   string         `json:"input"`
	Options map[string]any `json:"options"`
}

type manifestResponse struct {
	Success     bool             `json:"success"`
	Version     string           `json:"version"`
	GeneratedAt string           `json:"generated_at"`
	Profile     manifestProfile  `json:"profile"`
	Sources     []manifestSource `json:"sources"`
}

func normalizeRuntimeSource(input RuntimeSource, defaultDirectProxyScheme string) RuntimeSource {
	input.Name = strings.TrimSpace(input.Name)
	input.Input = strings.TrimSpace(config.NormalizeProxyURIInput(input.Input, defaultDirectProxyScheme))
	if input.Options == nil {
		input.Options = map[string]any{}
	}
	input.Kind = inferSourceKind(input.Kind, input.Input)
	return input
}

func inferSourceKind(kind SourceKind, input string) SourceKind {
	switch kind {
	case SourceKindSubscription, SourceKindProxyURI, SourceKindConnector:
		return kind
	}

	normalizedInput := strings.TrimSpace(input)
	if normalizedInput == "" {
		return SourceKindProxyURI
	}
	if isLikelyHTTPProxyInput(normalizedInput) {
		return SourceKindProxyURI
	}
	lowerInput := strings.ToLower(normalizedInput)
	if strings.HasPrefix(lowerInput, "http://") || strings.HasPrefix(lowerInput, "https://") {
		return SourceKindSubscription
	}
	return SourceKindProxyURI
}

func isLikelyHTTPProxyInput(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}

	hasCredentials := parsed.User != nil && (parsed.User.Username() != "" || func() bool {
		_, ok := parsed.User.Password()
		return ok
	}())
	hasPort := parsed.Port() != ""
	path := parsed.EscapedPath()
	hasOnlyRootPath := path == "" || path == "/"
	hasQueryOrFragment := parsed.RawQuery != "" || parsed.Fragment != ""

	return hasCredentials || (hasPort && hasOnlyRootPath && !hasQueryOrFragment)
}

func sourceKey(source RuntimeSource) string {
	return fmt.Sprintf("%s:%s", source.Kind, source.Input)
}

func dedupeSourcesWithPrecedence(groups ...[]RuntimeSource) []RuntimeSource {
	seen := make(map[string]struct{})
	var merged []RuntimeSource
	for _, group := range groups {
		for _, source := range group {
			key := sourceKey(source)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, source)
		}
	}
	return merged
}

func buildNodeName(uri string, fallback string) string {
	parsed, err := url.Parse(uri)
	if err == nil && parsed.Fragment != "" {
		if decoded, decodeErr := url.QueryUnescape(parsed.Fragment); decodeErr == nil && strings.TrimSpace(decoded) != "" {
			return strings.TrimSpace(decoded)
		}
		return strings.TrimSpace(parsed.Fragment)
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	return uri
}
