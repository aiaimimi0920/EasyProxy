package subscription

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
)

func (m *Manager) ListConfigConnectors(_ context.Context) ([]config.ConnectorSourceConfig, error) {
	m.mu.RLock()
	cfg := m.baseCfg
	m.mu.RUnlock()
	if cfg == nil {
		return nil, fmt.Errorf("%w: 配置未初始化", monitor.ErrInvalidConnector)
	}

	cfg.RLock()
	defer cfg.RUnlock()

	connectors := make([]config.ConnectorSourceConfig, len(cfg.Connectors))
	for idx, connector := range cfg.Connectors {
		connectors[idx] = cloneConnectorConfig(connector)
	}
	return connectors, nil
}

func (m *Manager) beginConnectorMutation(ctx context.Context) (func(), error) {
	m.mu.RLock()
	boxMgr := m.boxMgr
	m.mu.RUnlock()
	if boxMgr == nil {
		return func() {}, nil
	}
	return boxMgr.BeginConfigMutation(ctx)
}

func (m *Manager) beginConnectorCommit(ctx context.Context, expectedCfg *config.Config) (*config.Config, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := m.beginConnectorMutation(ctx)
	if err != nil {
		return nil, nil, err
	}
	currentCfg, err := m.configRef()
	if err != nil {
		release()
		return nil, nil, err
	}
	if expectedCfg != nil && currentCfg != expectedCfg {
		release()
		return nil, nil, fmt.Errorf("%w: 配置已更新，请重试", monitor.ErrConnectorConflict)
	}
	return currentCfg, release, nil
}

// saveConnectorCandidate persists a candidate while the caller holds cfg's
// write lock; failed writes therefore leave the active in-memory connectors untouched.
func saveConnectorCandidate(cfg *config.Config, connectors []config.ConnectorSourceConfig) error {
	candidate := cfg.Clone()
	candidate.Connectors = cloneConnectorConfigs(connectors)
	candidate.Lock()
	err := candidate.SaveSettings()
	candidate.Unlock()
	if err != nil {
		return err
	}
	cfg.Connectors = cloneConnectorConfigs(connectors)
	return nil
}

func (m *Manager) CreateConnector(ctx context.Context, connector config.ConnectorSourceConfig) (config.ConnectorSourceConfig, error) {
	cfg, release, err := m.beginConnectorCommit(ctx, nil)
	if err != nil {
		return config.ConnectorSourceConfig{}, err
	}
	defer release()

	normalized, err := normalizeManagedConnector(connector)
	if err != nil {
		return config.ConnectorSourceConfig{}, err
	}

	cfg.Lock()
	defer cfg.Unlock()

	if connectorIndexByName(cfg.Connectors, normalized.Name) >= 0 {
		return config.ConnectorSourceConfig{}, fmt.Errorf("%w: %s", monitor.ErrConnectorConflict, normalized.Name)
	}
	connectors := append(cloneConnectorConfigs(cfg.Connectors), normalized)
	if err := saveConnectorCandidate(cfg, connectors); err != nil {
		return config.ConnectorSourceConfig{}, fmt.Errorf("保存连接器配置失败: %w", err)
	}
	return cloneConnectorConfig(normalized), nil
}

func (m *Manager) UpdateConnector(ctx context.Context, name string, connector config.ConnectorSourceConfig) (config.ConnectorSourceConfig, error) {
	cfg, release, err := m.beginConnectorCommit(ctx, nil)
	if err != nil {
		return config.ConnectorSourceConfig{}, err
	}
	defer release()

	normalized, err := normalizeManagedConnector(connector)
	if err != nil {
		return config.ConnectorSourceConfig{}, err
	}

	cfg.Lock()
	defer cfg.Unlock()

	index := connectorIndexByName(cfg.Connectors, name)
	if index < 0 {
		return config.ConnectorSourceConfig{}, fmt.Errorf("%w: %s", monitor.ErrConnectorNotFound, name)
	}
	if normalized.Name != name && connectorIndexByName(cfg.Connectors, normalized.Name) >= 0 {
		return config.ConnectorSourceConfig{}, fmt.Errorf("%w: %s", monitor.ErrConnectorConflict, normalized.Name)
	}
	connectors := cloneConnectorConfigs(cfg.Connectors)
	connectors[index] = normalized
	if err := saveConnectorCandidate(cfg, connectors); err != nil {
		return config.ConnectorSourceConfig{}, fmt.Errorf("保存连接器配置失败: %w", err)
	}
	return cloneConnectorConfig(normalized), nil
}

func (m *Manager) DeleteConnector(ctx context.Context, name string) error {
	cfg, release, err := m.beginConnectorCommit(ctx, nil)
	if err != nil {
		return err
	}
	defer release()

	cfg.Lock()
	defer cfg.Unlock()

	index := connectorIndexByName(cfg.Connectors, name)
	if index < 0 {
		return fmt.Errorf("%w: %s", monitor.ErrConnectorNotFound, name)
	}
	connectors := append(cloneConnectorConfigs(cfg.Connectors[:index]), cloneConnectorConfigs(cfg.Connectors[index+1:])...)
	if err := saveConnectorCandidate(cfg, connectors); err != nil {
		return fmt.Errorf("保存连接器配置失败: %w", err)
	}
	return nil
}

