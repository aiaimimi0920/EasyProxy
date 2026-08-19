package app

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/dispatch"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/routerule"

	"github.com/sagernet/sing-box/adapter"
)

type thresholdUpdate struct {
	uptime time.Duration
	rate   float64
}

type blockingRoutingBoxManager struct {
	calls        chan thresholdUpdate
	releaseFirst chan struct{}

	mu      sync.Mutex
	current thresholdUpdate
}

type recordingAppliedConfigBoxManager struct {
	mu       sync.Mutex
	recorded *config.Config
}

type routingBoxManagerStub struct{}

func (*routingBoxManagerStub) PoolOutbound() (adapter.Outbound, bool) { return nil, false }
func (*routingBoxManagerStub) StickySnapshot() (pool.StickySnapshot, bool) {
	return pool.StickySnapshot{}, false
}
func (*routingBoxManagerStub) SetLongLivedThresholds(time.Duration, float64) {}
func (*routingBoxManagerStub) RecordAppliedConfig(*config.Config)            {}

type profileRuntimeStub struct {
	shared *profile.CompiledProfile
}

func (r *profileRuntimeStub) Credentials() profile.CredentialSnapshot {
	return profile.CredentialSnapshot{Username: "easyproxy", Password: "secret", Generation: 1}
}
func (r *profileRuntimeStub) Resolve(profile.RequestIdentity) profile.Resolution {
	return profile.Resolution{
		Source:          profile.IdentitySharedFallback,
		ProfileID:       r.shared.ID(),
		ProfileRevision: r.shared.Revision(),
		Profile:         r.shared,
	}
}
func (*profileRuntimeStub) Observe(profile.Resolution, netip.Addr, time.Time) {}
func (*profileRuntimeStub) PrepareConfig(*config.Config) error                { return nil }

