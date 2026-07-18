package config

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

type cloneTestNamedMap map[string]string
type cloneTestNamedSlice []int
type cloneTestNamedArray [1]map[string]string

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func cloneTestConfig() *Config {
	managementEnabled := true
	useDefaultRules := true
	connectorRuntimeEnabled := true

	cfg := &Config{
		ExtraListeners: []ExtraListenerConfig{{Address: "original-listener"}},
		Management: ManagementConfig{
			Enabled:      &managementEnabled,
			ProbeTargets: []string{"original-probe"},
		},
		Routing: RoutingConfig{
			UseDefaultRules: &useDefaultRules,
			Rules:           []string{"DOMAIN,original.example,DIRECT"},
			RuleProviders: []RuleProvider{{
				URL:    "https://original.example/rules.txt",
				Policy: "DIRECT",
			}},
		},
		SourceSync: SourceSyncConfig{
			FallbackSubscriptions: []string{"https://original.example/subscription"},
			ConnectorRuntime: ConnectorRuntimeConfig{
				Enabled: &connectorRuntimeEnabled,
			},
		},
		Subscriptions: []string{"https://original.example/main-subscription"},
		Nodes:         []NodeConfig{{Name: "original-node"}},
		Connectors: []ConnectorSourceConfig{{
			Name: "original-connector",
			ConnectorConfig: map[string]any{
				"nested_map": map[string]any{
					"value": "original-map-value",
					"items": []any{
						map[string]any{"value": "original-slice-map-value"},
						[]string{"original-nested-string"},
					},
				},
				"items": []any{
					"original-any-value",
					[]any{map[string]any{"value": "original-deep-map-value"}},
				},
				"strings": []string{"original-string"},
			},
		}},
	}
	cfg.SetFilePath("testdata/config.yaml")
	return cfg
}

