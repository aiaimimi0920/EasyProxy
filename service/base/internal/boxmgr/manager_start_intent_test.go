package boxmgr

import (
	"context"
	"errors"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestReloadIntentTokensAreNestableAndIdempotent(t *testing.T) {
	manager := &Manager{}
	listener := &recordingReloadIntentListener{}
	manager.AddReloadLifecycleListener(listener)

	first, err := manager.BeginReloadIntent(context.Background())
	if err != nil {
		t.Fatalf("first BeginReloadIntent() error = %v", err)
	}
	second, err := manager.BeginReloadIntent(context.Background())
	if err != nil {
		t.Fatalf("second BeginReloadIntent() error = %v", err)
	}
	listener.mu.Lock()
	if listener.active != 2 || listener.begins != 2 {
		t.Fatalf("nested intent state = active:%d begins:%d, want 2/2", listener.active, listener.begins)
	}
	listener.mu.Unlock()

	first.End()
	first.End()
	listener.mu.Lock()
	if listener.active != 1 || listener.ends != 1 {
		t.Fatalf("first intent end state = active:%d ends:%d, want 1/1", listener.active, listener.ends)
	}
	listener.mu.Unlock()

	second.End()
	listener.mu.Lock()
	defer listener.mu.Unlock()
	if listener.active != 0 || listener.ends != 2 {
		t.Fatalf("final intent end state = active:%d ends:%d, want 0/2", listener.active, listener.ends)
	}
}

func TestStartWithoutNodesEntersInitialIdle(t *testing.T) {
	manager := newInitialIdleTestManager(t)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := manager.CurrentReloadState()
	if !state.Idle || state.Config == nil || manager.currentBox != nil {
		t.Fatalf("state=%#v box=%v", state, manager.currentBox)
	}
}

func TestInitialIdleCanReloadWhenNodesAppear(t *testing.T) {
	manager := newInitialIdleTestManager(t)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	withNode := manager.CurrentReloadState().Config.Clone()
	withNode.Nodes = []config.NodeConfig{{Name: "node", URI: "http://127.0.0.1:18080"}}
	if err := manager.Reload(withNode); err != nil {
		t.Fatal(err)
	}
	if manager.CurrentReloadState().Idle {
		t.Fatal("manager remained idle after nodes appeared")
	}
}

func TestReloadIntentBlocksNodeMutationBeforeReloadLock(t *testing.T) {
	manager := &Manager{
		cfg:    &config.Config{Mode: "pool"},
		logger: defaultLogger{},
	}
	intent, err := manager.BeginReloadIntent(context.Background())
	if err != nil {
		t.Fatalf("BeginReloadIntent() error = %v", err)
	}

	createDone := make(chan error, 1)
	go func() {
		_, createErr := manager.CreateNode(context.Background(), config.NodeConfig{
			Name: "intent-blocked",
			URI:  "http://intent-blocked.example:80",
		})
		createDone <- createErr
	}()
	select {
	case err := <-createDone:
		intent.End()
		t.Fatalf("CreateNode completed while reload intent was active: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	intent.End()
	select {
	case err := <-createDone:
		if err != nil {
			t.Fatalf("CreateNode() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CreateNode did not resume after reload intent ended")
	}
}

func TestNodeMutationRechecksCanceledContextAfterReloadIntent(t *testing.T) {
	manager := &Manager{
		cfg:    &config.Config{Mode: "pool"},
		logger: defaultLogger{},
	}
	intent, err := manager.BeginReloadIntent(context.Background())
	if err != nil {
		t.Fatalf("BeginReloadIntent() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	createDone := make(chan error, 1)
	go func() {
		_, createErr := manager.CreateNode(ctx, config.NodeConfig{
			Name: "canceled",
			URI:  "http://canceled.example:80",
		})
		createDone <- createErr
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	intent.End()

	select {
	case err := <-createDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CreateNode() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CreateNode did not return after reload intent ended")
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if len(manager.cfg.Nodes) != 0 {
		t.Fatalf("canceled node mutation changed config: %+v", manager.cfg.Nodes)
	}
}
