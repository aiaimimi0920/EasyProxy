package boxmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/store"

	"github.com/sagernet/sing-box/adapter"
)

type fakeManagedBox struct {
	events   *[]string
	name     string
	startErr error
	closeErr error
	starts   int
	closes   int
	outbound adapter.OutboundManager
}

func (b *fakeManagedBox) Start() error {
	b.starts++
	if b.events != nil {
		*b.events = append(*b.events, b.name+":start")
	}
	return b.startErr
}

func (b *fakeManagedBox) Close() error {
	b.closes++
	if b.events != nil {
		*b.events = append(*b.events, b.name+":close")
	}
	return b.closeErr
}

func (b *fakeManagedBox) Outbound() adapter.OutboundManager {
	return b.outbound
}

type recordingReloadListener struct {
	manager     *Manager
	events      *[]string
	prepareErr  error
	completeErr error
	failedErr   error
	completeBox managedBox
	failed      []reloadFailureRecord
}

type reloadFailureRecord struct {
	cause    error
	restored bool
}

func (l *recordingReloadListener) PrepareReload(_ context.Context, from, to ReloadState) error {
	*l.events = append(*l.events, "prepare")
	return l.prepareErr
}

func (l *recordingReloadListener) CompleteReload(_ context.Context, from, to ReloadState) error {
	*l.events = append(*l.events, "complete")
	if l.manager != nil {
		l.manager.mu.RLock()
		l.completeBox = l.manager.currentBox
		l.manager.mu.RUnlock()
	}
	return l.completeErr
}

func (l *recordingReloadListener) FailedReload(_ context.Context, from, to ReloadState, cause error, restored bool) error {
	*l.events = append(*l.events, "failed")
	l.failed = append(l.failed, reloadFailureRecord{cause: cause, restored: restored})
	return l.failedErr
}

type blockingStartBox struct {
	startEntered chan struct{}
	releaseStart chan struct{}
	startOnce    sync.Once

	mu     sync.Mutex
	closes int
}

func (b *blockingStartBox) Start() error {
	b.startOnce.Do(func() { close(b.startEntered) })
	<-b.releaseStart
	return nil
}

func (b *blockingStartBox) Close() error {
	b.mu.Lock()
	b.closes++
	b.mu.Unlock()
	return nil
}

func (b *blockingStartBox) Outbound() adapter.OutboundManager { return nil }

func (b *blockingStartBox) closeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closes
}

type recordingConfigListener struct {
	events  *[]string
	configs []*config.Config
	lastRaw *config.Config
}

func (l *recordingConfigListener) OnConfigUpdate(cfg *config.Config) {
	*l.events = append(*l.events, "notify")
	l.lastRaw = cfg
	l.configs = append(l.configs, snapshotConfig(cfg))
}

func TestReloadTransactionSuccessOrdersLifecycleBeforeConfigNotification(t *testing.T) {
	events := []string{}
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	newCfg := &config.Config{Mode: "hybrid", Nodes: []config.NodeConfig{{Name: "new"}}}
	oldBox := &fakeManagedBox{events: &events, name: "old"}
	candidate := &fakeManagedBox{events: &events, name: "candidate"}

	manager := &Manager{
		baseCtx:             context.Background(),
		cfg:                 oldCfg,
		currentBox:          oldBox,
		lastAppliedCfg:      snapshotConfig(oldCfg),
		lastAppliedMode:     oldCfg.Mode,
		lastAppliedBasePort: oldCfg.MultiPort.BasePort,
		logger:              defaultLogger{},
		boxFactory: func(_ context.Context, cfg *config.Config) (managedBox, error) {
			events = append(events, "create:"+cfg.Mode)
			return candidate, nil
		},
	}
	lifecycle := &recordingReloadListener{manager: manager, events: &events}
	configListener := &recordingConfigListener{events: &events}
	manager.AddReloadLifecycleListener(lifecycle)
	manager.AddConfigListener(configListener)

	if err := manager.Reload(newCfg); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}

	wantEvents := []string{
		"prepare",
		"old:close",
		"create:hybrid",
		"candidate:start",
		"complete",
		"notify",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("reload events = %v, want %v", events, wantEvents)
	}
	if manager.currentBox != candidate {
		t.Fatal("candidate box was not published before reload completion")
	}
	if lifecycle.completeBox != candidate {
		t.Fatal("CompleteReload did not observe the published candidate box")
	}
	if manager.cfg == newCfg {
		t.Fatal("active config should be a transaction-owned snapshot")
	}
	if manager.cfg.Mode != newCfg.Mode || manager.lastAppliedCfg.Mode != newCfg.Mode {
		t.Fatalf("active snapshots were not committed: cfg=%+v last=%+v", manager.cfg, manager.lastAppliedCfg)
	}
	newCfg.Mode = "mutated-after-reload"
	if manager.cfg.Mode != "hybrid" || manager.lastAppliedCfg.Mode != "hybrid" {
		t.Fatal("committed config snapshots changed after caller mutation")
	}
}