func TestConfigCloneDeepCopiesReferenceFields(t *testing.T) {
	t.Run("extra listeners", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.ExtraListeners[0].Address = "changed"
		if got := original.ExtraListeners[0].Address; got != "original-listener" {
			t.Fatalf("original extra listener changed through clone: %q", got)
		}
	})

	t.Run("management probe targets", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.Management.ProbeTargets[0] = "changed"
		if got := original.Management.ProbeTargets[0]; got != "original-probe" {
			t.Fatalf("original management probe target changed through clone: %q", got)
		}
	})

	t.Run("management enabled pointer", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		*cloned.Management.Enabled = false
		if !*original.Management.Enabled {
			t.Fatal("original management enabled value changed through clone")
		}
	})

	t.Run("routing use default rules pointer", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		*cloned.Routing.UseDefaultRules = false
		if !*original.Routing.UseDefaultRules {
			t.Fatal("original routing use_default_rules value changed through clone")
		}
	})

	t.Run("routing rules", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.Routing.Rules[0] = "changed"
		if got := original.Routing.Rules[0]; got != "DOMAIN,original.example,DIRECT" {
			t.Fatalf("original routing rule changed through clone: %q", got)
		}
	})

	t.Run("routing rule providers", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.Routing.RuleProviders[0].URL = "changed"
		if got := original.Routing.RuleProviders[0].URL; got != "https://original.example/rules.txt" {
			t.Fatalf("original routing rule provider changed through clone: %q", got)
		}
	})

	t.Run("source sync fallback subscriptions", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.SourceSync.FallbackSubscriptions[0] = "changed"
		if got := original.SourceSync.FallbackSubscriptions[0]; got != "https://original.example/subscription" {
			t.Fatalf("original fallback subscription changed through clone: %q", got)
		}
	})

	t.Run("source sync connector runtime enabled pointer", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		*cloned.SourceSync.ConnectorRuntime.Enabled = false
		if !*original.SourceSync.ConnectorRuntime.Enabled {
			t.Fatal("original connector runtime enabled value changed through clone")
		}
	})

	t.Run("subscriptions", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.Subscriptions[0] = "changed"
		if got := original.Subscriptions[0]; got != "https://original.example/main-subscription" {
			t.Fatalf("original subscription changed through clone: %q", got)
		}
	})

	t.Run("nodes", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.Nodes[0].Name = "changed"
		if got := original.Nodes[0].Name; got != "original-node" {
			t.Fatalf("original node changed through clone: %q", got)
		}
	})

	t.Run("connectors", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.Connectors[0].Name = "changed"
		if got := original.Connectors[0].Name; got != "original-connector" {
			t.Fatalf("original connector changed through clone: %q", got)
		}
	})

	t.Run("connector config nested map", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.Connectors[0].ConnectorConfig["nested_map"].(map[string]any)["value"] = "changed"
		got := original.Connectors[0].ConnectorConfig["nested_map"].(map[string]any)["value"]
		if got != "original-map-value" {
			t.Fatalf("original nested connector map changed through clone: %#v", got)
		}
	})

	t.Run("connector config nested any slice", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		items := cloned.Connectors[0].ConnectorConfig["nested_map"].(map[string]any)["items"].([]any)
		items[0].(map[string]any)["value"] = "changed"
		originalItems := original.Connectors[0].ConnectorConfig["nested_map"].(map[string]any)["items"].([]any)
		if got := originalItems[0].(map[string]any)["value"]; got != "original-slice-map-value" {
			t.Fatalf("original map inside connector slice changed through clone: %#v", got)
		}
	})

	t.Run("connector config nested string slice", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		items := cloned.Connectors[0].ConnectorConfig["nested_map"].(map[string]any)["items"].([]any)
		items[1].([]string)[0] = "changed"
		originalItems := original.Connectors[0].ConnectorConfig["nested_map"].(map[string]any)["items"].([]any)
		if got := originalItems[1].([]string)[0]; got != "original-nested-string" {
			t.Fatalf("original string slice inside connector config changed through clone: %q", got)
		}
	})

	t.Run("connector config recursive any values", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		items := cloned.Connectors[0].ConnectorConfig["items"].([]any)
		items[0] = "changed"
		items[1].([]any)[0].(map[string]any)["value"] = "changed"

		originalItems := original.Connectors[0].ConnectorConfig["items"].([]any)
		if got := originalItems[0]; got != "original-any-value" {
			t.Fatalf("original connector any slice changed through clone: %#v", got)
		}
		if got := originalItems[1].([]any)[0].(map[string]any)["value"]; got != "original-deep-map-value" {
			t.Fatalf("original recursively nested connector map changed through clone: %#v", got)
		}
	})

	t.Run("connector config string slice", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.Connectors[0].ConnectorConfig["strings"].([]string)[0] = "changed"
		if got := original.Connectors[0].ConnectorConfig["strings"].([]string)[0]; got != "original-string" {
			t.Fatalf("original connector string slice changed through clone: %q", got)
		}
	})
}

func TestConfigClonePreservesFilePathAndUsesFreshMutex(t *testing.T) {
	original := cloneTestConfig()
	original.Lock()
	cloned := original.Clone()
	defer original.Unlock()

	if got, want := cloned.FilePath(), original.FilePath(); got != want {
		t.Fatalf("cloned file path = %q, want %q", got, want)
	}

	locked := make(chan struct{})
	go func() {
		cloned.Lock()
		close(locked)
		cloned.Unlock()
	}()

	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("cloned config mutex remained coupled to the locked original")
	}
}

func dynamicCloneTestConfig() *Config {
	namedMapPointer := cloneTestNamedMap{"value": "original-pointer-map-value"}
	mutex := &sync.Mutex{}
	return &Config{
		Connectors: []ConnectorSourceConfig{{
			ConnectorConfig: map[string]any{
				"string_map":  map[string]string{"value": "original-string-map-value"},
				"ints":        []int{1},
				"maps":        []map[string]string{{"value": "original-map-slice-value"}},
				"any_map":     map[any]any{"values": []int{2}},
				"named_map":   cloneTestNamedMap{"value": "original-named-map-value"},
				"named_slice": cloneTestNamedSlice{3},
				"named_array": cloneTestNamedArray{{"value": "original-named-array-value"}},
				"map_pointer": &namedMapPointer,
				"mutex":       mutex,
			},
		}},
	}
}

