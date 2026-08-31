package subscription

import (
	"fmt"
	"strings"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
)

func (m *connectorRuntimeManager) expandConnectorSources(cfg *config.Config, sources []RuntimeSource) ([]RuntimeSource, error) {
	if cfg == nil || len(sources) == 0 {
		return sources, nil
	}

	fanoutCount := cfg.SourceSync.ConnectorRuntime.PreferredIP.FanoutCount
	if fanoutCount <= 1 {
		return sources, nil
	}

	expanded := make([]RuntimeSource, 0, len(sources)*fanoutCount)
	nextCache := make(map[string][]RuntimeSource, len(sources))
	for _, source := range sources {
		if source.Kind != SourceKindConnector {
			expanded = append(expanded, source)
			continue
		}

		connectorType := extractStringOption(source.Options, "connector_type")
		connectorCfg := extractMapOption(source.Options, "connector_config")
		serverIP := strings.TrimSpace(extractStringOption(connectorCfg, "server_ip"))
		if connectorType != connectorTypeECHWorker || serverIP != "" {
			expanded = append(expanded, source)
			continue
		}

		cacheKey := sourceKey(source)
		if cached, ok := m.fanoutCache[cacheKey]; ok && len(cached) > 0 {
			cloned := cloneRuntimeSources(cached)
			nextCache[cacheKey] = cloned
			expanded = append(expanded, cloned...)
			expanded = append(expanded, source)
			continue
		}

		template := config.ConnectorSourceConfig{
			Name:            strings.TrimSpace(source.Name),
			Input:           strings.TrimSpace(source.Input),
			Enabled:         true,
			ConnectorType:   connectorTypeECHWorker,
			ConnectorConfig: cloneConnectorOptions(connectorCfg),
		}
		selected, _, _, err := m.preferredIPSelector(
			m.ctx,
			cfg.FilePath(),
			cfg.SourceSync.ConnectorRuntime,
			template,
			monitor.PreferredIPRefreshOptions{TopCount: fanoutCount},
		)
		if err != nil {
			m.logger.Warnf("preferred IP fanout failed for connector %s, using single connector: %v", source.Name, err)
			expanded = append(expanded, source)
			continue
		}
		generated := buildPreferredRuntimeSources(source, selected)
		if len(generated) == 0 {
			expanded = append(expanded, source)
			continue
		}
		nextCache[cacheKey] = cloneRuntimeSources(generated)
		expanded = append(expanded, generated...)
		expanded = append(expanded, source)
	}

	m.fanoutCache = nextCache
	return expanded, nil
}

func buildPreferredRuntimeSources(source RuntimeSource, selected []preferredIPResultRow) []RuntimeSource {
	generated := make([]RuntimeSource, 0, len(selected))
	for idx, item := range selected {
		clone := RuntimeSource{
			ID:     strings.TrimSpace(source.ID),
			Kind:   source.Kind,
			Name:   strings.TrimSpace(source.Name),
			Input:  strings.TrimSpace(source.Input),
			Origin: strings.TrimSpace(source.Origin),
		}
		if source.Options != nil {
			clone.Options = cloneConnectorOptions(source.Options)
		} else {
			clone.Options = map[string]any{}
		}

		connectorCfg := extractMapOption(clone.Options, "connector_config")
		if connectorCfg == nil {
			connectorCfg = map[string]any{}
		}
		connectorCfg["server_ip"] = item.IP
		clone.Options["connector_config"] = connectorCfg
		if strings.TrimSpace(clone.ID) == "" {
			clone.ID = fmt.Sprintf("connector-pref-%d", idx+1)
		} else {
			clone.ID = fmt.Sprintf("%s-pref-%d", clone.ID, idx+1)
		}
		if strings.TrimSpace(clone.Name) == "" {
			clone.Name = fmt.Sprintf("Connector Preferred %d", idx+1)
		} else {
			clone.Name = fmt.Sprintf("%s Preferred %d", clone.Name, idx+1)
		}
		generated = append(generated, clone)
	}
	return generated
}

func cloneRuntimeSources(input []RuntimeSource) []RuntimeSource {
	if len(input) == 0 {
		return nil
	}
	cloned := make([]RuntimeSource, 0, len(input))
	for _, item := range input {
		copied := RuntimeSource{
			ID:     item.ID,
			Kind:   item.Kind,
			Name:   item.Name,
			Input:  item.Input,
			Origin: item.Origin,
		}
		if item.Options != nil {
			copied.Options = cloneConnectorOptions(item.Options)
		}
		cloned = append(cloned, copied)
	}
	return cloned
}
