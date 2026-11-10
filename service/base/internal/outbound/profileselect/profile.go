// Package profileselect adapts immutable profiles to shared pool selection.
package profileselect

import (
	"context"
	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/profile"
	"net/netip"
	"time"
)

// Resolver resolves each new flow against the currently published registry.
type Resolver interface {
	Resolve(profile.RequestIdentity) profile.Resolution
	Observe(profile.Resolution, netip.Addr, time.Time)
}
type contextKey struct{}

func WithResolver(ctx context.Context, resolver Resolver) context.Context {
	return context.WithValue(ctx, contextKey{}, resolver)
}
func FromContext(ctx context.Context) Resolver {
	resolver, _ := ctx.Value(contextKey{}).(Resolver)
	return resolver
}
func Directive(resolution profile.Resolution, sessionFallback string) pool.SelectionDirective {
	if resolution.Profile == nil {
		return pool.SelectionDirective{}
	}
	selection := resolution.Profile.Selection()
	result := pool.SelectionDirective{
		ProfileID: resolution.ProfileID, ProfileRevision: resolution.ProfileRevision,
		Strategy: pool.NormalizeStrategy(selection.DefaultStrategy), SessionTTL: selection.SessionTTL,
		SelectionTiers: append([]config.SelectionTier(nil), selection.SelectionTiers...),
		Filter:         pool.NodeFilter{Countries: selection.Filter.Countries, Regions: selection.Filter.Regions, LongLived: selection.Filter.LongLived},
		LongLived:      pool.LongLivedPolicy{MinUptime: selection.LongLivedMinUptime, MinSuccessRate: selection.LongLivedMinSuccessRate},
	}
	if result.Strategy == pool.StrategySession {
		result.SessionKey = sessionFallback
	}
	return result
}
