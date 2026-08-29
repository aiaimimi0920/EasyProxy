package boxmgr

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
)

func TestReloadReleasesMonitorConfigLockBeforeConfigNotification(t *testing.T) {
	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("monitor.NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()

	managementEnabled := false
	oldCfg := &config.Config{
		Mode:       "pool",
		Management: config.ManagementConfig{Enabled: &managementEnabled},
	}
	newCfg := oldCfg.Clone()
	newCfg.Mode = "hybrid"
	server := monitor.NewServer(monitor.Config{}, monitorMgr, log.New(io.Discard, "", 0))
	server.SetConfig(oldCfg)

	oldBox := &fakeManagedBox{name: "old"}
	candidate := &fakeManagedBox{name: "candidate"}
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     oldBox,
		monitorMgr:     monitorMgr,
		monitorServer:  server,
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			return candidate, nil
		},
	}
	probe := &configCommitLockProbe{
		server:  server,
		manager: manager,
		result:  make(chan error, 1),
	}
	manager.AddConfigListener(probe)

	reloadDone := make(chan error, 1)
	go func() { reloadDone <- manager.Reload(newCfg) }()
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("Reload() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Reload deadlocked while config listener synchronously called SetConfig")
	}
	if err := <-probe.result; err != nil {
		t.Fatal(err)
	}
}

