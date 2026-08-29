package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"easy_proxies/internal/config"
)

func (m *Manager) buildActiveSourceSnapshot() (activeSourceSnapshot, error) {
	cfg := m.baseConfigSnapshot()

	snapshot := activeSourceSnapshot{}
	if cfg == nil {
		return snapshot, fmt.Errorf("config is nil")
	}

	localSources := m.buildLocalSources(cfg)
	snapshot.LocalSourceCount = len(localSources)

	var localSubscriptionSources []RuntimeSource
	var localProxySources []RuntimeSource
	var localConnectorSources []RuntimeSource
	for _, source := range localSources {
		switch source.Kind {
		case SourceKindSubscription:
			localSubscriptionSources = append(localSubscriptionSources, source)
		case SourceKindProxyURI:
			localProxySources = append(localProxySources, source)
		case SourceKindConnector:
			localConnectorSources = append(localConnectorSources, source)
		}
	}

	if !cfg.SourceSync.Enabled || strings.TrimSpace(cfg.SourceSync.ManifestURL) == "" {
		connectorProxySources, connectorErr := m.reconcileConnectorSources(cfg, localConnectorSources)
		if connectorErr != nil {
			m.logger.Warnf("connector reconcile failed: %v", connectorErr)
		}
		snapshot.SubscriptionSources = dedupeSourcesWithPrecedence(localSubscriptionSources)
		snapshot.EphemeralProxySources = dedupeSourcesWithPrecedence(connectorProxySources)
		snapshot.ConnectorSourceCount = len(localConnectorSources)
		snapshot.ConnectorInstanceCount = len(connectorProxySources)
		m.mu.Lock()
		m.sourceSyncStatus.Enabled = cfg.SourceSync.Enabled
		m.sourceSyncStatus.ManifestURL = strings.TrimSpace(cfg.SourceSync.ManifestURL)
		m.sourceSyncStatus.ManifestHealthy = false
		m.sourceSyncStatus.LastError = ""
		m.sourceSyncStatus.LocalSourceCount = snapshot.LocalSourceCount
		m.sourceSyncStatus.ManifestSourceCount = 0
		m.sourceSyncStatus.FallbackSourceCount = 0
		m.sourceSyncStatus.ConnectorSourceCount = snapshot.ConnectorSourceCount
		m.sourceSyncStatus.ConnectorInstanceCount = snapshot.ConnectorInstanceCount
		m.sourceSyncStatus.FallbackActive = false
		m.mu.Unlock()
		return snapshot, nil
	}

	manifestSources, err := m.fetchManifestSources(cfg)
	if err == nil {
		var manifestSubscriptionSources []RuntimeSource
		var manifestProxySources []RuntimeSource
		var manifestConnectorSources []RuntimeSource

		for _, source := range manifestSources {
			switch source.Kind {
			case SourceKindSubscription:
				manifestSubscriptionSources = append(manifestSubscriptionSources, source)
			case SourceKindProxyURI:
				manifestProxySources = append(manifestProxySources, source)
			case SourceKindConnector:
				manifestConnectorSources = append(manifestConnectorSources, source)
			}
		}
		activeConnectorSources := dedupeSourcesWithPrecedence(localConnectorSources, manifestConnectorSources)
		snapshot.ConnectorSourceCount = len(activeConnectorSources)

		connectorProxySources, connectorErr := m.reconcileConnectorSources(cfg, activeConnectorSources)
		if connectorErr != nil {
			m.logger.Warnf("connector reconcile failed: %v", connectorErr)
		}
		snapshot.ConnectorInstanceCount = len(connectorProxySources)

		snapshot.SubscriptionSources = dedupeSourcesWithPrecedence(localSubscriptionSources, manifestSubscriptionSources)
		localProxyKeys := make(map[string]struct{}, len(localProxySources))
		for _, source := range localProxySources {
			localProxyKeys[sourceKey(source)] = struct{}{}
		}
		for _, source := range dedupeSourcesWithPrecedence(manifestProxySources, connectorProxySources) {
			if _, exists := localProxyKeys[sourceKey(source)]; exists {
				continue
			}
			snapshot.EphemeralProxySources = append(snapshot.EphemeralProxySources, source)
		}
		snapshot.ManifestSourceCount = len(manifestSources)

		m.mu.Lock()
		m.sourceSyncStatus.Enabled = true
		m.sourceSyncStatus.ManifestURL = strings.TrimSpace(cfg.SourceSync.ManifestURL)
		m.sourceSyncStatus.ManifestHealthy = true
		m.sourceSyncStatus.LastSync = time.Now()
		m.sourceSyncStatus.LastSuccess = m.sourceSyncStatus.LastSync
		m.sourceSyncStatus.LastError = ""
		m.sourceSyncStatus.FallbackActive = false
		m.sourceSyncStatus.LocalSourceCount = snapshot.LocalSourceCount
		m.sourceSyncStatus.ManifestSourceCount = snapshot.ManifestSourceCount
		m.sourceSyncStatus.FallbackSourceCount = 0
		m.sourceSyncStatus.ConnectorSourceCount = snapshot.ConnectorSourceCount
		m.sourceSyncStatus.ConnectorInstanceCount = snapshot.ConnectorInstanceCount
		m.mu.Unlock()
		return snapshot, nil
	}

	connectorProxySources, connectorErr := m.reconcileConnectorSources(cfg, localConnectorSources)
	if connectorErr != nil {
		m.logger.Warnf("connector reconcile failed: %v", connectorErr)
	}
	fallbackSources := make([]RuntimeSource, 0, len(cfg.SourceSync.FallbackSubscriptions))
	for idx, subURL := range cfg.SourceSync.FallbackSubscriptions {
		normalized := normalizeRuntimeSource(RuntimeSource{
			ID:     fmt.Sprintf("fallback-%d", idx+1),
			Kind:   SourceKindSubscription,
			Name:   fmt.Sprintf("fallback-%d", idx+1),
			Input:  subURL,
			Origin: "fallback",
		}, cfg.SourceSync.DefaultDirectProxyScheme)
		if strings.TrimSpace(normalized.Input) == "" {
			continue
		}
		fallbackSources = append(fallbackSources, normalized)
	}

	snapshot.SubscriptionSources = dedupeSourcesWithPrecedence(localSubscriptionSources, fallbackSources)
	snapshot.EphemeralProxySources = dedupeSourcesWithPrecedence(connectorProxySources)
	snapshot.FallbackActive = len(fallbackSources) > 0
	snapshot.FallbackSourceCount = len(fallbackSources)
	snapshot.ConnectorSourceCount = len(localConnectorSources)
	snapshot.ConnectorInstanceCount = len(connectorProxySources)

	m.mu.Lock()
	m.sourceSyncStatus.Enabled = true
	m.sourceSyncStatus.ManifestURL = strings.TrimSpace(cfg.SourceSync.ManifestURL)
	m.sourceSyncStatus.ManifestHealthy = false
	m.sourceSyncStatus.LastSync = time.Now()
	m.sourceSyncStatus.LastError = err.Error()
	m.sourceSyncStatus.FallbackActive = snapshot.FallbackActive
	m.sourceSyncStatus.LocalSourceCount = snapshot.LocalSourceCount
	m.sourceSyncStatus.ManifestSourceCount = 0
	m.sourceSyncStatus.FallbackSourceCount = snapshot.FallbackSourceCount
	m.sourceSyncStatus.ConnectorSourceCount = snapshot.ConnectorSourceCount
	m.sourceSyncStatus.ConnectorInstanceCount = snapshot.ConnectorInstanceCount
	m.mu.Unlock()

	if len(snapshot.SubscriptionSources) == 0 && snapshot.LocalSourceCount == 0 {
		return snapshot, err
	}
	return snapshot, nil
}

