package app

import (
	"context"
	"path/filepath"
	"testing"

	"easy_proxies/internal/config"
	"easy_proxies/internal/store"
)

func TestShouldBootstrapRuntimeSourcesWithSubscriptions(t *testing.T) {
	cfg := &config.Config{
		Subscriptions: []string{"https://example.com/subscription"},
		Nodes: []config.NodeConfig{
			{
				Name:   "manual-node",
				URI:    "ss://YWVzLTI1Ni1nY206c2VjcmV0QDE5OC41MS4xMDAuMTA6ODM4OA==#manual-node",
				Source: config.NodeSourceManual,
			},
		},
	}

	if !shouldBootstrapRuntimeSources(cfg) {
		t.Fatal("expected configured subscriptions to trigger bootstrap even when local nodes already exist")
	}
}

func TestShouldBootstrapRuntimeSourcesWithManifestFallback(t *testing.T) {
	cfg := &config.Config{}
	cfg.SourceSync.Enabled = true
	cfg.SourceSync.ManifestURL = "https://example.com/manifest"

	if !shouldBootstrapRuntimeSources(cfg) {
		t.Fatal("expected source_sync manifest to trigger bootstrap")
	}
}

func TestLoadNodesFromStoreKeepsRuntimeRowsOutOfPersistentConfigMerge(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "easyproxy.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer dataStore.Close()

	manualNode := store.Node{
		URI:     "ss://manual-node#manual",
		Name:    "manual-node",
		Source:  store.NodeSourceManual,
		Enabled: true,
	}
	if err := dataStore.CreateNode(ctx, &manualNode); err != nil {
		t.Fatalf("CreateNode(manual) error = %v", err)
	}

	runtimeNode := store.Node{
		URI:     "ss://runtime-node#runtime",
		Name:    "runtime-node",
		Source:  store.NodeSourceSubscription,
		Enabled: true,
	}
	if err := dataStore.CreateNode(ctx, &runtimeNode); err != nil {
		t.Fatalf("CreateNode(runtime) error = %v", err)
	}

	cfg := &config.Config{
		Nodes: []config.NodeConfig{
			{
				Name:   "inline-node",
				URI:    "ss://inline-node#inline",
				Source: config.NodeSourceInline,
			},
			{
				Name:   "manifest-node",
				URI:    "ss://manifest-node#manifest",
				Source: config.NodeSourceManifest,
			},
		},
	}

	if err := loadNodesFromStore(ctx, cfg, dataStore); err != nil {
		t.Fatalf("loadNodesFromStore() error = %v", err)
	}

	if len(cfg.Nodes) != 3 {
		t.Fatalf("expected 3 merged nodes (inline + manifest + manual), got %d: %+v", len(cfg.Nodes), cfg.Nodes)
	}

	foundManual := false
	foundRuntime := false
	for _, node := range cfg.Nodes {
		if node.URI == manualNode.URI {
			foundManual = true
		}
		if node.URI == runtimeNode.URI {
			foundRuntime = true
		}
	}
	if !foundManual {
		t.Fatalf("expected manual store node to merge into config, got %+v", cfg.Nodes)
	}
	if foundRuntime {
		t.Fatalf("did not expect runtime store node to be merged into persistent config, got %+v", cfg.Nodes)
	}

	storeNodes, err := dataStore.ListNodes(ctx, store.NodeFilter{})
	if err != nil {
		t.Fatalf("ListNodes() error = %v", err)
	}
	if len(storeNodes) != 2 {
		t.Fatalf("expected runtime row to remain in store for stats restore, got %d rows", len(storeNodes))
	}
}
