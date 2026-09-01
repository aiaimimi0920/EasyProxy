package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/dispatch"
	"easy_proxies/internal/geoip"
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
	profiles   ProfileRuntime

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
	reloadIntents         int
	stopped               bool

	// providerApplyMu serializes generation checks with engine updates. A
	// ProviderManager.Stop only cancels its context; an in-flight fetch can
	// still finish and invoke the callback, so cancellation alone is not a
	// sufficient ordering guarantee during hot apply.
	providerApplyMu    sync.Mutex
	providerGeneration uint64
}

var errRoutingControllerStopped = errors.New("routing controller is stopped")

var _ boxmgr.ReloadLifecycleListener = (*RoutingController)(nil)
var _ boxmgr.ReloadIntentListener = (*RoutingController)(nil)
var _ boxmgr.ConfigUpdateListener = (*RoutingController)(nil)

type routingBoxManager interface {
	dispatch.PoolProvider
	StickySnapshot() (pool.StickySnapshot, bool)
	SetLongLivedThresholds(minUptime time.Duration, minRate float64)
	RecordAppliedConfig(cfg *config.Config)
}

type RoutingControllerOption func(*RoutingController)

type ProfileRuntime interface {
	dispatch.ProfileResolver
	PrepareConfig(*config.Config) error
}

func WithProfileRuntime(runtime ProfileRuntime) RoutingControllerOption {
	return func(rc *RoutingController) { rc.profiles = runtime }
}

// NewRoutingController creates a controller bound to a context and box manager.
func NewRoutingController(ctx context.Context, boxMgr routingBoxManager, opts ...RoutingControllerOption) *RoutingController {
	rc := &RoutingController{ctx: ctx, boxMgr: boxMgr}
	if concrete, ok := boxMgr.(*boxmgr.Manager); ok {
		rc.managerRef = concrete
	}
	for _, opt := range opts {
		if opt != nil {
			opt(rc)
		}
	}
	return rc
}

// Start builds and starts the dispatch entry if routing is enabled in cfg. It is
// a no-op (returning cleanly) when routing is disabled. Safe to call once at
// startup.
func (rc *RoutingController) Start(cfg *config.Config) error {
	state := boxmgr.ReloadState{Config: cloneConfigSnapshot(cfg)}
	return rc.StartState(state)
}

func (rc *RoutingController) StartState(state boxmgr.ReloadState) error {
	rc.operationMu.Lock()
	defer rc.operationMu.Unlock()
	return rc.startStateOperationLocked(state)
}

func (rc *RoutingController) Running() bool {
	if rc == nil {
		return false
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.running
}

// TransparentRouter returns a transparent data-plane router backed by the
// controller's current engine and pool. It returns nil while smart routing is
// disabled; callers may then construct a pool-only router for fail-open use.
func (rc *RoutingController) TransparentRouter(noAvailableProxyPolicy routerule.Policy) *dispatch.TransparentRouter {
	if rc == nil {
		return nil
	}
	rc.mu.Lock()
	server := rc.server
	rc.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.TransparentRouter(noAvailableProxyPolicy)
}

// startStateOperationLocked initializes one applied routing state. Caller holds
// operationMu; threshold propagation intentionally happens outside rc.mu.
func (rc *RoutingController) startStateOperationLocked(state boxmgr.ReloadState) error {
	rc.mu.Lock()
	if rc.stopped {
		rc.mu.Unlock()
		return errRoutingControllerStopped
	}
	rc.stopRuntimeLocked()
	if routingEnabled(state) {
		if err := rc.startLocked(state.Config); err != nil {
			rc.mu.Unlock()
			return err
		}
	} else {
		// GeoIP database maintenance is independent of whether the smart
		// dispatcher currently owns a listener.
		rc.openGeoLocked(state.Config)
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
	if cfg.LocalServer.Enabled {
		if rc.profiles == nil {
			return errors.New("Local Server profile runtime is unavailable")
		}
		listen := cfg.DispatchListen()
		username := cfg.LocalServer.Auth.Username
		password := cfg.LocalServer.Auth.Password
		if username == "" {
			username = cfg.Listener.Username
		}
		if password == "" {
			password = cfg.Listener.Password
		}
		srv := dispatch.NewServer(dispatch.Config{
			Listen:          listen,
			Username:        username,
			Password:        password,
			DefaultStrategy: pool.NormalizeStrategy(cfg.Routing.DefaultStrategy),
			LocalServer:     true,
			Profiles:        rc.profiles,
		}, rc.boxMgr, nil, dispatchLogger{})
		if err := srv.Start(rc.ctx); err != nil {
			return fmt.Errorf("start Local Server dispatch entry on %s: %w", listen, err)
		}
		rc.server = srv
		rc.openGeoLocked(cfg)
		rc.running = true
		return nil
	}
	if err := validateRuleProviders(cfg.Routing.RuleProviders); err != nil {
		return err
	}
	engine, err := buildEngine(cfg, rc.openGeoLocked(cfg))
	if err != nil {
		rc.closeGeoLocked()
		return err
	}
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
	if rc.stopped {
		rc.mu.Unlock()
		return false
	}
	if rc.reloadIntents > 0 || rc.hasPending {
		// A reload owns the target runtime until CompleteReload/FailedReload.
		// Persisted edits can request a follow-up reload instead of racing the
		// transaction and being overwritten by its previously captured target.
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
		if !routingHotApplyCompatible(rc.lastApplied.Config, cfg) {
			rc.mu.Unlock()
			return false
		}
	}

	if routingEnabled(target) {
		if !rc.running || rc.server == nil {
			rc.mu.Unlock()
			return false
		}
		if !cfg.LocalServer.Enabled {
			if rc.engine == nil {
				rc.mu.Unlock()
				return false
			}
			if err := rc.applyRuntimeConfigLocked(rc.cfg, cfg); err != nil {
				log.Printf("⚠️ routing hot apply rejected: %v", err)
				rc.mu.Unlock()
				return false
			}
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
	rc.recordAppliedConfig(cfg)

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
	// Keep pre-stop intents balanced with their eventual End calls. The stopped
	// gate rejects new intents, so an old token cannot consume a newer guard.
	rc.stopped = true
}

// BeginReloadIntent blocks hot updates before a reload caller captures its
// target. Intents are nestable because TriggerReload/refresh callers can hold
// an outer intent while ReloadWithPortMap owns the actual transaction.
