package boxmgr

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"easy_proxies/internal/builder"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
)

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

func TestStartEntersIdleWhenAllProxyNodesAreInvalid(t *testing.T) {
	managementDisabled := false
	cfg := &config.Config{
		Mode:       "pool",
		Management: config.ManagementConfig{Enabled: &managementDisabled},
		Routing:    config.RoutingConfig{Enabled: true},
		Nodes:      []config.NodeConfig{{Name: "broken", URI: "unsupported://node.example"}},
	}
	manager := New(cfg, monitor.Config{})
	manager.boxFactory = func(context.Context, *config.Config) (managedBox, error) {
		return nil, fmt.Errorf("wrapped build failure: %w", builder.ErrNoValidNodes)
	}

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want idle success", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	if manager.currentBox != nil || !manager.idle || !manager.lastAppliedIdle {
		t.Fatalf("manager state = current=%T idle=%v lastIdle=%v, want idle", manager.currentBox, manager.idle, manager.lastAppliedIdle)
	}
}

func TestReloadEntersIdleWhenAllProxyNodesBecomeInvalid(t *testing.T) {
	managementDisabled := false
	oldCfg := &config.Config{
		Mode:       "pool",
		Management: config.ManagementConfig{Enabled: &managementDisabled},
		Routing:    config.RoutingConfig{Enabled: true},
		Nodes:      []config.NodeConfig{{Name: "working", URI: "socks5://127.0.0.1:1080"}},
	}
	oldBox := &fakeManagedBox{name: "old"}
	manager := New(oldCfg, monitor.Config{})
	manager.portReleaseDelay = 0
	manager.boxFactory = func(context.Context, *config.Config) (managedBox, error) {
		return oldBox, nil
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	invalidCfg := oldCfg.Clone()
	invalidCfg.Nodes = []config.NodeConfig{{Name: "broken", URI: "unsupported://node.example"}}
	if err := manager.Reload(invalidCfg); err != nil {
		t.Fatalf("Reload() error = %v, want idle success", err)
	}
	if oldBox.closes != 1 {
		t.Fatalf("old box close count = %d, want 1", oldBox.closes)
	}
	if manager.currentBox != nil || !manager.idle || !manager.lastAppliedIdle {
		t.Fatalf("manager state = current=%T idle=%v lastIdle=%v, want idle", manager.currentBox, manager.idle, manager.lastAppliedIdle)
	}
}

func TestRecordAppliedConfigRefreshesIndependentRollbackSnapshot(t *testing.T) {
	oldUseDefaults := true
	appliedUseDefaults := false
	oldCfg := &config.Config{
		Mode:      "pool",
		MultiPort: config.MultiPortConfig{BasePort: 25000},
		Nodes:     []config.NodeConfig{{Name: "old-node", URI: "http://old.example:80"}},
		Routing: config.RoutingConfig{
			Enabled:         true,
			DefaultStrategy: "stable",
			UseDefaultRules: &oldUseDefaults,
			FinalPolicy:     "PROXY",
			Rules:           []string{"MATCH,PROXY"},
			RuleProviders:   []config.RuleProvider{{URL: "https://old.example/rules.txt", Policy: "PROXY"}},
			Session:         config.SessionConfig{TTL: 10 * time.Minute},
		},
		GeoIP: config.GeoIPConfig{
			Enabled:            false,
			DatabasePath:       "old.mmdb",
			Listen:             "127.0.0.1",
			Port:               24000,
			AutoUpdateEnabled:  false,
			AutoUpdateInterval: 24 * time.Hour,
		},
	}
	appliedCfg := &config.Config{
		Mode:      "hybrid",
		MultiPort: config.MultiPortConfig{BasePort: 33000},
		Nodes:     []config.NodeConfig{{Name: "new-node", URI: "http://new.example:80"}},
		Routing: config.RoutingConfig{
			Enabled:         true,
			DefaultStrategy: "session",
			UseDefaultRules: &appliedUseDefaults,
			FinalPolicy:     "DIRECT",
			Rules:           []string{"DOMAIN-SUFFIX,hot.example,DIRECT"},
			RuleProviders:   []config.RuleProvider{{URL: "https://hot.example/rules.txt", Policy: "DIRECT"}},
			Session:         config.SessionConfig{TTL: 20 * time.Minute},
			LongLived: config.LongLivedConfig{
				MinUptime:      3 * time.Hour,
				MinSuccessRate: 0.8,
			},
		},
		GeoIP: config.GeoIPConfig{
			Enabled:            true,
			DatabasePath:       "hot.mmdb",
			Listen:             "0.0.0.0",
			Port:               25000,
			AutoUpdateEnabled:  true,
			AutoUpdateInterval: time.Hour,
		},
	}
	manager := &Manager{
		lastAppliedCfg:      snapshotConfig(oldCfg),
		lastAppliedIdle:     true,
		lastAppliedMode:     oldCfg.Mode,
		lastAppliedBasePort: oldCfg.MultiPort.BasePort,
	}

	manager.RecordAppliedConfig(appliedCfg)

	if manager.lastAppliedCfg == nil || manager.lastAppliedCfg == appliedCfg {
		t.Fatalf("rollback config is not an independent snapshot: source=%p recorded=%p", appliedCfg, manager.lastAppliedCfg)
	}
	if manager.lastAppliedCfg.Mode != oldCfg.Mode || manager.lastAppliedCfg.MultiPort.BasePort != oldCfg.MultiPort.BasePort {
		t.Fatalf("rollback topology = %s/%d, want unapplied topology %s/%d", manager.lastAppliedCfg.Mode, manager.lastAppliedCfg.MultiPort.BasePort, oldCfg.Mode, oldCfg.MultiPort.BasePort)
	}
	if manager.lastAppliedMode != oldCfg.Mode || manager.lastAppliedBasePort != oldCfg.MultiPort.BasePort {
		t.Fatalf("topology markers = %s/%d, want %s/%d", manager.lastAppliedMode, manager.lastAppliedBasePort, oldCfg.Mode, oldCfg.MultiPort.BasePort)
	}
	if manager.lastAppliedCfg.Nodes[0].Name != oldCfg.Nodes[0].Name {
		t.Fatalf("rollback nodes changed before reload: %+v", manager.lastAppliedCfg.Nodes)
	}
	if manager.lastAppliedCfg.Routing.Session.TTL != oldCfg.Routing.Session.TTL {
		t.Fatalf("rollback session TTL = %s, want unapplied %s", manager.lastAppliedCfg.Routing.Session.TTL, oldCfg.Routing.Session.TTL)
	}
	if manager.lastAppliedCfg.Routing.FinalPolicy != appliedCfg.Routing.FinalPolicy ||
		manager.lastAppliedCfg.Routing.DefaultStrategy != appliedCfg.Routing.DefaultStrategy ||
		manager.lastAppliedCfg.Routing.UseDefaultRules == nil || *manager.lastAppliedCfg.Routing.UseDefaultRules ||
		manager.lastAppliedCfg.Routing.Rules[0] != appliedCfg.Routing.Rules[0] ||
		manager.lastAppliedCfg.Routing.RuleProviders[0].URL != appliedCfg.Routing.RuleProviders[0].URL ||
		manager.lastAppliedCfg.Routing.LongLived.MinUptime != appliedCfg.Routing.LongLived.MinUptime {
		t.Fatalf("hot-applied routing fields were not merged: %+v", manager.lastAppliedCfg.Routing)
	}
	if !manager.lastAppliedCfg.GeoIP.Enabled || manager.lastAppliedCfg.GeoIP.DatabasePath != appliedCfg.GeoIP.DatabasePath {
		t.Fatalf("hot-applied GeoIP engine fields were not merged: %+v", manager.lastAppliedCfg.GeoIP)
	}
	if manager.lastAppliedCfg.GeoIP.Listen != oldCfg.GeoIP.Listen ||
		manager.lastAppliedCfg.GeoIP.Port != oldCfg.GeoIP.Port ||
		manager.lastAppliedCfg.GeoIP.AutoUpdateEnabled != oldCfg.GeoIP.AutoUpdateEnabled ||
		manager.lastAppliedCfg.GeoIP.AutoUpdateInterval != oldCfg.GeoIP.AutoUpdateInterval {
		t.Fatalf("structural GeoIP fields changed before reload: %+v", manager.lastAppliedCfg.GeoIP)
	}
	if !manager.lastAppliedIdle {
		t.Fatal("recording a hot-applied config changed the active idle state")
	}

	appliedCfg.Mode = "mutated"
	appliedCfg.Routing.Rules[0] = "MATCH,PROXY"
	appliedCfg.Routing.RuleProviders[0].URL = "https://mutated.example/rules.txt"
	if manager.lastAppliedCfg.Mode != "pool" || manager.lastAppliedCfg.Nodes[0].Name != "old-node" || manager.lastAppliedCfg.Routing.Rules[0] != "DOMAIN-SUFFIX,hot.example,DIRECT" || manager.lastAppliedCfg.Routing.RuleProviders[0].URL != "https://hot.example/rules.txt" {
		t.Fatalf("rollback snapshot changed with caller mutation: %+v", manager.lastAppliedCfg)
	}
}

func TestRecordAppliedConfigMergesLocalServerCredentialsIntoRollbackSnapshot(t *testing.T) {
	oldCfg := &config.Config{
		Mode:       "pool",
		Listener:   config.ListenerConfig{Username: "old-user", Password: "old-secret"},
		Management: config.ManagementConfig{Password: "old-secret"},
		LocalServer: config.LocalServerConfig{
			Enabled:              true,
			Listen:               "127.0.0.1:22323",
			Auth:                 config.LocalServerAuthConfig{Username: "old-user", Password: "old-secret"},
			CredentialGeneration: 2,
		},
	}
	appliedCfg := snapshotConfig(oldCfg)
	appliedCfg.LocalServer.SharedRevision = 2
	appliedCfg.LocalServer.Auth = config.LocalServerAuthConfig{Username: "new-user", Password: "new-secret"}
	appliedCfg.LocalServer.CredentialGeneration = 3
	appliedCfg.Listener.Username = "new-user"
	appliedCfg.Listener.Password = "new-secret"
	appliedCfg.Management.Password = "new-secret"
	manager := &Manager{lastAppliedCfg: snapshotConfig(oldCfg)}

	manager.RecordAppliedConfig(appliedCfg)

	recorded := manager.lastAppliedCfg
	if recorded.LocalServer.Auth != appliedCfg.LocalServer.Auth || recorded.LocalServer.CredentialGeneration != 3 || recorded.LocalServer.SharedRevision != 2 {
		t.Fatalf("canonical credentials were not recorded: %#v", recorded.LocalServer)
	}
	if recorded.Listener.Username != "new-user" || recorded.Listener.Password != "new-secret" || recorded.Management.Password != "new-secret" {
		t.Fatalf("derived credentials were not recorded: listener=%#v management=%q", recorded.Listener, recorded.Management.Password)
	}
	if recorded.LocalServer.Listen != oldCfg.LocalServer.Listen || !recorded.LocalServer.Enabled {
		t.Fatalf("credential hot apply changed Local Server topology: %#v", recorded.LocalServer)
	}
}

func TestRecordAppliedConfigMergesLocalServerSharedProfileIntoRollbackSnapshot(t *testing.T) {
	oldLongLived := false
	newLongLived := true
	oldCfg := &config.Config{
		Mode: "pool",
		Routing: config.RoutingConfig{
			Enabled:    true,
			Listen:     "127.0.0.1:22323",
			NodeFilter: config.RoutingNodeFilterConfig{Countries: []string{"US"}, LongLived: &oldLongLived},
			Session:    config.SessionConfig{TTL: 10 * time.Minute},
		},
		LocalServer: config.LocalServerConfig{
			Enabled:        true,
			Listen:         "127.0.0.1:22323",
			SharedRevision: 1,
		},
	}
	appliedCfg := snapshotConfig(oldCfg)
	appliedCfg.Routing.Enabled = false
	appliedCfg.Routing.NodeFilter = config.RoutingNodeFilterConfig{Countries: []string{"JP"}, Regions: []string{"asia"}, LongLived: &newLongLived}
	appliedCfg.Routing.Session.TTL = 45 * time.Minute
	appliedCfg.LocalServer.SharedRevision = 2
	manager := &Manager{lastAppliedCfg: snapshotConfig(oldCfg)}

	manager.RecordAppliedConfig(appliedCfg)

	recorded := manager.lastAppliedCfg
	if recorded.LocalServer.SharedRevision != 2 || recorded.Routing.Enabled || recorded.Routing.Session.TTL != 45*time.Minute {
		t.Fatalf("shared hot fields were not recorded: local=%#v routing=%#v", recorded.LocalServer, recorded.Routing)
	}
	if len(recorded.Routing.NodeFilter.Countries) != 1 || recorded.Routing.NodeFilter.Countries[0] != "JP" || len(recorded.Routing.NodeFilter.Regions) != 1 || recorded.Routing.NodeFilter.Regions[0] != "asia" || recorded.Routing.NodeFilter.LongLived == nil || !*recorded.Routing.NodeFilter.LongLived {
		t.Fatalf("shared node filter was not recorded: %#v", recorded.Routing.NodeFilter)
	}
	if recorded.Routing.Listen != oldCfg.Routing.Listen || recorded.LocalServer.Listen != oldCfg.LocalServer.Listen || !recorded.LocalServer.Enabled {
		t.Fatalf("shared hot apply changed topology: local=%#v routing=%#v", recorded.LocalServer, recorded.Routing)
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
