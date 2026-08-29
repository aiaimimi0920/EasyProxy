package monitor

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestReloadWindowRejectsPersistedEdits(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	managementEnabled := false
	cfg := &config.Config{
		Mode:       "pool",
		Management: config.ManagementConfig{Enabled: &managementEnabled},
	}
	cfg.SetFilePath(configPath)
	server := NewServer(Config{}, mgr, log.New(io.Discard, "", 0))
	server.SetConfig(cfg)
	server.BeginReloadWindow()
	defer server.EndReloadWindow()

	if _, err := server.updateAllSettingsWithReload(allSettingsRequest{
		Mode:                          "hybrid",
		LogLevel:                      "info",
		ListenerAddress:               "0.0.0.0",
		ListenerPort:                  8080,
		ListenerProtocol:              "http",
		MultiPortAddress:              "0.0.0.0",
		MultiPortBasePort:             10000,
		MultiPortProtocol:             "http",
		PoolMode:                      "auto",
		PoolBlacklistDuration:         "1m",
		SubRefreshInterval:            "1m",
		SubRefreshTimeout:             "30s",
		SubRefreshHealthCheckTimeout:  "30s",
		SubRefreshDrainTimeout:        "10s",
		SourceSyncRefreshInterval:     "1m",
		SourceSyncRequestTimeout:      "30s",
		GeoIPAutoUpdateInterval:       "1h",
		ManagementHealthCheckInterval: "1m",
	}); !errors.Is(err, errReloadInProgress) {
		t.Fatalf("settings update error = %v, want reload-in-progress", err)
	}
	if _, err := server.updateRoutingConfig(routingConfigPayload{
		Enabled:            true,
		DefaultStrategy:    "stable",
		UseDefaultRules:    true,
		FinalPolicy:        "DIRECT",
		LongLivedMinUptime: "1h",
		SessionTTL:         "10m",
	}); !errors.Is(err, errReloadInProgress) {
		t.Fatalf("routing update error = %v, want reload-in-progress", err)
	}

	cfg.RLock()
	defer cfg.RUnlock()
	if cfg.Mode != "pool" || cfg.Routing.Enabled {
		t.Fatalf("reload-window edits mutated config: mode=%q routing=%v", cfg.Mode, cfg.Routing.Enabled)
	}
}

func TestServerStartReturnsBindError(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy listen: %v", err)
	}
	defer occupied.Close()

	server := NewServer(Config{Enabled: true, Listen: occupied.Addr().String()}, mgr, log.New(io.Discard, "", 0))
	defer shutdownLifecycleServer(server)
	if err := server.Start(context.Background()); err == nil {
		t.Fatal("Start() error = nil, want bind failure")
	}
}

