package boxmgr

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestTriggerReloadWithEphemeralNodesClearsOnlyAfterIdleCommit(t *testing.T) {
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
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     &fakeManagedBox{name: "old"},
		lastAppliedCfg: snapshotConfig(oldCfg),
		ephemeralNodes: cloneNodes(oldEphemeral),
		logger:         defaultLogger{},
	}
	lifecycle := &blockingCompleteReloadListener{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager.AddReloadLifecycleListener(lifecycle)

	done := make(chan error, 1)
	go func() {
		done <- manager.TriggerReloadWithEphemeralNodes(context.Background(), nil)
	}()
	select {
	case <-lifecycle.entered:
	case <-time.After(time.Second):
		t.Fatal("idle reload did not reach CompleteReload")
	}
	manager.mu.RLock()
	beforeCommit := cloneNodes(manager.ephemeralNodes)
	manager.mu.RUnlock()
	if !reflect.DeepEqual(beforeCommit, oldEphemeral) {
		t.Fatalf("ephemeral nodes cleared before idle reload committed: got %+v, want %+v", beforeCommit, oldEphemeral)
	}

	close(lifecycle.release)
	if err := <-done; err != nil {
		t.Fatalf("TriggerReloadWithEphemeralNodes() error = %v", err)
	}
	manager.mu.RLock()
	afterCommit := cloneNodes(manager.ephemeralNodes)
	manager.mu.RUnlock()
	if len(afterCommit) != 0 {
		t.Fatalf("ephemeral nodes after idle commit = %+v, want empty", afterCommit)
	}
}
