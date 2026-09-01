package app

import (
	"fmt"
	"log"
	"strings"
	"time"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/geoip"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"
)

func (rc *RoutingController) setAppliedStateLocked(state boxmgr.ReloadState) {
	state = cloneRoutingState(state)
	rc.lastApplied = state
	rc.hasLastApplied = true
	rc.cfg = state.Config
}

func (rc *RoutingController) stopRuntimeLocked() {
	if rc.server != nil {
		rc.server.Stop()
		rc.server = nil
	}
	rc.stopProviderLocked()
	rc.closeGeoLocked()
	rc.engine = nil
	rc.running = false
}

func (rc *RoutingController) applyThresholds(cfg *config.Config) {
	if cfg == nil || rc.boxMgr == nil {
		return
	}
	rc.boxMgr.SetLongLivedThresholds(
		cfg.Routing.LongLived.MinUptime,
		cfg.Routing.LongLived.MinSuccessRate,
	)
}

func (rc *RoutingController) recordAppliedConfig(cfg *config.Config) {
	if cfg != nil && rc.boxMgr != nil {
		rc.boxMgr.RecordAppliedConfig(cfg)
	}
}

// applyRuntimeConfigLocked updates a live dispatcher without replacing its
// listening socket. Caller holds rc.mu.
func (rc *RoutingController) applyRuntimeConfigLocked(previous, cfg *config.Config) error {
	if cfg == nil || rc.server == nil || rc.engine == nil {
		return fmt.Errorf("smart routing runtime is not active")
	}
	if err := validateRuleProviders(cfg.Routing.RuleProviders); err != nil {
		return err
	}

	geoChanged := false
	if previous != nil {
		geoChanged = routingGeoChanged(previous, cfg)
	} else {
		wantsGeo := cfg.GeoIP.Enabled && strings.TrimSpace(cfg.GeoIP.DatabasePath) != ""
		geoChanged = wantsGeo != (rc.geo != nil)
	}
	if geoChanged {
		newGeo, countryLookup := openGeoConfig(cfg)
		newEngine, err := buildEngine(cfg, countryLookup)
		if err != nil {
			if newGeo != nil {
				_ = newGeo.Close()
			}
			return err
		}
		oldGeo := rc.geo

		rc.stopProviderLocked()
		if err := rc.startProviderLocked(cfg, newEngine); err != nil {
			if newGeo != nil {
				_ = newGeo.Close()
			}
			return err
		}
		rc.geo = newGeo
		rc.engine = newEngine
		rc.server.SetEngine(newEngine)
		if oldGeo != nil {
			_ = oldGeo.Close()
		}
	} else if routingEngineInputsChanged(previous, cfg) {
		// Invalidate the old provider generation before publishing the new rule
		// snapshot so an in-flight stale callback cannot win last.
		rc.stopProviderLocked()
		rules, err := effectiveRules(cfg)
		if err != nil {
			return err
		}
		applyEngineConfig(rc.engine, rules, cfg.Routing.FinalPolicy)
		if err := rc.startProviderLocked(cfg, rc.engine); err != nil {
			return err
		}
	}

	rc.server.SetDefaultStrategy(pool.NormalizeStrategy(cfg.Routing.DefaultStrategy))
	return nil
}

// startProviderLocked wires remote rule providers when configured. Caller holds mu.
func (rc *RoutingController) startProviderLocked(cfg *config.Config, engine *routerule.Engine) error {
	if err := validateRuleProviders(cfg.Routing.RuleProviders); err != nil {
		return err
	}
	rc.providerApplyMu.Lock()
	rc.providerGeneration++
	generation := rc.providerGeneration
	rc.providerApplyMu.Unlock()

	if len(cfg.Routing.RuleProviders) == 0 {
		return nil
	}
	specs := make([]routerule.ProviderSpec, 0, len(cfg.Routing.RuleProviders))
	for _, p := range cfg.Routing.RuleProviders {
		specs = append(specs, routerule.ProviderSpec{
			URL:      strings.TrimSpace(p.URL),
			Policy:   routerule.NormalizePolicy(p.Policy),
			Interval: p.Interval,
			Behavior: p.Behavior,
		})
	}
	// Snapshot the static rule inputs so the provider callback merges against a
	// stable base (providers sit between user rules and the default set).
	staticRules, err := configuredRules(cfg)
	if err != nil {
		return err
	}
	useDefaults := cfg.RoutingUseDefaultRules()
	finalPolicy := cfg.Routing.FinalPolicy
	pm := routerule.NewProviderManager(specs, func(providerRules []string) {
		merged := make([]string, 0, len(staticRules)+len(providerRules)+64)
		merged = append(merged, staticRules...)
		merged = append(merged, providerRules...)
		if useDefaults {
			merged = append(merged, routerule.DefaultRules()...)
		}
		rc.applyProviderRules(generation, engine, merged, finalPolicy)
	})
	pm.Start(rc.ctx)
	rc.provider = pm
	return nil
}

func (rc *RoutingController) stopProviderLocked() {
	// Invalidate callbacks before cancelling the old manager. Stop does not
	// wait for an in-flight fetch, so callbacks may arrive after it returns.
	rc.providerApplyMu.Lock()
	rc.providerGeneration++
	rc.providerApplyMu.Unlock()

	if rc.provider != nil {
		rc.provider.Stop()
		rc.provider = nil
	}
}

// applyProviderRules applies a provider update only when it belongs to the
// currently active provider generation. It returns false for callbacks from a
// provider that was stopped or superseded by a hot apply.
func (rc *RoutingController) applyProviderRules(
	generation uint64,
	engine *routerule.Engine,
	rules []string,
	finalPolicy string,
) bool {
	rc.providerApplyMu.Lock()
	defer rc.providerApplyMu.Unlock()
	if generation != rc.providerGeneration {
		return false
	}
	applyEngineConfig(engine, rules, finalPolicy)
	return true
}

// openGeoLocked opens a GeoIP lookup for GEOIP rules when configured, returning
// nil when unavailable. Caller holds mu.
func (rc *RoutingController) openGeoLocked(cfg *config.Config) routerule.CountryLookup {
	gl, lookup := openGeoConfig(cfg)
	rc.geo = gl
	return lookup
}

func openGeoConfig(cfg *config.Config) (*geoip.Lookup, routerule.CountryLookup) {
	if cfg == nil || !cfg.GeoIP.Enabled || strings.TrimSpace(cfg.GeoIP.DatabasePath) == "" {
		return nil, nil
	}
	var (
		gl  *geoip.Lookup
		err error
	)
	if cfg.GeoIP.AutoUpdateEnabled {
		interval := cfg.GeoIP.AutoUpdateInterval
		if interval <= 0 {
			interval = 24 * time.Hour
		}
		gl, err = geoip.NewWithAutoUpdate(cfg.GeoIP.DatabasePath, interval)
	} else {
		gl, err = geoip.New(cfg.GeoIP.DatabasePath)
	}
	if err != nil {
		log.Printf("⚠️ routing GEOIP lookup unavailable: %v", err)
		return nil, nil
	}
	return gl, geoipCountryLookup{l: gl}
}

func (rc *RoutingController) closeGeoLocked() {
	if rc.geo != nil {
		rc.geo.Close()
		rc.geo = nil
	}
}

// RoutingStatus implements monitor.RoutingReporter for the /api/routing/status
// endpoint, reporting live dispatcher + sticky state.