func TestConfigCloneDeepCopiesDynamicConnectorConfigTypes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		read   func(map[string]any) any
		want   any
	}{
		{
			name: "map string string",
			mutate: func(values map[string]any) {
				values["string_map"].(map[string]string)["value"] = "changed"
			},
			read: func(values map[string]any) any {
				return values["string_map"].(map[string]string)["value"]
			},
			want: "original-string-map-value",
		},
		{
			name: "int slice",
			mutate: func(values map[string]any) {
				values["ints"].([]int)[0] = 9
			},
			read: func(values map[string]any) any {
				return values["ints"].([]int)[0]
			},
			want: 1,
		},
		{
			name: "map slice",
			mutate: func(values map[string]any) {
				values["maps"].([]map[string]string)[0]["value"] = "changed"
			},
			read: func(values map[string]any) any {
				return values["maps"].([]map[string]string)[0]["value"]
			},
			want: "original-map-slice-value",
		},
		{
			name: "map any any",
			mutate: func(values map[string]any) {
				values["any_map"].(map[any]any)["values"].([]int)[0] = 9
			},
			read: func(values map[string]any) any {
				return values["any_map"].(map[any]any)["values"].([]int)[0]
			},
			want: 2,
		},
		{
			name: "named map",
			mutate: func(values map[string]any) {
				values["named_map"].(cloneTestNamedMap)["value"] = "changed"
			},
			read: func(values map[string]any) any {
				return values["named_map"].(cloneTestNamedMap)["value"]
			},
			want: "original-named-map-value",
		},
		{
			name: "named slice",
			mutate: func(values map[string]any) {
				values["named_slice"].(cloneTestNamedSlice)[0] = 9
			},
			read: func(values map[string]any) any {
				return values["named_slice"].(cloneTestNamedSlice)[0]
			},
			want: 3,
		},
		{
			name: "named array with map",
			mutate: func(values map[string]any) {
				values["named_array"].(cloneTestNamedArray)[0]["value"] = "changed"
			},
			read: func(values map[string]any) any {
				return values["named_array"].(cloneTestNamedArray)[0]["value"]
			},
			want: "original-named-array-value",
		},
		{
			name: "pointer to named map",
			mutate: func(values map[string]any) {
				(*values["map_pointer"].(*cloneTestNamedMap))["value"] = "changed"
			},
			read: func(values map[string]any) any {
				return (*values["map_pointer"].(*cloneTestNamedMap))["value"]
			},
			want: "original-pointer-map-value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := dynamicCloneTestConfig()
			cloned := original.Clone()
			tt.mutate(cloned.Connectors[0].ConnectorConfig)
			if got := tt.read(original.Connectors[0].ConnectorConfig); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("original dynamic connector value changed through clone: got %#v, want %#v", got, tt.want)
			}
		})
	}

	original := dynamicCloneTestConfig()
	cloned := original.Clone()
	if got, want := cloned.Connectors[0].ConnectorConfig["mutex"], original.Connectors[0].ConnectorConfig["mutex"]; got != want {
		t.Fatal("unsupported mutex pointer should be preserved instead of copied")
	}
}

func TestIsProxyURIRecognizesHTTPAndSOCKS5(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want bool
	}{
		{name: "http", uri: "http://alice:secret@example.com:8080", want: true},
		{name: "socks5", uri: "socks5://alice:secret@example.com:1080", want: true},
		{name: "vmess", uri: "vmess://example", want: true},
		{name: "invalid", uri: "ftp://example.com", want: false},
		{name: "html garbage", uri: "http://<meta property=\"og:type\" content=\"website\">", want: false},
	}

	for _, tt := range tests {
		if got := IsProxyURI(tt.uri); got != tt.want {
			t.Fatalf("%s: IsProxyURI(%q) = %v, want %v", tt.name, tt.uri, got, tt.want)
		}
	}
}

