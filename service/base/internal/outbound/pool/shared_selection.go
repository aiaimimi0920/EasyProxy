package pool

import (
	"context"
	"github.com/sagernet/sing-box/adapter"
)

// SharedSelectionSource supplies a fresh immutable shared selection per flow.
// It is deliberately independent of device resolution and routing enablement.
type SharedSelectionSource func(context.Context) (*SelectionDirective, error)
type sharedSelectionKey struct{}

func WithSharedSelection(ctx context.Context, source SharedSelectionSource) context.Context {
	return context.WithValue(ctx, sharedSelectionKey{}, source)
}
func SharedSelectionFrom(ctx context.Context) SharedSelectionSource {
	source, _ := ctx.Value(sharedSelectionKey{}).(SharedSelectionSource)
	return source
}
func (p *poolOutbound) sharedSelectionContext(ctx context.Context) (context.Context, error) {
	// Device dispatch and TUN already supplied a directive. Never replace it.
	if !p.options.SharedSelection || DirectiveFrom(ctx) != nil {
		return ctx, nil
	}
	var d *SelectionDirective
	if p.sharedSelection != nil {
		var err error
		d, err = p.sharedSelection(ctx)
		if err != nil {
			return ctx, err
		}
	} else if len(p.options.SelectionTiers) > 0 {
		d = &SelectionDirective{ProfileID: "shared", SelectionTiers: p.options.SelectionTiers}
	}
	// Empty tiers retain the legacy pool's selection mode and retry semantics.
	if !hasSelectionTiers(d) {
		return ctx, nil
	}
	next := *d
	if next.Strategy == StrategySession {
		if m := adapter.ContextFrom(ctx); m != nil && m.Source.Addr.IsValid() {
			next.SessionKey = m.Source.Addr.Unmap().String()
		}
	}
	return WithDirective(ctx, &next), nil
}