func disabledSharedProfile(t *testing.T) *profile.CompiledProfile {
	t.Helper()
	compiled, err := profile.Compile("shared", profile.KindShared, 1, profile.Definition{
		SchemaVersion: 1,
		Enabled:       false,
		FinalPolicy:   "DIRECT",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func enabledSharedProfile(t *testing.T) *profile.CompiledProfile {
	t.Helper()
	compiled, err := profile.Compile("shared", profile.KindShared, 1, profile.Definition{
		SchemaVersion: 1,
		Enabled:       true,
		FinalPolicy:   "PROXY",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func localServerConfigForTest(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Mode:     "pool",
		Listener: config.ListenerConfig{Address: "127.0.0.1", Port: 22323, Protocol: config.InboundProtocolMixed},
		Routing:  config.RoutingConfig{Enabled: false, FinalPolicy: "PROXY"},
		LocalServer: config.LocalServerConfig{
			Enabled:              true,
			Listen:               freeRoutingListen(t),
			Auth:                 config.LocalServerAuthConfig{Username: "easyproxy", Password: "secret"},
			SharedRevision:       1,
			CredentialGeneration: 1,
		},
	}
}

func (m *recordingAppliedConfigBoxManager) PoolOutbound() (adapter.Outbound, bool) {
	return nil, false
}

func (m *recordingAppliedConfigBoxManager) StickySnapshot() (pool.StickySnapshot, bool) {
	return pool.StickySnapshot{}, false
}

func (m *recordingAppliedConfigBoxManager) SetLongLivedThresholds(time.Duration, float64) {}

func (m *recordingAppliedConfigBoxManager) RecordAppliedConfig(cfg *config.Config) {
	m.mu.Lock()
	m.recorded = cfg
	m.mu.Unlock()
}

func (m *recordingAppliedConfigBoxManager) recordedConfig() *config.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recorded
}

func newBlockingRoutingBoxManager() *blockingRoutingBoxManager {
	return &blockingRoutingBoxManager{
		calls:        make(chan thresholdUpdate, 2),
		releaseFirst: make(chan struct{}),
	}
}

func (m *blockingRoutingBoxManager) PoolOutbound() (adapter.Outbound, bool) {
	return nil, false
}

func (m *blockingRoutingBoxManager) StickySnapshot() (pool.StickySnapshot, bool) {
	return pool.StickySnapshot{}, false
}

func (m *blockingRoutingBoxManager) SetLongLivedThresholds(uptime time.Duration, rate float64) {
	update := thresholdUpdate{uptime: uptime, rate: rate}
	m.calls <- update
	if uptime == time.Hour {
		<-m.releaseFirst
	}
	m.mu.Lock()
	m.current = update
	m.mu.Unlock()
}

func (m *blockingRoutingBoxManager) RecordAppliedConfig(*config.Config) {}

func (m *blockingRoutingBoxManager) currentThresholds() thresholdUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

func TestBuildEngineFinalPolicy(t *testing.T) {
	cfg := &config.Config{}
	cfg.Routing.FinalPolicy = string(routerule.PolicyDirect)

	engine := buildEngine(cfg, nil)

	if engine.RuleCount() == 0 {
		t.Fatal("expected built-in default rules to be installed")
	}
	if got := engine.Final(); got != routerule.PolicyDirect {
		t.Fatalf("configured final policy should override legacy FINAL rules: got %s, want %s", got, routerule.PolicyDirect)
	}
}

func TestRoutingGeoChangedIncludesAutoUpdateLifecycle(t *testing.T) {
	base := &config.Config{GeoIP: config.GeoIPConfig{
		Enabled:            true,
		DatabasePath:       "/tmp/GeoLite2-Country.mmdb",
		AutoUpdateEnabled:  true,
		AutoUpdateInterval: 24 * time.Hour,
	}}

	unchanged := cloneConfigSnapshot(base)
	if routingGeoChanged(base, unchanged) {
		t.Fatal("equivalent GeoIP lifecycle settings reported as changed")
	}
	disabledUpdate := cloneConfigSnapshot(base)
	disabledUpdate.GeoIP.AutoUpdateEnabled = false
	if !routingGeoChanged(base, disabledUpdate) {
		t.Fatal("auto-update enablement change was ignored")
	}
	changedInterval := cloneConfigSnapshot(base)
	changedInterval.GeoIP.AutoUpdateInterval = 12 * time.Hour
	if !routingGeoChanged(base, changedInterval) {
		t.Fatal("auto-update interval change was ignored")
	}
}

func TestLocalServerStartsWhileSharedDisabledAndBoxIdle(t *testing.T) {
	cfg := localServerConfigForTest(t)
	box := &routingBoxManagerStub{}
	profiles := &profileRuntimeStub{shared: disabledSharedProfile(t)}
	controller := NewRoutingController(context.Background(), box, WithProfileRuntime(profiles))
	defer controller.Stop()
	if err := controller.StartState(boxmgr.ReloadState{Config: cfg, Idle: true}); err != nil {
		t.Fatal(err)
	}
	if !controller.Running() {
		t.Fatal("Local Server dispatcher did not start")
	}
	status := controller.RoutingStatus()
	if status.Enabled || !status.DispatcherReady || status.SharedEnabled {
		t.Fatalf("Local Server status = %+v", status)
	}
}

func TestLocalServerStaysRunningAcrossIdleTransitions(t *testing.T) {
	cfg := localServerConfigForTest(t)
	cfg.Routing.Enabled = true
	controller := NewRoutingController(context.Background(), &routingBoxManagerStub{}, WithProfileRuntime(&profileRuntimeStub{shared: enabledSharedProfile(t)}))
	defer controller.Stop()
	if err := controller.StartState(boxmgr.ReloadState{Config: cfg, Idle: false}); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	initial := controller.server
	controller.mu.Unlock()

	idleCfg := cloneConfigSnapshot(cfg)
	if err := controller.PrepareReload(context.Background(), boxmgr.ReloadState{Config: cfg}, boxmgr.ReloadState{Config: idleCfg, Idle: true}); err != nil {
		t.Fatal(err)
	}
	if err := controller.CompleteReload(context.Background(), boxmgr.ReloadState{Config: cfg}, boxmgr.ReloadState{Config: idleCfg, Idle: true}); err != nil {
		t.Fatal(err)
	}
	if !controller.Running() {
		t.Fatal("Local Server stopped during running-to-idle transition")
	}

	if err := controller.PrepareReload(context.Background(), boxmgr.ReloadState{Config: idleCfg, Idle: true}, boxmgr.ReloadState{Config: cfg, Idle: false}); err != nil {
		t.Fatal(err)
	}
	if err := controller.CompleteReload(context.Background(), boxmgr.ReloadState{Config: idleCfg, Idle: true}, boxmgr.ReloadState{Config: cfg, Idle: false}); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	final := controller.server
	controller.mu.Unlock()
	if final != initial {
		t.Fatal("Local Server listener was replaced across idle transitions")
	}
}

func TestLocalServerHotAppliesCredentialAndSessionChangesWithoutRestart(t *testing.T) {
	cfg := localServerConfigForTest(t)
	cfg.Routing.Enabled = true
	controller := NewRoutingController(context.Background(), &routingBoxManagerStub{}, WithProfileRuntime(&profileRuntimeStub{shared: enabledSharedProfile(t)}))
	defer controller.Stop()
	if err := controller.Start(cfg); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	initial := controller.server
	controller.mu.Unlock()

	hot := cloneConfigSnapshot(cfg)
	hot.LocalServer.Auth.Password = "rotated-secret"
	hot.Listener.Password = "rotated-secret"
	hot.Management.Password = "rotated-secret"
	hot.Routing.Session.TTL = 5 * time.Minute
	if !controller.ApplyHot(hot) {
		t.Fatal("Local Server credential/session change was not hot-applied")
	}
	controller.mu.Lock()
	final := controller.server
	controller.mu.Unlock()
	if final != initial {
		t.Fatal("Local Server hot apply replaced the listener")
	}
}

func TestLocalServerReloadChangesListenTransactionally(t *testing.T) {
	cfg := localServerConfigForTest(t)
	cfg.Routing.Enabled = true
	controller := NewRoutingController(context.Background(), &routingBoxManagerStub{}, WithProfileRuntime(&profileRuntimeStub{shared: enabledSharedProfile(t)}))
	defer controller.Stop()
	if err := controller.Start(cfg); err != nil {
		t.Fatal(err)
	}
	newCfg := cloneConfigSnapshot(cfg)
	newCfg.LocalServer.Listen = freeRoutingListen(t)
	from := boxmgr.ReloadState{Config: cfg}
	to := boxmgr.ReloadState{Config: newCfg}
	if err := controller.PrepareReload(context.Background(), from, to); err != nil {
		t.Fatal(err)
	}
	if controller.Running() {
		t.Fatal("Local Server listener remained active during listen transition prepare")
	}
	if err := controller.CompleteReload(context.Background(), from, to); err != nil {
		t.Fatal(err)
	}
	if !controller.Running() || controller.RoutingStatus().Listen != newCfg.LocalServer.Listen {
		t.Fatalf("Local Server listen transition status = %+v", controller.RoutingStatus())
	}
}

func TestLocalServerRollbackFailureRestoresDirectDispatcher(t *testing.T) {
	cfg := localServerConfigForTest(t)
	cfg.Routing.Enabled = true
	controller := NewRoutingController(context.Background(), &routingBoxManagerStub{}, WithProfileRuntime(&profileRuntimeStub{shared: enabledSharedProfile(t)}))
	defer controller.Stop()
	if err := controller.Start(cfg); err != nil {
		t.Fatal(err)
	}
	newCfg := cloneConfigSnapshot(cfg)
	newCfg.LocalServer.Listen = freeRoutingListen(t)
	from := boxmgr.ReloadState{Config: cfg}
	to := boxmgr.ReloadState{Config: newCfg}
	if err := controller.PrepareReload(context.Background(), from, to); err != nil {
		t.Fatal(err)
	}
	if err := controller.FailedReload(context.Background(), from, to, errors.New("pool rollback failed"), false); err != nil {
		t.Fatal(err)
	}
	if !controller.Running() || controller.RoutingStatus().Listen != cfg.LocalServer.Listen {
		t.Fatalf("Local Server rollback status = %+v", controller.RoutingStatus())
	}
}

func TestApplyEffectiveRulesFinalPolicy(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("provider.example\n"))
	}))
	defer provider.Close()

	useDefaults := false
	cfg := &config.Config{}
	cfg.Routing.FinalPolicy = string(routerule.PolicyDirect)
	cfg.Routing.UseDefaultRules = &useDefaults
	cfg.Routing.Rules = []string{"FINAL,PROXY"}
	cfg.Routing.RuleProviders = []config.RuleProvider{{
		URL:      provider.URL,
		Policy:   string(routerule.PolicyProxy),
		Behavior: "domain",
	}}

	engine := routerule.New(nil, routerule.PolicyDirect, nil)
	rc := &RoutingController{ctx: context.Background()}
	rc.startProviderLocked(cfg, engine)
	defer rc.stopProviderLocked()

	if got := engine.RuleCount(); got != 1 {
		t.Fatalf("expected provider callback to install one rule, got %d", got)
	}
	if got := engine.Final(); got != routerule.PolicyDirect {
		t.Fatalf("configured final policy should survive provider rule replacement: got %s, want %s", got, routerule.PolicyDirect)
	}
}