func TestParseSubscriptionContentSkipsGarbageHTTPLines(t *testing.T) {
	content := strings.TrimSpace(`
http://<meta property="og:type" content="website">
http://set: function setWithExpiry(key, value, ttl) {
http://alice:secret@example.com:8080/proxy
`)

	nodes, err := ParseSubscriptionContent(content)
	if err != nil {
		t.Fatalf("ParseSubscriptionContent() error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	if nodes[0].URI != "http://alice:secret@example.com:8080/proxy" {
		t.Fatalf("expected the valid proxy URI to survive, got %q", nodes[0].URI)
	}
}

func TestApplyDefaultsSetsNeutralProbeTargets(t *testing.T) {
	cfg := &Config{}

	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults() error = %v", err)
	}

	if cfg.Management.ProbeTarget != "" {
		t.Fatalf("expected single probe target to stay empty by default, got %q", cfg.Management.ProbeTarget)
	}
	if len(cfg.Management.ProbeTargets) == 0 {
		t.Fatal("expected default probe targets to be populated")
	}
	wantTargets := []string{
		"https://connectivitycheck.gstatic.com/generate_204",
		"https://cp.cloudflare.com/generate_204",
		"https://www.msftconnecttest.com/connecttest.txt",
		"https://www.google.com/generate_204",
		"https://www.google.com/robots.txt",
		"https://www.youtube.com/robots.txt",
	}
	for _, want := range wantTargets {
		found := false
		for _, target := range cfg.Management.ProbeTargets {
			if target == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected probe target %q in defaults, got %v", want, cfg.Management.ProbeTargets)
		}
	}
	if cfg.Pool.Mode != "auto" {
		t.Fatalf("unexpected default pool mode: %q", cfg.Pool.Mode)
	}
}

func TestNormalizeVLESSFlowCanonicalizesLegacyUDP443Variant(t *testing.T) {
	if got := NormalizeVLESSFlow("xtls-rprx-vision-udp443"); got != "xtls-rprx-vision" {
		t.Fatalf("expected legacy UDP443 flow to normalize, got %q", got)
	}
	if got := NormalizeVLESSFlow("xtls-rprx-vision-udp443-udp443"); got != "xtls-rprx-vision" {
		t.Fatalf("expected repeated legacy UDP443 flow to normalize, got %q", got)
	}
	if got := NormalizeVLESSFlow("xtls-rprx-vision"); got != "xtls-rprx-vision" {
		t.Fatalf("expected plain vision flow to remain unchanged, got %q", got)
	}
}

