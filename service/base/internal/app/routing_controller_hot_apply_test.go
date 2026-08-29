package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/dispatch"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/routerule"
)

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
