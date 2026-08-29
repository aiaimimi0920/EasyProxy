package subscription

import (
	"fmt"
	"strings"

	"easy_proxies/internal/config"
	"easy_proxies/internal/store"
)

func (m *Manager) fetchSubscriptionSources(sources []RuntimeSource) ([]config.NodeConfig, error) {
	var allNodes []config.NodeConfig
	var lastErr error

	timeout := m.currentFetchTimeout()
	cacheDir, localCacheTTL, sourceSyncCacheTTL := m.currentSubscriptionCacheSettings()
	for _, source := range sources {
		if source.Kind != SourceKindSubscription {
			continue
		}
		cacheTTL := sourceSyncCacheTTL
		if source.Origin == "local" {
			cacheTTL = localCacheTTL
		}
		nodes, err := m.fetchSubscription(source.Input, timeout, cacheDir, cacheTTL)
		if err != nil {
			m.logger.Warnf("failed to fetch %s: %v", source.Input, err)
			lastErr = err
			continue
		}
		for idx := range nodes {
			nodes[idx].Source = mapSourceOriginToNodeSource(source.Origin)
			nodes[idx].Name = buildNodeName(nodes[idx].URI, source.Name)
			nodes[idx].SourceKind = string(source.Kind)
			nodes[idx].SourceName = strings.TrimSpace(source.Name)
			nodes[idx].SourceRef = runtimeSourceRef(source)
		}
		allNodes = append(allNodes, nodes...)
	}

	if len(allNodes) == 0 && lastErr != nil && len(sources) > 0 {
		return nil, lastErr
	}
	return allNodes, nil
}

func (m *Manager) materializeProxySources(sources []RuntimeSource) []config.NodeConfig {
	var nodes []config.NodeConfig
	for idx, source := range sources {
		if source.Kind != SourceKindProxyURI {
			continue
		}
		uri := strings.TrimSpace(source.Input)
		if uri == "" {
			continue
		}
		name := buildNodeName(uri, source.Name)
		if name == "" {
			name = fmt.Sprintf("remote-node-%d", idx+1)
		}
		nodes = append(nodes, config.NodeConfig{
			Name:       name,
			URI:        uri,
			Source:     mapSourceOriginToNodeSource(source.Origin),
			SourceKind: string(source.Kind),
			SourceName: strings.TrimSpace(source.Name),
			SourceRef:  runtimeSourceRef(source),
		})
	}
	return nodes
}

func (m *Manager) syncRuntimeNodesToStore(nodes []config.NodeConfig) error {
	if m == nil || m.store == nil || len(nodes) == 0 {
		return nil
	}

	storeNodes := make([]store.Node, 0, len(nodes))
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		source := string(node.Source)
		if !store.IsRuntimeNodeSource(source) {
			continue
		}

		uri := strings.TrimSpace(node.URI)
		if uri == "" {
			continue
		}
		if _, ok := seen[uri]; ok {
			continue
		}
		seen[uri] = struct{}{}

		name := strings.TrimSpace(node.Name)
		if name == "" {
			name = buildNodeName(uri, node.SourceName)
		}
		if name == "" {
			name = buildNodeName(uri, "runtime-node")
		}

		storeNodes = append(storeNodes, store.Node{
			URI:      uri,
			Name:     name,
			Source:   source,
			Port:     node.Port,
			Username: node.Username,
			Password: node.Password,
			Enabled:  true,
		})
	}

	if len(storeNodes) == 0 {
		return nil
	}
	return m.store.BulkUpsertNodes(m.ctx, storeNodes)
}

func mapSourceOriginToNodeSource(origin string) config.NodeSource {
	switch origin {
	case "manifest":
		return config.NodeSourceManifest
	case "fallback":
		return config.NodeSourceFallback
	default:
		return config.NodeSourceSubscription
	}
}

func cloneConnectorOptions(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		if nested, ok := value.(map[string]any); ok {
			child := make(map[string]any, len(nested))
			for childKey, childValue := range nested {
				child[childKey] = childValue
			}
			cloned[key] = child
			continue
		}
		cloned[key] = value
	}
	return cloned
}