func TestParseSubscriptionContentParsesClashYAMLBeyondInitialHeader(t *testing.T) {
	content := strings.TrimSpace(`
port: 7890
socks-port: 7891
allow-lan: true
mode: rule
log-level: info
dns:
  enable: true
  ipv6: true
proxies:
  - {name: "Delayed Clash", server: "198.51.100.20", port: 8443, type: "vless", uuid: "11111111-1111-1111-1111-111111111111", tls: true, servername: "edge.example.com"}
`)

	nodes, err := ParseSubscriptionContent(content)
	if err != nil {
		t.Fatalf("ParseSubscriptionContent() error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	if !strings.HasPrefix(nodes[0].URI, "vless://") {
		t.Fatalf("expected parsed Clash YAML to produce a VLESS URI, got %q", nodes[0].URI)
	}
	if nodes[0].Name != "Delayed Clash" {
		t.Fatalf("expected Clash proxy name to be preserved, got %q", nodes[0].Name)
	}
}

func TestParseSubscriptionContentParsesClashYAMLShadowsocksObfsPlugin(t *testing.T) {
	content := strings.TrimSpace(`
proxies:
  - name: "Glados SS"
    type: ss
    server: b497b27.r8.glados-config.net
    port: 2377
    cipher: chacha20-ietf-poly1305
    password: t0srmdxrm3xyjnvqz9ewlxb2myq7rjuv
    plugin: obfs
    plugin-opts:
      mode: tls
      host: b497b27.default.microsoft.lt:100531
`)

	nodes, err := ParseSubscriptionContent(content)
	if err != nil {
		t.Fatalf("ParseSubscriptionContent() error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	if !strings.HasPrefix(nodes[0].URI, "ss://") {
		t.Fatalf("expected parsed Clash YAML to produce an SS URI, got %q", nodes[0].URI)
	}
	if !strings.Contains(nodes[0].URI, "plugin=obfs-local") {
		t.Fatalf("expected shadowsocks plugin to normalize to obfs-local, got %q", nodes[0].URI)
	}
	if !strings.Contains(nodes[0].URI, "plugin-opts=") ||
		!strings.Contains(nodes[0].URI, "obfs%3Dtls") ||
		!strings.Contains(nodes[0].URI, "obfs-host%3Db497b27.default.microsoft.lt%3A100531") {
		t.Fatalf("expected plugin opts to preserve obfs mode/host, got %q", nodes[0].URI)
	}
}

func TestLoadForReloadIncludesNodesFile(t *testing.T) {
	dir := t.TempDir()
	nodesPath := filepath.Join(dir, "nodes.txt")
	configPath := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(nodesPath, []byte("http://alice:secret@example.com:8080/proxy\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(nodesPath) error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte("nodes_file: nodes.txt\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath) error = %v", err)
	}

	cfg, err := LoadForReload(configPath)
	if err != nil {
		t.Fatalf("LoadForReload() error = %v", err)
	}
	if len(cfg.Nodes) != 1 {
		t.Fatalf("expected 1 node from nodes_file on reload, got %d", len(cfg.Nodes))
	}
	if cfg.Nodes[0].URI != "http://alice:secret@example.com:8080/proxy" {
		t.Fatalf("unexpected nodes_file URI: %q", cfg.Nodes[0].URI)
	}
	if cfg.Nodes[0].Source != NodeSourceFile {
		t.Fatalf("expected nodes_file source, got %q", cfg.Nodes[0].Source)
	}
}

func TestFetchSubscriptionNodesRetriesHTTP1AfterHTTP2HeaderTimeout(t *testing.T) {
	fallbackRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackRequests++
		_, _ = w.Write([]byte("ss://YWVzLTI1Ni1nY206c2VjcmV0QDE5OC41MS4xMDAuMTA6ODM4OA==#h1-fallback\n"))
	}))
	defer server.Close()

	initialAttempts := 0
	failingHTTP2Client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			initialAttempts++
			return nil, errors.New("Get \"" + req.URL.String() + "\": http2: timeout awaiting response headers")
		}),
	}

	nodes, err := FetchSubscriptionNodesWithClient(server.URL, time.Second, "", 0, failingHTTP2Client)
	if err != nil {
		t.Fatalf("expected HTTP/1.1 fallback to recover subscription fetch, got error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node from HTTP/1.1 fallback, got %d", len(nodes))
	}
	if initialAttempts != 1 {
		t.Fatalf("expected one initial HTTP/2-like attempt, got %d", initialAttempts)
	}
	if fallbackRequests != 1 {
		t.Fatalf("expected one HTTP/1.1 fallback request, got %d", fallbackRequests)
	}
}

func TestFetchSubscriptionNodesReusesFreshCache(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		_, _ = w.Write([]byte("ss://YWVzLTI1Ni1nY206c2VjcmV0QDE5OC41MS4xMDAuMTA6ODM4OA==#cached-node\n"))
	}))
	defer server.Close()

	cacheDir := filepath.Join(t.TempDir(), "subscription-cache")
	nodes, err := FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Hour)
	if err != nil {
		t.Fatalf("FetchSubscriptionNodes() first call error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node from first fetch, got %d", len(nodes))
	}

	nodes, err = FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Hour)
	if err != nil {
		t.Fatalf("FetchSubscriptionNodes() second call error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node from cached fetch, got %d", len(nodes))
	}
	if requestCount != 1 {
		t.Fatalf("expected remote subscription to be fetched once, got %d requests", requestCount)
	}
}