func TestValidateManagementListenerAuth(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		listen   string
		password string
		wantErr  bool
	}{
		{name: "disabled wildcard", listen: "0.0.0.0:29888"},
		{name: "IPv4 loopback", enabled: true, listen: "127.0.0.1:29888"},
		{name: "IPv6 loopback", enabled: true, listen: "[::1]:29888"},
		{name: "localhost", enabled: true, listen: "localhost:29888"},
		{name: "authenticated wildcard", enabled: true, listen: "0.0.0.0:29888", password: "secret"},
		{name: "IPv4 wildcard", enabled: true, listen: "0.0.0.0:29888", wantErr: true},
		{name: "IPv6 wildcard", enabled: true, listen: "[::]:29888", wantErr: true},
		{name: "LAN address", enabled: true, listen: "192.0.2.10:29888", wantErr: true},
		{name: "hostname", enabled: true, listen: "management.internal:29888", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateManagementListenerAuth(tt.enabled, tt.listen, tt.password)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateManagementListenerAuth() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestServerStartRejectsUnauthenticatedNonLoopbackListener(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	server := NewServer(Config{Enabled: true, Listen: "0.0.0.0:0"}, mgr, log.New(io.Discard, "", 0))
	defer shutdownLifecycleServer(server)
	err = server.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "requires a password") {
		t.Fatalf("Start() error = %v, want non-loopback password requirement", err)
	}
}

func TestListenerTransitionBindFailureKeepsOldListener(t *testing.T) {
	server, oldListen := startLifecycleTestServer(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy replacement listen: %v", err)
	}
	defer occupied.Close()

	if _, err := server.PrepareListener(true, occupied.Addr().String()); err == nil {
		t.Fatal("PrepareListener() error = nil, want bind failure")
	}
	waitLifecycleListen(t, oldListen, true)
}

func TestListenerTransitionRollbackKeepsOldAndClosesCandidate(t *testing.T) {
	server, oldListen := startLifecycleTestServer(t)
	newListen := freeLifecycleListenDifferent(t, oldListen)
	transition, err := server.PrepareListener(true, newListen)
	if err != nil {
		t.Fatalf("PrepareListener() error = %v", err)
	}
	if err := transition.Activate(context.Background()); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	waitLifecycleListen(t, oldListen, true)
	waitLifecycleListen(t, newListen, true)
	if err := transition.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	waitLifecycleListen(t, oldListen, true)
	waitLifecycleListen(t, newListen, false)
}

func TestListenerTransitionSerializesPersistedConfigUpdates(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	enabled := true
	oldCfg := &config.Config{Management: config.ManagementConfig{Enabled: &enabled}}
	oldCfg.SetFilePath(configPath)
	targetCfg := oldCfg.Clone()
	targetCfg.SetFilePath(configPath)
	server := NewServer(Config{}, mgr, log.New(io.Discard, "", 0))
	server.SetConfig(oldCfg)

	transition, err := server.PrepareListener(false, "", targetCfg)
	if err != nil {
		t.Fatalf("PrepareListener() error = %v", err)
	}
	if server.configUpdateMu.TryLock() {
		server.configUpdateMu.Unlock()
		t.Fatal("listener transition did not hold the config update lock")
	}

	updateDone := make(chan error, 1)
	go func() {
		_, updateErr := server.updateRoutingConfig(routingConfigPayload{
			Enabled:            true,
			DefaultStrategy:    "stable",
			UseDefaultRules:    true,
			FinalPolicy:        "DIRECT",
			LongLivedMinUptime: "2h",
			LongLivedMinRate:   0.9,
			SessionTTL:         "10m",
		})
		updateDone <- updateErr
	}()

	select {
	case err := <-updateDone:
		t.Fatalf("config update completed before transition commit: %v", err)
	case <-time.After(40 * time.Millisecond):
	}

	if err := transition.Activate(context.Background()); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	transition.Finalize()
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("config update after transition commit failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("config update did not resume after transition commit")
	}

	targetCfg.RLock()
	finalPolicy := targetCfg.Routing.FinalPolicy
	targetCfg.RUnlock()
	if finalPolicy != "DIRECT" {
		t.Fatalf("config update wrote the retired source instead of target: final policy %q", finalPolicy)
	}
}

func TestUpdateAllSettingsWithReloadUsesCommittedConfigSource(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	managementEnabled := false
	oldCfg := &config.Config{
		Mode:       "pool",
		Management: config.ManagementConfig{Enabled: &managementEnabled},
	}
	oldCfg.SetFilePath(configPath)
	targetCfg := oldCfg.Clone()
	targetCfg.Mode = "hybrid"
	targetCfg.SetFilePath(configPath)
	server := NewServer(Config{}, mgr, log.New(io.Discard, "", 0))
	server.SetConfig(oldCfg)

	transition, err := server.PrepareListener(false, "", targetCfg)
	if err != nil {
		t.Fatalf("PrepareListener() error = %v", err)
	}
	if err := transition.Activate(context.Background()); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}

	result := make(chan struct {
		needReload bool
		err        error
	}, 1)
	go func() {
		needReload, updateErr := server.updateAllSettingsWithReload(allSettingsRequest{
			Mode:                          "pool",
			LogLevel:                      "info",
			ListenerAddress:               "0.0.0.0",
			ListenerPort:                  8080,
			ListenerProtocol:              "http",
			MultiPortAddress:              "0.0.0.0",
			MultiPortBasePort:             10000,
			MultiPortProtocol:             "http",
			PoolMode:                      "auto",
			PoolBlacklistDuration:         "1m",
			SubRefreshInterval:            "1m",
			SubRefreshTimeout:             "30s",
			SubRefreshHealthCheckTimeout:  "30s",
			SubRefreshDrainTimeout:        "10s",
			SourceSyncRefreshInterval:     "1m",
			SourceSyncRequestTimeout:      "30s",
			GeoIPAutoUpdateInterval:       "1h",
			ManagementHealthCheckInterval: "1m",
		})
		result <- struct {
			needReload bool
			err        error
		}{needReload: needReload, err: updateErr}
	}()

	select {
	case got := <-result:
		t.Fatalf("settings update completed before transition commit: need_reload=%v err=%v", got.needReload, got.err)
	case <-time.After(40 * time.Millisecond):
	}
	transition.Finalize()

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("updateAllSettingsWithReload() error = %v", got.err)
		}
		if !got.needReload {
			t.Fatal("settings update computed need_reload from the retired config source")
		}
	case <-time.After(time.Second):
		t.Fatal("settings update did not resume after transition commit")
	}
}