func TestReloadPrepareFailureKeepsOldBoxOpenAndRestoresVisibleConfig(t *testing.T) {
	events := []string{}
	prepareErr := errors.New("routing prepare rejected")
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	newCfg := &config.Config{Mode: "hybrid", Nodes: []config.NodeConfig{{Name: "new"}}}
	oldBox := &fakeManagedBox{events: &events, name: "old"}
	factoryCalls := 0
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            newCfg,
		currentBox:     oldBox,
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			factoryCalls++
			return nil, errors.New("factory should not be called")
		},
	}
	lifecycle := &recordingReloadListener{
		manager:    manager,
		events:     &events,
		prepareErr: prepareErr,
	}
	configListener := &recordingConfigListener{events: &events}
	manager.AddReloadLifecycleListener(lifecycle)
	manager.AddConfigListener(configListener)
	lastAppliedBefore := manager.lastAppliedCfg

	err := manager.Reload(newCfg)
	if !errors.Is(err, prepareErr) {
		t.Fatalf("Reload() error = %v, want prepare error", err)
	}
	if oldBox.closes != 0 || manager.currentBox != oldBox {
		t.Fatalf("prepare failure disturbed old box: closes=%d current=%T", oldBox.closes, manager.currentBox)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory called %d times after prepare failure", factoryCalls)
	}
	if manager.cfg == newCfg || manager.cfg.Mode != oldCfg.Mode {
		t.Fatalf("visible config was not restored from last-applied snapshot: %+v", manager.cfg)
	}
	if len(lifecycle.failed) != 1 || !lifecycle.failed[0].restored || !errors.Is(lifecycle.failed[0].cause, prepareErr) {
		t.Fatalf("unexpected failure callback: %+v", lifecycle.failed)
	}
	if len(configListener.configs) != 1 || configListener.configs[0].Mode != oldCfg.Mode {
		t.Fatalf("ordinary listener did not receive restored config: %+v", configListener.configs)
	}
	if manager.lastAppliedCfg != lastAppliedBefore {
		t.Fatal("prepare failure replaced the last-applied config snapshot")
	}
}

func TestReloadCandidateStartFailureClosesCandidateAndRestoresOld(t *testing.T) {
	events := []string{}
	startErr := errors.New("candidate start failed")
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	newCfg := &config.Config{Mode: "hybrid", Nodes: []config.NodeConfig{{Name: "new"}}}
	oldBox := &fakeManagedBox{events: &events, name: "old"}
	candidate := &fakeManagedBox{events: &events, name: "candidate", startErr: startErr}
	restoredBox := &fakeManagedBox{events: &events, name: "restored"}
	created := []string{}
	boxes := []managedBox{candidate, restoredBox}
	manager := &Manager{
		baseCtx:             context.Background(),
		cfg:                 newCfg,
		currentBox:          oldBox,
		lastAppliedCfg:      snapshotConfig(oldCfg),
		lastAppliedMode:     oldCfg.Mode,
		lastAppliedBasePort: oldCfg.MultiPort.BasePort,
		logger:              defaultLogger{},
		boxFactory: func(_ context.Context, cfg *config.Config) (managedBox, error) {
			created = append(created, cfg.Mode)
			box := boxes[0]
			boxes = boxes[1:]
			return box, nil
		},
	}
	lifecycle := &recordingReloadListener{manager: manager, events: &events}
	configListener := &recordingConfigListener{events: &events}
	manager.AddReloadLifecycleListener(lifecycle)
	manager.AddConfigListener(configListener)
	lastAppliedBefore := manager.lastAppliedCfg

	err := manager.Reload(newCfg)
	if !errors.Is(err, startErr) {
		t.Fatalf("Reload() error = %v, want candidate start error", err)
	}
	if candidate.closes != 1 {
		t.Fatalf("candidate close count = %d, want 1", candidate.closes)
	}
	if restoredBox.starts != 1 || manager.currentBox != restoredBox {
		t.Fatalf("old box was not restored: starts=%d current=%T", restoredBox.starts, manager.currentBox)
	}
	if !reflect.DeepEqual(created, []string{"hybrid", "pool"}) {
		t.Fatalf("factory config modes = %v, want [hybrid pool]", created)
	}
	if len(lifecycle.failed) != 1 || !lifecycle.failed[0].restored || !errors.Is(lifecycle.failed[0].cause, startErr) {
		t.Fatalf("unexpected failure callback: %+v", lifecycle.failed)
	}
	if len(configListener.configs) != 1 || configListener.configs[0].Mode != "pool" {
		t.Fatalf("ordinary listener did not receive old config: %+v", configListener.configs)
	}
	if configListener.lastRaw != manager.cfg {
		t.Fatal("ordinary listener did not receive the restored active config object")
	}
	if manager.lastAppliedCfg != lastAppliedBefore {
		t.Fatal("failed reload replaced the last-applied config snapshot")
	}
	failedIndex := -1
	notifyIndex := -1
	for idx, event := range events {
		switch event {
		case "failed":
			failedIndex = idx
		case "notify":
			notifyIndex = idx
		}
	}
	if failedIndex == -1 || notifyIndex == -1 || failedIndex > notifyIndex {
		t.Fatalf("failure/config notification order = %v, want failed before notify", events)
	}
}

