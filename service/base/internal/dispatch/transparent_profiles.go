package dispatch

import (
	"context"
	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/outbound/profileselect"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/routerule"
	N "github.com/sagernet/sing/common/network"
	"net"
	"time"
)

func (s *Server) transparentProfiles() profileselect.Resolver {
	if s.cfg.LocalServer {
		return s.cfg.Profiles
	}
	return nil
}

func (r *TransparentRouter) resolveProfile(ctx context.Context, remote net.Addr, host string, port uint16, policy routerule.Policy) (context.Context, routerule.Policy) {
	if r.cfg.Profiles != nil {
		peer := peerAddr(remote)
		resolution := r.cfg.Profiles.Resolve(profile.RequestIdentity{PeerIP: peer})
		r.cfg.Profiles.Observe(resolution, peer, time.Now().UTC())
		session := ""
		if peer.IsValid() {
			session = peer.String()
		}
		directive := profileselect.Directive(resolution, session)
		ctx = pool.WithDirective(ctx, &directive)
		if resolution.Profile == nil || !resolution.Profile.Enabled() {
			policy = routerule.PolicyDirect
		} else {
			policy = resolution.Profile.MatchRequest(routerule.Request{Host: host, Port: port, Network: N.NetworkTCP})
		}
	}
	if r.cfg.Profiles == nil && r.cfg.SelectionTiers != nil {
		if tiers := r.cfg.SelectionTiers(); len(tiers) > 0 {
			strategy := pool.StrategyStable
			if r.cfg.DefaultStrategy != nil {
				strategy = r.cfg.DefaultStrategy()
			}
			directive := pool.SelectionDirective{Strategy: strategy, SelectionTiers: config.CloneSelectionTiers(tiers)}
			if peer := peerAddr(remote); peer.IsValid() && strategy == pool.StrategySession {
				directive.SessionKey = peer.String()
			}
			ctx = pool.WithDirective(ctx, &directive)
		}
	}
	return ctx, policy
}
