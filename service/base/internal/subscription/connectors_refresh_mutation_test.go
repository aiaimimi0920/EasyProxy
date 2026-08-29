package subscription

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
)

func TestRefreshPreferredEntryIPsRejectsStaleConfigAfterReload(t *testing.T) {
	t.Parallel()
	oldCfg := preferredIPTestConfig(t, "Old Template", "old-token")
	newCfg := preferredIPTestConfig(t, "Old Template", "new-token")

	selectionStarted := make(chan struct{})
	releaseSelection := make(chan struct{})
	manager := New(
		oldCfg,
		nil,
		WithConnectorRuntime(&fakeConnectorRuntime{}),
		withPreferredIPSelector(func(_ context.Context, _ string, _ config.ConnectorRuntimeConfig, _ config.ConnectorSourceConfig, _ monitor.PreferredIPRefreshOptions) ([]preferredIPResultRow, string, string, error) {
			close(selectionStarted)
			<-releaseSelection
			return []preferredIPResultRow{{IP: "198.41.132.114"}}, "artifacts", "result.csv", nil
		}),
	)

	refreshDone := make(chan error, 1)
	go func() {
		_, err := manager.RefreshPreferredEntryIPs(context.Background(), "Old Template", monitor.PreferredIPRefreshOptions{TopCount: 1})
		refreshDone <- err
	}()

	<-selectionStarted
	manager.OnConfigUpdate(newCfg)
	close(releaseSelection)

	err := <-refreshDone
	if !errors.Is(err, monitor.ErrConnectorConflict) {
		t.Fatalf("RefreshPreferredEntryIPs() error = %v, want connector conflict", err)
	}
	assertPreferredConnectors(t, oldCfg, nil)
	assertPreferredConnectors(t, newCfg, nil)
}

func TestRefreshPreferredEntryIPsWaitsForReloadIntentBeforeCommit(t *testing.T) {
	t.Parallel()
	cfg := preferredIPTestConfig(t, "ECH Template", "token")
	boxManager := boxmgr.New(cfg, monitor.Config{})
	selectionStarted := make(chan struct{})
	releaseSelection := make(chan struct{})
	manager := New(
		cfg,
		boxManager,
		WithConnectorRuntime(&fakeConnectorRuntime{}),
		withPreferredIPSelector(func(_ context.Context, _ string, _ config.ConnectorRuntimeConfig, _ config.ConnectorSourceConfig, _ monitor.PreferredIPRefreshOptions) ([]preferredIPResultRow, string, string, error) {
			close(selectionStarted)
			<-releaseSelection
			return []preferredIPResultRow{{IP: "198.41.132.114"}}, "artifacts", "result.csv", nil
		}),
	)

	refreshCtx, cancelRefresh := context.WithCancel(context.Background())
	defer cancelRefresh()
	refreshDone := make(chan error, 1)
	go func() {
		_, err := manager.RefreshPreferredEntryIPs(refreshCtx, "ECH Template", monitor.PreferredIPRefreshOptions{TopCount: 1})
		refreshDone <- err
	}()

	<-selectionStarted
	intent, err := boxManager.BeginReloadIntent(context.Background())
	if err != nil {
		t.Fatalf("BeginReloadIntent() error = %v", err)
	}
	defer intent.End()
	close(releaseSelection)

	select {
	case err := <-refreshDone:
		t.Fatalf("RefreshPreferredEntryIPs returned during reload intent: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	cfg.RLock()
	committed := len(cfg.Connectors) > 1
	cfg.RUnlock()
	if committed {
		t.Fatal("preferred connectors committed while reload intent was active")
	}

	cancelRefresh()
	manager.cancel()
	intent.End()
	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("RefreshPreferredEntryIPs did not finish after reload intent ended")
	}
	cfg.RLock()
	defer cfg.RUnlock()
	if len(cfg.Connectors) != 1 {
		t.Fatalf("connectors changed after canceled reload-guarded refresh: %#v", cfg.Connectors)
	}
}

func TestRefreshPreferredEntryIPsRollsBackMemoryWhenSaveFails(t *testing.T) {
	t.Parallel()
	cfg := preferredIPTestConfig(t, "ECH Template", "token")
	cfg.SetFilePath(t.TempDir())
	original := cloneConnectors(cfg.Connectors)
	manager := New(
		cfg,
		nil,
		WithConnectorRuntime(&fakeConnectorRuntime{}),
		withPreferredIPSelector(func(_ context.Context, _ string, _ config.ConnectorRuntimeConfig, _ config.ConnectorSourceConfig, _ monitor.PreferredIPRefreshOptions) ([]preferredIPResultRow, string, string, error) {
			return []preferredIPResultRow{{IP: "198.41.132.114"}}, "artifacts", "result.csv", nil
		}),
	)

	_, err := manager.RefreshPreferredEntryIPs(context.Background(), "ECH Template", monitor.PreferredIPRefreshOptions{TopCount: 1})
	if err == nil || !strings.Contains(err.Error(), "保存连接器配置失败") {
		t.Fatalf("RefreshPreferredEntryIPs() error = %v, want save failure", err)
	}

	cfg.RLock()
	got := cloneConnectors(cfg.Connectors)
	cfg.RUnlock()
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("connectors changed after save failure: got %#v, want %#v", got, original)
	}
}

