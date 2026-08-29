package config

import (
	"net/http"
	"sync"
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
	dnsEnabled := false

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
		DNS: DNSConfig{
			Enabled:       &dnsEnabled,
			RemoteServers: []string{"https://dns.example.test/query"},
			Detour:        "node-pool",
			Strategy:      "ipv4_only",
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
