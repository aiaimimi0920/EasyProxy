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

// configuredRules builds the static rule set: inline rules first, then local
// files in declaration order. Remote providers and defaults are layered later.
func configuredRules(cfg *config.Config) ([]string, error) {
	localRules, err := routerule.LoadLocalRuleFiles(cfg.Routing.RuleFiles)
	if err != nil {
		return nil, err
	}
	rules := make([]string, 0, len(cfg.Routing.Rules)+len(localRules))
	rules = append(rules, cfg.Routing.Rules...)
	rules = append(rules, localRules...)
	return rules, nil
}

// effectiveRules appends built-in defaults after the static rule set. Remote
// providers are inserted between local files and defaults by their callback.
func effectiveRules(cfg *config.Config) ([]string, error) {
	rules, err := configuredRules(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.RoutingUseDefaultRules() {
		rules = append(rules, routerule.DefaultRules()...)
	}
	return rules, nil
}

func applyEngineConfig(engine *routerule.Engine, rules []string, finalPolicy string) {
	engine.SetRulesAndFinal(rules, routerule.NormalizePolicy(finalPolicy))
}

// buildEngine constructs a rule engine from cfg with an optional country lookup.
func buildEngine(cfg *config.Config, countryLookup routerule.CountryLookup) (*routerule.Engine, error) {
	rules, err := effectiveRules(cfg)
	if err != nil {
		return nil, err
	}
	engine := routerule.New(nil, routerule.NormalizePolicy(cfg.Routing.FinalPolicy), countryLookup)
	applyEngineConfig(engine, rules, cfg.Routing.FinalPolicy)
	return engine, nil
}
