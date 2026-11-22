package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeRouteLoadCloneSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mode: pool\nnodes:\n  - name: test\n    uri: socks5://127.0.0.1:1080\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaudeRoute.Enabled {
		t.Fatal("absent setting must remain disabled")
	}
	want := ClaudeRoute{IncludeSharedDependencies: true, Enabled: true, NodeTag: "approved-member", NodeFingerprint: NodeFingerprint("socks5://127.0.0.1:1080")}
	cfg.ClaudeRoute = want
	cloned := cfg.Clone()
	if cloned.ClaudeRoute != want {
		t.Fatal("clone lost route binding")
	}
	cloned.ClaudeRoute.NodeTag = "other"
	if cfg.ClaudeRoute != want {
		t.Fatal("clone mutation changed original")
	}
	for _, enabled := range []bool{true, false} {
		want.Enabled = enabled
		cfg.ClaudeRoute = want
		cfg.Lock()
		err = cfg.SaveSettings()
		cfg.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		cfg, err = Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ClaudeRoute != want {
			t.Fatalf("saved binding = %+v, want %+v", cfg.ClaudeRoute, want)
		}
		if len(cfg.Nodes) != 1 || cfg.Nodes[0].Name != "test" {
			t.Fatal("save changed node configuration")
		}
	}
}
