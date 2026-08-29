package subscription

import (
	"fmt"
	"strings"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
)

func normalizeManagedConnector(connector config.ConnectorSourceConfig) (config.ConnectorSourceConfig, error) {
	connector.Name = strings.TrimSpace(connector.Name)
	connector.Input = strings.TrimSpace(connector.Input)
	connector.Group = strings.TrimSpace(connector.Group)
	connector.Notes = strings.TrimSpace(connector.Notes)
	connector.ConnectorType = strings.TrimSpace(connector.ConnectorType)
	if connector.Name == "" {
		return config.ConnectorSourceConfig{}, fmt.Errorf("%w: 连接器名称不能为空", monitor.ErrInvalidConnector)
	}
	if connector.Input == "" {
		return config.ConnectorSourceConfig{}, fmt.Errorf("%w: 连接器入口不能为空", monitor.ErrInvalidConnector)
	}
	if connector.ConnectorType == "" {
		connector.ConnectorType = connectorTypeECHWorker
	}
	if connector.ConnectorConfig == nil {
		connector.ConnectorConfig = map[string]any{}
	} else {
		connector.ConnectorConfig = cloneConnectorOptions(connector.ConnectorConfig)
	}

	switch connector.ConnectorType {
	case connectorTypeECHWorker:
		if strings.TrimSpace(extractStringOption(connector.ConnectorConfig, "local_protocol")) == "" {
			connector.ConnectorConfig["local_protocol"] = "socks5"
		}
	case connectorTypeZenProxyClient:
		if strings.TrimSpace(extractStringOption(connector.ConnectorConfig, "api_key")) == "" {
			return config.ConnectorSourceConfig{}, fmt.Errorf("%w: zenproxy_client 缺少 api_key", monitor.ErrInvalidConnector)
		}
		if extractIntOption(connector.ConnectorConfig, "count", 0) <= 0 {
			connector.ConnectorConfig["count"] = 10
		}
	default:
		return config.ConnectorSourceConfig{}, fmt.Errorf("%w: 当前仅支持 ech_worker 和 zenproxy_client", monitor.ErrInvalidConnector)
	}
	return connector, nil
}

func cloneConnectorConfig(connector config.ConnectorSourceConfig) config.ConnectorSourceConfig {
	cloned := connector
	cloned.ConnectorConfig = cloneConnectorOptions(connector.ConnectorConfig)
	return cloned
}

func cloneConnectorConfigs(connectors []config.ConnectorSourceConfig) []config.ConnectorSourceConfig {
	cloned := make([]config.ConnectorSourceConfig, len(connectors))
	for index, connector := range connectors {
		cloned[index] = cloneConnectorConfig(connector)
	}
	return cloned
}

func connectorIndexByName(connectors []config.ConnectorSourceConfig, name string) int {
	for idx, connector := range connectors {
		if connector.Name == name {
			return idx
		}
	}
	return -1
}

func (m *Manager) configRef() (*config.Config, error) {
	m.mu.RLock()
	cfg := m.baseCfg
	m.mu.RUnlock()
	if cfg == nil {
		return nil, fmt.Errorf("%w: 配置未初始化", monitor.ErrInvalidConnector)
	}
	return cfg, nil
}
