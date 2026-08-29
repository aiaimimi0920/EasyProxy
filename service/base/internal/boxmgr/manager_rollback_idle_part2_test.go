package boxmgr

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"easy_proxies/internal/config"
)

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
