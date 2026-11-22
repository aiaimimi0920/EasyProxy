package pool

import (
	"context"
	"errors"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"strings"
)

func (p *poolOutbound) claudeSelectionContext(ctx context.Context, destination M.Socksaddr) (context.Context, error) {
	host := destination.AddrString()
	if metadata := adapter.ContextFrom(ctx); metadata != nil && metadata.Domain != "" {
		host = metadata.Domain
	}
	if !p.options.ClaudeRoute.Matches(host) {
		return p.sharedSelectionContext(ctx)
	}
	tag := strings.TrimSpace(p.options.ClaudeRoute.NodeTag)
	if !p.options.ClaudeRoute.ValidBinding() {
		return ctx, errors.New("Claude route requires a node tag and valid node fingerprint")
	}
	return WithDirective(ctx, &SelectionDirective{RequiredTag: tag, RequiredNodeFingerprint: p.options.ClaudeRoute.NormalizedFingerprint()}), nil
}
