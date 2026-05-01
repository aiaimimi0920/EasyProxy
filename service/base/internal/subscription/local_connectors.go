package subscription

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

func (m *Manager) CreateConnector(_ context.Context, connector config.ConnectorSourceConfig) (config.ConnectorSourceConfig, error) {
	cfg, err := m.configRef()
	if err != nil {
		return config.ConnectorSourceConfig{}, err
	}

	normalized, err := normalizeManagedConnector(connector)
	if err != nil {
		return config.ConnectorSourceConfig{}, err
	}

	cfg.Lock()
	defer cfg.Unlock()

	if connectorIndexByName(cfg.Connectors, normalized.Name) >= 0 {
		return config.ConnectorSourceConfig{}, fmt.Errorf("%w: %s", monitor.ErrConnectorConflict, normalized.Name)
	}
	cfg.Connectors = append(cfg.Connectors, normalized)
	if err := cfg.SaveSettings(); err != nil {
		return config.ConnectorSourceConfig{}, fmt.Errorf("保存连接器配置失败: %w", err)
	}
	return cloneConnectorConfig(normalized), nil
}

func (m *Manager) UpdateConnector(_ context.Context, name string, connector config.ConnectorSourceConfig) (config.ConnectorSourceConfig, error) {
	cfg, err := m.configRef()
	if err != nil {
		return config.ConnectorSourceConfig{}, err
	}

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
	cfg.Connectors[index] = normalized
	if err := cfg.SaveSettings(); err != nil {
		return config.ConnectorSourceConfig{}, fmt.Errorf("保存连接器配置失败: %w", err)
	}
	return cloneConnectorConfig(normalized), nil
}

func (m *Manager) DeleteConnector(_ context.Context, name string) error {
	cfg, err := m.configRef()
	if err != nil {
		return err
	}

	cfg.Lock()
	defer cfg.Unlock()

	index := connectorIndexByName(cfg.Connectors, name)
	if index < 0 {
		return fmt.Errorf("%w: %s", monitor.ErrConnectorNotFound, name)
	}
	cfg.Connectors = append(cfg.Connectors[:index], cfg.Connectors[index+1:]...)
	if err := cfg.SaveSettings(); err != nil {
		return fmt.Errorf("保存连接器配置失败: %w", err)
	}
	return nil
}