func TestApplyProviderRulesRejectsStaleGeneration(t *testing.T) {
	rc := &RoutingController{}
	engine := routerule.New(nil, routerule.PolicyDirect, nil)

	rc.providerApplyMu.Lock()
	rc.providerGeneration = 7
	rc.providerApplyMu.Unlock()

	if !rc.applyProviderRules(7, engine, []string{"DOMAIN-SUFFIX,new.example,PROXY"}, string(routerule.PolicyDirect)) {
		t.Fatal("current provider generation should apply")
	}
	rc.providerApplyMu.Lock()
	rc.providerGeneration++
	rc.providerApplyMu.Unlock()

	if rc.applyProviderRules(7, engine, []string{"DOMAIN-SUFFIX,stale.example,PROXY"}, string(routerule.PolicyProxy)) {
		t.Fatal("stale provider generation should be rejected")
	}
	if got := engine.Final(); got != routerule.PolicyDirect {
		t.Fatalf("stale callback changed final policy: got %s", got)
	}
	if got := engine.Match("stale.example"); got != routerule.PolicyDirect {
		t.Fatalf("stale callback changed active rules: got %s", got)
	}
}

func TestApplyHotPublishesNewConfigAfterOldProviderCallbackFinishes(t *testing.T) {
	engine := routerule.New(nil, routerule.PolicyProxy, nil)
	rc := &RoutingController{
		ctx:                context.Background(),
		engine:             engine,
		server:             dispatch.NewServer(dispatch.Config{}, nil, engine, dispatchLogger{}),
		running:            true,
		providerGeneration: 1,
	}

	// Keep the old callback inside applyProviderRules long enough to force the
	// hot-apply interleaving that previously allowed stale rules to win last.
	staleRules := make([]string, 200_000)
	for i := range staleRules {
		staleRules[i] = "DOMAIN-SUFFIX,stale.example,PROXY"
	}
	staleDone := make(chan struct{})
	go func() {
		defer close(staleDone)
		rc.applyProviderRules(1, engine, staleRules, string(routerule.PolicyProxy))
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if !rc.providerApplyMu.TryLock() {
			break
		}
		rc.providerApplyMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("old provider callback did not acquire the apply lock")
		}
		runtime.Gosched()
	}

	useDefaults := false
	newCfg := &config.Config{}
	newCfg.Routing.Enabled = true
	newCfg.Routing.UseDefaultRules = &useDefaults
	newCfg.Routing.FinalPolicy = string(routerule.PolicyDirect)
	if !rc.ApplyHot(newCfg) {
		t.Fatal("expected running controller to hot-apply the new config")
	}
	<-staleDone

	if got := engine.Final(); got != routerule.PolicyDirect {
		t.Fatalf("old provider callback won after hot apply: got final %s, want %s", got, routerule.PolicyDirect)
	}
	if got := engine.Match("stale.example"); got != routerule.PolicyDirect {
		t.Fatalf("old provider rules won after hot apply: got %s, want %s", got, routerule.PolicyDirect)
	}
}

func TestApplyHotUpdatesLongLivedThresholdsWithoutReload(t *testing.T) {
	managementDisabled := false
	boxCfg := &config.Config{}
	boxCfg.Management.Enabled = &managementDisabled
	boxManager := boxmgr.New(boxCfg, monitor.Config{})
	if err := boxManager.PrepareMonitor(context.Background()); err != nil {
		t.Fatalf("PrepareMonitor() error = %v", err)
	}
	defer boxManager.Close()

	engine := routerule.New(nil, routerule.PolicyProxy, nil)
	rc := &RoutingController{
		ctx:     context.Background(),
		boxMgr:  boxManager,
		engine:  engine,
		server:  dispatch.NewServer(dispatch.Config{}, nil, engine, dispatchLogger{}),
		running: true,
	}

	useDefaults := false
	cfg := &config.Config{}
	cfg.Routing.Enabled = true
	cfg.Routing.UseDefaultRules = &useDefaults
	cfg.Routing.FinalPolicy = string(routerule.PolicyProxy)
	cfg.Routing.LongLived.MinUptime = time.Nanosecond
	cfg.Routing.LongLived.MinSuccessRate = 0.8
	if !rc.ApplyHot(cfg) {
		t.Fatal("expected running controller to hot-apply thresholds")
	}

	monitorManager := boxManager.MonitorManager()
	handle := monitorManager.Register(monitor.NodeInfo{Tag: "routing-threshold", Name: "Routing Threshold"})
	handle.MarkInitialCheckDone(true)
	time.Sleep(time.Millisecond)
	if snap := handle.Snapshot(); !snap.LongLived {
		t.Fatalf("expected controller hot apply to update monitor thresholds: %+v", snap)
	}
}

