package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ResolveRuleFilePaths anchors local routing rule files to the primary config
// directory and rejects missing or non-regular inputs before runtime startup.
func ResolveRuleFilePaths(configPath string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	baseDir := filepath.Dir(configPath)
	resolved := make([]string, 0, len(paths))
	for idx, value := range paths {
		path := strings.TrimSpace(value)
		if path == "" {
			return nil, fmt.Errorf("routing rule file %d has an empty path", idx+1)
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		path = filepath.Clean(path)
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("routing rule file %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("routing rule file %q is not a regular file", path)
		}
		resolved = append(resolved, path)
	}
	return resolved, nil
}

// ValidateRuleProviders rejects provider URLs that the routing runtime cannot fetch.
func ValidateRuleProviders(providers []RuleProvider) error {
	for idx, provider := range providers {
		rawURL := strings.TrimSpace(provider.URL)
		parsed, err := url.Parse(rawURL)
		scheme := ""
		host := ""
		if parsed != nil {
			scheme = strings.ToLower(parsed.Scheme)
			host = parsed.Host
		}
		if err != nil || host == "" || (scheme != "http" && scheme != "https") {
			if err != nil {
				return fmt.Errorf("routing rule provider %d has invalid URL %q: %w", idx+1, provider.URL, err)
			}
			return fmt.Errorf("routing rule provider %d has invalid URL %q", idx+1, provider.URL)
		}
	}
	return nil
}

func validIdentityToken(value string) bool {
	token := strings.TrimSpace(value)
	if token == "" || len(token) > 64 {
		return false
	}
	for idx := 0; idx < len(token); idx++ {
		ch := token[idx]
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '.', ch == '_', ch == '-':
		default:
			return false
		}
	}
	return true
}