func (m *Manager) SetConnectorEnabled(_ context.Context, name string, enabled bool) error {
	cfg, err := m.configRef()
	if err != nil {
		return err
	}

	cfg.Lock()
	defer cfg.Unlock()

	index := connectorIndexByName(cfg.Connectors, name)
	if index < 0 {
		return fmt.Errorf("%w: %s", monitor.ErrConnectorNotFound, name)
	}
	cfg.Connectors[index].Enabled = enabled
	if err := cfg.SaveSettings(); err != nil {
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
	hasSources := hasRuntimeRefreshSources(cfg)
	m.mu.RUnlock()

	if hasSources {
		return m.RefreshNow()
	}
	if boxMgr == nil {
		return nil
	}
	boxMgr.SetEphemeralNodes(nil)
	return boxMgr.TriggerReload(ctx)
}

func (m *Manager) RefreshPreferredEntryIPs(ctx context.Context, name string, options monitor.PreferredIPRefreshOptions) (*monitor.PreferredIPRefreshResult, error) {
	cfg, err := m.configRef()
	if err != nil {
		return nil, err
	}

	cfg.RLock()
	index := connectorIndexByName(cfg.Connectors, name)
	if index < 0 {
		cfg.RUnlock()
		return nil, fmt.Errorf("%w: %s", monitor.ErrConnectorNotFound, name)
	}
	template := cloneConnectorConfig(cfg.Connectors[index])
	runtimeCfg := cfg.SourceSync.ConnectorRuntime
	configPath := cfg.FilePath()
	cfg.RUnlock()

	if strings.TrimSpace(template.ConnectorType) != connectorTypeECHWorker {
		return nil, fmt.Errorf("%w: 仅支持 ech_worker 模板", monitor.ErrInvalidConnector)
	}

	selected, artifactDir, resultCSV, err := runPreferredIPSelection(ctx, configPath, runtimeCfg, template, options)
	if err != nil {
		return nil, err
	}

	generated := buildPreferredConnectorSet(template, selected)

	cfg.Lock()
	filtered := make([]config.ConnectorSourceConfig, 0, len(cfg.Connectors)+len(generated))
	prefix := preferredConnectorNamePrefix(template.Name)
	for _, existing := range cfg.Connectors {
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
	cfg.Connectors = filtered
	if err := cfg.SaveSettings(); err != nil {
		cfg.Unlock()
		return nil, fmt.Errorf("保存连接器配置失败: %w", err)
	}
	cfg.Unlock()

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

type preferredIPResultRow struct {
	IP               string
	AverageLatencyMs float64
	LossRate         float64
	SpeedMBS         float64
	Colo             string
}

func runPreferredIPSelection(ctx context.Context, configPath string, runtimeCfg config.ConnectorRuntimeConfig, template config.ConnectorSourceConfig, options monitor.PreferredIPRefreshOptions) ([]preferredIPResultRow, string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	preferredCfg := runtimeCfg.PreferredIP
	binaryPath, err := resolvePreferredIPBinary(preferredCfg.BinaryPath)
	if err != nil {
		return nil, "", "", err
	}
	ipFilePath, err := resolvePreferredIPFilePath(configPath, preferredCfg.IPFilePath)
	if err != nil {
		return nil, "", "", err
	}
	workingDir, err := resolvePreferredIPWorkingDir(configPath, preferredCfg.WorkingDirectory)
	if err != nil {
		return nil, "", "", err
	}

	runDir := filepath.Join(workingDir, time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, "", "", fmt.Errorf("create preferred-ip artifact dir: %w", err)
	}

	resultCSV := filepath.Join(runDir, "result.csv")
	topCount := options.TopCount
	if topCount <= 0 {
		topCount = 5
	}
	latencyThreads := options.LatencyThreads
	if latencyThreads <= 0 {
		latencyThreads = 200
	}
	latencySamples := options.LatencySamples
	if latencySamples <= 0 {
		latencySamples = 4
	}
	maxLoss := options.MaxLossRate
	if maxLoss < 0 {
		maxLoss = 0
	}

	port, err := connectorInputPort(template.Input)
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: %v", monitor.ErrInvalidConnector, err)
	}

	commandTimeout := preferredCfg.Timeout
	if commandTimeout <= 0 {
		commandTimeout = 5 * time.Minute
	}
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	args := []string{
		"-tp", strconv.Itoa(port),
		"-dd",
		"-f", ipFilePath,
		"-n", strconv.Itoa(latencyThreads),
		"-t", strconv.Itoa(latencySamples),
		"-tlr", strconv.FormatFloat(maxLoss, 'f', 2, 64),
		"-p", strconv.Itoa(topCount),
		"-o", resultCSV,
	}
	if options.AllIP {
		args = append(args, "-allip")
	}

	commandSpec := map[string]any{
		"binary": binaryPath,
		"args":   args,
	}
	if data, marshalErr := json.MarshalIndent(commandSpec, "", "  "); marshalErr == nil {
		_ = os.WriteFile(filepath.Join(runDir, "speedtest-command.json"), data, 0o644)
	}

	cmd := exec.CommandContext(commandCtx, binaryPath, args...)
	cmd.Dir = runDir
	output, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(runDir, "speedtest-output.log"), output, 0o644)
	if err != nil {
		return nil, "", "", fmt.Errorf("run CloudflareSpeedTest: %w", err)
	}

	selected, err := parsePreferredIPCSV(resultCSV, topCount)
	if err != nil {
		return nil, "", "", err
	}
	if len(selected) == 0 {
		return nil, "", "", fmt.Errorf("%w: CloudflareSpeedTest 未返回可用 IP", monitor.ErrInvalidConnector)
	}

	return selected, runDir, resultCSV, nil
}

func buildPreferredConnectorSet(template config.ConnectorSourceConfig, selected []preferredIPResultRow) []config.ConnectorSourceConfig {
	generated := make([]config.ConnectorSourceConfig, 0, len(selected))
	prefix := preferredConnectorNamePrefix(template.Name)
	for idx, item := range selected {
		connector := cloneConnectorConfig(template)
		connector.Name = fmt.Sprintf("%s%d", prefix, idx+1)
		connector.Enabled = true
		connector.TemplateOnly = false
		if connector.Group == "" {
			connector.Group = "ECH Connectors"
		}
		connector.Notes = fmt.Sprintf("Preferred Cloudflare entry IP #%d generated from %s", idx+1, template.Name)
		if connector.ConnectorConfig == nil {
			connector.ConnectorConfig = map[string]any{}
		}
		connector.ConnectorConfig["server_ip"] = item.IP
		generated = append(generated, connector)
	}
	return generated
}

func preferredConnectorNamePrefix(templateName string) string {
	return strings.TrimSpace(templateName) + " Preferred "
}

func parsePreferredIPCSV(path string, topCount int) ([]preferredIPResultRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open preferred-ip csv: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read preferred-ip csv: %w", err)
	}
	if len(rows) <= 1 {
		return nil, nil
	}

	headerIndex := make(map[string]int, len(rows[0]))
	for idx, title := range rows[0] {
		headerIndex[strings.TrimSpace(strings.TrimPrefix(title, "\uFEFF"))] = idx
	}

	getValue := func(row []string, key string) string {
		index, ok := headerIndex[key]
		if !ok || index >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[index])
	}

	selected := make([]preferredIPResultRow, 0, topCount)
	for _, row := range rows[1:] {
		ip := getValue(row, "IP 地址")
		if ip == "" {
			continue
		}
		latency, _ := strconv.ParseFloat(getValue(row, "平均延迟"), 64)
		lossRate, _ := strconv.ParseFloat(getValue(row, "丢包率"), 64)
		speed, _ := strconv.ParseFloat(getValue(row, "下载速度(MB/s)"), 64)
		selected = append(selected, preferredIPResultRow{
			IP:               ip,
			AverageLatencyMs: latency,
			LossRate:         lossRate,
			SpeedMBS:         speed,
			Colo:             getValue(row, "地区码"),
		})
		if len(selected) >= topCount {
			break
		}
	}
	return selected, nil
}

