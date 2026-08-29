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
		path = "/usr/share/easyproxy/cfst/ip.txt"
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
