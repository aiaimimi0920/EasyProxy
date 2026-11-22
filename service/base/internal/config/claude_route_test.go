package config

import "testing"

func TestClaudeRouteDomainBoundary(t *testing.T) {
	c := ClaudeRoute{Enabled: true}
	for _, h := range []string{"API.Anthropic.COM.", "claude.ai", "x.frame.claudeusercontent.com", "downloads.claude.ai"} {
		if !c.Matches(h) {
			t.Errorf("missing %s", h)
		}
	}
	for _, h := range []string{"evilclaude.ai", "claude.ai.evil.com", "github.com", "registry.npmjs.org", "storage.googleapis.com", "203.0.113.1", "frame.claudeusercontent.com"} {
		if c.Matches(h) {
			t.Errorf("overmatch %s", h)
		}
	}
	c.Enabled = false
	if c.Matches("claude.ai") {
		t.Fatal("disabled match")
	}
	original := &Config{ClaudeRoute: ClaudeRoute{Enabled: true, NodeTag: "fixed"}}
	if original.Clone().ClaudeRoute != original.ClaudeRoute {
		t.Fatal("clone loses policy")
	}
}

func TestClaudeRouteBindingValidation(t *testing.T) {
	for _, f := range []string{"", "bad", "0123"} {
		if (ClaudeRoute{NodeTag: "n", NodeFingerprint: f}).ValidBinding() {
			t.Fatal("invalid fingerprint accepted")
		}
	}
	if !(ClaudeRoute{NodeTag: "n", NodeFingerprint: NodeFingerprint("uri")}).ValidBinding() {
		t.Fatal("valid binding rejected")
	}
	fp := NodeFingerprint("vless://example")
	upperRoute := ClaudeRoute{NodeTag: "n", NodeFingerprint: "  " + "\t" + string([]byte(fp[:8])) + stringsUpperASCII(fp[8:]) + "\n"}
	if !upperRoute.ValidBinding() || upperRoute.NormalizedFingerprint() != fp {
		t.Fatalf("uppercase/whitespace fingerprint normalization failed: valid=%v got=%q want=%q", upperRoute.ValidBinding(), upperRoute.NormalizedFingerprint(), fp)
	}
}

func stringsUpperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'f' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}