func TestConnectorMutationsRollBackMemoryWhenSaveFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Manager) error
	}{
		{
			name: "create",
			mutate: func(manager *Manager) error {
				_, err := manager.CreateConnector(context.Background(), config.ConnectorSourceConfig{
					Name:          "New Connector",
					Input:         "https://new.example.com/connect",
					Enabled:       true,
					ConnectorType: connectorTypeECHWorker,
				})
				return err
			},
		},
		{
			name: "update",
			mutate: func(manager *Manager) error {
				_, err := manager.UpdateConnector(context.Background(), "ECH Template", config.ConnectorSourceConfig{
					Name:          "ECH Template",
					Input:         "https://updated.example.com/connect",
					Enabled:       true,
					ConnectorType: connectorTypeECHWorker,
				})
				return err
			},
		},
		{
			name: "delete",
			mutate: func(manager *Manager) error {
				return manager.DeleteConnector(context.Background(), "ECH Template")
			},
		},
		{
			name: "set enabled",
			mutate: func(manager *Manager) error {
				return manager.SetConnectorEnabled(context.Background(), "ECH Template", true)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := preferredIPTestConfig(t, "ECH Template", "token")
			cfg.SetFilePath(t.TempDir())
			original := cloneConnectors(cfg.Connectors)
			manager := New(cfg, nil, WithConnectorRuntime(&fakeConnectorRuntime{}))

			if err := test.mutate(manager); err == nil {
				t.Fatal("connector mutation unexpectedly succeeded")
			}

			cfg.RLock()
			got := cloneConnectors(cfg.Connectors)
			cfg.RUnlock()
			if !reflect.DeepEqual(got, original) {
				t.Fatalf("connectors changed after save failure: got %#v, want %#v", got, original)
			}
		})
	}
}

func TestCreateConnectorMutatesConfigInstalledDuringReloadIntent(t *testing.T) {
	t.Parallel()
	oldCfg := preferredIPTestConfig(t, "Old Template", "old-token")
	newCfg := preferredIPTestConfig(t, "New Template", "new-token")
	boxManager := boxmgr.New(oldCfg, monitor.Config{})
	manager := New(oldCfg, boxManager, WithConnectorRuntime(&fakeConnectorRuntime{}))

	intent, err := boxManager.BeginReloadIntent(context.Background())
	if err != nil {
		t.Fatalf("BeginReloadIntent() error = %v", err)
	}
	mutationDone := make(chan error, 1)
	go func() {
		_, err := manager.CreateConnector(context.Background(), config.ConnectorSourceConfig{
			Name:          "Created After Reload",
			Input:         "https://created.example.com/connect",
			Enabled:       true,
			ConnectorType: connectorTypeECHWorker,
		})
		mutationDone <- err
	}()

	select {
	case err := <-mutationDone:
		t.Fatalf("CreateConnector returned during reload intent: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	manager.OnConfigUpdate(newCfg)
	intent.End()
	select {
	case err := <-mutationDone:
		if err != nil {
			t.Fatalf("CreateConnector() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CreateConnector did not finish after reload intent ended")
	}

	oldCfg.RLock()
	oldHasCreated := connectorIndexByName(oldCfg.Connectors, "Created After Reload") >= 0
	oldCfg.RUnlock()
	if oldHasCreated {
		t.Fatal("CreateConnector mutated the retired config")
	}
	newCfg.RLock()
	newHasCreated := connectorIndexByName(newCfg.Connectors, "Created After Reload") >= 0
	newCfg.RUnlock()
	if !newHasCreated {
		t.Fatal("CreateConnector did not mutate the config installed during reload")
	}
}
