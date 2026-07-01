package app

import (
	"context"
	"log"
	"strings"
	"sync"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/dispatch"
	"easy_proxies/internal/geoip"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"
)

// RoutingController owns the lifecycle of the smart dispatch entry and its rule
// engine. It centralizes construction so that runtime edits from the management
// API can be applied with the cheapest mechanism that suffices:
//
//   - rule / provider / final-policy / default-strategy edits are hot-applied to
//     the running engine and dispatcher without touching sing-box or the pool.
//   - enable/disable and listen-address edits require (re)starting the dispatch
//     listener; because route-A takeover changes whether the pool inbound binds
//     the port, those still need a full sing-box reload, which the API signals
//     via need_reload (handled by the existing /api/reload path).
//
// The controller is safe for concurrent use.
type RoutingController struct {
	ctx    context.Context
	boxMgr *boxmgr.Manager

	mu       sync.Mutex
	cfg      *config.Config
	engine   *routerule.Engine
	server   *dispatch.Server
	geo      *geoip.Lookup
	provider *routerule.ProviderManager
	running  bool
}

// NewRoutingController creates a controller bound to a context and box manager.
func NewRoutingController(ctx context.Context, boxMgr *boxmgr.Manager) *RoutingController {
	return &RoutingController{ctx: ctx, boxMgr: boxMgr}
}

// Start builds and starts the dispatch entry if routing is enabled in cfg. It is
// a no-op (returning cleanly) when routing is disabled. Safe to call once at
// startup.
func (rc *RoutingController) Start(cfg *config.Config) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.cfg = cfg
	if cfg == nil || !cfg.Routing.Enabled {
		return
	}
	rc.startLocked(cfg)
}

// startLocked builds the engine + dispatcher and starts serving. Caller holds mu.
func (rc *RoutingController) startLocked(cfg *config.Config) {
	engine := buildEngine(cfg, rc.openGeoLocked(cfg))
	rc.engine = engine

	rc.startProviderLocked(cfg, engine)

	listen := cfg.DispatchListen()
	srv := dispatch.NewServer(dispatch.Config{
		Listen:          listen,
		Username:        cfg.Listener.Username,
		Password:        cfg.Listener.Password,
		DefaultStrategy: pool.NormalizeStrategy(cfg.Routing.DefaultStrategy),
	}, rc.boxMgr, engine, dispatchLogger{})

	if err := srv.Start(rc.ctx); err != nil {
		log.Printf("⚠️ smart dispatch entry failed to start on %s: %v", listen, err)
		rc.closeGeoLocked()
		rc.stopProviderLocked()
		rc.engine = nil
		return
	}
	rc.server = srv
	rc.running = true
	log.Printf("✅ smart dispatch entry active on %s (default strategy: %s)", listen, cfg.Routing.DefaultStrategy)
}

// ApplyHot applies rule/provider/final-policy/default-strategy changes to the
// running engine and dispatcher without restarting anything. It returns false
// when the controller is not currently running (nothing to hot-apply); callers
// then rely on the reload path. The provided cfg becomes the controller's new
// source of truth for subsequent status/snapshot reads.
func (rc *RoutingController) ApplyHot(cfg *config.Config) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.cfg = cfg
	if !rc.running || rc.server == nil || rc.engine == nil || cfg == nil {
		return false
	}

	// Rebuild the effective rule set and swap it atomically.
	rc.engine.SetRules(effectiveRules(cfg))
	rc.engine.SetFinal(routerule.NormalizePolicy(cfg.Routing.FinalPolicy))
	rc.server.SetDefaultStrategy(pool.NormalizeStrategy(cfg.Routing.DefaultStrategy))

	// Restart providers so interval/url edits take effect.
	rc.stopProviderLocked()
	rc.startProviderLocked(cfg, rc.engine)

	log.Printf("🧭 routing rules hot-reloaded (%d active rules, final=%s, strategy=%s)",
		rc.engine.RuleCount(), cfg.Routing.FinalPolicy, cfg.Routing.DefaultStrategy)
	return true
}