func resolvePreferredIPBinary(configuredPath string) (string, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		configuredPath = "cfst"
	}
	path, err := exec.LookPath(configuredPath)
	if err == nil {
		return path, nil
	}
	if filepath.IsAbs(configuredPath) {
		if _, statErr := os.Stat(configuredPath); statErr == nil {
			return configuredPath, nil
		}
	}
	return "", fmt.Errorf("%w: CloudflareSpeedTest binary %q not found", monitor.ErrInvalidConnector, configuredPath)
}

func resolvePreferredIPFilePath(configPath string, configuredPath string) (string, error) {
	path := strings.TrimSpace(configuredPath)
	if path == "" {
		path = "/usr/local/share/cfst/ip.txt"
	}
	if !filepath.IsAbs(path) && strings.TrimSpace(configPath) != "" {
		path = filepath.Join(filepath.Dir(configPath), path)
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%w: CloudflareSpeedTest ip.txt not found at %s", monitor.ErrInvalidConnector, path)
	}
	return path, nil
}

func resolvePreferredIPWorkingDir(configPath string, configuredPath string) (string, error) {
	path := strings.TrimSpace(configuredPath)
	if path == "" {
		baseDir := "."
		if strings.TrimSpace(configPath) != "" {
			baseDir = filepath.Dir(configPath)
		}
		path = filepath.Join(baseDir, "data", "connectors", "preferred-ip")
	}
	if !filepath.IsAbs(path) && strings.TrimSpace(configPath) != "" {
		path = filepath.Join(filepath.Dir(configPath), path)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", fmt.Errorf("create preferred-ip working dir: %w", err)
	}
	return path, nil
}

func connectorInputPort(input string) (int, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return 0, fmt.Errorf("connector input is empty")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid connector input: %w", err)
	}
	if parsed.Port() != "" {
		port, convErr := strconv.Atoi(parsed.Port())
		if convErr != nil {
			return 0, convErr
		}
		return port, nil
	}
	if strings.EqualFold(parsed.Scheme, "http") {
		return 80, nil
	}
	return 443, nil
}

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
