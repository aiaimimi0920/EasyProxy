package config

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ClaudeRoute pins a documented domain group to one binding. Disabled is the
// backwards-compatible default. NodeTag is an exact pool member tag, not a
// region, source name, or proof of a stable public IP.
type ClaudeRoute struct {
	// IncludeSharedDependencies also pins shared package, metadata and telemetry hosts.
	// This affects every application using these hosts through EasyProxy.
	IncludeSharedDependencies bool   `yaml:"include_shared_dependencies"`
	Enabled                   bool   `yaml:"enabled"`
	NodeFingerprint           string `yaml:"node_fingerprint"`
	NodeTag                   string `yaml:"node_tag"`
}

func (c ClaudeRoute) Matches(host string) bool {
	if !c.Enabled {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	switch host {
	case "api.anthropic.com", "claude.ai", "claude.com", "platform.claude.com", "sso.claude.com", "mcp-proxy.anthropic.com", "downloads.claude.ai", "bridge.claudeusercontent.com", "code.claude.com", "assets-proxy.anthropic.com":
		return true
	}
	if strings.HasSuffix(host, ".frame.claudeusercontent.com") || strings.HasSuffix(host, ".claudemcpcontent.com") {
		return true
	}
	if c.IncludeSharedDependencies {
		switch host {
		case "storage.googleapis.com", "registry.npmjs.org", "github.com", "raw.githubusercontent.com", "http-intake.logs.us5.datadoghq.com", "browser-intake-us5-datadoghq.com", "formulae.brew.sh":
			return true
		}
	}
	return false
}

// NodeFingerprint binds the exact subscription node URI without logging it.
func NodeFingerprint(uri string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(uri)))
	return hex.EncodeToString(sum[:])
}

func (c ClaudeRoute) NormalizedFingerprint() string {
	return strings.ToLower(strings.TrimSpace(c.NodeFingerprint))
}

func (c ClaudeRoute) ValidBinding() bool {
	b, e := hex.DecodeString(c.NormalizedFingerprint())
	return strings.TrimSpace(c.NodeTag) != "" && e == nil && len(b) == sha256.Size
}
