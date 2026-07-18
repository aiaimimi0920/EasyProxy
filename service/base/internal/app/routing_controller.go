package app

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

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
//   - reload lifecycle hooks stop the dispatch listener before a topology change
//     tears down the old box and start it only after the candidate pool is live.
//   - source-only reloads keep the existing listener, engine, and providers.
//
// The controller is safe for concurrent use.
type RoutingController struct {
	ctx        context.Context
	boxMgr     routingBoxManager
	managerRef *boxmgr.Manager

	// operationMu makes Start/ApplyHot/Stop and reload hooks linearizable while
	// still allowing cross-component monitor updates to run outside rc.mu.
	operationMu sync.Mutex

	mu       sync.Mutex
	cfg      *config.Config
	engine   *routerule.Engine
	server   *dispatch.Server
	geo      *geoip.Lookup
	provider *routerule.ProviderManager
	running  bool

	lastApplied           boxmgr.ReloadState
	hasLastApplied        bool
	pendingFrom           boxmgr.ReloadState
	hasPending            bool
	pendingRuntimeMutated bool

	// providerApplyMu serializes generation checks with engine updates. A
	// ProviderManager.Stop only cancels its context; an in-flight fetch can
	// still finish and invoke the callback, so cancellation alone is not a
	// sufficient ordering guarantee during hot apply.
	providerApplyMu    sync.Mutex
	providerGeneration uint64
}

var _ boxmgr.ReloadLifecycleListener = (*RoutingController)(nil)
var _ boxmgr.ConfigUpdateListener = (*RoutingController)(nil)

type routingBoxManager interface {
	dispatch.PoolProvider
	StickySnapshot() (pool.StickySnapshot, bool)
	SetLongLivedThresholds(minUptime time.Duration, minRate float64)
}

// NewRoutingController creates a controller bound to a context and box manager.
func NewRoutingController(ctx context.Context, boxMgr *boxmgr.Manager) *RoutingController {
	return &RoutingController{ctx: ctx, boxMgr: boxMgr, managerRef: boxMgr}
}

// Start builds and starts the dispatch entry if routing is enabled in cfg. It is
// a no-op (returning cleanly) when routing is disabled. Safe to call once at
// startup.
func (rc *RoutingController) Start(cfg *config.Config) error {
	state := boxmgr.ReloadState{Config: cloneConfigSnapshot(cfg)}
	rc.operationMu.Lock()
	defer rc.operationMu.Unlock()
	return rc.startStateOperationLocked(state)
}

// startStateOperationLocked initializes one applied routing state. Caller holds
// operationMu; threshold propagation intentionally happens outside rc.mu.
func (rc *RoutingController) startStateOperationLocked(state boxmgr.ReloadState) error {
	rc.mu.Lock()
	rc.stopRuntimeLocked()
	if routingEnabled(state) {
		if err := rc.startLocked(state.Config); err != nil {
			rc.mu.Unlock()
			return err
		}
	}
	rc.setAppliedStateLocked(state)
	rc.hasPending = false
	rc.pendingRuntimeMutated = false
	rc.mu.Unlock()
	rc.applyThresholds(state.Config)
	return nil
}

// startLocked builds the engine + dispatcher and starts serving. Caller holds
// rc.mu. Any setup failure leaves no routing resources behind.
func (rc *RoutingController) startLocked(cfg *config.Config) error {
	if err := validateRuleProviders(cfg.Routing.RuleProviders); err != nil {
		return err
	}
	engine := buildEngine(cfg, rc.openGeoLocked(cfg))
	rc.engine = engine

	if err := rc.startProviderLocked(cfg, engine); err != nil {
		rc.stopRuntimeLocked()
		return err
	}

	listen := cfg.DispatchListen()
	srv := dispatch.NewServer(dispatch.Config{
		Listen:          listen,
		Username:        cfg.Listener.Username,
		Password:        cfg.Listener.Password,
		DefaultStrategy: pool.NormalizeStrategy(cfg.Routing.DefaultStrategy),
	}, rc.boxMgr, engine, dispatchLogger{})

	if err := srv.Start(rc.ctx); err != nil {
		rc.stopRuntimeLocked()
		return fmt.Errorf("start smart dispatch entry on %s: %w", listen, err)
	}
	rc.server = srv
	rc.running = true
	log.Printf("✅ smart dispatch entry active on %s (default strategy: %s)", listen, cfg.Routing.DefaultStrategy)
	return nil
}

