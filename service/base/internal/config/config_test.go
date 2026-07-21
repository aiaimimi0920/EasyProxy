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

	"gopkg.in/yaml.v3"
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
	longLivedOnly := true

	cfg := &Config{
		ExtraListeners: []ExtraListenerConfig{{Address: "original-listener"}},
		LocalServer: LocalServerConfig{
			Enabled: true,
			Listen:  "127.0.0.1:22324",
			Auth: LocalServerAuthConfig{
				Username: "original-user",
				Password: "original-password",
			},
			SharedRevision:       3,
			CredentialGeneration: 4,
		},
		Management: ManagementConfig{
			Enabled:      &managementEnabled,
			ProbeTargets: []string{"original-probe"},
		},
		Routing: RoutingConfig{
			UseDefaultRules: &useDefaultRules,
			Rules:           []string{"DOMAIN,original.example,DIRECT"},
			NodeFilter: RoutingNodeFilterConfig{
				Countries: []string{"US"},
				Regions:   []string{"americas"},
				LongLived: &longLivedOnly,
			},
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

func TestBuildPortMapWaitsForConfigWriter(t *testing.T) {
	cfg := &Config{Nodes: []NodeConfig{{URI: "http://node.example:80", Port: 25001}}}
	cfg.Lock()
	started := make(chan struct{})
	done := make(chan map[string]uint16, 1)
	go func() {
		close(started)
		done <- cfg.BuildPortMap()
	}()
	<-started

	select {
	case <-done:
		cfg.Unlock()
		t.Fatal("BuildPortMap read nodes while the config write lock was held")
	case <-time.After(50 * time.Millisecond):
	}

	cfg.Unlock()
	select {
	case portMap := <-done:
		if got := portMap["http://node.example:80"]; got != 25001 {
			t.Fatalf("port map value = %d, want 25001", got)
		}
	case <-time.After(time.Second):
		t.Fatal("BuildPortMap did not resume after the config write lock was released")
	}
}

func TestConfigCloneDeepCopiesReferenceFields(t *testing.T) {
	t.Run("local server", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		if !reflect.DeepEqual(cloned.LocalServer, original.LocalServer) {
			t.Fatalf("cloned local server = %#v, want %#v", cloned.LocalServer, original.LocalServer)
		}
		cloned.LocalServer.Auth.Password = "changed"
		if got := original.LocalServer.Auth.Password; got != "original-password" {
			t.Fatalf("original local server password changed through clone: %q", got)
		}
	})

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

	t.Run("routing node filter countries", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.Routing.NodeFilter.Countries[0] = "changed"
		if got := original.Routing.NodeFilter.Countries[0]; got != "US" {
			t.Fatalf("original routing node filter country changed through clone: %q", got)
		}
	})

	t.Run("routing node filter regions", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		cloned.Routing.NodeFilter.Regions[0] = "changed"
		if got := original.Routing.NodeFilter.Regions[0]; got != "americas" {
			t.Fatalf("original routing node filter region changed through clone: %q", got)
		}
	})

	t.Run("routing node filter long lived pointer", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		*cloned.Routing.NodeFilter.LongLived = false
		if !*original.Routing.NodeFilter.LongLived {
			t.Fatal("original routing node filter long_lived changed through clone")
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

func localServerNormalizeConfig() *Config {
	return &Config{
		Mode: "pool",
		Listener: ListenerConfig{
			Address:  "127.0.0.1",
			Port:     22323,
			Protocol: InboundProtocolMixed,
			Username: "legacy_user",
			Password: "shared-secret",
		},
		Management: ManagementConfig{
			Password: "shared-secret",
		},
		LocalServer: LocalServerConfig{
			Enabled: true,
			Listen:  "127.0.0.1:22324",
		},
	}
}

func TestNormalizeLocalServerMigratesCanonicalCredential(t *testing.T) {
	cfg := localServerNormalizeConfig()

	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize returned error: %v", err)
	}

	if got, want := cfg.LocalServer.Auth.Username, "legacy_user"; got != want {
		t.Fatalf("canonical username = %q, want %q", got, want)
	}
	if got, want := cfg.LocalServer.Auth.Password, "shared-secret"; got != want {
		t.Fatalf("canonical password = %q, want %q", got, want)
	}
	if got, want := cfg.Listener.Username, cfg.LocalServer.Auth.Username; got != want {
		t.Fatalf("listener username = %q, want canonical %q", got, want)
	}
	if got, want := cfg.Listener.Password, cfg.LocalServer.Auth.Password; got != want {
		t.Fatalf("listener password = %q, want canonical %q", got, want)
	}
	if got, want := cfg.Management.Password, cfg.LocalServer.Auth.Password; got != want {
		t.Fatalf("management password = %q, want canonical %q", got, want)
	}
	if got, want := cfg.LocalServer.SharedRevision, int64(1); got != want {
		t.Fatalf("shared revision = %d, want %d", got, want)
	}
	if got, want := cfg.LocalServer.CredentialGeneration, uint64(2); got != want {
		t.Fatalf("credential generation = %d, want %d", got, want)
	}
}

func TestNormalizeLocalServerCredentialMigrationIsIdempotent(t *testing.T) {
	cfg := localServerNormalizeConfig()

	if err := cfg.normalize(); err != nil {
		t.Fatalf("first normalize returned error: %v", err)
	}
	if got := cfg.LocalServer.CredentialGeneration; got != 2 {
		t.Fatalf("credential generation after first normalize = %d, want 2", got)
	}

	if err := cfg.normalize(); err != nil {
		t.Fatalf("second normalize returned error: %v", err)
	}
	if got := cfg.LocalServer.CredentialGeneration; got != 2 {
		t.Fatalf("credential generation after second normalize = %d, want 2", got)
	}
}

func TestNormalizeLocalServerRejectsBypassTopology(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "hybrid mode",
			mutate: func(cfg *Config) {
				cfg.Mode = "hybrid"
			},
		},
		{
			name: "listener protocol is not mixed",
			mutate: func(cfg *Config) {
				cfg.Listener.Protocol = InboundProtocolHTTP
			},
		},
		{
			name: "extra listeners bypass dispatcher",
			mutate: func(cfg *Config) {
				cfg.ExtraListeners = []ExtraListenerConfig{{Address: "127.0.0.1", Port: 22325}}
			},
		},
		{
			name: "local and routing listen conflict",
			mutate: func(cfg *Config) {
				cfg.Routing.Listen = "127.0.0.1:22326"
			},
		},
		{
			name: "legacy passwords conflict during migration",
			mutate: func(cfg *Config) {
				cfg.Listener.Password = "listener-secret"
				cfg.Management.Password = "management-secret"
			},
		},
		{
			name: "no password source",
			mutate: func(cfg *Config) {
				cfg.Listener.Password = ""
				cfg.Management.Password = ""
			},
		},
		{
			name: "explicit canonical username is invalid",
			mutate: func(cfg *Config) {
				cfg.LocalServer.Auth.Username = "invalid+username"
				cfg.LocalServer.Auth.Password = "canonical-secret"
			},
		},
		{
			name: "password contains NUL",
			mutate: func(cfg *Config) {
				cfg.LocalServer.Auth.Password = "canonical\x00secret"
			},
		},
		{
			name: "password exceeds 256 bytes",
			mutate: func(cfg *Config) {
				cfg.LocalServer.Auth.Password = strings.Repeat("x", 257)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := localServerNormalizeConfig()
			tt.mutate(cfg)
			if err := cfg.normalize(); err == nil {
				t.Fatal("normalize accepted a Local Server topology that can bypass the dispatcher")
			}
		})
	}
}

