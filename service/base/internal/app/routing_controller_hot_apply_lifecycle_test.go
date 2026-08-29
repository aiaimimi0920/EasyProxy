package app

import (
	"context"
	"testing"
	"time"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/dispatch"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/routerule"
)

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