func TestApplyHotRecordsRollbackSnapshotAfterSuccess(t *testing.T) {
	boxManager := &recordingAppliedConfigBoxManager{}
	engine := routerule.New(nil, routerule.PolicyProxy, nil)
	rc := &RoutingController{
		ctx:     context.Background(),
		boxMgr:  boxManager,
		engine:  engine,
		server:  dispatch.NewServer(dispatch.Config{}, boxManager, engine, dispatchLogger{}),
		running: true,
	}

	useDefaults := false
	cfg := &config.Config{
		Mode:      "hybrid",
		MultiPort: config.MultiPortConfig{BasePort: 32100},
		Routing: config.RoutingConfig{
			Enabled:         true,
			UseDefaultRules: &useDefaults,
			FinalPolicy:     string(routerule.PolicyDirect),
			Rules:           []string{"DOMAIN-SUFFIX,hot.example,DIRECT"},
		},
	}
	if !rc.ApplyHot(cfg) {
		t.Fatal("expected hot apply to succeed")
	}

	recorded := boxManager.recordedConfig()
	if recorded == nil {
		t.Fatal("successful hot apply did not refresh the box-manager rollback snapshot")
	}
	if recorded == cfg {
		t.Fatal("controller passed the caller's mutable config as the rollback snapshot")
	}
	if recorded.Mode != cfg.Mode || recorded.MultiPort.BasePort != cfg.MultiPort.BasePort {
		t.Fatalf("recorded topology = %s/%d, want %s/%d", recorded.Mode, recorded.MultiPort.BasePort, cfg.Mode, cfg.MultiPort.BasePort)
	}
	if recorded.Routing.FinalPolicy != string(routerule.PolicyDirect) || len(recorded.Routing.Rules) != 1 {
		t.Fatalf("recorded routing config does not match the hot-applied state: %+v", recorded.Routing)
	}

	cfg.Mode = "mutated"
	cfg.Routing.Rules[0] = "MATCH,PROXY"
	if recorded.Mode != "hybrid" || recorded.Routing.Rules[0] != "DOMAIN-SUFFIX,hot.example,DIRECT" {
		t.Fatalf("recorded config changed with caller mutation: %+v", recorded)
	}
}

func TestApplyHotDefersWhileReloadIsPending(t *testing.T) {
	boxManager := &recordingAppliedConfigBoxManager{}
	engine := routerule.New(nil, routerule.PolicyProxy, nil)
	rc := &RoutingController{
		ctx:     context.Background(),
		boxMgr:  boxManager,
		engine:  engine,
		server:  dispatch.NewServer(dispatch.Config{}, boxManager, engine, dispatchLogger{}),
		running: true,
	}

	from := &config.Config{}
	from.Routing.Enabled = true
	from.Routing.FinalPolicy = string(routerule.PolicyProxy)
	rc.mu.Lock()
	rc.pendingFrom = boxmgr.ReloadState{Config: from}
	rc.hasPending = true
	rc.mu.Unlock()

	hot := &config.Config{}
	hot.Routing.Enabled = true
	hot.Routing.FinalPolicy = string(routerule.PolicyDirect)
	if rc.ApplyHot(hot) {
		t.Fatal("hot apply should be deferred while a reload transaction is pending")
	}
	if got := engine.Final(); got != routerule.PolicyProxy {
		t.Fatalf("deferred hot apply changed the live engine: got %s", got)
	}
}

func TestApplyHotDefersWhileReloadIntentIsActive(t *testing.T) {
	boxManager := &recordingAppliedConfigBoxManager{}
	engine := routerule.New(nil, routerule.PolicyProxy, nil)
	rc := &RoutingController{
		ctx:     context.Background(),
		boxMgr:  boxManager,
		engine:  engine,
		server:  dispatch.NewServer(dispatch.Config{}, boxManager, engine, dispatchLogger{}),
		running: true,
	}

	if err := rc.BeginReloadIntent(context.Background()); err != nil {
		t.Fatalf("BeginReloadIntent() error = %v", err)
	}
	if err := rc.BeginReloadIntent(context.Background()); err != nil {
		t.Fatalf("nested BeginReloadIntent() error = %v", err)
	}
	hot := &config.Config{}
	hot.Routing.Enabled = true
	hot.Routing.FinalPolicy = string(routerule.PolicyDirect)
	if rc.ApplyHot(hot) {
		t.Fatal("hot apply should be deferred before a reload captures its target")
	}
	if got := engine.Final(); got != routerule.PolicyProxy {
		t.Fatalf("reload-intent hot apply changed the live engine: got %s", got)
	}
	if recorded := boxManager.recordedConfig(); recorded != nil {
		t.Fatalf("deferred hot apply advanced rollback state: %+v", recorded)
	}

	rc.EndReloadIntent(context.Background())
	if rc.ApplyHot(hot) {
		t.Fatal("hot apply resumed while a nested reload intent remained active")
	}
	rc.EndReloadIntent(context.Background())
	if !rc.ApplyHot(hot) {
		t.Fatal("hot apply should resume after the reload intent ends")
	}
}

func TestStopPreservesReloadIntentAndRejectsLateCompletion(t *testing.T) {
	boxManager := &recordingAppliedConfigBoxManager{}
	listen := freeRoutingListen(t)
	toCfg := routingLifecycleConfig(t, true, listen, "", "")
	rc := &RoutingController{
		ctx:    context.Background(),
		boxMgr: boxManager,
	}

	if err := rc.BeginReloadIntent(context.Background()); err != nil {
		t.Fatalf("BeginReloadIntent() error = %v", err)
	}
	rc.Stop()

	rc.mu.Lock()
	intents := rc.reloadIntents
	rc.mu.Unlock()
	if intents != 1 {
		t.Fatalf("Stop() cleared an active reload intent: got %d, want 1", intents)
	}
	if err := rc.BeginReloadIntent(context.Background()); !errors.Is(err, errRoutingControllerStopped) {
		t.Fatalf("BeginReloadIntent() after Stop error = %v, want %v", err, errRoutingControllerStopped)
	}
	rc.mu.Lock()
	intents = rc.reloadIntents
	rc.mu.Unlock()
	if intents != 1 {
		t.Fatalf("rejected post-Stop intent changed the active count: got %d, want 1", intents)
	}

	from := boxmgr.ReloadState{Config: &config.Config{}}
	to := boxmgr.ReloadState{Config: toCfg}
	if err := rc.CompleteReload(context.Background(), from, to); err != nil {
		t.Fatalf("late CompleteReload() error = %v", err)
	}
	if status := rc.RoutingStatus(); status.Enabled {
		t.Fatalf("late CompleteReload restarted the stopped dispatcher: %+v", status)
	}
	rc.mu.Lock()
	intents = rc.reloadIntents
	rc.mu.Unlock()
	if intents != 1 {
		t.Fatalf("late CompleteReload changed the active intent count: got %d, want 1", intents)
	}

	rc.EndReloadIntent(context.Background())
	rc.mu.Lock()
	intents = rc.reloadIntents
	rc.mu.Unlock()
	if intents != 0 {
		t.Fatalf("EndReloadIntent() left an intent active: got %d", intents)
	}
}