func TestReloadCompleteFailureClosesRunningCandidateAndRestoresOld(t *testing.T) {
	events := []string{}
	completeErr := errors.New("routing completion failed")
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	newCfg := &config.Config{Mode: "hybrid", Nodes: []config.NodeConfig{{Name: "new"}}}
	oldBox := &fakeManagedBox{events: &events, name: "old"}
	candidate := &fakeManagedBox{events: &events, name: "candidate"}
	restoredBox := &fakeManagedBox{events: &events, name: "restored"}
	boxes := []managedBox{candidate, restoredBox}
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     oldBox,
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			box := boxes[0]
			boxes = boxes[1:]
			return box, nil
		},
	}
	lifecycle := &recordingReloadListener{
		manager:     manager,
		events:      &events,
		completeErr: completeErr,
	}
	configListener := &recordingConfigListener{events: &events}
	manager.AddReloadLifecycleListener(lifecycle)
	manager.AddConfigListener(configListener)

	err := manager.Reload(newCfg)
	if !errors.Is(err, completeErr) {
		t.Fatalf("Reload() error = %v, want complete error", err)
	}
	if lifecycle.completeBox != candidate {
		t.Fatal("CompleteReload did not observe running candidate")
	}
	if candidate.starts != 1 || candidate.closes != 1 {
		t.Fatalf("candidate lifecycle = starts:%d closes:%d, want 1/1", candidate.starts, candidate.closes)
	}
	if manager.currentBox != restoredBox || restoredBox.starts != 1 {
		t.Fatalf("old box was not restored after complete failure: current=%T starts=%d", manager.currentBox, restoredBox.starts)
	}
	if len(lifecycle.failed) != 1 || !lifecycle.failed[0].restored || !errors.Is(lifecycle.failed[0].cause, completeErr) {
		t.Fatalf("unexpected failure callback: %+v", lifecycle.failed)
	}
	if len(configListener.configs) != 1 || configListener.configs[0].Mode != oldCfg.Mode {
		t.Fatalf("ordinary listener did not receive old config: %+v", configListener.configs)
	}
}

func TestReloadRollbackFailureLeavesNoCurrentBox(t *testing.T) {
	rollbackCreateErr := errors.New("rollback create failed")
	rollbackStartErr := errors.New("rollback start failed")
	for _, tc := range []struct {
		name        string
		rollbackBox *fakeManagedBox
		rollbackErr error
	}{
		{name: "create", rollbackErr: rollbackCreateErr},
		{name: "start", rollbackBox: &fakeManagedBox{name: "rollback", startErr: rollbackStartErr}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []string{}
			candidateErr := errors.New("candidate failed")
			oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
			newCfg := &config.Config{Mode: "hybrid", Nodes: []config.NodeConfig{{Name: "new"}}}
			oldBox := &fakeManagedBox{events: &events, name: "old"}
			candidate := &fakeManagedBox{events: &events, name: "candidate", startErr: candidateErr}
			factoryCalls := 0
			manager := &Manager{
				baseCtx:        context.Background(),
				cfg:            oldCfg,
				currentBox:     oldBox,
				lastAppliedCfg: snapshotConfig(oldCfg),
				logger:         defaultLogger{},
				boxFactory: func(context.Context, *config.Config) (managedBox, error) {
					factoryCalls++
					if factoryCalls == 1 {
						return candidate, nil
					}
					if tc.rollbackErr != nil {
						return nil, tc.rollbackErr
					}
					tc.rollbackBox.events = &events
					return tc.rollbackBox, nil
				},
			}
			lifecycle := &recordingReloadListener{manager: manager, events: &events}
			manager.AddReloadLifecycleListener(lifecycle)

			err := manager.Reload(newCfg)
			if !errors.Is(err, candidateErr) {
				t.Fatalf("Reload() error = %v, want candidate error", err)
			}
			if manager.currentBox != nil {
				t.Fatalf("current box = %T, want nil after rollback failure", manager.currentBox)
			}
			if len(lifecycle.failed) != 1 || lifecycle.failed[0].restored {
				t.Fatalf("unexpected failure callback: %+v", lifecycle.failed)
			}
			if tc.rollbackBox != nil && tc.rollbackBox.closes != 1 {
				t.Fatalf("failed rollback box close count = %d, want 1", tc.rollbackBox.closes)
			}
		})
	}
}

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

func reserveFreePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatalf("release free port: %v", err)
	}
	return port
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

