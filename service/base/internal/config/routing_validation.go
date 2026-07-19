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
