package pool

import (
	"context"
	"easy_proxies/internal/config"
	M "github.com/sagernet/sing/common/metadata"
	"testing"
)

func TestClaudeDomainGroupUsesOneBinding(t *testing.T) {
	binding := config.ClaudeRoute{Enabled: true, IncludeSharedDependencies: true, NodeTag: "single-approved-node", NodeFingerprint: config.NodeFingerprint("one-uri")}
	p := &poolOutbound{options: Options{ClaudeRoute: binding}}
	for _, host := range []string{"api.anthropic.com", "claude.ai", "claude.com", "platform.claude.com", "sso.claude.com", "mcp-proxy.anthropic.com", "downloads.claude.ai", "bridge.claudeusercontent.com", "code.claude.com", "assets-proxy.anthropic.com", "a.frame.claudeusercontent.com", "a.claudemcpcontent.com", "storage.googleapis.com", "registry.npmjs.org", "github.com", "raw.githubusercontent.com", "http-intake.logs.us5.datadoghq.com", "browser-intake-us5-datadoghq.com", "formulae.brew.sh"} {
		ctx := WithDirective(context.Background(), &SelectionDirective{RequiredTag: "unrelated-hint"})
		next, err := p.claudeSelectionContext(ctx, M.ParseSocksaddr(host+":443"))
		if err != nil {
			t.Fatal(err)
		}
		d := DirectiveFrom(next)
		if d == nil || d.RequiredTag != binding.NodeTag || d.RequiredNodeFingerprint != binding.NodeFingerprint {
			t.Fatalf("%s did not use common binding: %+v", host, d)
		}
	}
}