func (m *Manager) SetConnectorEnabled(ctx context.Context, name string, enabled bool) error {
	cfg, release, err := m.beginConnectorCommit(ctx, nil)
	if err != nil {
		return err
	}
	defer release()

	cfg.Lock()
	defer cfg.Unlock()

	index := connectorIndexByName(cfg.Connectors, name)
	if index < 0 {
		return fmt.Errorf("%w: %s", monitor.ErrConnectorNotFound, name)
	}
	connectors := cloneConnectorConfigs(cfg.Connectors)
	connectors[index].Enabled = enabled
	if err := saveConnectorCandidate(cfg, connectors); err != nil {
		return fmt.Errorf("保存连接器配置失败: %w", err)
	}
	return nil
}

func (m *Manager) RefreshRuntimeSources(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.RLock()
	cfg := m.baseCfg
	boxMgr := m.boxMgr
	m.mu.RUnlock()
	hasSources := false
	if cfg != nil {
		cfg.RLock()
		hasSources = hasRuntimeRefreshSources(cfg)
		cfg.RUnlock()
	}

	if hasSources {
		return m.RefreshNow()
	}
	if boxMgr == nil {
		return nil
	}
	return boxMgr.TriggerReloadWithEphemeralNodes(ctx, nil)
}

func (m *Manager) RefreshPreferredEntryIPs(ctx context.Context, name string, options monitor.PreferredIPRefreshOptions) (*monitor.PreferredIPRefreshResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := m.configRef()
	if err != nil {
		return nil, err
	}

	cfg.RLock()
	snapshot := cfg.Clone()
	cfg.RUnlock()
	index := connectorIndexByName(snapshot.Connectors, name)
	if index < 0 {
		return nil, fmt.Errorf("%w: %s", monitor.ErrConnectorNotFound, name)
	}
	template := cloneConnectorConfig(snapshot.Connectors[index])
	runtimeCfg := snapshot.SourceSync.ConnectorRuntime
	configPath := snapshot.FilePath()
	initialConnectors := cloneConnectorConfigs(snapshot.Connectors)

	if strings.TrimSpace(template.ConnectorType) != connectorTypeECHWorker {
		return nil, fmt.Errorf("%w: 仅支持 ech_worker 模板", monitor.ErrInvalidConnector)
	}

	selector := m.preferredIPSelector
	if selector == nil {
		selector = runPreferredIPSelection
	}
	selected, artifactDir, resultCSV, err := selector(ctx, configPath, runtimeCfg, template, options)
	if err != nil {
		return nil, err
	}

	generated := buildPreferredConnectorSet(template, selected)
	currentCfg, release, err := m.beginConnectorCommit(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var commitErr error
	func() {
		defer release()
		currentCfg.Lock()
		defer currentCfg.Unlock()
		currentSnapshot := currentCfg.Clone()
		if !reflect.DeepEqual(initialConnectors, currentSnapshot.Connectors) ||
			!reflect.DeepEqual(runtimeCfg, currentSnapshot.SourceSync.ConnectorRuntime) ||
			configPath != currentSnapshot.FilePath() {
			commitErr = fmt.Errorf("%w: 连接器配置已在优选期间更新，请重试", monitor.ErrConnectorConflict)
			return
		}
		index = connectorIndexByName(currentSnapshot.Connectors, name)
		if index < 0 {
			commitErr = fmt.Errorf("%w: %s", monitor.ErrConnectorNotFound, name)
			return
		}
		filtered := make([]config.ConnectorSourceConfig, 0, len(currentSnapshot.Connectors)+len(generated))
		prefix := preferredConnectorNamePrefix(template.Name)
		for _, existing := range currentSnapshot.Connectors {
			if existing.Name == template.Name {
				filtered = append(filtered, existing)
				continue
			}
			if strings.HasPrefix(existing.Name, prefix) {
				continue
			}
			filtered = append(filtered, existing)
		}
		filtered = append(filtered, generated...)
		if err := saveConnectorCandidate(currentCfg, filtered); err != nil {
			commitErr = fmt.Errorf("保存连接器配置失败: %w", err)
		}
	}()
	if commitErr != nil {
		return nil, commitErr
	}

	result := &monitor.PreferredIPRefreshResult{
		TemplateName:        template.Name,
		ArtifactDir:         artifactDir,
		ResultCSV:           resultCSV,
		GeneratedConnectors: generated,
	}
	for _, item := range selected {
		result.SelectedIPs = append(result.SelectedIPs, monitor.PreferredIPSelection{
			IP:               item.IP,
			AverageLatencyMs: item.AverageLatencyMs,
			LossRate:         item.LossRate,
			SpeedMBS:         item.SpeedMBS,
			Colo:             item.Colo,
		})
	}

	if err := m.RefreshRuntimeSources(ctx); err != nil {
		return nil, err
	}
	result.RuntimeRefreshed = true
	return result, nil
}
