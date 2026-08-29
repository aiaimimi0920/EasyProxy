package config

import (
	"reflect"
	"testing"
	"time"
)

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

	t.Run("DNS settings", func(t *testing.T) {
		original := cloneTestConfig()
		cloned := original.Clone()
		if cloned.DNS.Enabled == nil || *cloned.DNS.Enabled != false {
			t.Fatalf("cloned DNS enabled = %#v, want false", cloned.DNS.Enabled)
		}
		cloned.DNS.RemoteServers[0] = "changed"
		*cloned.DNS.Enabled = true
		if got := original.DNS.RemoteServers[0]; got != "https://dns.example.test/query" {
			t.Fatalf("original DNS remote server changed through clone: %q", got)
		}
		if *original.DNS.Enabled {
			t.Fatal("original DNS enabled value changed through clone")
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