func TestApplyHotRejectsSessionTTLChange(t *testing.T) {
	boxManager := &recordingAppliedConfigBoxManager{}
	engine := routerule.New(nil, routerule.PolicyProxy, nil)
	from := &config.Config{}
	from.Routing.Enabled = true
	from.Routing.FinalPolicy = string(routerule.PolicyProxy)
	from.Routing.Session.TTL = 10 * time.Minute
	rc := &RoutingController{
		ctx:     context.Background(),
		boxMgr:  boxManager,
		engine:  engine,
		server:  dispatch.NewServer(dispatch.Config{}, boxManager, engine, dispatchLogger{}),
		running: true,
	}
	rc.mu.Lock()
	rc.setAppliedStateLocked(boxmgr.ReloadState{Config: from})
	rc.mu.Unlock()

	hot := cloneConfigSnapshot(from)
	hot.Routing.FinalPolicy = string(routerule.PolicyDirect)
	hot.Routing.Session.TTL = 20 * time.Minute
	if rc.ApplyHot(hot) {
		t.Fatal("session TTL changes require a pool reload")
	}
	if got := engine.Final(); got != routerule.PolicyProxy {
		t.Fatalf("rejected session TTL change mutated the engine: got %s", got)
	}
	if recorded := boxManager.recordedConfig(); recorded != nil {
		t.Fatalf("rejected session TTL change advanced rollback state: %+v", recorded)
	}

	topologyEdit := cloneConfigSnapshot(from)
	topologyEdit.Mode = "hybrid"
	topologyEdit.MultiPort.BasePort++
	if rc.ApplyHot(topologyEdit) {
		t.Fatal("mode/base-port changes require a box reload")
	}
}

func TestCompleteReloadDefersHotApplyUntilConfigCommit(t *testing.T) {
	boxManager := &recordingAppliedConfigBoxManager{}
	engine := routerule.New(nil, routerule.PolicyProxy, nil)
	rc := &RoutingController{
		ctx:     context.Background(),
		boxMgr:  boxManager,
		engine:  engine,
		server:  dispatch.NewServer(dispatch.Config{}, boxManager, engine, dispatchLogger{}),
		running: true,
	}

	from := &config.Config{}
	from.Routing.Enabled = true
	from.Routing.FinalPolicy = string(routerule.PolicyProxy)
	to := &config.Config{}
	to.Routing.Enabled = true
	to.Routing.FinalPolicy = string(routerule.PolicyDirect)
	rc.mu.Lock()
	rc.pendingFrom = boxmgr.ReloadState{Config: from}
	rc.hasPending = true
	rc.mu.Unlock()

	if err := rc.CompleteReload(context.Background(), boxmgr.ReloadState{Config: from}, boxmgr.ReloadState{Config: to}); err != nil {
		t.Fatalf("CompleteReload() error = %v", err)
	}
	rc.mu.Lock()
	pending := rc.hasPending
	rc.mu.Unlock()
	if !pending {
		t.Fatal("CompleteReload cleared pending before the box-manager commit")
	}

	hot := cloneConfigSnapshot(to)
	hot.Routing.FinalPolicy = string(routerule.PolicyProxy)
	if rc.ApplyHot(hot) {
		t.Fatal("hot apply should remain deferred until the config commit notification")
	}

	rc.OnConfigUpdate(to)
	rc.mu.Lock()
	pending = rc.hasPending
	rc.mu.Unlock()
	if pending {
		t.Fatal("config commit notification did not clear the pending transaction")
	}
}

func TestApplyHotUpdatesThresholdsWhileRoutingDisabled(t *testing.T) {
	managementDisabled := false
	boxCfg := &config.Config{}
	boxCfg.Management.Enabled = &managementDisabled
	boxManager := boxmgr.New(boxCfg, monitor.Config{})
	if err := boxManager.PrepareMonitor(context.Background()); err != nil {
		t.Fatalf("PrepareMonitor() error = %v", err)
	}
	defer boxManager.Close()

	rc := &RoutingController{ctx: context.Background(), boxMgr: boxManager}
	cfg := &config.Config{}
	cfg.Routing.Enabled = false
	cfg.Routing.LongLived.MinUptime = time.Nanosecond
	cfg.Routing.LongLived.MinSuccessRate = 0.8
	if !rc.ApplyHot(cfg) {
		t.Fatal("disabled routing should accept a threshold-only hot apply")
	}

	handle := boxManager.MonitorManager().Register(monitor.NodeInfo{Tag: "disabled-threshold"})
	handle.MarkInitialCheckDone(true)
	time.Sleep(time.Millisecond)
	if snap := handle.Snapshot(); !snap.LongLived {
		t.Fatalf("disabled routing threshold update did not reach monitor: %+v", snap)
	}
}