func TestEnterIdleCommitsLastAppliedStateAfterLifecycleCompletion(t *testing.T) {
	events := []string{}
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	idleCfg := &config.Config{Mode: "hybrid", Nodes: []config.NodeConfig{}}
	oldBox := &fakeManagedBox{events: &events, name: "old"}
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     oldBox,
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
	}
	lifecycle := &recordingReloadListener{manager: manager, events: &events}
	configListener := &recordingConfigListener{events: &events}
	manager.AddReloadLifecycleListener(lifecycle)
	manager.AddConfigListener(configListener)

	if err := manager.enterIdle(idleCfg); err != nil {
		t.Fatalf("enterIdle() error = %v", err)
	}

	wantEvents := []string{"prepare", "old:close", "complete", "notify"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("idle transition events = %v, want %v", events, wantEvents)
	}
	if manager.currentBox != nil || !manager.idle || !manager.lastAppliedIdle {
		t.Fatalf("idle state not committed: current=%T idle=%v lastIdle=%v", manager.currentBox, manager.idle, manager.lastAppliedIdle)
	}
	if manager.lastAppliedCfg == idleCfg || manager.lastAppliedCfg.Mode != idleCfg.Mode {
		t.Fatalf("idle last-applied config not snapshotted: %+v", manager.lastAppliedCfg)
	}
}

func TestEnterIdleCompleteFailureRestoresPreviousRunningState(t *testing.T) {
	events := []string{}
	completeErr := errors.New("idle completion failed")
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old"}}}
	idleCfg := &config.Config{Mode: "hybrid", Nodes: []config.NodeConfig{}}
	oldBox := &fakeManagedBox{events: &events, name: "old"}
	restoredBox := &fakeManagedBox{events: &events, name: "restored"}
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     oldBox,
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			return restoredBox, nil
		},
	}
	lifecycle := &recordingReloadListener{
		manager:     manager,
		events:      &events,
		completeErr: completeErr,
	}
	configListener := &recordingConfigListener{events: &events}
	manager.AddReloadLifecycleListener(lifecycle)
	manager.AddConfigListener(configListener)

	err := manager.enterIdle(idleCfg)
	if !errors.Is(err, completeErr) {
		t.Fatalf("enterIdle() error = %v, want complete error", err)
	}
	if oldBox.closes != 1 || restoredBox.starts != 1 || manager.currentBox != restoredBox {
		t.Fatalf("running state not restored: old closes=%d restored starts=%d current=%T", oldBox.closes, restoredBox.starts, manager.currentBox)
	}
	if manager.idle || manager.lastAppliedIdle || manager.cfg.Mode != oldCfg.Mode {
		t.Fatalf("manager state not restored: idle=%v lastIdle=%v cfg=%+v", manager.idle, manager.lastAppliedIdle, manager.cfg)
	}
	if len(lifecycle.failed) != 1 || !lifecycle.failed[0].restored {
		t.Fatalf("unexpected failure callback: %+v", lifecycle.failed)
	}
	if len(configListener.configs) != 1 || configListener.configs[0].Mode != oldCfg.Mode {
		t.Fatalf("ordinary listener did not receive restored config: %+v", configListener.configs)
	}
}

func TestStartCapturesIndependentLastAppliedSnapshot(t *testing.T) {
	managementDisabled := false
	cfg := &config.Config{
		Mode:  "pool",
		Nodes: []config.NodeConfig{{Name: "initial"}},
		Management: config.ManagementConfig{
			Enabled: &managementDisabled,
		},
		Routing: config.RoutingConfig{
			Rules: []string{"DOMAIN,example.com,DIRECT"},
		},
	}
	startedBox := &fakeManagedBox{name: "initial"}
	manager := New(cfg, monitor.Config{})
	manager.boxFactory = func(context.Context, *config.Config) (managedBox, error) {
		return startedBox, nil
	}

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	if manager.lastAppliedCfg == nil || manager.lastAppliedCfg == cfg || manager.lastAppliedCfg == manager.cfg {
		t.Fatalf("last-applied config is not an independent snapshot: source=%p active=%p last=%p", cfg, manager.cfg, manager.lastAppliedCfg)
	}
	if manager.lastAppliedIdle {
		t.Fatal("successful Start unexpectedly recorded idle state")
	}

	cfg.Lock()
	cfg.Mode = "hybrid"
	cfg.Nodes[0].Name = "mutated"
	cfg.Routing.Rules[0] = "MATCH,PROXY"
	cfg.Unlock()
	if manager.cfg.Mode != "pool" || manager.lastAppliedCfg.Mode != "pool" {
		t.Fatal("source config mutation leaked into active or last-applied snapshot")
	}
	if manager.lastAppliedCfg.Nodes[0].Name != "initial" || manager.lastAppliedCfg.Routing.Rules[0] != "DOMAIN,example.com,DIRECT" {
		t.Fatalf("last-applied deep snapshot changed: %+v", manager.lastAppliedCfg)
	}
}

