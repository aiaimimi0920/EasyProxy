package config

import "testing"

func TestClaudeDomainGroup(t *testing.T) {
	owned := []string{"api.anthropic.com", "claude.ai", "claude.com", "platform.claude.com", "sso.claude.com", "mcp-proxy.anthropic.com", "downloads.claude.ai", "bridge.claudeusercontent.com", "code.claude.com", "assets-proxy.anthropic.com", "a.frame.claudeusercontent.com", "a.claudemcpcontent.com"}
	shared := []string{"storage.googleapis.com", "registry.npmjs.org", "github.com", "raw.githubusercontent.com", "http-intake.logs.us5.datadoghq.com", "browser-intake-us5-datadoghq.com", "formulae.brew.sh"}
	excluded := []string{"evilgithub.com", "github.com.attacker.test", "other.googleapis.com", "other.datadoghq.com", "claudemcpcontent.com", "evilclaudemcpcontent.com", "frame.claudeusercontent.com", "203.0.113.1", "::1", "https://github.com/path", "github.com:443", "api.anthropic.com.attacker.test"}
	for _, enable := range []bool{false, true} {
		for _, optIn := range []bool{false, true} {
			c := ClaudeRoute{Enabled: enable, IncludeSharedDependencies: optIn}
			for _, host := range owned {
				if c.Matches(host) != enable {
					t.Errorf("owned %s", host)
				}
			}
			for _, host := range shared {
				if c.Matches(host) != (enable && optIn) {
					t.Errorf("shared %s", host)
				}
			}
			for _, host := range excluded {
				if c.Matches(host) {
					t.Errorf("overmatch %s", host)
				}
			}
		}
	}
	if !(ClaudeRoute{Enabled: true, IncludeSharedDependencies: true}).Matches(" RAW.GITHUBUSERCONTENT.COM. ") {
		t.Fatal("normalization")
	}
}
