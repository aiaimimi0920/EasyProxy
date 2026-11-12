package config

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestLoadModesPreservePathsAndSourcePrecedence(t *testing.T) {
	for _, reload := range []bool{false, true} {
		for _, subscription := range []bool{false, true} {
			t.Run(fmt.Sprintf("reload=%t/subscription=%t", reload, subscription), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					fmt.Fprintln(w, "http://remote.example:8080")
				}))
				defer server.Close()
				dir := t.TempDir()
				path := filepath.Join(dir, "config.yaml")
				if err := os.WriteFile(filepath.Join(dir, "nodes.txt"), []byte("http://file.example:8080\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "rules.txt"), []byte("DOMAIN,example.com,DIRECT\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				text := "nodes_file: nodes.txt\ngeoip:\n  database_path: country.mmdb\nrouting:\n  rule_files: [rules.txt]\nnodes:\n  - uri: http://inline.example:8080\n"
				if subscription {
					text += fmt.Sprintf("subscriptions: [%q]\n", server.URL)
				}
				if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
				load := Load
				if reload {
					load = LoadForReload
				}
				cfg, err := load(path)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.FilePath() != path || cfg.NodesFile != filepath.Join(dir, "nodes.txt") || cfg.GeoIP.DatabasePath != filepath.Join(dir, "country.mmdb") {
					t.Fatal("loader lost config-relative paths")
				}
				if len(cfg.Routing.RuleFiles) != 1 || cfg.Routing.RuleFiles[0] != filepath.Join(dir, "rules.txt") {
					t.Fatalf("unexpected rule paths: %v", cfg.Routing.RuleFiles)
				}
				wantCount := 2
				wantRequests := int32(0)
				wantSource := NodeSourceFile
				if subscription {
					wantSource = NodeSourceSubscription
					if reload {
						wantCount = 1
					} else {
						wantRequests = 1
					}
				}
				if requests.Load() != wantRequests || len(cfg.Nodes) != wantCount {
					t.Fatalf("requests=%d nodes=%d, want requests=%d nodes=%d", requests.Load(), len(cfg.Nodes), wantRequests, wantCount)
				}
				if cfg.Nodes[0].Source != NodeSourceInline {
					t.Fatal("inline node source changed")
				}
				if wantCount == 2 && cfg.Nodes[1].Source != wantSource {
					t.Fatalf("source=%q, want %q", cfg.Nodes[1].Source, wantSource)
				}
			})
		}
	}
}