func TestReloadRollbackAdvancesMonitorGenerationAndSweepsCandidateNodes(t *testing.T) {
	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	monitorMgr.Register(monitor.NodeInfo{Tag: "old", Name: "old"})

	managementDisabled := false
	oldCfg := &config.Config{
		Mode:       "pool",
		Management: config.ManagementConfig{Enabled: &managementDisabled},
		Nodes:      []config.NodeConfig{{Name: "old"}},
	}
	newCfg := &config.Config{
		Mode:       "hybrid",
		Management: config.ManagementConfig{Enabled: &managementDisabled},
		Nodes:      []config.NodeConfig{{Name: "candidate"}},
	}
	oldBox := &fakeManagedBox{name: "old"}
	candidate := &fakeManagedBox{name: "candidate", startErr: errors.New("candidate start failed")}
	restoredBox := &fakeManagedBox{name: "restored"}
	boxes := []managedBox{candidate, restoredBox}
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     oldBox,
		lastAppliedCfg: snapshotConfig(oldCfg),
		monitorMgr:     monitorMgr,
		logger:         defaultLogger{},
		boxFactory: func(_ context.Context, cfg *config.Config) (managedBox, error) {
			monitorMgr.Register(monitor.NodeInfo{Tag: cfg.Nodes[0].Name, Name: cfg.Nodes[0].Name})
			box := boxes[0]
			boxes = boxes[1:]
			return box, nil
		},
	}

	if err := manager.Reload(newCfg); err == nil {
		t.Fatal("Reload() error = nil, want candidate failure")
	}
	snapshots := monitorMgr.Snapshot()
	if len(snapshots) != 1 || snapshots[0].Tag != "old" {
		t.Fatalf("monitor generation retained stale candidate entries: %+v", snapshots)
	}
}

func TestReloadFailureRestoresManagerSettingsFromLastAppliedConfig(t *testing.T) {
	managementDisabled := false
	oldCfg := &config.Config{
		Mode:           "pool",
		SkipCertVerify: false,
		Management:     config.ManagementConfig{Enabled: &managementDisabled},
		MultiPort:      config.MultiPortConfig{BasePort: 25000},
		SubscriptionRefresh: config.SubscriptionRefreshConfig{
			MinAvailableNodes: 2,
		},
		Nodes: []config.NodeConfig{{Name: "old"}},
	}
	newCfg := &config.Config{
		Mode:           "hybrid",
		SkipCertVerify: true,
		Management:     config.ManagementConfig{Enabled: &managementDisabled},
		MultiPort:      config.MultiPortConfig{BasePort: 33000},
		SubscriptionRefresh: config.SubscriptionRefreshConfig{
			MinAvailableNodes: 9,
		},
		Nodes: []config.NodeConfig{{Name: "new"}},
	}
	monitorMgr, err := monitor.NewManager(monitor.Config{SkipCertVerify: true})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	candidate := &fakeManagedBox{name: "candidate", startErr: errors.New("candidate failed")}
	restoredBox := &fakeManagedBox{name: "restored"}
	boxes := []managedBox{candidate, restoredBox}
	manager := &Manager{
		baseCtx:             context.Background(),
		cfg:                 newCfg,
		currentBox:          &fakeManagedBox{name: "old"},
		lastAppliedCfg:      snapshotConfig(oldCfg),
		lastAppliedMode:     newCfg.Mode,
		lastAppliedBasePort: newCfg.MultiPort.BasePort,
		monitorMgr:          monitorMgr,
		monitorCfg:          monitor.Config{SkipCertVerify: true},
		minAvailableNodes:   newCfg.SubscriptionRefresh.MinAvailableNodes,
		logger:              defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			box := boxes[0]
			boxes = boxes[1:]
			return box, nil
		},
	}

	if err := manager.Reload(newCfg); err == nil {
		t.Fatal("Reload() error = nil, want candidate failure")
	}
	if manager.minAvailableNodes != oldCfg.SubscriptionRefresh.MinAvailableNodes {
		t.Fatalf("minAvailableNodes = %d, want %d", manager.minAvailableNodes, oldCfg.SubscriptionRefresh.MinAvailableNodes)
	}
	if manager.lastAppliedMode != oldCfg.Mode || manager.lastAppliedBasePort != oldCfg.MultiPort.BasePort {
		t.Fatalf("last-applied topology not restored: mode=%s base=%d", manager.lastAppliedMode, manager.lastAppliedBasePort)
	}
	if manager.monitorCfg.SkipCertVerify || monitorMgr.SkipCertVerify() {
		t.Fatal("skip-cert-verify setting was not restored in manager and live monitor")
	}
}

func TestAvailableNodeCountUsesEffectiveAvailability(t *testing.T) {
	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	proven := monitorMgr.Register(monitor.NodeInfo{Tag: "traffic-proven", Name: "Traffic Proven"})
	proven.MarkInitialCheckDone(false)
	proven.RecordFailure(errors.New("tls handshake: EOF"), "www.google.com:443")
	proven.RecordSuccess("api.openai.com:443")

	bad := monitorMgr.Register(monitor.NodeInfo{Tag: "still-bad", Name: "Still Bad"})
	bad.MarkInitialCheckDone(false)
	bad.RecordFailure(errors.New("tls handshake: EOF"), "www.google.com:443")

	manager := &Manager{monitorMgr: monitorMgr}
	available, total := manager.availableNodeCount()
	if available != 1 || total != 2 {
		t.Fatalf("expected effective availability count 1/2, got %d/%d", available, total)
	}
}