func (m *Manager) reconcileConnectorSources(cfg *config.Config, connectorSources []RuntimeSource) ([]RuntimeSource, error) {
	if m.connectorRuntime == nil {
		return nil, nil
	}
	return m.connectorRuntime.Reconcile(cfg, connectorSources)
}

func (m *Manager) buildLocalSources(cfg *config.Config) []RuntimeSource {
	var sources []RuntimeSource

	for idx, subURL := range cfg.Subscriptions {
		normalized := normalizeRuntimeSource(RuntimeSource{
			ID:     fmt.Sprintf("local-sub-%d", idx+1),
			Kind:   SourceKindSubscription,
			Name:   fmt.Sprintf("subscription-%d", idx+1),
			Input:  subURL,
			Origin: "local",
		}, cfg.SourceSync.DefaultDirectProxyScheme)
		if strings.TrimSpace(normalized.Input) == "" {
			continue
		}
		sources = append(sources, normalized)
	}

	for idx, node := range cfg.Nodes {
		switch node.Source {
		case config.NodeSourceInline, config.NodeSourceFile, config.NodeSourceManual:
			normalized := normalizeRuntimeSource(RuntimeSource{
				ID:     fmt.Sprintf("local-node-%d", idx+1),
				Kind:   SourceKindProxyURI,
				Name:   node.Name,
				Input:  node.URI,
				Origin: "local",
			}, cfg.SourceSync.DefaultDirectProxyScheme)
			if strings.TrimSpace(normalized.Input) == "" {
				continue
			}
			sources = append(sources, normalized)
		}
	}

	for idx, connector := range cfg.Connectors {
		if !connector.Enabled || connector.TemplateOnly {
			continue
		}
		normalized := normalizeRuntimeSource(RuntimeSource{
			ID:     fmt.Sprintf("local-connector-%d", idx+1),
			Kind:   SourceKindConnector,
			Name:   connector.Name,
			Input:  connector.Input,
			Origin: "local",
			Options: map[string]any{
				"connector_type":   strings.TrimSpace(connector.ConnectorType),
				"connector_config": cloneConnectorOptions(connector.ConnectorConfig),
			},
		}, cfg.SourceSync.DefaultDirectProxyScheme)
		if strings.TrimSpace(normalized.Input) == "" {
			continue
		}
		sources = append(sources, normalized)
	}

	return dedupeSourcesWithPrecedence(sources)
}