func TestListenerTransitionFinalizeReusesServerRuntimeState(t *testing.T) {
	server, oldListen := startLifecycleTestServer(t)
	compatState := server.proxyCompat
	newListen := freeLifecycleListenDifferent(t, oldListen)
	transition, err := server.PrepareListener(true, newListen)
	if err != nil {
		t.Fatalf("PrepareListener() error = %v", err)
	}
	if err := transition.Activate(context.Background()); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	transition.Finalize()

	waitLifecycleListen(t, newListen, true)
	waitLifecycleListen(t, oldListen, false)
	if server.proxyCompat != compatState {
		t.Fatal("listener transition replaced proxy compatibility runtime state")
	}
}

func TestReloadHandlerCanFinalizeItsOwnListenerTransition(t *testing.T) {
	server, oldListen := startLifecycleTestServer(t)
	newListen := freeLifecycleListenDifferent(t, oldListen)
	server.SetNodeManager(&lifecycleNodeManager{trigger: func(context.Context) error {
		transition, err := server.PrepareListener(true, newListen)
		if err != nil {
			return err
		}
		if err := transition.Activate(context.Background()); err != nil {
			transition.Abort()
			return err
		}
		transition.Finalize()
		return nil
	}})

	client := &http.Client{Timeout: 2 * time.Second}
	request, err := http.NewRequest(http.MethodPost, "http://"+oldListen+"/api/reload", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("reload request deadlocked or failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reload status = %d, want 200", response.StatusCode)
	}
	waitLifecycleListen(t, newListen, true)
	waitLifecycleListen(t, oldListen, false)
}

func TestDisabledServerCanBeWiredBeforeFirstListenerStart(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	server := NewServer(Config{Enabled: false}, mgr, log.New(io.Discard, "", 0))
	if server == nil {
		t.Fatal("disabled monitor config did not create a reusable server runtime")
	}
	nodeManager := &lifecycleNodeManager{}
	server.SetNodeManager(nodeManager)

	listen := freeLifecycleListen(t)
	transition, err := server.PrepareListener(true, listen)
	if err != nil {
		t.Fatalf("PrepareListener() error = %v", err)
	}
	if err := transition.Activate(context.Background()); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	transition.Finalize()
	defer shutdownLifecycleServer(server)
	waitLifecycleListen(t, listen, true)
	if server.nodeManagerSnapshot() != nodeManager {
		t.Fatal("first listener start lost pre-wired dependencies")
	}
}

func startLifecycleTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	listen := freeLifecycleListen(t)
	server := NewServer(Config{Enabled: true, Listen: listen}, mgr, log.New(io.Discard, "", 0))
	if err := server.Start(context.Background()); err != nil {
		mgr.Stop()
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		shutdownLifecycleServer(server)
		mgr.Stop()
	})
	waitLifecycleListen(t, listen, true)
	return server, listen
}

func shutdownLifecycleServer(server *Server) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func freeLifecycleListen(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release listen: %v", err)
	}
	return address
}

func freeLifecycleListenDifferent(t *testing.T, avoid string) string {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		address := freeLifecycleListen(t)
		if address != avoid {
			return address
		}
	}
	t.Fatalf("could not reserve a listener address different from %s", avoid)
	return ""
}

func waitLifecycleListen(t *testing.T, address string, want bool) {
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

type lifecycleNodeManager struct {
	trigger func(context.Context) error
}

func (*lifecycleNodeManager) ListConfigNodes(context.Context) ([]config.NodeConfig, error) {
	return nil, nil
}

func (*lifecycleNodeManager) CreateNode(_ context.Context, node config.NodeConfig) (config.NodeConfig, error) {
	return node, nil
}

func (*lifecycleNodeManager) UpdateNode(_ context.Context, _ string, node config.NodeConfig) (config.NodeConfig, error) {
	return node, nil
}

func (*lifecycleNodeManager) DeleteNode(context.Context, string) error { return nil }

func (*lifecycleNodeManager) SetNodeEnabled(context.Context, string, bool) error { return nil }

func (m *lifecycleNodeManager) TriggerReload(ctx context.Context) error {
	if m.trigger == nil {
		return nil
	}
	return m.trigger(ctx)
}
