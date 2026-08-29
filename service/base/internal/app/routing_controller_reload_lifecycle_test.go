package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/routerule"
)

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