func TestNormalizeLocalServerUsesDefaultUsernameWhenLegacyUsernameIsInvalid(t *testing.T) {
	cfg := localServerNormalizeConfig()
	cfg.Listener.Username = "legacy+username"

	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize returned error: %v", err)
	}
	if got, want := cfg.LocalServer.Auth.Username, "easyproxy"; got != want {
		t.Fatalf("canonical username = %q, want fallback %q", got, want)
	}

	explicit := localServerNormalizeConfig()
	explicit.LocalServer.Auth = LocalServerAuthConfig{
		Username: "canonical+username",
		Password: "canonical-secret",
	}
	if err := explicit.normalize(); err == nil {
		t.Fatal("normalize accepted an invalid explicit canonical username")
	}
}

func TestNormalizeLocalServerCanonicalCredentialOverridesLegacyFields(t *testing.T) {
	cfg := localServerNormalizeConfig()
	cfg.LocalServer.Auth = LocalServerAuthConfig{
		Username: "canonical-user",
		Password: "canonical-secret",
	}
	cfg.Listener.Username = "legacy+invalid"
	cfg.Listener.Password = "listener-secret"
	cfg.Management.Password = "management-secret"

	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize returned error: %v", err)
	}
	if got, want := cfg.Listener.Username, "canonical-user"; got != want {
		t.Fatalf("listener username = %q, want %q", got, want)
	}
	if got, want := cfg.Listener.Password, "canonical-secret"; got != want {
		t.Fatalf("listener password = %q, want %q", got, want)
	}
	if got, want := cfg.Management.Password, "canonical-secret"; got != want {
		t.Fatalf("management password = %q, want %q", got, want)
	}
}

