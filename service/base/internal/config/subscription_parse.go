package config

import (
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
)

func ParseSubscriptionContent(content string) ([]NodeConfig, error) {
	content = strings.TrimSpace(content)

	// Clash subscriptions often include metadata headers before the actual
	// `proxies:` section, so checking only the first few bytes is too brittle.
	// Try the YAML parser whenever the document contains a proxies block.
	if looksLikeClashYAML(content) {
		nodes, err := parseClashYAML(content)
		if err == nil {
			return nodes, nil
		}
	}

	// Check if it's base64 encoded (common for v2ray subscriptions).
	// Providers use both standard and URL-safe alphabets, with or without
	// padding, so decode all four variants before falling back to plain text.
	if decoded, ok := decodeSubscriptionBase64(content); ok {
		content = string(decoded)
	}

	// Parse as plain text (one URI per line)
	return parseNodesFromContent(content)
}

func parseSubscriptionContent(content string) ([]NodeConfig, error) {
	return ParseSubscriptionContent(content)
}

func looksLikeClashYAML(content string) bool {
	normalized := strings.ToLower(strings.TrimSpace(content))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "\nproxies:") || strings.HasPrefix(normalized, "proxies:")
}

// parseNodesFromContent parses nodes from plain text content (one URI per line)
func parseNodesFromContent(content string) ([]NodeConfig, error) {
	var nodes []NodeConfig
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check if it's a valid proxy URI
		normalizedLine := NormalizeProxyURIInput(line, "http")
		if IsProxyURI(normalizedLine) {
			nodes = append(nodes, NodeConfig{
				URI: normalizedLine,
			})
		}
	}

	return nodes, nil
}

// isBase64 checks if a string looks like base64 encoded content (optimized version)
func isBase64(s string) bool {
	s = compactBase64Whitespace(s)
	if len(s) == 0 {
		return false
	}

	// Quick check: if it contains proxy URI schemes, it's not base64
	if strings.Contains(s, "://") {
		return false
	}

	// Accept both standard (+/) and URL-safe (-_) base64 alphabets.
	// This is much faster than trying to decode
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '+' || c == '/' || c == '-' || c == '_' || c == '=') {
			return false
		}
	}

	// A base64 payload can be unpadded, but a remainder of 1 is impossible.
	return len(s)%4 != 1
}

func decodeSubscriptionBase64(content string) ([]byte, bool) {
	encoded := compactBase64Whitespace(content)
	if !isBase64(encoded) {
		return nil, false
	}
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if decoded, err := encoding.DecodeString(encoded); err == nil {
			return decoded, true
		}
	}
	return nil, false
}

func compactBase64Whitespace(value string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
}

// IsProxyURI checks if a string is a valid proxy URI
func IsProxyURI(s string) bool {
	normalized := strings.TrimSpace(NormalizeProxyURIInput(s, "http"))
	lower := strings.ToLower(normalized)
	if normalized == "" {
		return false
	}
	for _, scheme := range supportedProxyURISchemes {
		if strings.HasPrefix(lower, scheme) {
			return looksLikeProxyURI(normalized)
		}
	}
	return false
}

func looksLikeProxyURI(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "socks5":
		if strings.ContainsAny(raw, "<> \t\r\n") {
			return false
		}
		host := strings.TrimSpace(parsed.Hostname())
		if host == "" || strings.ContainsAny(host, "<> \t\r\n") {
			return false
		}
		if port := strings.TrimSpace(parsed.Port()); port != "" {
			if _, err := strconv.Atoi(port); err != nil {
				return false
			}
		}
		return true
	case "vmess", "vless", "trojan", "ss", "ssr", "hysteria", "hysteria2", "hy2", "anytls":
		return true
	default:
		return false
	}
}

// clashConfig represents a minimal Clash configuration for parsing proxies
