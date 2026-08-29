package boxmgr

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/pool"
)

func TestCloseSerializesWithReloadAndClosesPublishedCandidate(t *testing.T) {
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	newCfg := &config.Config{Mode: "hybrid", Nodes: []config.NodeConfig{{Name: "new"}}}
	candidate := &blockingStartBox{
		startEntered: make(chan struct{}),
		releaseStart: make(chan struct{}),
	}
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     &fakeManagedBox{name: "old"},
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			return candidate, nil
		},
	}

	reloadDone := make(chan error, 1)
	go func() { reloadDone <- manager.Reload(newCfg) }()
	select {
	case <-candidate.startEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not reach candidate Start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	returnedEarly := false
	select {
	case <-closeDone:
		returnedEarly = true
	case <-time.After(50 * time.Millisecond):
	}
	close(candidate.releaseStart)

	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("Reload() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Reload() did not finish")
	}
	if !returnedEarly {
		select {
		case err := <-closeDone:
			if err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Close() did not finish")
		}
	}

	if returnedEarly {
		t.Fatal("Close() returned while Reload() still owned the lifecycle transaction")
	}
	manager.mu.RLock()
	current := manager.currentBox
	baseCtx := manager.baseCtx
	idle := manager.idle
	manager.mu.RUnlock()
	if current != nil || baseCtx != nil || idle {
		t.Fatalf("manager revived after Close: current=%T baseCtx=%v idle=%v", current, baseCtx, idle)
	}
	if got := candidate.closeCount(); got != 1 {
		t.Fatalf("candidate close count = %d, want 1", got)
	}
}

func TestReloadCandidateCloseFailureNeverReportsRestoredTrue(t *testing.T) {
	startErr := errors.New("candidate start failed")
	closeErr := errors.New("candidate close failed")
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	newCfg := &config.Config{Mode: "hybrid", Nodes: []config.NodeConfig{{Name: "new"}}}
	candidate := &fakeManagedBox{name: "candidate", startErr: startErr, closeErr: closeErr}
	restoredBox := &fakeManagedBox{name: "restored"}
	boxes := []managedBox{candidate, restoredBox}
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     &fakeManagedBox{name: "old"},
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			box := boxes[0]
			boxes = boxes[1:]
			return box, nil
		},
	}
	events := []string{}
	lifecycle := &recordingReloadListener{events: &events}
	manager.AddReloadLifecycleListener(lifecycle)

	err := manager.Reload(newCfg)
	if !errors.Is(err, startErr) || !errors.Is(err, closeErr) {
		t.Fatalf("Reload() error = %v, want start and close errors", err)
	}
	if manager.currentBox != restoredBox || restoredBox.starts != 1 {
		t.Fatalf("old box rollback did not run: current=%T starts=%d", manager.currentBox, restoredBox.starts)
	}
	if len(lifecycle.failed) != 1 || lifecycle.failed[0].restored {
		t.Fatalf("candidate close failure reported a full restore: %+v", lifecycle.failed)
	}
}

func TestReloadRetryCloseFailureAbortsTransaction(t *testing.T) {
	port := reserveFreePort(t)
	bindErr := fmt.Errorf("listen tcp 0.0.0.0:%d: bind: address already in use", port)
	closeErr := errors.New("close conflicted candidate")
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	newCfg := &config.Config{
		Mode: "multi-port",
		MultiPort: config.MultiPortConfig{
			Address:  "127.0.0.1",
			BasePort: port,
		},
		Nodes: []config.NodeConfig{{Name: "new", Port: port}},
	}
	candidate := &fakeManagedBox{name: "candidate", startErr: bindErr, closeErr: closeErr}
	restoredBox := &fakeManagedBox{name: "restored"}
	targetCreates := 0
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     &fakeManagedBox{name: "old"},
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
		boxFactory: func(_ context.Context, cfg *config.Config) (managedBox, error) {
			if cfg.Mode == newCfg.Mode {
				targetCreates++
				if targetCreates == 1 {
					return candidate, nil
				}
				return &fakeManagedBox{name: "unexpected retry"}, nil
			}
			return restoredBox, nil
		},
	}

	err := manager.Reload(newCfg)
	if !errors.Is(err, closeErr) {
		t.Fatalf("Reload() error = %v, want candidate close error", err)
	}
	if targetCreates != 1 {
		t.Fatalf("candidate factory calls = %d, want retry aborted after first close failure", targetCreates)
	}
	if manager.currentBox != restoredBox || restoredBox.starts != 1 {
		t.Fatalf("old box was not restored after retry close failure: current=%T starts=%d", manager.currentBox, restoredBox.starts)
	}
}