func TestNormalizeWithPortMapRejectsInvalidLocalServerTopology(t *testing.T) {
	cfg := localServerNormalizeConfig()
	cfg.Listener.Protocol = InboundProtocolHTTP
	cfg.Nodes = []NodeConfig{{
		Name: "valid-node",
		URI:  "socks5://127.0.0.1:1080",
		Port: 25001,
	}}

	if err := cfg.NormalizeWithPortMap(nil); err == nil {
		t.Fatal("NormalizeWithPortMap accepted an invalid Local Server topology")
	}
}

func TestNormalizeWithPortMapReassignsDuplicatePreservedPort(t *testing.T) {
	const preservedPort uint16 = 25001

	cfg := &Config{
		Mode: "hybrid",
		Listener: ListenerConfig{
			Address:  "127.0.0.1",
			Port:     22323,
			Protocol: InboundProtocolHTTP,
		},
		MultiPort: MultiPortConfig{
			Address:  "127.0.0.1",
			BasePort: 25000,
			Protocol: InboundProtocolHTTP,
		},
		Nodes: []NodeConfig{
			{
				Name: "stale-port-node",
				URI:  "socks5://127.0.0.1:1081",
				Port: preservedPort,
			},
			{
				Name: "preserved-node",
				URI:  "socks5://127.0.0.1:1080",
			},
		},
	}

	portMap := map[string]uint16{
		cfg.Nodes[1].NodeKey(): preservedPort,
	}
	if err := cfg.NormalizeWithPortMap(portMap); err != nil {
		t.Fatalf("NormalizeWithPortMap() error = %v", err)
	}
	if got := cfg.Nodes[1].Port; got != preservedPort {
		t.Fatalf("preserved node port = %d, want %d", got, preservedPort)
	}
	if got := cfg.Nodes[0].Port; got == 0 || got == preservedPort {
		t.Fatalf("duplicate node port = %d, want a non-zero reassigned port", got)
	}
}

