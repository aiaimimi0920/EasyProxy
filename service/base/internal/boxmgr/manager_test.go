package boxmgr

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/store"
)

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