func TestApplyHotUsesImmutableConfigSnapshot(t *testing.T) {
	engine := routerule.New(nil, routerule.PolicyProxy, nil)
	rc := &RoutingController{
		ctx:     context.Background(),
		engine:  engine,
		server:  dispatch.NewServer(dispatch.Config{}, nil, engine, dispatchLogger{}),
		running: true,
	}

	useDefaults := false
	cfg := &config.Config{}
	cfg.Routing.Enabled = true
	cfg.Routing.UseDefaultRules = &useDefaults
	cfg.Routing.FinalPolicy = string(routerule.PolicyDirect)
	cfg.Routing.Rules = []string{"DOMAIN-SUFFIX,snapshot.example,PROXY"}
	if !rc.ApplyHot(cfg) {
		t.Fatal("expected hot apply to succeed")
	}

	cfg.Lock()
	cfg.Routing.FinalPolicy = string(routerule.PolicyProxy)
	cfg.Routing.Rules[0] = "DOMAIN-SUFFIX,mutated.example,PROXY"
	cfg.Unlock()

	rc.mu.Lock()
	stored := rc.cfg
	rc.mu.Unlock()
	if stored == cfg {
		t.Fatal("controller retained the caller's mutable config pointer")
	}
	if stored.Routing.FinalPolicy != string(routerule.PolicyDirect) {
		t.Fatalf("stored final policy changed with caller config: %s", stored.Routing.FinalPolicy)
	}
	if got := stored.Routing.Rules[0]; got != "DOMAIN-SUFFIX,snapshot.example,PROXY" {
		t.Fatalf("stored rules changed with caller config: %q", got)
	}
}

func TestApplyHotSerializesThresholdSideEffects(t *testing.T) {
	boxManager := newBlockingRoutingBoxManager()
	engine := routerule.New(nil, routerule.PolicyProxy, nil)
	rc := &RoutingController{
		ctx:     context.Background(),
		boxMgr:  boxManager,
		engine:  engine,
		server:  dispatch.NewServer(dispatch.Config{}, boxManager, engine, dispatchLogger{}),
		running: true,
	}

	makeConfig := func(uptime time.Duration, final routerule.Policy) *config.Config {
		useDefaults := false
		cfg := &config.Config{}
		cfg.Routing.Enabled = true
		cfg.Routing.UseDefaultRules = &useDefaults
		cfg.Routing.FinalPolicy = string(final)
		cfg.Routing.LongLived.MinUptime = uptime
		cfg.Routing.LongLived.MinSuccessRate = 0.8
		return cfg
	}

	firstDone := make(chan bool, 1)
	go func() {
		firstDone <- rc.ApplyHot(makeConfig(time.Hour, routerule.PolicyProxy))
	}()
	if first := <-boxManager.calls; first.uptime != time.Hour {
		t.Fatalf("first threshold update = %+v", first)
	}

	secondDone := make(chan bool, 1)
	go func() {
		secondDone <- rc.ApplyHot(makeConfig(2*time.Hour, routerule.PolicyDirect))
	}()

	// A serialized implementation will not enter the second threshold update
	// until the first one is released. The old implementation enters here and
	// later lets the first request overwrite the newer threshold.
	select {
	case <-boxManager.calls:
	case <-time.After(20 * time.Millisecond):
	}
	close(boxManager.releaseFirst)
	if !<-firstDone || !<-secondDone {
		t.Fatal("both hot applies should succeed")
	}

	current := boxManager.currentThresholds()
	if current.uptime != 2*time.Hour || current.rate != 0.8 {
		t.Fatalf("threshold side effects completed out of order: %+v", current)
	}
	if got := engine.Final(); got != routerule.PolicyDirect {
		t.Fatalf("engine final = %s, want newest DIRECT config", got)
	}
}

func TestRoutingControllerReloadLifecycleDisabledToEnabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc := &RoutingController{ctx: ctx}
	fromCfg := routingLifecycleConfig(t, false, freeRoutingListen(t), "", "")
	toListen := freeRoutingListen(t)
	toCfg := routingLifecycleConfig(t, true, toListen, "", "")

	if err := rc.Start(fromCfg); err != nil {
		t.Fatalf("Start(disabled) error = %v", err)
	}
	from := boxmgr.ReloadState{Config: fromCfg}
	to := boxmgr.ReloadState{Config: toCfg}
	if err := rc.PrepareReload(ctx, from, to); err != nil {
		t.Fatalf("PrepareReload() error = %v", err)
	}
	if err := rc.CompleteReload(ctx, from, to); err != nil {
		t.Fatalf("CompleteReload() error = %v", err)
	}
	defer rc.Stop()

	waitRoutingReachable(t, toListen, true)
	if status := rc.RoutingStatus(); !status.Enabled || status.Listen != toListen {
		t.Fatalf("unexpected enabled routing status: %+v", status)
	}
}

func TestRoutingControllerReloadLifecycleEnabledToDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listen := freeRoutingListen(t)
	fromCfg := routingLifecycleConfig(t, true, listen, "", "")
	toCfg := routingLifecycleConfig(t, false, listen, "", "")
	rc := &RoutingController{ctx: ctx}
	if err := rc.Start(fromCfg); err != nil {
		t.Fatalf("Start(enabled) error = %v", err)
	}
	defer rc.Stop()

	from := boxmgr.ReloadState{Config: fromCfg}
	to := boxmgr.ReloadState{Config: toCfg}
	if err := rc.PrepareReload(ctx, from, to); err != nil {
		t.Fatalf("PrepareReload() error = %v", err)
	}
	waitRoutingReachable(t, listen, false)
	if err := rc.CompleteReload(ctx, from, to); err != nil {
		t.Fatalf("CompleteReload() error = %v", err)
	}
	if status := rc.RoutingStatus(); status.Enabled {
		t.Fatalf("routing remained enabled after disable reload: %+v", status)
	}
}

func TestRoutingControllerReloadLifecycleKeepsServerForUnchangedTopology(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listen := freeRoutingListen(t)
	fromCfg := routingLifecycleConfig(t, true, listen, "user", "pass")
	fromCfg.Routing.FinalPolicy = string(routerule.PolicyProxy)
	toCfg := routingLifecycleConfig(t, true, listen, "user", "pass")
	toCfg.Routing.FinalPolicy = string(routerule.PolicyDirect)
	toCfg.Routing.Rules = []string{"DOMAIN-SUFFIX,proxy.example,PROXY"}
	rc := &RoutingController{ctx: ctx}
	if err := rc.Start(fromCfg); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer rc.Stop()

	rc.mu.Lock()
	originalServer := rc.server
	rc.mu.Unlock()
	from := boxmgr.ReloadState{Config: fromCfg}
	to := boxmgr.ReloadState{Config: toCfg}
	if err := rc.PrepareReload(ctx, from, to); err != nil {
		t.Fatalf("PrepareReload() error = %v", err)
	}
	waitRoutingReachable(t, listen, true)
	if err := rc.CompleteReload(ctx, from, to); err != nil {
		t.Fatalf("CompleteReload() error = %v", err)
	}

	rc.mu.Lock()
	currentServer := rc.server
	rc.mu.Unlock()
	if currentServer != originalServer {
		t.Fatal("unchanged routing topology restarted the dispatcher")
	}
	if status := rc.RoutingStatus(); status.FinalPolicy != string(routerule.PolicyDirect) || status.RuleCount != 1 {
		t.Fatalf("unchanged-topology reload did not hot apply config: %+v", status)
	}
}

