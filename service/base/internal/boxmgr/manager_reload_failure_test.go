package boxmgr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestTriggerReloadWithEphemeralNodesKeepsOldOnIdleFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("mode: pool\nmanagement:\n  enabled: false\nnodes: []\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	oldCfg := &config.Config{
		Mode:  "pool",
		Nodes: []config.NodeConfig{{Name: "old-runtime", URI: "http://old-runtime.example:80"}},
	}
	oldCfg.SetFilePath(configPath)
	oldEphemeral := []config.NodeConfig{{Name: "old-runtime", URI: "http://old-runtime.example:80"}}
	prepareErr := errors.New("idle candidate rejected")
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     &fakeManagedBox{name: "old"},
		lastAppliedCfg: snapshotConfig(oldCfg),
		ephemeralNodes: cloneNodes(oldEphemeral),
		logger:         defaultLogger{},
	}
	manager.AddReloadLifecycleListener(&recordingReloadListener{
		events:     &[]string{},
		prepareErr: prepareErr,
	})

	err := manager.TriggerReloadWithEphemeralNodes(context.Background(), nil)
	if !errors.Is(err, prepareErr) {
		t.Fatalf("TriggerReloadWithEphemeralNodes() error = %v, want %v", err, prepareErr)
	}
	manager.mu.RLock()
	got := cloneNodes(manager.ephemeralNodes)
	manager.mu.RUnlock()
	if !reflect.DeepEqual(got, oldEphemeral) {
		t.Fatalf("ephemeral nodes changed after failed idle reload: got %+v, want %+v", got, oldEphemeral)
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

func TestReloadRefreshesRollbackSnapshotAfterPrepareWait(t *testing.T) {
	startErr := errors.New("candidate start failed")
	oldCfg := &config.Config{
		Mode:  "pool",
		Nodes: []config.NodeConfig{{Name: "old"}},
		Routing: config.RoutingConfig{
			FinalPolicy: "PROXY",
		},
	}
	hotCfg := snapshotConfig(oldCfg)
	hotCfg.Routing.FinalPolicy = "DIRECT"
	hotCfg.Routing.Rules = []string{"DOMAIN-SUFFIX,hot.example,DIRECT"}
	newCfg := &config.Config{
		Mode:  "hybrid",
		Nodes: []config.NodeConfig{{Name: "new"}},
		Routing: config.RoutingConfig{
			FinalPolicy: "PROXY",
		},
	}
	candidate := &fakeManagedBox{name: "candidate", startErr: startErr}
	restored := &fakeManagedBox{name: "restored"}
	createdPolicies := []string{}
	boxes := []managedBox{candidate, restored}
	manager := &Manager{
		baseCtx:          context.Background(),
		cfg:              oldCfg,
		currentBox:       &fakeManagedBox{name: "old"},
		lastAppliedCfg:   snapshotConfig(oldCfg),
		logger:           defaultLogger{},
		portReleaseDelay: 0,
		boxFactory: func(_ context.Context, cfg *config.Config) (managedBox, error) {
			createdPolicies = append(createdPolicies, cfg.Routing.FinalPolicy)
			box := boxes[0]
			boxes = boxes[1:]
			return box, nil
		},
	}
	prepare := &blockingPrepareReloadListener{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager.AddReloadLifecycleListener(prepare)

	reloadDone := make(chan error, 1)
	go func() { reloadDone <- manager.Reload(newCfg) }()
	select {
	case <-prepare.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not enter PrepareReload")
	}

	manager.RecordAppliedConfig(hotCfg)
	close(prepare.release)

	select {
	case err := <-reloadDone:
		if !errors.Is(err, startErr) {
			t.Fatalf("Reload() error = %v, want candidate start error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Reload() did not finish")
	}

	if !reflect.DeepEqual(createdPolicies, []string{"PROXY", "DIRECT"}) {
		t.Fatalf("factory policies = %v, want candidate PROXY then hot-applied DIRECT rollback", createdPolicies)
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

func TestReloadRollbackFailureEntersRecoverableIdle(t *testing.T) {
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
			recovered := &fakeManagedBox{events: &events, name: "recovered"}
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
					if factoryCalls == 2 {
						if tc.rollbackErr != nil {
							return nil, tc.rollbackErr
						}
						tc.rollbackBox.events = &events
						return tc.rollbackBox, nil
					}
					return recovered, nil
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
			if !manager.idle || !manager.lastAppliedIdle {
				t.Fatalf("idle state = current:%v applied:%v, want true/true", manager.idle, manager.lastAppliedIdle)
			}
			state := manager.CurrentReloadState()
			if !state.Idle || state.Config == nil || state.Config.Mode != oldCfg.Mode {
				t.Fatalf("reload state = %+v, want old config in idle mode", state)
			}
			if len(lifecycle.failed) != 1 || lifecycle.failed[0].restored {
				t.Fatalf("unexpected failure callback: %+v", lifecycle.failed)
			}
			if tc.rollbackBox != nil && tc.rollbackBox.closes != 1 {
				t.Fatalf("failed rollback box close count = %d, want 1", tc.rollbackBox.closes)
			}

			if err := manager.Reload(newCfg); err != nil {
				t.Fatalf("Reload() after rollback failure error = %v", err)
			}
			if manager.currentBox != recovered || recovered.starts != 1 {
				t.Fatalf("recovered box = %T starts:%d, want current with one start", manager.currentBox, recovered.starts)
			}
			if manager.idle || manager.lastAppliedIdle {
				t.Fatalf("idle state after recovery = current:%v applied:%v, want false/false", manager.idle, manager.lastAppliedIdle)
			}
		})
	}
}
