package config

import (
	"fmt"
	"net/url"
	"strings"
)

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
