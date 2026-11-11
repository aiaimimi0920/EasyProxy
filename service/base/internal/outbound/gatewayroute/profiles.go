package gatewayroute

import (
	"context"
	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/outbound/profileselect"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/routerule"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"net/netip"
	"strings"
	"time"
)

func (r *routeOutbound) selection(ctx context.Context, network string, destination M.Socksaddr) (routerule.Policy, *pool.SelectionDirective, error) {
	directive := pool.SelectionDirective{ProfileID: tcpProfileID, Strategy: pool.NormalizeStrategy(string(r.options.DefaultStrategy)), SelectionTiers: append([]config.SelectionTier(nil), r.options.SelectionTiers...)}
	policy := r.policy(ctx, network, destination)
	if r.profiles == nil && r.sharedSelection != nil {
		// Routing-disabled legacy TUN must not become DIRECT or enable device maps.
		// Only the shared selector is live; existing gateway policy stays in force.
		shared, err := r.sharedSelection(ctx)
		if err != nil {
			return "", nil, err
		}
		if shared != nil {
			directive.SelectionTiers = append([]config.SelectionTier(nil), shared.SelectionTiers...)
			directive.ProfileRevision = shared.ProfileRevision
			if len(shared.SelectionTiers) > 0 {
				directive.Strategy = shared.Strategy
				directive.SessionTTL = shared.SessionTTL
				if shared.Strategy == pool.StrategySession {
					if metadata := adapter.ContextFrom(ctx); metadata != nil && metadata.Source.Addr.IsValid() {
						directive.SessionKey = metadata.Source.Addr.Unmap().String()
					}
				}
				directive.Filter = shared.Filter
				directive.LongLived = shared.LongLived
			}
		}
	}
	if r.profiles != nil {
		// Source is the original inbound peer, never a sniffed domain, destination or header.
		var peer netip.Addr
		host := destination.AddrString()
		if metadata := adapter.ContextFrom(ctx); metadata != nil {
			peer = metadata.Source.Addr.Unmap()
			if strings.TrimSpace(metadata.Domain) != "" {
				host = metadata.Domain
			}
		}
		resolution := r.profiles.Resolve(profile.RequestIdentity{PeerIP: peer})
		r.profiles.Observe(resolution, peer, time.Now().UTC())
		session := ""
		if peer.IsValid() {
			session = peer.String()
		}
		directive = profileselect.Directive(resolution, session)
		if resolution.Profile == nil || !resolution.Profile.Enabled() {
			policy = routerule.PolicyDirect
		} else {
			policy = resolution.Profile.MatchRequest(routerule.Request{Host: host, Port: destination.Port, Network: network})
		}
	}
	if network == N.NetworkUDP {
		if r.profiles == nil {
			directive.ProfileID = udpProfileID
			directive.Strategy = pool.StrategyAuto
		} else {
			directive.ProfileID += "/udp"
		}
		directive.PreferredProtocolFamilies = append([]string(nil), nativeUDPProtocolFamilies...)
		directive.RequireAvailablePreferred = true
	}
	return policy, &directive, nil
}