func hasEnabledLocalConnectors(connectors []config.ConnectorSourceConfig) bool {
	for _, connector := range connectors {
		if connector.Enabled && !connector.TemplateOnly && strings.TrimSpace(connector.Input) != "" {
			return true
		}
	}
	return false
}

func (m *Manager) fetchManifestSources(cfg *config.Config) ([]RuntimeSource, error) {
	timeout := cfg.SourceSync.RequestTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.SourceSync.ManifestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create manifest request: %w", err)
	}
	if strings.TrimSpace(cfg.SourceSync.ManifestToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.SourceSync.ManifestToken))
	}
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest returned status %d", resp.StatusCode)
	}

	var payload manifestResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if !payload.Success {
		return nil, fmt.Errorf("manifest response indicated failure")
	}

	var sources []RuntimeSource
	for _, source := range payload.Sources {
		if !source.Enabled {
			continue
		}
		normalized := normalizeRuntimeSource(RuntimeSource{
			ID:      source.ID,
			Kind:    source.Kind,
			Name:    source.Name,
			Input:   source.Input,
			Options: source.Options,
			Origin:  "manifest",
		}, cfg.SourceSync.DefaultDirectProxyScheme)
		if strings.TrimSpace(normalized.Input) == "" {
			continue
		}
		sources = append(sources, normalized)
	}

	return dedupeSourcesWithPrecedence(sources), nil
}
