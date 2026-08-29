package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

func (c *Config) FilePath() string {
	if c == nil {
		return ""
	}
	return c.filePath
}

// SubscriptionCacheDir returns the directory used to cache subscription fetch results.
func (c *Config) SubscriptionCacheDir() string {
	if c == nil {
		return filepath.Join("data", "subscription-cache")
	}
	if c.DatabasePath != "" {
		dbPath := c.DatabasePath
		if c.filePath != "" && !filepath.IsAbs(dbPath) {
			dbPath = filepath.Join(filepath.Dir(c.filePath), dbPath)
		}
		return filepath.Join(filepath.Dir(dbPath), "subscription-cache")
	}
	if c.filePath != "" {
		return filepath.Join(filepath.Dir(c.filePath), "data", "subscription-cache")
	}
	return filepath.Join("data", "subscription-cache")
}

// SetFilePath sets the config file path (used when creating config programmatically).
func (c *Config) SetFilePath(path string) {
	if c != nil {
		c.filePath = path
	}
}

// NOTE: ManualNodesFilePath, LoadManualNodes, SaveManualNodes, writeNodesToFile,
// SaveNodes, Save have been removed. All node persistence is now handled by
// the SQLite Store (internal/store package).

// SaveSettings persists all editable settings to config.yaml.
// Node data is managed by the SQLite Store, not config.yaml.
// The caller must hold the config lock (c.mu) before calling this method.
func (c *Config) SaveSettings() error {
	if c == nil {
		return errors.New("config is nil")
	}
	if c.filePath == "" {
		return errors.New("config file path is unknown")
	}

	// Build a clean config struct for serialization.
	// We read the existing YAML to preserve fields not managed by the settings API
	// (e.g., nodes, nodes_file, database_path).
	data, err := os.ReadFile(c.filePath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var saveCfg Config
	if err := yaml.Unmarshal(data, &saveCfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}

	// Sync all editable fields from runtime config
	saveCfg.Mode = c.Mode
	saveCfg.LogLevel = c.LogLevel
	saveCfg.ExternalIP = c.ExternalIP
	saveCfg.SkipCertVerify = c.SkipCertVerify
	saveCfg.DNS = c.DNS
	saveCfg.DNS.Enabled = cloneConfigBool(c.DNS.Enabled)
	saveCfg.DNS.RemoteServers = cloneConfigSlice(c.DNS.RemoteServers)

	// Listener
	saveCfg.Listener = c.Listener

	// Multi-port
	saveCfg.MultiPort = c.MultiPort

	// Pool
	saveCfg.Pool = c.Pool

	// Management
	saveCfg.Management = c.Management

	// Subscription refresh
	saveCfg.SubscriptionRefresh = c.SubscriptionRefresh

	// Source sync
	saveCfg.SourceSync = c.SourceSync

	// GeoIP
	saveCfg.GeoIP = c.GeoIP

	// Local Server
	saveCfg.LocalServer = c.LocalServer

	// Routing (smart dispatch entry)
	saveCfg.Routing = c.Routing

	// Connectors
	saveCfg.Connectors = c.Connectors

	// Subscriptions
	saveCfg.Subscriptions = c.Subscriptions

	newData, err := yaml.Marshal(&saveCfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	// Use atomic write to prevent data loss from concurrent/interrupted writes
	if err := writeFileAtomic(c.filePath, newData, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// ValidateSettingsRequest validates the settings request fields and returns
// a descriptive error for any invalid values, instead of silently ignoring them.
func ValidateSettingsRequest(mode string, listenerPort, multiPortBasePort uint16,
	listenerProtocol, multiPortProtocol, poolMode,
	poolBlacklistDuration, subRefreshInterval, subRefreshTimeout,
	subRefreshHealthCheckTimeout, subRefreshDrainTimeout,
	sourceSyncRefreshInterval, sourceSyncRequestTimeout,
	geoIPAutoUpdateInterval, managementHealthCheckInterval string) error {

	// Validate mode
	switch mode {
	case "pool", "multi-port", "hybrid":
	default:
		return fmt.Errorf("不支持的运行模式: %q", mode)
	}

	switch strings.ToLower(strings.TrimSpace(poolMode)) {
	case "auto", "sequential", "random", "balance":
	default:
		return fmt.Errorf("不支持的代理池调度模式: %q", poolMode)
	}

	// Validate ports
	if listenerPort == 0 {
		return fmt.Errorf("监听端口不能为 0")
	}
	if multiPortBasePort == 0 {
		return fmt.Errorf("多端口起始端口不能为 0")
	}

	// Validate inbound protocols
	if _, err := NormalizeInboundProtocol(listenerProtocol); err != nil {
		return fmt.Errorf("监听入口协议无效: %w", err)
	}
	if _, err := NormalizeInboundProtocol(multiPortProtocol); err != nil {
		return fmt.Errorf("多端口入口协议无效: %w", err)
	}

	// Validate duration fields
	durationFields := []struct {
		name  string
		value string
	}{
		{"黑名单持续时间", poolBlacklistDuration},
		{"订阅刷新间隔", subRefreshInterval},
		{"订阅获取超时", subRefreshTimeout},
		{"健康检查超时", subRefreshHealthCheckTimeout},
		{"排空超时", subRefreshDrainTimeout},
		{"Source Sync 刷新间隔", sourceSyncRefreshInterval},
		{"Source Sync 请求超时", sourceSyncRequestTimeout},
		{"GeoIP 更新间隔", geoIPAutoUpdateInterval},
		{"周期健康检查间隔", managementHealthCheckInterval},
	}

	for _, field := range durationFields {
		if field.value != "" {
			d, err := time.ParseDuration(field.value)
			if err != nil {
				return fmt.Errorf("%s 格式无效: %q (%v)", field.name, field.value, err)
			}
			if d <= 0 {
				return fmt.Errorf("%s 必须大于 0: %q", field.name, field.value)
			}
		}
	}

	return nil
}

// isPortAvailable checks if a port is available for binding.
func isPortAvailable(address string, port uint16) bool {
	addr := fmt.Sprintf("%s:%d", address, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// writeFileAtomic writes data to a file atomically by writing to a temporary
// file first and then renaming it. This prevents data loss if the process is
// interrupted or if concurrent reads occur during the write.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	// Create temp file in same directory (required for atomic rename on same filesystem)
	tmpFile, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Clean up temp file on error
	success := false
	defer func() {
		if !success {
			tmpFile.Close()
			os.Remove(tmpPath)
		}
	}()

	// Write data to temp file
	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	// Sync to disk before rename
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}

	// Close before rename (required on Windows)
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Set permissions
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		// 当 config.yaml 通过 bind-mount 映射到宿主机时，部分宿主机文件系统/挂载模式会导致
		// 对“正在被挂载使用的目标文件”执行 rename 返回 EBUSY（device or resource busy）。
		// 这种情况下回退为“直接覆盖写入”（非原子，但可用），以保证 WebUI 能保存配置。
		if errors.Is(err, syscall.EBUSY) {
			data, rerr := os.ReadFile(tmpPath)
			if rerr != nil {
				return fmt.Errorf("rename temp file: %w", err)
			}
			if werr := os.WriteFile(path, data, perm); werr != nil {
				return fmt.Errorf("rename temp file: %w", err)
			}
			if cerr := os.Remove(tmpPath); cerr != nil {
				// best-effort cleanup
			}
			return nil
		}
		return fmt.Errorf("rename temp file: %w", err)
	}

	success = true
	return nil
}