func TestRestoreMonitorStatsFromStoreHydratesMonitor(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "easyproxy.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	node := &store.Node{
		URI:     "vmess://restored-node",
		Name:    "Restored Node",
		Source:  store.NodeSourceManual,
		Enabled: true,
	}
	if err := dataStore.CreateNode(ctx, node); err != nil {
		t.Fatalf("CreateNode() error = %v", err)
	}

	now := time.Now()
	if err := dataStore.UpsertNodeStats(ctx, &store.NodeStats{
		NodeID:               node.ID,
		FailureCount:         2,
		SuccessCount:         6,
		TrafficSuccessCount:  4,
		LastError:            "tls handshake: EOF",
		LastFailureAt:        now.Add(-20 * time.Minute),
		LastSuccessAt:        now.Add(-10 * time.Minute),
		LastTrafficSuccessAt: now.Add(-3 * time.Minute),
		LastProbeAt:          now.Add(-25 * time.Minute),
		LastProbeSuccessAt:   now.Add(-30 * time.Minute),
		LastLatencyMs:        180,
		Available:            false,
		InitialCheckDone:     true,
		TotalUploadBytes:     4096,
		TotalDownloadBytes:   8192,
	}); err != nil {
		t.Fatalf("UpsertNodeStats() error = %v", err)
	}

	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	handle := monitorMgr.Register(monitor.NodeInfo{
		Tag:  "restored-tag",
		Name: node.Name,
		URI:  node.URI,
	})

	manager := &Manager{
		monitorMgr: monitorMgr,
		store:      dataStore,
		logger:     defaultLogger{},
	}
	if err := manager.restoreMonitorStatsFromStore(ctx); err != nil {
		t.Fatalf("restoreMonitorStatsFromStore() error = %v", err)
	}

	snap := handle.Snapshot()
	if snap.FailureCount != 2 || snap.SuccessCount != 6 || snap.TrafficSuccessCount != 4 {
		t.Fatalf("unexpected restored counters: %+v", snap)
	}
	if snap.LastLatencyMs != 180 {
		t.Fatalf("expected restored latency, got %+v", snap)
	}
	if snap.TotalUpload != 4096 || snap.TotalDownload != 8192 {
		t.Fatalf("unexpected restored traffic totals: %+v", snap)
	}
	if !snap.InitialCheckDone || snap.Available {
		t.Fatalf("expected restored probe status to remain unavailable, got %+v", snap)
	}
	if !snap.EffectiveAvailable || !snap.TrafficProvenUsable {
		t.Fatalf("expected recent traffic to restore effective availability, got %+v", snap)
	}
}

func TestApplyConfigSettingsPropagatesSkipCertVerify(t *testing.T) {
	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager := &Manager{monitorMgr: monitorMgr}
	manager.applyConfigSettings(&config.Config{SkipCertVerify: true})
	if !manager.monitorCfg.SkipCertVerify {
		t.Fatal("expected monitor config to inherit skip_cert_verify")
	}
	if !monitorMgr.SkipCertVerify() {
		t.Fatal("expected live monitor manager to inherit skip_cert_verify")
	}

	manager.applyConfigSettings(&config.Config{SkipCertVerify: false})
	if manager.monitorCfg.SkipCertVerify {
		t.Fatal("expected monitor config skip_cert_verify to update to false")
	}
	if monitorMgr.SkipCertVerify() {
		t.Fatal("expected live monitor manager skip_cert_verify to update to false")
	}
}

func TestSetLongLivedThresholdsUpdatesMonitorManager(t *testing.T) {
	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager := &Manager{
		monitorMgr: monitorMgr,
		monitorCfg: monitor.Config{},
	}

	handle := monitorMgr.Register(monitor.NodeInfo{Tag: "threshold-boxmgr", Name: "Threshold BoxMgr"})
	handle.MarkInitialCheckDone(true)
	time.Sleep(time.Millisecond)
	manager.SetLongLivedThresholds(time.Nanosecond, 0.8)

	if manager.monitorCfg.LongLivedMinUptime != time.Nanosecond {
		t.Fatalf("manager monitor config uptime = %s, want 1ns", manager.monitorCfg.LongLivedMinUptime)
	}
	if manager.monitorCfg.LongLivedMinSuccessRate != 0.8 {
		t.Fatalf("manager monitor config rate = %.2f, want 0.8", manager.monitorCfg.LongLivedMinSuccessRate)
	}

	if snap := handle.Snapshot(); !snap.LongLived {
		t.Fatalf("box manager threshold update did not reach the existing monitor entry: %+v", snap)
	}
}

