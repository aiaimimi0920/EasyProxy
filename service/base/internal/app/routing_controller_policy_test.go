package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/routerule"
)

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
