package routerule

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxLocalRuleFileBytes = 16 * 1024 * 1024

type localRulePayload struct {
	Payload []string `yaml:"payload"`
}

// LoadLocalRuleFiles materializes text and Clash-style YAML payload files in
// declared order. Invalid rules fail the whole load with a path-qualified error.
func LoadLocalRuleFiles(paths []string) ([]string, error) {
	var result []string
	for _, value := range paths {
		path := strings.TrimSpace(value)
		if path == "" {
			return nil, fmt.Errorf("load local routing rule file: empty path")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("load local routing rule file %q: %w", path, err)
		}
		if len(data) > maxLocalRuleFileBytes {
			return nil, fmt.Errorf("load local routing rule file %q: file exceeds %d bytes", path, maxLocalRuleFileBytes)
		}

		var rules []string
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml":
			var payload localRulePayload
			if err := yaml.Unmarshal(data, &payload); err != nil {
				return nil, fmt.Errorf("load local routing rule file %q: decode YAML: %w", path, err)
			}
			rules = payload.Payload
		default:
			rules = strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n")
		}

		materialized := make([]string, 0, len(rules))
		for _, rule := range rules {
			trimmed := strings.TrimSpace(rule)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			materialized = append(materialized, trimmed)
		}
		if err := ValidateRules(materialized); err != nil {
			return nil, fmt.Errorf("load local routing rule file %q: %w", path, err)
		}
		result = append(result, materialized...)
	}
	return result, nil
}
