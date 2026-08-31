package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndClonePreservePoolDetourSourceRefs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`
mode: pool
pool:
  detour_source_refs:
    - manifest:conn_zenproxy_primary
nodes:
  - name: bootstrap
    uri: socks5://127.0.0.1:1080
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got, want := cfg.Pool.DetourSourceRefs, []string{"manifest:conn_zenproxy_primary"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("detour source refs = %#v, want %#v", got, want)
	}

	cloned := cfg.Clone()
	cloned.Pool.DetourSourceRefs[0] = "changed"
	if cfg.Pool.DetourSourceRefs[0] != "manifest:conn_zenproxy_primary" {
		t.Fatal("Clone shares pool detour source refs with original config")
	}

	cfg.Pool.DetourSourceRefs[0] = "manifest:updated-zen-source"
	cfg.Lock()
	err = cfg.SaveSettings()
	cfg.Unlock()
	if err != nil {
		t.Fatalf("SaveSettings returned error: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	if got := reloaded.Pool.DetourSourceRefs; len(got) != 1 || got[0] != "manifest:updated-zen-source" {
		t.Fatalf("saved detour source refs = %#v", got)
	}
}
