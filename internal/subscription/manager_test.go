package subscription

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"easy_proxies/internal/config"
)

func TestFetchSubscriptionSourcesParsesClashYAML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(strings.TrimSpace(`
proxies:
  - {name: "Manifest Clash", server: "198.51.100.10", port: 8388, type: "ss", cipher: "aes-256-gcm", password: "secret-pass", udp: true}
`)))
	}))
	defer server.Close()

	manager := New(&config.Config{}, nil)
	sources := []RuntimeSource{
		{
			ID:     "manifest-sub",
			Kind:   SourceKindSubscription,
			Name:   "Aggregator Stable",
			Input:  server.URL,
			Origin: "manifest",
		},
	}

	nodes, err := manager.fetchSubscriptionSources(sources)
	if err != nil {
		t.Fatalf("fetchSubscriptionSources() error = %v", err)
	}

	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	if !strings.HasPrefix(nodes[0].URI, "ss://") {
		t.Fatalf("expected ss URI, got %q", nodes[0].URI)
	}
	if nodes[0].Source != config.NodeSourceManifest {
		t.Fatalf("expected manifest source, got %q", nodes[0].Source)
	}
	if strings.TrimSpace(nodes[0].Name) == "" {
		t.Fatalf("expected parsed node name to be preserved")
	}
}
