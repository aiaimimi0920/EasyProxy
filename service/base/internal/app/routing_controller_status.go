package app

import (
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/routerule"
)

func (rc *RoutingController) RoutingStatus() monitor.RoutingStatus {
	rc.mu.Lock()
	srv := rc.server
	running := rc.running
	cfg := cloneConfigSnapshot(rc.cfg)
	profiles := rc.profiles
	rc.mu.Unlock()

	st := monitor.RoutingStatus{Enabled: running, DispatcherReady: running && srv != nil}
	if cfg != nil {
		st.SharedEnabled = cfg.Routing.Enabled
		st.ProfileScope = "shared"
		st.SharedRevision = cfg.LocalServer.SharedRevision
		if cfg.LocalServer.Enabled {
			st.Enabled = cfg.Routing.Enabled
			st.DefaultStrategy = cfg.Routing.DefaultStrategy
			st.FinalPolicy = cfg.Routing.FinalPolicy
			st.RuleCount = len(cfg.Routing.Rules)
		}
	}
	if provider, ok := profiles.(interface {
		SharedProfile() *profile.CompiledProfile
	}); ok {
		if shared := provider.SharedProfile(); shared != nil {
			st.SharedEnabled = shared.Enabled()
			st.Enabled = shared.Enabled()
			st.SharedRevision = shared.Revision()
			st.DefaultStrategy = shared.Selection().DefaultStrategy
			st.FinalPolicy = string(shared.FinalPolicy())
			st.RuleCount = shared.RuleCount()
		}
	}
	if srv == nil {
		return st
	}
	st.Listen = srv.Listen()
	if cfg == nil || !cfg.LocalServer.Enabled {
		st.DefaultStrategy = string(srv.DefaultStrategy())
		st.FinalPolicy = srv.FinalPolicy()
		st.RuleCount = srv.RuleCount()
	}
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

func applyEngineConfig(engine *routerule.Engine, rules []string, finalPolicy string) {
	engine.SetRulesAndFinal(rules, routerule.NormalizePolicy(finalPolicy))
}

// buildEngine constructs a rule engine from cfg with an optional country lookup.
func buildEngine(cfg *config.Config, countryLookup routerule.CountryLookup) *routerule.Engine {
	engine := routerule.New(nil, routerule.NormalizePolicy(cfg.Routing.FinalPolicy), countryLookup)
	applyEngineConfig(engine, effectiveRules(cfg), cfg.Routing.FinalPolicy)
	return engine
}