func TestRoutingControllerFailedReloadKeepsUnchangedRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listen := freeRoutingListen(t)
	fromCfg := routingLifecycleConfig(t, true, listen, "user", "pass")
	toCfg := cloneConfigSnapshot(fromCfg)
	toCfg.SourceSync.ManifestURL = "https://source-sync.example/manifest.json"
	rc := &RoutingController{ctx: ctx}
	if err := rc.Start(fromCfg); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer rc.Stop()

	rc.mu.Lock()
	originalServer := rc.server
	originalProvider := rc.provider
	rc.mu.Unlock()
	from := boxmgr.ReloadState{Config: fromCfg}
	to := boxmgr.ReloadState{Config: toCfg}
	if err := rc.PrepareReload(ctx, from, to); err != nil {
		t.Fatalf("PrepareReload() error = %v", err)
	}
	if err := rc.FailedReload(ctx, from, to, errors.New("candidate failed"), true); err != nil {
		t.Fatalf("FailedReload() error = %v", err)
	}

	rc.mu.Lock()
	currentServer := rc.server
	currentProvider := rc.provider
	rc.mu.Unlock()
	if currentServer != originalServer {
		t.Fatal("unchanged-topology failure restarted the dispatcher")
	}
	if currentProvider != originalProvider {
		t.Fatal("unchanged-topology failure restarted the provider manager")
	}
	waitRoutingReachable(t, listen, true)
}

func TestRoutingControllerReloadLifecycleChangesListenSuccessfully(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldListen := freeRoutingListen(t)
	newListen := freeRoutingListen(t)
	fromCfg := routingLifecycleConfig(t, true, oldListen, "", "")
	toCfg := routingLifecycleConfig(t, true, newListen, "", "")
	rc := &RoutingController{ctx: ctx}
	if err := rc.Start(fromCfg); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer rc.Stop()

	from := boxmgr.ReloadState{Config: fromCfg}
	to := boxmgr.ReloadState{Config: toCfg}
	if err := rc.PrepareReload(ctx, from, to); err != nil {
		t.Fatalf("PrepareReload() error = %v", err)
	}
	waitRoutingReachable(t, oldListen, false)
	if err := rc.CompleteReload(ctx, from, to); err != nil {
		t.Fatalf("CompleteReload() error = %v", err)
	}
	waitRoutingReachable(t, newListen, true)
	if status := rc.RoutingStatus(); status.Listen != newListen {
		t.Fatalf("routing listen = %q, want %q", status.Listen, newListen)
	}
}

func TestRoutingControllerReloadLifecycleChangesAuthSuccessfully(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listen := freeRoutingListen(t)
	fromCfg := routingLifecycleConfig(t, true, listen, "old-user", "old-pass")
	toCfg := routingLifecycleConfig(t, true, listen, "new-user", "new-pass")
	fromCfg.Routing.FinalPolicy = string(routerule.PolicyDirect)
	toCfg.Routing.FinalPolicy = string(routerule.PolicyDirect)
	origin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen origin: %v", err)
	}
	defer origin.Close()
	target := origin.Addr().String()
	originDone := make(chan struct{})
	go func() {
		defer close(originDone)
		conn, acceptErr := origin.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	rc := &RoutingController{ctx: ctx}
	if err := rc.Start(fromCfg); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer rc.Stop()

	from := boxmgr.ReloadState{Config: fromCfg}
	to := boxmgr.ReloadState{Config: toCfg}
	if err := rc.PrepareReload(ctx, from, to); err != nil {
		t.Fatalf("PrepareReload() error = %v", err)
	}
	if err := rc.CompleteReload(ctx, from, to); err != nil {
		t.Fatalf("CompleteReload() error = %v", err)
	}

	if got := routingProxyStatus(t, listen, target, "old-user", "old-pass"); got != http.StatusProxyAuthRequired {
		t.Fatalf("old credentials status = %d, want 407", got)
	}
	if got := routingProxyStatus(t, listen, target, "new-user", "new-pass"); got != http.StatusOK {
		t.Fatalf("new credentials status = %d, want 200", got)
	}
	select {
	case <-originDone:
	case <-time.After(time.Second):
		t.Fatal("origin connection did not finish")
	}
}

func TestRoutingControllerReloadLifecycleRestartsForAuthChangeAndRestoresOnFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listen := freeRoutingListen(t)
	fromCfg := routingLifecycleConfig(t, true, listen, "old-user", "old-pass")
	toCfg := routingLifecycleConfig(t, true, listen, "new-user", "new-pass")
	rc := &RoutingController{ctx: ctx}
	if err := rc.Start(fromCfg); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer rc.Stop()

	from := boxmgr.ReloadState{Config: fromCfg}
	to := boxmgr.ReloadState{Config: toCfg}
	if err := rc.PrepareReload(ctx, from, to); err != nil {
		t.Fatalf("PrepareReload() error = %v", err)
	}
	waitRoutingReachable(t, listen, false)
	if err := rc.FailedReload(ctx, from, to, context.DeadlineExceeded, true); err != nil {
		t.Fatalf("FailedReload() error = %v", err)
	}
	waitRoutingReachable(t, listen, true)
	if status := rc.RoutingStatus(); !status.Enabled || status.Listen != listen {
		t.Fatalf("old dispatcher was not restored: %+v", status)
	}
}