func TestReloadListenerRestoreFailureMarksTransactionUnrestored(t *testing.T) {
	candidateErr := errors.New("candidate failed")
	listenerErr := errors.New("routing restore failed")
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	newCfg := &config.Config{Mode: "hybrid", Nodes: []config.NodeConfig{{Name: "new"}}}
	candidate := &fakeManagedBox{name: "candidate", startErr: candidateErr}
	restoredBox := &fakeManagedBox{name: "restored"}
	boxes := []managedBox{candidate, restoredBox}
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     &fakeManagedBox{name: "old"},
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			box := boxes[0]
			boxes = boxes[1:]
			return box, nil
		},
	}
	events := []string{}
	restoreFailure := &recordingReloadListener{events: &events, failedErr: listenerErr}
	observer := &recordingReloadListener{events: &events}
	configListener := &recordingConfigListener{events: &events}
	manager.AddReloadLifecycleListener(restoreFailure)
	manager.AddReloadLifecycleListener(observer)
	manager.AddConfigListener(configListener)

	err := manager.Reload(newCfg)
	if !errors.Is(err, candidateErr) || !errors.Is(err, listenerErr) {
		t.Fatalf("Reload() error = %v, want candidate and listener restore errors", err)
	}
	if len(observer.failed) != 1 || observer.failed[0].restored {
		t.Fatalf("later listener did not observe degraded restore: %+v", observer.failed)
	}
	if len(configListener.configs) != 0 {
		t.Fatalf("ordinary config listeners were notified after partial restore: %+v", configListener.configs)
	}
}

func TestReloadFailureRestoresOldSharedStateRegistry(t *testing.T) {
	pool.ResetSharedStateStore()
	oldRegistry := pool.SnapshotSharedStateStore()
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	newCfg := &config.Config{Mode: "hybrid", Nodes: []config.NodeConfig{{Name: "new"}}}
	candidate := &fakeManagedBox{name: "candidate", startErr: errors.New("candidate failed")}
	restoredBox := &fakeManagedBox{name: "restored"}
	boxes := []managedBox{candidate, restoredBox}
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     &fakeManagedBox{name: "old"},
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			box := boxes[0]
			boxes = boxes[1:]
			return box, nil
		},
	}

	if err := manager.Reload(newCfg); err == nil {
		t.Fatal("Reload() error = nil, want candidate failure")
	}
	if got := pool.SnapshotSharedStateStore(); got != oldRegistry {
		t.Fatal("failed reload did not restore the old shared state registry")
	}
}