// Stop tears down the dispatcher and releases resources. Safe to call multiple
// times.
func (rc *RoutingController) Stop() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.server != nil {
		rc.server.Stop()
		rc.server = nil
	}
	rc.stopProviderLocked()
	rc.closeGeoLocked()
	rc.engine = nil
	rc.running = false
}

// startProviderLocked wires remote rule providers when configured. Caller holds mu.
func (rc *RoutingController) startProviderLocked(cfg *config.Config, engine *routerule.Engine) {
	if len(cfg.Routing.RuleProviders) == 0 {
		return
	}
	specs := make([]routerule.ProviderSpec, 0, len(cfg.Routing.RuleProviders))
	for _, p := range cfg.Routing.RuleProviders {
		specs = append(specs, routerule.ProviderSpec{
			URL:      p.URL,
			Policy:   routerule.NormalizePolicy(p.Policy),
			Interval: p.Interval,
			Behavior: p.Behavior,
		})
	}
	// Snapshot the static rule inputs so the provider callback merges against a
	// stable base (providers sit between user rules and the default set).
	staticRules := append([]string(nil), cfg.Routing.Rules...)
	useDefaults := cfg.RoutingUseDefaultRules()
	pm := routerule.NewProviderManager(specs, func(providerRules []string) {
		merged := make([]string, 0, len(staticRules)+len(providerRules)+64)
		merged = append(merged, staticRules...)
		merged = append(merged, providerRules...)
		if useDefaults {
			merged = append(merged, routerule.DefaultRules()...)
		}
		engine.SetRules(merged)
	})
	pm.Start(rc.ctx)
	rc.provider = pm
}

func (rc *RoutingController) stopProviderLocked() {
	if rc.provider != nil {
		rc.provider.Stop()
		rc.provider = nil
	}
}

// openGeoLocked opens a GeoIP lookup for GEOIP rules when configured, returning
// nil when unavailable. Caller holds mu.
func (rc *RoutingController) openGeoLocked(cfg *config.Config) routerule.CountryLookup {
	if !cfg.GeoIP.Enabled || strings.TrimSpace(cfg.GeoIP.DatabasePath) == "" {
		return nil
	}
	gl, err := geoip.New(cfg.GeoIP.DatabasePath)
	if err != nil {
		log.Printf("⚠️ routing GEOIP lookup unavailable: %v", err)
		return nil
	}
	rc.geo = gl
	return geoipCountryLookup{l: gl}
}

func (rc *RoutingController) closeGeoLocked() {
	if rc.geo != nil {
		rc.geo.Close()
		rc.geo = nil
	}
}

// RoutingStatus implements monitor.RoutingReporter for the /api/routing/status
// endpoint, reporting live dispatcher + sticky state.
func (rc *RoutingController) RoutingStatus() monitor.RoutingStatus {
	rc.mu.Lock()
	srv := rc.server
	rc.mu.Unlock()

	st := monitor.RoutingStatus{Enabled: false}
	if srv == nil {
		return st
	}
	st.Enabled = true
	st.Listen = srv.Listen()
	st.DefaultStrategy = string(srv.DefaultStrategy())
	st.FinalPolicy = srv.FinalPolicy()
	st.RuleCount = srv.RuleCount()
	if rc.boxMgr != nil {
		if snap, ok := rc.boxMgr.StickySnapshot(); ok {
			st.StickyBuckets = snap.Buckets
			st.StickySessions = snap.Sessions
		}
	}
	return st
}

// effectiveRules builds the ordered rule set: user rules first (highest
// priority), then the built-in China-direct defaults unless opted out. Remote
// providers are layered in at runtime by the provider callback.
func effectiveRules(cfg *config.Config) []string {
	rules := make([]string, 0, len(cfg.Routing.Rules)+64)
	rules = append(rules, cfg.Routing.Rules...)
	if cfg.RoutingUseDefaultRules() {
		rules = append(rules, routerule.DefaultRules()...)
	}
	return rules
}

// buildEngine constructs a rule engine from cfg with an optional country lookup.
func buildEngine(cfg *config.Config, countryLookup routerule.CountryLookup) *routerule.Engine {
	return routerule.New(effectiveRules(cfg), routerule.NormalizePolicy(cfg.Routing.FinalPolicy), countryLookup)
}
