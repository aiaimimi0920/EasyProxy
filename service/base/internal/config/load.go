package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := defaultConfigForDecode()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	cfg.filePath = path

	// Resolve relative paths to be relative to the config file directory
	configDir := filepath.Dir(path)
	if cfg.NodesFile != "" && !filepath.IsAbs(cfg.NodesFile) {
		cfg.NodesFile = filepath.Join(configDir, cfg.NodesFile)
	}
	if cfg.GeoIP.DatabasePath != "" && !filepath.IsAbs(cfg.GeoIP.DatabasePath) {
		cfg.GeoIP.DatabasePath = filepath.Join(configDir, cfg.GeoIP.DatabasePath)
	}
	resolvedRuleFiles, err := ResolveRuleFilePaths(path, cfg.Routing.RuleFiles)
	if err != nil {
		return nil, err
	}
	cfg.Routing.RuleFiles = resolvedRuleFiles

	if err := cfg.normalizeInternal(false); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadForReload reads YAML config from disk for a reload operation.
// Unlike Load, it does NOT re-fetch remote subscription URLs.
// Inline nodes and local file-backed nodes are loaded from disk; persisted
// manual nodes are loaded from the SQLite Store by the caller.
func LoadForReload(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := defaultConfigForDecode()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	cfg.filePath = path

	// Resolve relative paths to be relative to the config file directory
	configDir := filepath.Dir(path)
	if cfg.NodesFile != "" && !filepath.IsAbs(cfg.NodesFile) {
		cfg.NodesFile = filepath.Join(configDir, cfg.NodesFile)
	}
	if cfg.GeoIP.DatabasePath != "" && !filepath.IsAbs(cfg.GeoIP.DatabasePath) {
		cfg.GeoIP.DatabasePath = filepath.Join(configDir, cfg.GeoIP.DatabasePath)
	}
	resolvedRuleFiles, err := ResolveRuleFilePaths(path, cfg.Routing.RuleFiles)
	if err != nil {
		return nil, err
	}
	cfg.Routing.RuleFiles = resolvedRuleFiles

	if err := cfg.normalizeInternal(true); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) normalize() error {
	return c.normalizeInternal(false)
}

// applyDefaults sets default values for all config fields.
// This is the single source of truth for defaults, used by both
// normalizeInternal and NormalizeWithPortMap to avoid code duplication.
