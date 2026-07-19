package profile

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"easy_proxies/internal/config"
)

type Kind string

const (
	KindShared Kind = "shared"
	KindDevice Kind = "device"
)

type Strategy string

const (
	StrategyAuto    Strategy = "auto"
	StrategyStable  Strategy = "stable"
	StrategySession Strategy = "session"
)

type RuleProvider struct {
	URL      string `json:"url"`
	Policy   string `json:"policy"`
	Behavior string `json:"behavior"`
	Interval string `json:"interval"`
}

type NodeFilter struct {
	Countries []string `json:"countries"`
	Regions   []string `json:"regions"`
	LongLived *bool    `json:"long_lived"`
}

type LongLivedPolicy struct {
	MinUptime      string  `json:"min_uptime"`
	MinSuccessRate float64 `json:"min_success_rate"`
}

type SessionPolicy struct {
	TTL string `json:"ttl"`
}

type Definition struct {
	SchemaVersion   int             `json:"schema_version"`
	Enabled         bool            `json:"enabled"`
	DefaultStrategy string          `json:"default_strategy"`
	UseDefaultRules bool            `json:"use_default_rules"`
	FinalPolicy     string          `json:"final_policy"`
	Rules           []string        `json:"rules"`
	RuleProviders   []RuleProvider  `json:"rule_providers"`
	NodeFilter      NodeFilter      `json:"node_filter"`
	LongLived       LongLivedPolicy `json:"long_lived"`
	Session         SessionPolicy   `json:"session"`
}

type SelectionSettings struct {
	DefaultStrategy         string
	Filter                  NodeFilter
	LongLivedMinUptime      time.Duration
	LongLivedMinSuccessRate float64
	SessionTTL              time.Duration
}

func DefinitionFromRouting(routing config.RoutingConfig) Definition {
	def := Definition{
		SchemaVersion: 1,
		Enabled:       routing.Enabled,
		FinalPolicy:   strings.TrimSpace(routing.FinalPolicy),
		Rules:         cloneStringSlice(routing.Rules),
		NodeFilter:    cloneRoutingNodeFilter(routing.NodeFilter),
	}
	if strings.TrimSpace(routing.DefaultStrategy) != "" {
		def.DefaultStrategy = strings.TrimSpace(routing.DefaultStrategy)
	} else {
		def.DefaultStrategy = "stable"
	}
	if routing.UseDefaultRules == nil {
		def.UseDefaultRules = true
	} else {
		def.UseDefaultRules = *routing.UseDefaultRules
	}

	def.RuleProviders = make([]RuleProvider, 0, len(routing.RuleProviders))
	for _, provider := range routing.RuleProviders {
		interval := provider.Interval
		if interval <= 0 {
			interval = 24 * time.Hour
		}
		behavior := strings.TrimSpace(provider.Behavior)
		if behavior == "" {
			behavior = "domain"
		}
		def.RuleProviders = append(def.RuleProviders, RuleProvider{
			URL:      strings.TrimSpace(provider.URL),
			Policy:   strings.TrimSpace(provider.Policy),
			Behavior: behavior,
			Interval: interval.String(),
		})
	}

	uptime := routing.LongLived.MinUptime
	if uptime <= 0 {
		uptime = 2 * time.Hour
	}
	successRate := routing.LongLived.MinSuccessRate
	if successRate <= 0 {
		successRate = 0.9
	}
	def.LongLived = LongLivedPolicy{
		MinUptime:      uptime.String(),
		MinSuccessRate: successRate,
	}
	ttl := routing.Session.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	def.Session = SessionPolicy{TTL: ttl.String()}
	return def
}

func ApplyDefinitionToRouting(def Definition, routing *config.RoutingConfig) error {
	if routing == nil {
		return fmt.Errorf("routing config is nil")
	}
	normalized, selection, err := normalizeDefinition(def)
	if err != nil {
		return err
	}

	routing.Enabled = normalized.Enabled
	routing.DefaultStrategy = selection.DefaultStrategy
	useDefaults := normalized.UseDefaultRules
	routing.UseDefaultRules = &useDefaults
	routing.FinalPolicy = normalized.FinalPolicy
	routing.Rules = cloneStringSlice(normalized.Rules)
	routing.RuleProviders = make([]config.RuleProvider, 0, len(normalized.RuleProviders))
	for _, provider := range normalized.RuleProviders {
		interval, err := time.ParseDuration(provider.Interval)
		if err != nil {
			return fmt.Errorf("parse rule provider interval %q: %w", provider.Interval, err)
		}
		routing.RuleProviders = append(routing.RuleProviders, config.RuleProvider{
			URL:      provider.URL,
			Policy:   provider.Policy,
			Behavior: provider.Behavior,
			Interval: interval,
		})
	}

	routing.NodeFilter = config.RoutingNodeFilterConfig{
		Countries: cloneStringSlice(normalized.NodeFilter.Countries),
		Regions:   cloneStringSlice(normalized.NodeFilter.Regions),
		LongLived: cloneBoolPointer(normalized.NodeFilter.LongLived),
	}
	routing.LongLived.MinUptime = selection.LongLivedMinUptime
	routing.LongLived.MinSuccessRate = selection.LongLivedMinSuccessRate
	routing.Session.TTL = selection.SessionTTL
	return nil
}

func cloneDefinition(def Definition) Definition {
	cloned := def
	cloned.Rules = cloneStringSlice(def.Rules)
	cloned.RuleProviders = cloneRuleProviders(def.RuleProviders)
	cloned.NodeFilter = cloneNodeFilter(def.NodeFilter)
	return cloned
}

func cloneSelection(selection SelectionSettings) SelectionSettings {
	cloned := selection
	cloned.Filter = cloneNodeFilter(selection.Filter)
	return cloned
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func cloneRuleProviders(values []RuleProvider) []RuleProvider {
	if values == nil {
		return nil
	}
	cloned := make([]RuleProvider, len(values))
	copy(cloned, values)
	return cloned
}

func cloneNodeFilter(filter NodeFilter) NodeFilter {
	cloned := NodeFilter{
		Countries: cloneStringSlice(filter.Countries),
		Regions:   cloneStringSlice(filter.Regions),
		LongLived: cloneBoolPointer(filter.LongLived),
	}
	return cloned
}

func cloneRoutingNodeFilter(filter config.RoutingNodeFilterConfig) NodeFilter {
	return NodeFilter{
		Countries: cloneStringSlice(filter.Countries),
		Regions:   cloneStringSlice(filter.Regions),
		LongLived: cloneBoolPointer(filter.LongLived),
	}
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func normalizeNodeFilter(filter NodeFilter) NodeFilter {
	out := NodeFilter{
		Countries: normalizeTokens(filter.Countries, strings.ToUpper),
		Regions:   normalizeTokens(filter.Regions, strings.ToLower),
		LongLived: cloneBoolPointer(filter.LongLived),
	}
	return out
}

func normalizeTokens(values []string, transform func(string) string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := transform(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sortStrings(out)
	return out
}

func sortStrings(values []string) {
	if len(values) < 2 {
		return
	}
	for i := 0; i < len(values)-1; i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}

func validateProviderURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if (scheme != "http" && scheme != "https") || strings.TrimSpace(parsed.Host) == "" {
		return fmt.Errorf("invalid URL %q", raw)
	}
	return nil
}