func TestSaveSettingsPersistsLocalServerAndRoutingNodeFilter(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	nodesPath := filepath.Join(dir, "preserved-nodes.txt")
	if err := os.WriteFile(nodesPath, []byte("socks5://127.0.0.1:1081#file-node\n"), 0o644); err != nil {
		t.Fatalf("write nodes file: %v", err)
	}

	const initialYAML = `mode: pool
listener:
  address: 127.0.0.1
  port: 22323
  protocol: mixed
  username: legacy_user
  password: shared-secret
management:
  password: shared-secret
local_server:
  enabled: true
  listen: 127.0.0.1:22324
routing:
  enabled: false
  node_filter:
    countries: [US, JP]
    regions: [americas, asia]
    long_lived: true
nodes_file: preserved-nodes.txt
database_path: preserved.db
nodes:
  - name: preserved-node
    uri: socks5://127.0.0.1:1080
`
	if err := os.WriteFile(configPath, []byte(initialYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	cfg.Lock()
	err = cfg.SaveSettings()
	cfg.Unlock()
	if err != nil {
		t.Fatalf("SaveSettings returned error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var saved Config
	if err := yaml.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode saved config: %v", err)
	}

	if !saved.LocalServer.Enabled {
		t.Fatal("saved local_server.enabled = false, want true")
	}
	if got, want := saved.LocalServer.Listen, "127.0.0.1:22324"; got != want {
		t.Fatalf("saved local_server.listen = %q, want %q", got, want)
	}
	if got, want := saved.LocalServer.Auth.Username, "legacy_user"; got != want {
		t.Fatalf("saved canonical username = %q, want %q", got, want)
	}
	if got, want := saved.LocalServer.Auth.Password, "shared-secret"; got != want {
		t.Fatalf("saved canonical password = %q, want %q", got, want)
	}
	if got, want := saved.LocalServer.SharedRevision, int64(1); got != want {
		t.Fatalf("saved shared revision = %d, want %d", got, want)
	}
	if got, want := saved.LocalServer.CredentialGeneration, uint64(2); got != want {
		t.Fatalf("saved credential generation = %d, want %d", got, want)
	}
	if got, want := saved.Listener.Username, saved.LocalServer.Auth.Username; got != want {
		t.Fatalf("saved listener username = %q, want canonical %q", got, want)
	}
	if got, want := saved.Listener.Password, saved.LocalServer.Auth.Password; got != want {
		t.Fatalf("saved listener password = %q, want canonical %q", got, want)
	}
	if got, want := saved.Management.Password, saved.LocalServer.Auth.Password; got != want {
		t.Fatalf("saved management password = %q, want canonical %q", got, want)
	}
	if got, want := saved.Routing.NodeFilter.Countries, []string{"US", "JP"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("saved node filter countries = %#v, want %#v", got, want)
	}
	if got, want := saved.Routing.NodeFilter.Regions, []string{"americas", "asia"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("saved node filter regions = %#v, want %#v", got, want)
	}
	if saved.Routing.NodeFilter.LongLived == nil || !*saved.Routing.NodeFilter.LongLived {
		t.Fatalf("saved node filter long_lived = %v, want true", saved.Routing.NodeFilter.LongLived)
	}

	if got, want := saved.NodesFile, "preserved-nodes.txt"; got != want {
		t.Fatalf("saved nodes_file = %q, want preserved value %q", got, want)
	}
	if got, want := saved.DatabasePath, "preserved.db"; got != want {
		t.Fatalf("saved database_path = %q, want preserved value %q", got, want)
	}
	if len(saved.Nodes) != 1 || saved.Nodes[0].Name != "preserved-node" {
		t.Fatalf("saved nodes = %#v, want original non-settings node", saved.Nodes)
	}
}

func TestDispatchOwnsPrimaryInboundInLocalServerMode(t *testing.T) {
	cfg := &Config{
		Listener: ListenerConfig{
			Address: "127.0.0.1",
			Port:    22323,
		},
		LocalServer: LocalServerConfig{
			Enabled: true,
			Listen:  "127.0.0.1:22324",
		},
	}

	if !cfg.DispatchOwnsPrimaryInbound() {
		t.Fatal("Local Server did not claim the primary pool inbound")
	}
	if got, want := cfg.DispatchListen(), "127.0.0.1:22324"; got != want {
		t.Fatalf("DispatchListen() = %q, want Local Server listen %q", got, want)
	}
	if !cfg.DispatchEnabled() {
		t.Fatal("DispatchEnabled() = false with Local Server enabled")
	}
}

func TestDisabledLocalServerPreservesLegacyDispatchTopology(t *testing.T) {
	tests := []struct {
		name           string
		cfg            *Config
		wantListen     string
		wantOwnership  bool
		wantDispatcher bool
	}{
		{
			name: "routing disabled uses listener",
			cfg: &Config{
				Listener:    ListenerConfig{Address: "127.0.0.1", Port: 22323},
				LocalServer: LocalServerConfig{Listen: "127.0.0.1:30000"},
			},
			wantListen: "127.0.0.1:22323",
		},
		{
			name: "zero listener uses legacy defaults",
			cfg: &Config{
				LocalServer: LocalServerConfig{Listen: "127.0.0.1:30000"},
			},
			wantListen: "0.0.0.0:22323",
		},
		{
			name: "route A owns listener",
			cfg: &Config{
				Listener: ListenerConfig{Address: "127.0.0.1", Port: 22323},
				Routing:  RoutingConfig{Enabled: true},
			},
			wantListen:     "127.0.0.1:22323",
			wantOwnership:  true,
			wantDispatcher: true,
		},
		{
			name: "route B coexists on separate listen",
			cfg: &Config{
				Listener: ListenerConfig{Address: "127.0.0.1", Port: 22323},
				Routing: RoutingConfig{
					Enabled: true,
					Listen:  "127.0.0.1:22324",
				},
			},
			wantListen:     "127.0.0.1:22324",
			wantDispatcher: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.DispatchListen(); got != tt.wantListen {
				t.Fatalf("DispatchListen() = %q, want %q", got, tt.wantListen)
			}
			if got := tt.cfg.DispatchOwnsPrimaryInbound(); got != tt.wantOwnership {
				t.Fatalf("DispatchOwnsPrimaryInbound() = %v, want %v", got, tt.wantOwnership)
			}
			if got := tt.cfg.DispatchEnabled(); got != tt.wantDispatcher {
				t.Fatalf("DispatchEnabled() = %v, want %v", got, tt.wantDispatcher)
			}
		})
	}
}

func TestGatewayDefaultsFailOpen(t *testing.T) {
	cfg := &Config{}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Gateway.Routing.NoAvailableProxyPolicy, "DIRECT"; got != want {
		t.Fatalf("no_available_proxy_policy = %q, want %q", got, want)
	}
	if got, want := cfg.Gateway.Listen, "0.0.0.0:15001"; got != want {
		t.Fatalf("gateway listen = %q, want %q", got, want)
	}
}

func TestGatewayRejectsInvalidCIDRAndPolicy(t *testing.T) {
	cfg := &Config{Gateway: GatewayConfig{
		Enabled: true,
		Routing: GatewayRoutingConfig{NoAvailableProxyPolicy: "DROP"},
		Ingress: GatewayIngressConfig{TrustedCIDRs: []string{"not-a-cidr"}},
	}}
	if err := cfg.normalize(); err == nil {
		t.Fatal("expected gateway validation error")
	}
}

func TestGatewayDeviceAliasesClone(t *testing.T) {
	cfg := &Config{Gateway: GatewayConfig{Devices: map[string]GatewayDeviceConfig{
		"laptop": {Addresses: []string{"192.168.15.100", "100.64.0.20"}},
	}}}
	clone := cfg.Clone()
	clone.Gateway.Devices["laptop"].Addresses[0] = "10.0.0.1"
	if got := cfg.Gateway.Devices["laptop"].Addresses[0]; got != "192.168.15.100" {
		t.Fatalf("device aliases were not deep-cloned: got %q", got)
	}
}