func TestEnterIdleReleasesMonitorConfigLockBeforeConfigNotification(t *testing.T) {
	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("monitor.NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()

	managementEnabled := false
	oldCfg := &config.Config{
		Mode:       "pool",
		Nodes:      []config.NodeConfig{{Name: "old", URI: "http://127.0.0.1:1"}},
		Management: config.ManagementConfig{Enabled: &managementEnabled},
	}
	idleCfg := oldCfg.Clone()
	idleCfg.Mode = "hybrid"
	idleCfg.Nodes = nil
	server := monitor.NewServer(monitor.Config{}, monitorMgr, log.New(io.Discard, "", 0))
	server.SetConfig(oldCfg)

	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     &fakeManagedBox{name: "old"},
		monitorMgr:     monitorMgr,
		monitorServer:  server,
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
	}
	probe := &configCommitLockProbe{
		server:  server,
		manager: manager,
		result:  make(chan error, 1),
	}
	manager.AddConfigListener(probe)

	idleDone := make(chan error, 1)
	go func() { idleDone <- manager.enterIdle(idleCfg) }()
	select {
	case err := <-idleDone:
		if err != nil {
			t.Fatalf("enterIdle() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("enterIdle deadlocked while config listener synchronously called SetConfig")
	}
	if err := <-probe.result; err != nil {
		t.Fatal(err)
	}
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
	intentListener := &recordingReloadIntentListener{}
	configListener := &recordingConfigListener{events: &events}
	manager.AddReloadLifecycleListener(lifecycle)
	manager.AddReloadLifecycleListener(intentListener)
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
	intentListener.mu.Lock()
	prepareActive := intentListener.prepareActive
	active := intentListener.active
	intentListener.mu.Unlock()
	if prepareActive != 1 || active != 0 {
		t.Fatalf("reload intent lifecycle = prepare-active:%d active:%d, want 1/0", prepareActive, active)
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

func TestReloadWithPortMapAndEphemeralNodesPublishesAfterSuccessfulReload(t *testing.T) {
	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("monitor.NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()
	managementEnabled := false

	oldCfg := &config.Config{
		Mode:       "pool",
		Nodes:      []config.NodeConfig{{Name: "old", URI: "http://old.example:80"}},
		Management: config.ManagementConfig{Enabled: &managementEnabled},
	}
	newCfg := &config.Config{
		Mode:       "pool",
		Nodes:      []config.NodeConfig{{Name: "new", URI: "http://new.example:80"}},
		Management: config.ManagementConfig{Enabled: &managementEnabled},
	}
	oldEphemeral := []config.NodeConfig{{Name: "ephemeral-old", URI: "http://ephemeral-old.example:80"}}
	newEphemeral := []config.NodeConfig{{Name: "ephemeral-new", URI: "http://ephemeral-new.example:80"}}
	manager := &Manager{
		baseCtx:         context.Background(),
		cfg:             oldCfg,
		currentBox:      &fakeManagedBox{name: "old"},
		lastAppliedCfg:  snapshotConfig(oldCfg),
		lastAppliedMode: oldCfg.Mode,
		monitorMgr:      monitorMgr,
		logger:          defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			handle := monitorMgr.Register(monitor.NodeInfo{Tag: "new", Name: "new"})
			handle.SetProbe(func(context.Context) (time.Duration, error) {
				return time.Millisecond, nil
			})
			return &fakeManagedBox{name: "candidate"}, nil
		},
		ephemeralNodes: cloneNodes(oldEphemeral),
	}
	lifecycle := &blockingCompleteReloadListener{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager.AddReloadLifecycleListener(lifecycle)
	commitProbe := &ephemeralCommitProbe{
		manager: manager,
		seen:    make(chan []config.NodeConfig, 1),
	}
	manager.AddConfigListener(commitProbe)

	done := make(chan error, 1)
	go func() {
		done <- manager.ReloadWithPortMapAndEphemeralNodes(newCfg, nil, newEphemeral)
	}()
	select {
	case <-lifecycle.entered:
	case <-time.After(time.Second):
		t.Fatal("reload did not reach CompleteReload")
	}

	manager.mu.RLock()
	beforeCommit := cloneNodes(manager.ephemeralNodes)
	manager.mu.RUnlock()
	if !reflect.DeepEqual(beforeCommit, oldEphemeral) {
		t.Fatalf("ephemeral nodes published before reload completed: got %+v, want %+v", beforeCommit, oldEphemeral)
	}

	close(lifecycle.release)
	if err := <-done; err != nil {
		t.Fatalf("ReloadWithPortMapAndEphemeralNodes() error = %v", err)
	}
	manager.mu.RLock()
	afterCommit := cloneNodes(manager.ephemeralNodes)
	manager.mu.RUnlock()
	if !reflect.DeepEqual(afterCommit, newEphemeral) {
		t.Fatalf("ephemeral nodes after successful reload = %+v, want %+v", afterCommit, newEphemeral)
	}
	if seen := <-commitProbe.seen; !reflect.DeepEqual(seen, newEphemeral) {
		t.Fatalf("config listener observed ephemeral nodes %+v, want committed %+v", seen, newEphemeral)
	}
}

func TestReloadWithPortMapAndEphemeralNodesKeepsOldOnReloadFailure(t *testing.T) {
	oldCfg := &config.Config{
		Mode:  "pool",
		Nodes: []config.NodeConfig{{Name: "old", URI: "http://old.example:80"}},
	}
	newCfg := &config.Config{
		Mode:  "pool",
		Nodes: []config.NodeConfig{{Name: "new", URI: "http://new.example:80"}},
	}
	oldEphemeral := []config.NodeConfig{{Name: "ephemeral-old", URI: "http://ephemeral-old.example:80"}}
	newEphemeral := []config.NodeConfig{{Name: "ephemeral-new", URI: "http://ephemeral-new.example:80"}}
	startErr := errors.New("candidate start failed")
	manager := &Manager{
		baseCtx:         context.Background(),
		cfg:             oldCfg,
		currentBox:      &fakeManagedBox{name: "old"},
		lastAppliedCfg:  snapshotConfig(oldCfg),
		lastAppliedMode: oldCfg.Mode,
		logger:          defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			return &fakeManagedBox{name: "candidate", startErr: startErr}, nil
		},
		ephemeralNodes: cloneNodes(oldEphemeral),
	}

	err := manager.ReloadWithPortMapAndEphemeralNodes(newCfg, nil, newEphemeral)
	if err == nil || !strings.Contains(err.Error(), startErr.Error()) {
		t.Fatalf("ReloadWithPortMapAndEphemeralNodes() error = %v, want candidate start failure", err)
	}
	manager.mu.RLock()
	got := cloneNodes(manager.ephemeralNodes)
	manager.mu.RUnlock()
	if !reflect.DeepEqual(got, oldEphemeral) {
		t.Fatalf("ephemeral nodes changed after failed reload: got %+v, want %+v", got, oldEphemeral)
	}
}

func TestNodeMutationWaitsForReloadCommit(t *testing.T) {
	oldCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "old", URI: "http://old.example:80"}}}
	newCfg := &config.Config{Mode: "pool", Nodes: []config.NodeConfig{{Name: "new", URI: "http://new.example:80"}}}
	oldBox := &fakeManagedBox{name: "old"}
	candidate := &fakeManagedBox{name: "candidate"}
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            oldCfg,
		currentBox:     oldBox,
		lastAppliedCfg: snapshotConfig(oldCfg),
		logger:         defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			return candidate, nil
		},
	}
	lifecycle := &blockingCompleteReloadListener{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager.AddReloadLifecycleListener(lifecycle)

	reloadDone := make(chan error, 1)
	go func() { reloadDone <- manager.Reload(newCfg) }()
	select {
	case <-lifecycle.entered:
	case <-time.After(time.Second):
		t.Fatal("reload did not reach CompleteReload")
	}

	createDone := make(chan error, 1)
	go func() {
		_, err := manager.CreateNode(context.Background(), config.NodeConfig{
			Name: "late",
			URI:  "http://late.example:80",
		})
		createDone <- err
	}()
	select {
	case err := <-createDone:
		t.Fatalf("CreateNode completed before reload commit: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(lifecycle.release)
	if err := <-reloadDone; err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if err := <-createDone; err != nil {
		t.Fatalf("CreateNode() error = %v", err)
	}

	if len(manager.lastAppliedCfg.Nodes) != 1 || manager.lastAppliedCfg.Nodes[0].Name != "new" {
		t.Fatalf("last-applied nodes were polluted by the late mutation: %+v", manager.lastAppliedCfg.Nodes)
	}
}

func TestTriggerReloadCapturesConfigAfterReloadSerialization(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	oldYAML := []byte("mode: pool\nskip_cert_verify: false\nmanagement:\n  enabled: false\nnodes: []\n")
	if err := os.WriteFile(configPath, oldYAML, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	oldCfg := &config.Config{Mode: "pool"}
	oldCfg.SetFilePath(configPath)
	oldBox := &fakeManagedBox{name: "old"}
	manager := &Manager{
		baseCtx:             context.Background(),
		cfg:                 oldCfg,
		currentBox:          oldBox,
		lastAppliedCfg:      snapshotConfig(oldCfg),
		lastAppliedMode:     oldCfg.Mode,
		lastAppliedBasePort: oldCfg.MultiPort.BasePort,
		logger:              defaultLogger{},
		portReleaseDelay:    0,
	}
	intentListener := &signalingReloadIntentListener{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager.AddReloadLifecycleListener(intentListener)

	reloadDone := make(chan error, 1)
	go func() { reloadDone <- manager.TriggerReload(context.Background()) }()
	select {
	case <-intentListener.started:
	case <-time.After(time.Second):
		t.Fatal("TriggerReload did not begin its reload intent")
	}
	manager.reloadMu.Lock()
	close(intentListener.release)
	// The reload mutex is deliberately held by the test. A correct TriggerReload
	// must wait before reading disk, so this edit is the authoritative target.
	time.Sleep(100 * time.Millisecond)
	newYAML := []byte("mode: pool\nskip_cert_verify: true\nmanagement:\n  enabled: false\nnodes: []\n")
	if err := os.WriteFile(configPath, newYAML, 0o644); err != nil {
		manager.reloadMu.Unlock()
		t.Fatalf("WriteFile() updated config error = %v", err)
	}
	manager.reloadMu.Unlock()

	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("TriggerReload() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TriggerReload did not finish")
	}
	manager.mu.RLock()
	capturedSkip := manager.cfg != nil && manager.cfg.SkipCertVerify
	manager.mu.RUnlock()
	if !capturedSkip {
		t.Fatal("TriggerReload captured the config before acquiring reload serialization")
	}
}

func TestTriggerReloadRechecksCanceledContextAfterSerializationWait(t *testing.T) {
	manager := &Manager{
		baseCtx:        context.Background(),
		cfg:            &config.Config{Mode: "pool"},
		currentBox:     &fakeManagedBox{name: "old"},
		lastAppliedCfg: &config.Config{Mode: "pool"},
		logger:         defaultLogger{},
	}
	manager.reloadMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- manager.TriggerReload(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	manager.reloadMu.Unlock()

	select {
	case err := <-reloadDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("TriggerReload() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TriggerReload did not return after serialization wait was released")
	}
}