func TestReloadWaitsForCandidateGenerationProbeAndIgnoresOldTrafficFallback(t *testing.T) {
	monitorMgr, err := monitor.NewManager(monitor.Config{ProbeTarget: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()
	oldHandle := monitorMgr.Register(monitor.NodeInfo{Tag: "same-node", Name: "same-node"})
	oldHandle.MarkInitialCheckDone(false)
	oldHandle.RecordSuccess("old-traffic.example:443")

	probeStarted := make(chan struct{})
	probeRelease := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(probeRelease) })
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "same-node"}}}
	newCfg := &config.Config{
		Mode:  "hybrid",
		Nodes: []config.NodeConfig{{Name: "same-node"}},
		SubscriptionRefresh: config.SubscriptionRefreshConfig{
			MinAvailableNodes:  1,
			HealthCheckTimeout: time.Second,
		},
	}
	candidate := &fakeManagedBox{name: "candidate"}
	restored := &fakeManagedBox{name: "restored"}
	factoryCalls := 0
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     &fakeManagedBox{name: "old"},
		lastAppliedCfg: snapshotConfig(oldCfg),
		monitorMgr:     monitorMgr,
		logger:         defaultLogger{},
		boxFactory: func(_ context.Context, cfg *config.Config) (managedBox, error) {
			factoryCalls++
			handle := monitorMgr.Register(monitor.NodeInfo{Tag: "same-node", Name: "same-node"})
			if cfg.Mode == newCfg.Mode {
				handle.SetProbe(func(context.Context) (time.Duration, error) {
					close(probeStarted)
					<-probeRelease
					return 0, errors.New("candidate health failed")
				})
				return candidate, nil
			}
			handle.SetProbe(func(context.Context) (time.Duration, error) {
				return time.Millisecond, nil
			})
			return restored, nil
		},
	}

	reloadDone := make(chan error, 1)
	go func() { reloadDone <- manager.Reload(newCfg) }()
	select {
	case err := <-reloadDone:
		releaseOnce.Do(func() { close(probeRelease) })
		t.Fatalf("Reload() returned before candidate probe ran: %v", err)
	case <-probeStarted:
	}
	select {
	case err := <-reloadDone:
		releaseOnce.Do(func() { close(probeRelease) })
		t.Fatalf("Reload() returned while candidate probe was blocked: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(probeRelease) })
	select {
	case err := <-reloadDone:
		if err == nil || !strings.Contains(err.Error(), "reload health check failed") {
			t.Fatalf("Reload() error = %v, want candidate health failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Reload() did not finish after candidate probe failed")
	}
	if manager.currentBox != restored || restored.starts != 1 || factoryCalls != 2 {
		t.Fatalf("rollback state = current:%T starts:%d factoryCalls:%d", manager.currentBox, restored.starts, factoryCalls)
	}
}

func TestReloadAppliesCandidateProbeConfigBeforeBuildingBox(t *testing.T) {
	monitorMgr, err := monitor.NewManager(monitor.Config{ProbeTarget: "http://old.example/", SkipCertVerify: false})
	if err != nil {
		t.Fatalf("monitor.NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()

	managementDisabled := false
	oldCfg := &config.Config{
		Mode:           "pool",
		SkipCertVerify: false,
		Management:     config.ManagementConfig{Enabled: &managementDisabled, ProbeTarget: "http://old.example/"},
		Nodes:          []config.NodeConfig{{Name: "probe-config-node"}},
	}
	newCfg := &config.Config{
		Mode:           "hybrid",
		SkipCertVerify: true,
		Management:     config.ManagementConfig{Enabled: &managementDisabled, ProbeTarget: "http://new.example/"},
		Nodes:          []config.NodeConfig{{Name: "probe-config-node"}},
		SubscriptionRefresh: config.SubscriptionRefreshConfig{
			MinAvailableNodes:  1,
			HealthCheckTimeout: time.Second,
		},
	}
	candidate := &fakeManagedBox{name: "candidate"}
	oldBox := &fakeManagedBox{name: "old"}
	var factoryProbeTarget string
	var factorySkipCertVerify bool
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     oldBox,
		lastAppliedCfg: snapshotConfig(oldCfg),
		monitorMgr:     monitorMgr,
		monitorCfg:     monitor.Config{ProbeTarget: "http://old.example/"},
		logger:         defaultLogger{},
		boxFactory: func(_ context.Context, cfg *config.Config) (managedBox, error) {
			handle := monitorMgr.Register(monitor.NodeInfo{Tag: "probe-config-node", Name: "probe-config-node"})
			factorySkipCertVerify = monitorMgr.SkipCertVerify()
			targets, ok := monitorMgr.ProbeTargets()
			if ok && len(targets) > 0 {
				factoryProbeTarget = targets[0].Original
			}
			if cfg.Mode == newCfg.Mode {
				handle.SetProbe(func(context.Context) (time.Duration, error) {
					if factoryProbeTarget != newCfg.Management.ProbeTarget || factorySkipCertVerify != newCfg.SkipCertVerify {
						return 0, fmt.Errorf("candidate saw probe target %q and skip_cert_verify=%v", factoryProbeTarget, factorySkipCertVerify)
					}
					return time.Millisecond, nil
				})
				return candidate, nil
			}
			return oldBox, nil
		},
	}

	if err := manager.Reload(newCfg); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if factoryProbeTarget != newCfg.Management.ProbeTarget {
		t.Fatalf("candidate factory saw probe target %q, want %q", factoryProbeTarget, newCfg.Management.ProbeTarget)
	}
	if factorySkipCertVerify != newCfg.SkipCertVerify {
		t.Fatalf("candidate factory saw skip_cert_verify=%v, want %v", factorySkipCertVerify, newCfg.SkipCertVerify)
	}
}

func TestReloadRollbackUsesLastAppliedSnapshotAfterInPlaceConfigMutation(t *testing.T) {
	events := []string{}
	startErr := errors.New("candidate failed")
	sharedCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	oldSnapshot := snapshotConfig(sharedCfg)
	oldBox := &fakeManagedBox{events: &events, name: "old"}
	candidate := &fakeManagedBox{events: &events, name: "candidate", startErr: startErr}
	restoredBox := &fakeManagedBox{events: &events, name: "restored"}
	createdModes := []string{}
	boxes := []managedBox{candidate, restoredBox}
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            sharedCfg,
		currentBox:     oldBox,
		lastAppliedCfg: oldSnapshot,
		logger:         defaultLogger{},
		boxFactory: func(_ context.Context, cfg *config.Config) (managedBox, error) {
			createdModes = append(createdModes, cfg.Mode)
			box := boxes[0]
			boxes = boxes[1:]
			return box, nil
		},
	}
	lifecycle := &recordingReloadListener{manager: manager, events: &events}
	manager.AddReloadLifecycleListener(lifecycle)

	sharedCfg.Lock()
	sharedCfg.Mode = "hybrid"
	sharedCfg.Nodes = []config.NodeConfig{{Name: "new"}}
	sharedCfg.Unlock()

	err := manager.Reload(sharedCfg)
	if !errors.Is(err, startErr) {
		t.Fatalf("Reload() error = %v, want candidate error", err)
	}
	if !reflect.DeepEqual(createdModes, []string{"hybrid", "pool"}) {
		t.Fatalf("factory config modes = %v, want new then last-applied old", createdModes)
	}
	if manager.cfg == sharedCfg || manager.cfg.Mode != "pool" {
		t.Fatalf("rollback did not restore isolated old snapshot: %+v", manager.cfg)
	}
}