func TestListConfigNodesExcludesRuntimeStoreNodes(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "easyproxy.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	manualNode := &store.Node{
		URI:     "ss://manual-node#manual",
		Name:    "manual-node",
		Source:  store.NodeSourceManual,
		Enabled: true,
	}
	if err := dataStore.CreateNode(ctx, manualNode); err != nil {
		t.Fatalf("CreateNode(manual) error = %v", err)
	}

	runtimeNode := &store.Node{
		URI:     "ss://runtime-node#runtime",
		Name:    "runtime-node",
		Source:  store.NodeSourceManifest,
		Enabled: true,
	}
	if err := dataStore.CreateNode(ctx, runtimeNode); err != nil {
		t.Fatalf("CreateNode(runtime) error = %v", err)
	}

	manager := &Manager{
		cfg: &config.Config{
			Nodes: []config.NodeConfig{
				{Name: manualNode.Name, URI: manualNode.URI, Source: config.NodeSourceManual, Port: 12001},
				{Name: runtimeNode.Name, URI: runtimeNode.URI, Source: config.NodeSourceManifest, Port: 12002},
			},
		},
		store:  dataStore,
		logger: defaultLogger{},
	}

	nodes, err := manager.ListConfigNodes(ctx)
	if err != nil {
		t.Fatalf("ListConfigNodes() error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected only persistent config node, got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].URI != manualNode.URI || nodes[0].Source != config.NodeSourceManual {
		t.Fatalf("expected manual node only, got %+v", nodes[0])
	}
}

func TestPrepareNodeLockedAssignsHybridPortsAndCredentials(t *testing.T) {
	manager := &Manager{
		cfg: &config.Config{
			Mode: "hybrid",
			MultiPort: config.MultiPortConfig{
				BasePort: 32000,
				Username: "hybrid-user",
				Password: "hybrid-pass",
			},
			Nodes: []config.NodeConfig{
				{
					Name: "existing",
					URI:  "ss://existing#existing",
					Port: 32000,
				},
			},
		},
	}

	node, err := manager.prepareNodeLocked(config.NodeConfig{
		URI: "ss://new-node#new-node",
	}, "")
	if err != nil {
		t.Fatalf("prepareNodeLocked() error = %v", err)
	}

	if node.Name != "new-node" {
		t.Fatalf("expected name to be derived from URI fragment, got %+v", node)
	}
	if node.Port != 32001 {
		t.Fatalf("expected next hybrid port 32001, got %+v", node)
	}
	if node.Username != "hybrid-user" || node.Password != "hybrid-pass" {
		t.Fatalf("expected hybrid credentials to be applied, got %+v", node)
	}
}

func TestPrepareNodeLockedRejectsHybridPortConflicts(t *testing.T) {
	manager := &Manager{
		cfg: &config.Config{
			Mode: "hybrid",
			MultiPort: config.MultiPortConfig{
				BasePort: 32000,
			},
			Nodes: []config.NodeConfig{
				{
					Name: "existing",
					URI:  "ss://existing#existing",
					Port: 32000,
				},
			},
		},
	}

	_, err := manager.prepareNodeLocked(config.NodeConfig{
		Name: "conflict",
		URI:  "ss://conflict#conflict",
		Port: 32000,
	}, "")
	if !errors.Is(err, monitor.ErrNodeConflict) {
		t.Fatalf("expected hybrid port conflict, got %v", err)
	}
}

func TestNodeOperationsAcceptURIRefs(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "easyproxy.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	node := &store.Node{
		URI:     "ss://disabled-node#disabled-node",
		Name:    "disabled-node",
		Source:  store.NodeSourceManual,
		Enabled: false,
	}
	if err := dataStore.CreateNode(ctx, node); err != nil {
		t.Fatalf("CreateNode() error = %v", err)
	}

	manager := &Manager{
		cfg:    &config.Config{Nodes: []config.NodeConfig{}},
		store:  dataStore,
		logger: defaultLogger{},
	}

	if err := manager.SetNodeEnabled(ctx, node.URI, true); err != nil {
		t.Fatalf("SetNodeEnabled() error = %v", err)
	}
	updated, err := dataStore.GetNodeByURI(ctx, node.URI)
	if err != nil {
		t.Fatalf("GetNodeByURI() error = %v", err)
	}
	if updated == nil || !updated.Enabled {
		t.Fatalf("expected node to be enabled by URI ref, got %+v", updated)
	}

	if err := manager.DeleteNode(ctx, node.URI); err != nil {
		t.Fatalf("DeleteNode() error = %v", err)
	}
	removed, err := dataStore.GetNodeByURI(ctx, node.URI)
	if err != nil {
		t.Fatalf("GetNodeByURI(after delete) error = %v", err)
	}
	if removed != nil {
		t.Fatalf("expected node to be deleted by URI ref, got %+v", removed)
	}
}