// ApplyHot applies rule/provider/final-policy/default-strategy changes to the
// running engine and dispatcher without restarting anything. It returns false
// when the controller is not currently running (nothing to hot-apply); callers
// then rely on the reload path. The provided cfg becomes the controller's new
// source of truth for subsequent status/snapshot reads.
func (rc *RoutingController) ApplyHot(cfg *config.Config) bool {
	cfg = cloneConfigSnapshot(cfg)
	rc.operationMu.Lock()
	defer rc.operationMu.Unlock()

	rc.mu.Lock()
	if cfg == nil {
		rc.mu.Unlock()
		return false
	}

	target := boxmgr.ReloadState{Config: cfg}
	if rc.hasLastApplied {
		target.Idle = rc.lastApplied.Idle
		if routingTopologyFor(rc.lastApplied) != routingTopologyFor(target) {
			rc.mu.Unlock()
			return false
		}
	}

	if routingEnabled(target) {
		if !rc.running || rc.server == nil || rc.engine == nil {
			rc.mu.Unlock()
			return false
		}
		if err := rc.applyRuntimeConfigLocked(rc.cfg, cfg); err != nil {
			log.Printf("⚠️ routing hot apply rejected: %v", err)
			rc.mu.Unlock()
			return false
		}
	} else if rc.running || rc.server != nil {
		rc.mu.Unlock()
		return false
	}

	ruleCount := 0
	if rc.engine != nil {
		ruleCount = rc.engine.RuleCount()
	}
	rc.setAppliedStateLocked(target)
	rc.hasPending = false
	rc.pendingRuntimeMutated = false
	rc.mu.Unlock()

	rc.applyThresholds(cfg)

	if routingEnabled(target) {
		log.Printf("🧭 routing config hot-reloaded (%d active rules, final=%s, strategy=%s)",
			ruleCount, cfg.Routing.FinalPolicy, cfg.Routing.DefaultStrategy)
	}
	return true
}

// Stop tears down the dispatcher and releases resources. Safe to call multiple
// times.
func (rc *RoutingController) Stop() {
	rc.operationMu.Lock()
	defer rc.operationMu.Unlock()

	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.stopRuntimeLocked()
	rc.hasPending = false
	rc.pendingRuntimeMutated = false
}

// PrepareReload stops the dispatcher before the old box releases ports when
// the effective routing topology changes. Source-only and rule-only reloads
// leave the listener running.
func (rc *RoutingController) PrepareReload(_ context.Context, from, to boxmgr.ReloadState) error {
	from = cloneRoutingState(from)
	to = cloneRoutingState(to)
	rc.operationMu.Lock()
	defer rc.operationMu.Unlock()

	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.hasLastApplied {
		from = cloneRoutingState(rc.lastApplied)
	}
	rc.pendingFrom = cloneRoutingState(from)
	rc.hasPending = true
	rc.pendingRuntimeMutated = false
	if routingTopologyFor(from) != routingTopologyFor(to) {
		rc.pendingRuntimeMutated = true
		rc.stopRuntimeLocked()
	}
	return nil
}

// CompleteReload applies the candidate state after boxmgr has published the
// new box, so proxy requests always resolve the live candidate PoolOutbound.
func (rc *RoutingController) CompleteReload(_ context.Context, from, to boxmgr.ReloadState) error {
	from = cloneRoutingState(from)
	to = cloneRoutingState(to)
	rc.operationMu.Lock()
	defer rc.operationMu.Unlock()

	rc.mu.Lock()
	if rc.hasPending {
		from = cloneRoutingState(rc.pendingFrom)
	} else if rc.hasLastApplied {
		from = cloneRoutingState(rc.lastApplied)
	}

	topologyChanged := routingTopologyFor(from) != routingTopologyFor(to)
	if !routingEnabled(to) {
		if rc.running || rc.server != nil || rc.engine != nil {
			rc.pendingRuntimeMutated = true
		}
		rc.stopRuntimeLocked()
	} else if topologyChanged || !rc.running || rc.server == nil || rc.engine == nil {
		rc.pendingRuntimeMutated = true
		rc.stopRuntimeLocked()
		if err := rc.startLocked(to.Config); err != nil {
			rc.mu.Unlock()
			return err
		}
	} else if routingRuntimeChanged(from.Config, to.Config) {
		rc.pendingRuntimeMutated = true
		if err := rc.applyRuntimeConfigLocked(from.Config, to.Config); err != nil {
			rc.mu.Unlock()
			return err
		}
	}

	rc.setAppliedStateLocked(to)
	rc.mu.Unlock()
	rc.applyThresholds(to.Config)
	return nil
}

// FailedReload restores the last-good dispatcher after boxmgr has restored the
// old box. When box restoration failed, routing remains disabled so it cannot
// expose an entry backed by a missing or rejected pool.
func (rc *RoutingController) FailedReload(_ context.Context, from, _ boxmgr.ReloadState, cause error, restored bool) error {
	from = cloneRoutingState(from)
	rc.operationMu.Lock()
	defer rc.operationMu.Unlock()

	rc.mu.Lock()
	if rc.hasPending {
		from = cloneRoutingState(rc.pendingFrom)
	}
	if restored && rc.hasPending && !rc.pendingRuntimeMutated {
		rc.setAppliedStateLocked(from)
		rc.hasPending = false
		rc.pendingRuntimeMutated = false
		rc.mu.Unlock()
		rc.applyThresholds(from.Config)
		return nil
	}
	rc.stopRuntimeLocked()
	if !restored {
		rc.setAppliedStateLocked(from)
		rc.hasPending = false
		rc.pendingRuntimeMutated = false
		rc.mu.Unlock()
		log.Printf("⚠️ routing remains disabled after failed box reload: %v", cause)
		return nil
	}

	if routingEnabled(from) {
		if err := rc.startLocked(from.Config); err != nil {
			rc.stopRuntimeLocked()
			rc.setAppliedStateLocked(from)
			rc.hasPending = false
			rc.pendingRuntimeMutated = false
			rc.mu.Unlock()
			rc.applyThresholds(from.Config)
			log.Printf("⚠️ failed to restore smart routing after box rollback: %v", err)
			return fmt.Errorf("restore smart routing after box rollback: %w", err)
		}
	}
	rc.setAppliedStateLocked(from)
	rc.hasPending = false
	rc.pendingRuntimeMutated = false
	rc.mu.Unlock()
	rc.applyThresholds(from.Config)
	return nil
}

