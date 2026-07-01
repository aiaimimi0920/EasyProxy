package app

import (
	"context"
	"log"
	"net/netip"
	"strings"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/dispatch"
	"easy_proxies/internal/geoip"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"
)

// startDispatch wires and starts the smart dispatch entry when routing is
// enabled. It returns the running server (nil when disabled) and the geoip
// lookup that must be closed on shutdown. The dispatcher reuses the live pool
// outbound from boxMgr, so the health/blacklist/stats engine is shared with
// the plain inbound path.
func startDispatch(ctx context.Context, cfg *config.Config, boxMgr *boxmgr.Manager) (*dispatch.Server, *geoip.Lookup) {
	if cfg == nil || !cfg.Routing.Enabled {
		return nil, nil
	}

	// Build the rule engine: custom rules first (highest priority), then the
	// built-in China-direct defaults unless the operator opted out.
	rules := make([]string, 0, len(cfg.Routing.Rules)+64)
	rules = append(rules, cfg.Routing.Rules...)
	if cfg.RoutingUseDefaultRules() {
		rules = append(rules, routerule.DefaultRules()...)
	}
	finalPolicy := routerule.NormalizePolicy(cfg.Routing.FinalPolicy)

	// Optional GeoIP lookup for GEOIP rules (literal-IP destinations only).
	var geoLookup *geoip.Lookup
	var countryLookup routerule.CountryLookup
	if cfg.GeoIP.Enabled && strings.TrimSpace(cfg.GeoIP.DatabasePath) != "" {
		if gl, err := geoip.New(cfg.GeoIP.DatabasePath); err == nil {
			geoLookup = gl
			countryLookup = geoipCountryLookup{l: gl}
		}
	}

	engine := routerule.New(rules, finalPolicy, countryLookup)

	// Wire remote rule providers (fail-soft; refreshed on their interval).
	if len(cfg.Routing.RuleProviders) > 0 {
		specs := make([]routerule.ProviderSpec, 0, len(cfg.Routing.RuleProviders))
		for _, p := range cfg.Routing.RuleProviders {
			specs = append(specs, routerule.ProviderSpec{
				URL:      p.URL,
				Policy:   routerule.NormalizePolicy(p.Policy),
				Interval: p.Interval,
				Behavior: p.Behavior,
			})
		}
		pm := routerule.NewProviderManager(specs, func(providerRules []string) {
			merged := make([]string, 0, len(cfg.Routing.Rules)+len(providerRules)+64)
			merged = append(merged, cfg.Routing.Rules...)
			merged = append(merged, providerRules...)
			if cfg.RoutingUseDefaultRules() {
				merged = append(merged, routerule.DefaultRules()...)
			}
			engine.SetRules(merged)
		})
		pm.Start(ctx)
	}

	// Listen address is resolved by config (single source of truth shared with
	// the builder's pool-inbound takeover decision).
	listen := cfg.DispatchListen()

	srv := dispatch.NewServer(dispatch.Config{
		Listen:          listen,
		Username:        cfg.Listener.Username,
		Password:        cfg.Listener.Password,
		DefaultStrategy: pool.NormalizeStrategy(cfg.Routing.DefaultStrategy),
	}, boxMgr, engine, dispatchLogger{})

	if err := srv.Start(ctx); err != nil {
		log.Printf("⚠️ smart dispatch entry failed to start on %s: %v", listen, err)
		if geoLookup != nil {
			geoLookup.Close()
		}
		return nil, nil
	}
	if server := boxMgr.MonitorServer(); server != nil {
		server.SetRoutingReporter(routingReporter{srv: srv, boxMgr: boxMgr})
	}
	log.Printf("✅ smart dispatch entry active on %s (default strategy: %s)", listen, cfg.Routing.DefaultStrategy)
	return srv, geoLookup
}

// geoipCountryLookup adapts geoip.Lookup to routerule.CountryLookup.
type geoipCountryLookup struct{ l *geoip.Lookup }

func (g geoipCountryLookup) CountryISO(ip netip.Addr) string {
	if g.l == nil {
		return ""
	}
	info := g.l.LookupIP(ip.String())
	return strings.ToUpper(strings.TrimSpace(info.ISOCode))
}

// dispatchLogger adapts the standard logger to dispatch.Logger.
type dispatchLogger struct{}

func (dispatchLogger) Infof(format string, args ...any) { log.Printf(format, args...) }
func (dispatchLogger) Warnf(format string, args ...any) { log.Printf(format, args...) }

// routingReporter adapts the running dispatch server + pool sticky state into
// the monitor.RoutingReporter surface for the /api/routing/status endpoint.
type routingReporter struct {
	srv    *dispatch.Server
	boxMgr *boxmgr.Manager
}

func (r routingReporter) RoutingStatus() monitor.RoutingStatus {
	st := monitor.RoutingStatus{Enabled: false}
	if r.srv == nil {
		return st
	}
	st.Enabled = true
	st.Listen = r.srv.Listen()
	st.DefaultStrategy = string(r.srv.DefaultStrategy())
	st.FinalPolicy = string(r.srv.FinalPolicy())
	st.RuleCount = r.srv.RuleCount()
	if r.boxMgr != nil {
		if snap, ok := r.boxMgr.StickySnapshot(); ok {
			st.StickyBuckets = snap.Buckets
			st.StickySessions = snap.Sessions
		}
	}
	return st
}