func TestFetchSubscriptionNodesFallsBackToStaleCacheOnFailure(t *testing.T) {
	shouldFail := false
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if shouldFail {
			http.Error(w, "temporarily blocked", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ss://YWVzLTI1Ni1nY206c2VjcmV0QDE5OC41MS4xMDAuMTA6ODM4OA==#stale-node\n"))
	}))
	defer server.Close()

	cacheDir := filepath.Join(t.TempDir(), "subscription-cache")
	nodes, err := FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Millisecond)
	if err != nil {
		t.Fatalf("FetchSubscriptionNodes() warm cache error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node from warm fetch, got %d", len(nodes))
	}

	time.Sleep(5 * time.Millisecond)
	shouldFail = true

	nodes, err = FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Millisecond)
	if err != nil {
		t.Fatalf("expected stale cache fallback, got error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected cached node on fallback, got %d", len(nodes))
	}
	if requestCount != 2 {
		t.Fatalf("expected two remote attempts (warm + failed refresh), got %d", requestCount)
	}
}

func TestFetchSubscriptionNodesCachesRecentFailures(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.Error(w, "temporarily blocked", http.StatusTooManyRequests)
	}))
	defer server.Close()

	cacheDir := filepath.Join(t.TempDir(), "subscription-cache")
	_, err := FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Hour)
	if err == nil {
		t.Fatal("expected initial fetch failure")
	}
	if requestCount != 1 {
		t.Fatalf("expected 1 remote attempt after initial failure, got %d", requestCount)
	}

	_, err = FetchSubscriptionNodes(server.URL, time.Second, cacheDir, time.Hour)
	if err == nil {
		t.Fatal("expected cached failure to be surfaced")
	}
	if !strings.Contains(err.Error(), "cooling down") {
		t.Fatalf("expected failure cache cooldown error, got %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("expected recent failure cache to suppress refetch, got %d remote attempts", requestCount)
	}
}

func TestSubscriptionCacheDirResolvesRelativeDatabasePathAgainstConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		DatabasePath: filepath.Join("data", "runtime.db"),
	}
	cfg.SetFilePath(filepath.Join(dir, "config.yaml"))

	got := cfg.SubscriptionCacheDir()
	want := filepath.Join(dir, "data", "subscription-cache")
	if got != want {
		t.Fatalf("SubscriptionCacheDir() = %q, want %q", got, want)
	}
}

func TestRoutingTakesOverPoolInbound(t *testing.T) {
	mk := func(enabled bool, listenerAddr string, listenerPort uint16, routingListen string) *Config {
		c := &Config{}
		c.Listener.Address = listenerAddr
		c.Listener.Port = listenerPort
		c.Routing.Enabled = enabled
		c.Routing.Listen = routingListen
		return c
	}

	tests := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{
			name: "routing disabled never takes over",
			cfg:  mk(false, "0.0.0.0", 22323, ""),
			want: false,
		},
		{
			name: "route A: default listen takes over same port",
			cfg:  mk(true, "0.0.0.0", 22323, ""),
			want: true,
		},
		{
			name: "route A: empty host equals 0.0.0.0",
			cfg:  mk(true, "", 22323, ""),
			want: true,
		},
		{
			name: "route A: explicit routing.listen matching port",
			cfg:  mk(true, "0.0.0.0", 22323, "0.0.0.0:22323"),
			want: true,
		},
		{
			name: "route B: different port coexists",
			cfg:  mk(true, "0.0.0.0", 22323, "0.0.0.0:22324"),
			want: false,
		},
	}

	for _, tt := range tests {
		if got := tt.cfg.RoutingTakesOverPoolInbound(); got != tt.want {
			t.Fatalf("%s: RoutingTakesOverPoolInbound() = %v, want %v", tt.name, got, tt.want)
		}
	}
}
