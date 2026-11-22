package boxmgr

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/profileselect"
)

func TestReloadCommitsEarlyOnQuorumWhileSlowTailProbesFinishInBackground(t *testing.T) {
	monitorMgr, err := monitor.NewManager(monitor.Config{ProbeTarget: "http://example.com/"})
	if err != nil {
		t.Fatalf("monitor.NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()

	// Seed previous generation so fast-node is prioritized ahead of unknown/slow nodes.
	prevFast := monitorMgr.Register(monitor.NodeInfo{Tag: "fast-node", Name: "fast-node", URI: "http://127.0.0.1:10001"})
	prevFast.RecordSuccessWithLatency(5 * time.Millisecond)

	managementDisabled := false
	oldCfg := &config.Config{
		Mode:       "pool",
		Management: config.ManagementConfig{Enabled: &managementDisabled},
		Nodes:      []config.NodeConfig{{Name: "fast-node", URI: "http://127.0.0.1:10001"}},
	}
	newCfg := &config.Config{
		Mode:       "pool",
		Management: config.ManagementConfig{Enabled: &managementDisabled},
		Nodes: []config.NodeConfig{
			{Name: "slow-1", URI: "http://127.0.0.1:10002"},
			{Name: "slow-2", URI: "http://127.0.0.1:10003"},
			{Name: "fast-node", URI: "http://127.0.0.1:10001"},
		},
		SubscriptionRefresh: config.SubscriptionRefreshConfig{
			MinAvailableNodes:  1,
			HealthCheckTimeout: 2 * time.Second,
		},
	}

	tailRelease := make(chan struct{})
	var tailCompleted atomic.Int32
	oldGate := new(profileselect.Admission)
	oldGate.Commit()

	manager := &Manager{
		baseCtx:          context.Background(),
		cfg:              oldCfg,
		currentBox:       &profileGatedBox{managedBox: &fakeManagedBox{name: "old"}, gate: oldGate},
		lastAppliedCfg:   snapshotConfig(oldCfg),
		monitorMgr:       monitorMgr,
		profiles:         admissionResolver{},
		portReleaseDelay: 0,
		logger:           defaultLogger{},
		boxFactory: func(_ context.Context, cfg *config.Config) (managedBox, error) {
			for _, node := range cfg.Nodes {
				tag := node.Name
				handle := monitorMgr.Register(monitor.NodeInfo{Tag: tag, Name: node.Name, URI: node.URI})
				if tag == "fast-node" {
					handle.SetProbe(func(context.Context) (time.Duration, error) {
						return 5 * time.Millisecond, nil
					})
				} else {
					handle.SetProbe(func(ctx context.Context) (time.Duration, error) {
						select {
						case <-tailRelease:
							tailCompleted.Add(1)
							return 50 * time.Millisecond, nil
						case <-ctx.Done():
							return 0, ctx.Err()
						}
					})
				}
			}
			return &fakeManagedBox{name: "candidate"}, nil
		},
	}

	start := time.Now()
	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- manager.Reload(newCfg)
	}()

	select {
	case err := <-reloadDone:
		if err != nil {
			close(tailRelease)
			t.Fatalf("Reload() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		close(tailRelease)
		t.Fatal("Reload() blocked on slow tail probes instead of committing once MinAvailableNodes was reached")
	}
	elapsed := time.Since(start)
	if elapsed >= 500*time.Millisecond {
		close(tailRelease)
		t.Fatalf("Reload() took %s, expected early commit on quorum", elapsed)
	}

	gated, ok := manager.currentBox.(*profileGatedBox)
	if !ok || gated.gate.Check() != nil {
		close(tailRelease)
		t.Fatal("profile admission gate was not committed immediately after quorum reload")
	}

	// Release slow tail nodes and confirm background probes still complete cleanly.
	close(tailRelease)
	deadline := time.Now().Add(time.Second)
	for tailCompleted.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if tailCompleted.Load() != 2 {
		t.Fatalf("tailCompleted = %d, want 2 background tail probes to finish", tailCompleted.Load())
	}
}

func TestReloadWithEphemeralNodesSkipsRebuildWhenConfigUnchanged(t *testing.T) {
	managementDisabled := false
	ephemeral := []config.NodeConfig{
		{
			Name:       "runtime-us",
			URI:        "http://127.0.0.1:18080#runtime-us",
			Source:     config.NodeSourceManifest,
			SourceKind: "subscription",
			SourceRef:  "manifest:agg",
		},
	}
	baseCfg := &config.Config{
		Mode:       "hybrid",
		MultiPort:  config.MultiPortConfig{Address: "127.0.0.1", BasePort: 24000, Username: "u", Password: "p"},
		Management: config.ManagementConfig{Enabled: &managementDisabled},
		Nodes:      cloneNodes(ephemeral),
	}
	if err := baseCfg.NormalizeWithPortMap(map[string]uint16{}); err != nil {
		t.Fatalf("NormalizeWithPortMap() error = %v", err)
	}

	oldBox := &fakeManagedBox{name: "active"}
	factoryCalls := 0
	notified := 0
	manager := &Manager{
		baseCtx:             context.Background(),
		cfg:                 snapshotConfig(baseCfg),
		currentBox:          oldBox,
		lastAppliedCfg:      snapshotConfig(baseCfg),
		lastAppliedMode:     baseCfg.Mode,
		lastAppliedBasePort: baseCfg.MultiPort.BasePort,
		portReleaseDelay:    0,
		logger:              defaultLogger{},
		boxFactory: func(context.Context, *config.Config) (managedBox, error) {
			factoryCalls++
			return &fakeManagedBox{name: "rebuilt"}, nil
		},
	}
	manager.AddConfigListener(admissionConfigListener(func(*config.Config) {
		notified++
	}))

	candidateCfg := snapshotConfig(baseCfg)
	portMap := manager.CurrentPortMap()
	if err := manager.ReloadWithPortMapAndEphemeralNodes(candidateCfg, portMap, ephemeral); err != nil {
		t.Fatalf("ReloadWithPortMapAndEphemeralNodes() error = %v", err)
	}
	if factoryCalls != 0 || oldBox.closes != 0 || manager.currentBox != oldBox {
		t.Fatalf("unchanged ephemeral reload rebuilt box: factoryCalls=%d closes=%d", factoryCalls, oldBox.closes)
	}
	if notified != 1 || len(manager.CurrentEphemeralNodes()) != 1 {
		t.Fatalf("bookkeeping not updated on skipped rebuild: notified=%d ephemeral=%d", notified, len(manager.CurrentEphemeralNodes()))
	}

	// Explicit operator Reload must still force a full rebuild even when config is identical.
	if err := manager.Reload(snapshotConfig(baseCfg)); err != nil {
		t.Fatalf("explicit Reload() error = %v", err)
	}
	if factoryCalls != 1 || oldBox.closes != 1 {
		t.Fatalf("explicit Reload() did not rebuild box: factoryCalls=%d closes=%d", factoryCalls, oldBox.closes)
	}
}