// OnConfigUpdate reattaches the routing API when boxmgr replaces the monitor
// server because its enablement or listen address changed during reload.
func (rc *RoutingController) OnConfigUpdate(_ *config.Config) {
	if rc.managerRef == nil {
		return
	}
	if server := rc.managerRef.MonitorServer(); server != nil {
		server.SetRoutingController(rc)
	}
}

func cloneConfigSnapshot(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	cfg.RLock()
	defer cfg.RUnlock()
	return cfg.Clone()
}

func cloneRoutingState(state boxmgr.ReloadState) boxmgr.ReloadState {
	return boxmgr.ReloadState{
		Config: cloneConfigSnapshot(state.Config),
		Idle:   state.Idle,
	}
}

type routingTopology struct {
	enabled  bool
	listen   string
	username string
	password string
}

func routingEnabled(state boxmgr.ReloadState) bool {
	return state.Config != nil && state.Config.Routing.Enabled && !state.Idle
}

func routingTopologyFor(state boxmgr.ReloadState) routingTopology {
	topology := routingTopology{enabled: routingEnabled(state)}
	if !topology.enabled {
		return topology
	}
	topology.listen = state.Config.DispatchListen()
	topology.username = state.Config.Listener.Username
	topology.password = state.Config.Listener.Password
	return topology
}

func routingRuntimeChanged(from, to *config.Config) bool {
	if from == nil || to == nil {
		return from != to
	}
	return !reflect.DeepEqual(from.Routing, to.Routing) || routingGeoChanged(from, to)
}

func routingGeoChanged(from, to *config.Config) bool {
	if from == nil || to == nil {
		return from != to
	}
	if from.GeoIP.Enabled != to.GeoIP.Enabled {
		return true
	}
	if !from.GeoIP.Enabled {
		return false
	}
	return strings.TrimSpace(from.GeoIP.DatabasePath) != strings.TrimSpace(to.GeoIP.DatabasePath)
}

func routingEngineInputsChanged(from, to *config.Config) bool {
	if from == nil || to == nil {
		return from != to
	}
	if from.RoutingUseDefaultRules() != to.RoutingUseDefaultRules() ||
		routerule.NormalizePolicy(from.Routing.FinalPolicy) != routerule.NormalizePolicy(to.Routing.FinalPolicy) ||
		!reflect.DeepEqual(from.Routing.Rules, to.Routing.Rules) ||
		!reflect.DeepEqual(from.Routing.RuleProviders, to.Routing.RuleProviders) {
		return true
	}
	return false
}

func validateRuleProviders(providers []config.RuleProvider) error {
	for idx, provider := range providers {
		rawURL := strings.TrimSpace(provider.URL)
		parsed, err := url.Parse(rawURL)
		scheme := ""
		host := ""
		if parsed != nil {
			scheme = strings.ToLower(parsed.Scheme)
			host = parsed.Host
		}
		if err != nil || host == "" || (scheme != "http" && scheme != "https") {
			if err != nil {
				return fmt.Errorf("routing rule provider %d has invalid URL %q: %w", idx+1, provider.URL, err)
			}
			return fmt.Errorf("routing rule provider %d has invalid URL %q", idx+1, provider.URL)
		}
	}
	return nil
}

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
		newEngine := buildEngine(cfg, countryLookup)
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
		applyEngineConfig(rc.engine, effectiveRules(cfg), cfg.Routing.FinalPolicy)
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
	staticRules := append([]string(nil), cfg.Routing.Rules...)
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
	if !cfg.GeoIP.Enabled || strings.TrimSpace(cfg.GeoIP.DatabasePath) == "" {
		return nil, nil
	}
	gl, err := geoip.New(cfg.GeoIP.DatabasePath)
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

func applyEngineConfig(engine *routerule.Engine, rules []string, finalPolicy string) {
	engine.SetRulesAndFinal(rules, routerule.NormalizePolicy(finalPolicy))
}

// buildEngine constructs a rule engine from cfg with an optional country lookup.
func buildEngine(cfg *config.Config, countryLookup routerule.CountryLookup) *routerule.Engine {
	engine := routerule.New(nil, routerule.NormalizePolicy(cfg.Routing.FinalPolicy), countryLookup)
	applyEngineConfig(engine, effectiveRules(cfg), cfg.Routing.FinalPolicy)
	return engine
}