func TestSyncMonitorServerLifecycleRebindsSameServerOnListenChange(t *testing.T) {
	getFreeListenPair := func(t *testing.T) (string, string) {
		t.Helper()
		first, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen() error = %v", err)
		}
		second, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = first.Close()
			t.Fatalf("net.Listen() error = %v", err)
		}
		firstAddr := first.Addr().String()
		secondAddr := second.Addr().String()
		_ = first.Close()
		_ = second.Close()
		return firstAddr, secondAddr
	}

	waitReachable := func(t *testing.T, addr string, want bool) {
		t.Helper()
		var lastErr error
		for i := 0; i < 20; i++ {
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				if want {
					return
				}
			} else {
				lastErr = err
				if !want {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		if want {
			t.Fatalf("expected %s to become reachable, last error: %v", addr, lastErr)
		}
		t.Fatalf("expected %s to stop listening", addr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	oldListen, newListen := getFreeListenPair(t)

	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	prevCfg := monitor.Config{Enabled: true, Listen: oldListen}
	currentServer := monitor.NewServer(prevCfg, monitorMgr, log.New(io.Discard, "", 0))
	if currentServer == nil {
		t.Fatal("expected initial monitor server to be created")
	}
	if err := currentServer.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitReachable(t, oldListen, true)

	manager := &Manager{
		monitorMgr:    monitorMgr,
		monitorServer: currentServer,
		monitorCfg: monitor.Config{
			Enabled: true,
			Listen:  newListen,
		},
		logger: defaultLogger{},
	}

	enabled := true
	activeCfg := &config.Config{}
	activeCfg.Management.Enabled = &enabled
	activeCfg.Management.Listen = newListen

	if err := manager.syncMonitorServerLifecycle(ctx, prevCfg, activeCfg); err != nil {
		t.Fatalf("syncMonitorServerLifecycle() error = %v", err)
	}

	waitReachable(t, newListen, true)
	waitReachable(t, oldListen, false)

	if manager.monitorServer == nil {
		t.Fatal("expected monitor server to remain available after restart")
	}
	if manager.monitorServer != currentServer {
		t.Fatal("listen change replaced the monitor server runtime")
	}

	manager.monitorServer.Shutdown(context.Background())
}

func TestSyncMonitorServerLifecycleBindFailureKeepsOldListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldListen := fmt.Sprintf("127.0.0.1:%d", reserveFreePort(t))
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy replacement listen: %v", err)
	}
	defer occupied.Close()
	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()
	prevCfg := monitor.Config{Enabled: true, Listen: oldListen}
	currentServer := monitor.NewServer(prevCfg, monitorMgr, log.New(io.Discard, "", 0))
	if err := currentServer.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer currentServer.Shutdown(context.Background())
	waitReachableAddress(t, oldListen, true)

	manager := &Manager{
		monitorMgr:    monitorMgr,
		monitorServer: currentServer,
		monitorCfg: monitor.Config{
			Enabled: true,
			Listen:  occupied.Addr().String(),
		},
		logger: defaultLogger{},
	}
	enabled := true
	activeCfg := &config.Config{Management: config.ManagementConfig{
		Enabled: &enabled,
		Listen:  occupied.Addr().String(),
	}}
	if err := manager.syncMonitorServerLifecycle(ctx, prevCfg, activeCfg); err == nil {
		t.Fatal("syncMonitorServerLifecycle() error = nil, want bind failure")
	}
	if manager.monitorServer != currentServer {
		t.Fatal("bind failure replaced the monitor server runtime")
	}
	waitReachableAddress(t, oldListen, true)
}

func TestPrepareMonitorServerTransitionStagesTargetPasswordBeforeActivation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldListen := fmt.Sprintf("127.0.0.1:%d", reserveFreePort(t))
	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()
	currentServer := monitor.NewServer(
		monitor.Config{Enabled: true, Listen: oldListen},
		monitorMgr,
		log.New(io.Discard, "", 0),
	)
	if err := currentServer.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer currentServer.Shutdown(context.Background())

	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve target listen: %v", err)
	}
	targetListen := targetListener.Addr().String()
	if err := targetListener.Close(); err != nil {
		t.Fatalf("release target listen: %v", err)
	}
	manager := &Manager{
		monitorMgr:    monitorMgr,
		monitorServer: currentServer,
		logger:        defaultLogger{},
	}
	enabled := true
	targetCfg := &config.Config{Management: config.ManagementConfig{
		Enabled:  &enabled,
		Listen:   targetListen,
		Password: "target-password",
	}}
	transition, err := manager.prepareMonitorServerTransition(targetCfg)
	if err != nil {
		t.Fatalf("prepareMonitorServerTransition() error = %v", err)
	}
	if err := transition.Activate(ctx); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	defer transition.Abort()

	request, err := http.NewRequest(http.MethodGet, "http://"+targetListen+"/api/nodes", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		t.Fatalf("GET target monitor listener: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("target listener status = %d, want 401 before transaction commit", response.StatusCode)
	}
}

func TestPrepareMonitorBindFailureCleansCreatedRuntime(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy management listen: %v", err)
	}
	defer occupied.Close()
	enabled := true
	cfg := &config.Config{Management: config.ManagementConfig{
		Enabled: &enabled,
		Listen:  occupied.Addr().String(),
	}}
	manager := New(cfg, monitor.Config{Enabled: true, Listen: occupied.Addr().String()})
	if err := manager.PrepareMonitor(context.Background()); err == nil {
		t.Fatal("PrepareMonitor() error = nil, want bind failure")
	}
	manager.mu.RLock()
	monitorServer := manager.monitorServer
	monitorMgr := manager.monitorMgr
	manager.mu.RUnlock()
	if monitorServer != nil || monitorMgr != nil {
		t.Fatalf("bind failure retained monitor runtime: server=%p manager=%p", monitorServer, monitorMgr)
	}
}

func waitReachableAddress(t *testing.T, address string, want bool) {
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
			t.Fatalf("listen %s reachable=%v, want %v (last error: %v)", address, err == nil, want, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