func TestRoutingControllerCompleteReloadReturnsDispatchBindError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy listen: %v", err)
	}
	defer occupied.Close()

	rc := &RoutingController{ctx: ctx}
	fromCfg := routingLifecycleConfig(t, false, freeRoutingListen(t), "", "")
	toCfg := routingLifecycleConfig(t, true, occupied.Addr().String(), "", "")
	from := boxmgr.ReloadState{Config: fromCfg}
	to := boxmgr.ReloadState{Config: toCfg}
	if err := rc.PrepareReload(ctx, from, to); err != nil {
		t.Fatalf("PrepareReload() error = %v", err)
	}
	if err := rc.CompleteReload(ctx, from, to); err == nil {
		t.Fatal("expected dispatcher bind failure to fail reload completion")
	}
	if status := rc.RoutingStatus(); status.Enabled {
		t.Fatalf("failed dispatcher was published as enabled: %+v", status)
	}
}

func TestRoutingControllerReloadLifecycleStopsForIdleState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listen := freeRoutingListen(t)
	cfg := routingLifecycleConfig(t, true, listen, "", "")
	rc := &RoutingController{ctx: ctx}
	if err := rc.Start(cfg); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer rc.Stop()

	from := boxmgr.ReloadState{Config: cfg}
	to := boxmgr.ReloadState{Config: cfg, Idle: true}
	if err := rc.PrepareReload(ctx, from, to); err != nil {
		t.Fatalf("PrepareReload() error = %v", err)
	}
	waitRoutingReachable(t, listen, false)
	if err := rc.CompleteReload(ctx, from, to); err != nil {
		t.Fatalf("CompleteReload() error = %v", err)
	}
	if status := rc.RoutingStatus(); status.Enabled {
		t.Fatalf("idle routing status should be disabled: %+v", status)
	}
}

func TestRoutingControllerStartReturnsDispatchBindError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy listen: %v", err)
	}
	defer occupied.Close()

	rc := &RoutingController{ctx: ctx}
	cfg := routingLifecycleConfig(t, true, occupied.Addr().String(), "", "")
	if err := rc.Start(cfg); err == nil {
		t.Fatal("expected dispatcher bind failure to fail startup")
	}
	if status := rc.RoutingStatus(); status.Enabled {
		t.Fatalf("failed dispatcher was published as enabled: %+v", status)
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.server != nil || rc.engine != nil || rc.provider != nil || rc.geo != nil || rc.running {
		t.Fatal("failed startup retained routing resources")
	}
}

func TestRoutingControllerStartReturnsProviderSetupError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rc := &RoutingController{ctx: ctx}
	cfg := routingLifecycleConfig(t, true, freeRoutingListen(t), "", "")
	cfg.Routing.RuleProviders = []config.RuleProvider{{URL: "://invalid"}}
	if err := rc.Start(cfg); err == nil {
		t.Fatal("expected invalid provider URL to fail startup")
	}
	if status := rc.RoutingStatus(); status.Enabled {
		t.Fatalf("provider setup failure published routing as enabled: %+v", status)
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.server != nil || rc.engine != nil || rc.provider != nil || rc.geo != nil || rc.running {
		t.Fatal("provider setup failure retained routing resources")
	}
}

func TestRoutingControllerReloadLifecycleDoesNotRefetchUnchangedProviders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var fetches atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		_, _ = w.Write([]byte("provider.example\n"))
	}))
	defer provider.Close()

	listen := freeRoutingListen(t)
	fromCfg := routingLifecycleConfig(t, true, listen, "", "")
	fromCfg.Routing.RuleProviders = []config.RuleProvider{{
		URL:      provider.URL,
		Policy:   string(routerule.PolicyProxy),
		Behavior: "domain",
		Interval: time.Hour,
	}}
	toCfg := cloneConfigSnapshot(fromCfg)
	toCfg.SourceSync.ManifestURL = "https://source-sync.example/manifest.json"

	rc := &RoutingController{ctx: ctx}
	if err := rc.Start(fromCfg); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer rc.Stop()
	if got := fetches.Load(); got != 1 {
		t.Fatalf("initial provider fetches = %d, want 1", got)
	}

	rc.mu.Lock()
	originalServer := rc.server
	originalProvider := rc.provider
	rc.mu.Unlock()
	from := boxmgr.ReloadState{Config: fromCfg}
	to := boxmgr.ReloadState{Config: toCfg}
	if err := rc.PrepareReload(ctx, from, to); err != nil {
		t.Fatalf("PrepareReload() error = %v", err)
	}
	if err := rc.CompleteReload(ctx, from, to); err != nil {
		t.Fatalf("CompleteReload() error = %v", err)
	}

	rc.mu.Lock()
	currentServer := rc.server
	currentProvider := rc.provider
	rc.mu.Unlock()
	if currentServer != originalServer {
		t.Fatal("source-only reload restarted the dispatcher")
	}
	if currentProvider != originalProvider {
		t.Fatal("source-only reload restarted the provider manager")
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("source-only reload fetched provider %d times, want 1", got)
	}
}

func routingLifecycleConfig(t *testing.T, enabled bool, listen, username, password string) *config.Config {
	t.Helper()
	useDefaults := false
	cfg := &config.Config{}
	cfg.Listener.Username = username
	cfg.Listener.Password = password
	cfg.Routing.Enabled = enabled
	cfg.Routing.Listen = listen
	cfg.Routing.UseDefaultRules = &useDefaults
	cfg.Routing.FinalPolicy = string(routerule.PolicyProxy)
	return cfg
}

func freeRoutingListen(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve routing listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release routing listen: %v", err)
	}
	return address
}

func waitRoutingReachable(t *testing.T, address string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			if want {
				return
			}
		} else if !want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("routing listen %s reachable=%v, want %v (last error: %v)", address, err == nil, want, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func routingProxyStatus(t *testing.T, address, target, username, password string) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("dial routing proxy: %v", err)
	}
	defer conn.Close()
	credentials := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	if _, err := fmt.Fprintf(conn,
		"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n",
		target,
		target,
		credentials,
	); err != nil {
		t.Fatalf("write proxy request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read proxy response: %v", err)
	}
	defer response.Body.Close()
	return response.StatusCode
}
