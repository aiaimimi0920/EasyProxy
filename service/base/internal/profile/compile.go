package profile

import (
	"fmt"
	"strings"
	"time"

	"easy_proxies/internal/routerule"
)

type CompiledProfile struct {
	id            string
	kind          Kind
	revision      int64
	definition    Definition
	selection     SelectionSettings
	engine        *routerule.Engine
	baseRules     []string
	finalPolicy   routerule.Policy
	providerSpecs []routerule.ProviderSpec
}

func Compile(id string, kind Kind, revision int64, definition Definition, lookup routerule.CountryLookup) (*CompiledProfile, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("profile id is required")
	}
	switch kind {
	case KindShared, KindDevice:
	default:
		return nil, fmt.Errorf("unsupported profile kind %q", kind)
	}
	if revision <= 0 {
		return nil, fmt.Errorf("profile revision must be positive")
	}

	normalized, selection, err := normalizeDefinition(definition)
	if err != nil {
		return nil, err
	}

	rules := make([]string, 0, len(normalized.Rules)+64)
	rules = append(rules, normalized.Rules...)
	if normalized.UseDefaultRules {
		rules = append(rules, routerule.DefaultRules()...)
	}
	if err := routerule.ValidateRules(rules); err != nil {
		return nil, err
	}

	finalPolicy, err := parsePolicy(normalized.FinalPolicy, routerule.PolicyProxy, true)
	if err != nil {
		return nil, err
	}

	providerSpecs := make([]routerule.ProviderSpec, 0, len(normalized.RuleProviders))
	for _, provider := range normalized.RuleProviders {
		interval, err := time.ParseDuration(provider.Interval)
		if err != nil {
			return nil, fmt.Errorf("parse rule provider interval %q: %w", provider.Interval, err)
		}
		providerSpecs = append(providerSpecs, routerule.ProviderSpec{
			URL:      provider.URL,
			Policy:   routerule.Policy(provider.Policy),
			Behavior: provider.Behavior,
			Interval: interval,
		})
	}

	engine := routerule.New(rules, finalPolicy, lookup)
	return &CompiledProfile{
		id:            id,
		kind:          kind,
		revision:      revision,
		definition:    cloneDefinition(normalized),
		selection:     cloneSelection(selection),
		engine:        engine,
		baseRules:     cloneStringSlice(rules),
		finalPolicy:   finalPolicy,
		providerSpecs: cloneProviderSpecs(providerSpecs),
	}, nil
}

func (p *CompiledProfile) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

func (p *CompiledProfile) Kind() Kind {
	if p == nil {
		return ""
	}
	return p.kind
}

func (p *CompiledProfile) Revision() int64 {
	if p == nil {
		return 0
	}
	return p.revision
}

func (p *CompiledProfile) Enabled() bool {
	return p != nil && p.definition.Enabled
}

func (p *CompiledProfile) Match(host string) routerule.Policy {
	if p == nil || p.engine == nil {
		return routerule.PolicyProxy
	}
	return p.engine.Match(host)
}

func (p *CompiledProfile) Selection() SelectionSettings {
	if p == nil {
		return SelectionSettings{}
	}
	return cloneSelection(p.selection)
}

func (p *CompiledProfile) Definition() Definition {
	if p == nil {
		return Definition{}
	}
	return cloneDefinition(p.definition)
}

func (p *CompiledProfile) RuleCount() int {
	if p == nil || p.engine == nil {
		return 0
	}
	return p.engine.RuleCount()
}

func (p *CompiledProfile) FinalPolicy() routerule.Policy {
	if p == nil || p.engine == nil {
		return routerule.PolicyProxy
	}
	return p.engine.Final()
}

func (p *CompiledProfile) ProviderSpecs() []routerule.ProviderSpec {
	if p == nil {
		return nil
	}
	return cloneProviderSpecs(p.providerSpecs)
}

func (p *CompiledProfile) WithRevision(revision int64) *CompiledProfile {
	if p == nil {
		return nil
	}
	cloned := *p
	cloned.revision = revision
	return &cloned
}

func cloneProviderSpecs(values []routerule.ProviderSpec) []routerule.ProviderSpec {
	if values == nil {
		return nil
	}
	cloned := make([]routerule.ProviderSpec, len(values))
	copy(cloned, values)
	return cloned
}

func normalizeDefinition(def Definition) (Definition, SelectionSettings, error) {
	if def.SchemaVersion != 0 && def.SchemaVersion != 1 {
		return Definition{}, SelectionSettings{}, fmt.Errorf("unsupported schema version %d", def.SchemaVersion)
	}
	normalized := cloneDefinition(def)
	normalized.SchemaVersion = 1
	if strings.TrimSpace(normalized.DefaultStrategy) == "" {
		normalized.DefaultStrategy = "stable"
	}
	strategy, err := normalizeStrategy(normalized.DefaultStrategy)
	if err != nil {
		return Definition{}, SelectionSettings{}, err
	}
	normalized.DefaultStrategy = strategy
	finalPolicy, err := parsePolicy(normalized.FinalPolicy, routerule.PolicyProxy, true)
	if err != nil {
		return Definition{}, SelectionSettings{}, err
	}
	normalized.FinalPolicy = string(finalPolicy)

	for idx := range normalized.RuleProviders {
		provider := &normalized.RuleProviders[idx]
		if err := validateProviderURL(provider.URL); err != nil {
			return Definition{}, SelectionSettings{}, fmt.Errorf("rule provider %d: %w", idx+1, err)
		}
		if provider.Policy == "" {
			return Definition{}, SelectionSettings{}, fmt.Errorf("rule provider %d: policy is required", idx+1)
		}
		providerPolicy, err := parsePolicy(provider.Policy, routerule.PolicyProxy, false)
		if err != nil {
			return Definition{}, SelectionSettings{}, fmt.Errorf("rule provider %d: %w", idx+1, err)
		}
		provider.Policy = string(providerPolicy)
		behavior := strings.ToLower(strings.TrimSpace(provider.Behavior))
		switch behavior {
		case "", "domain":
			provider.Behavior = "domain"
		case "classical":
			provider.Behavior = "classical"
		default:
			return Definition{}, SelectionSettings{}, fmt.Errorf("rule provider %d: unsupported behavior %q", idx+1, provider.Behavior)
		}
		interval, err := parseDurationWithDefault(provider.Interval, 24*time.Hour)
		if err != nil {
			return Definition{}, SelectionSettings{}, fmt.Errorf("rule provider %d: %w", idx+1, err)
		}
		provider.Interval = interval.String()
	}
	if err := routerule.ValidateRules(normalized.Rules); err != nil {
		return Definition{}, SelectionSettings{}, err
	}

	if normalized.LongLived.MinUptime == "" {
		normalized.LongLived.MinUptime = (2 * time.Hour).String()
	}
	longLivedMinUptime, err := parseDurationWithDefault(normalized.LongLived.MinUptime, 2*time.Hour)
	if err != nil {
		return Definition{}, SelectionSettings{}, fmt.Errorf("long_lived.min_uptime: %w", err)
	}
	longLivedMinSuccessRate := normalized.LongLived.MinSuccessRate
	if longLivedMinSuccessRate <= 0 {
		longLivedMinSuccessRate = 0.9
	}
	if longLivedMinSuccessRate > 1 {
		return Definition{}, SelectionSettings{}, fmt.Errorf("long_lived.min_success_rate must be between 0 and 1")
	}

	sessionTTL, err := parseDurationWithDefault(normalized.Session.TTL, 10*time.Minute)
	if err != nil {
		return Definition{}, SelectionSettings{}, fmt.Errorf("session.ttl: %w", err)
	}

	selection := SelectionSettings{
		DefaultStrategy:         strategy,
		Filter:                  normalizeNodeFilter(normalized.NodeFilter),
		LongLivedMinUptime:      longLivedMinUptime,
		LongLivedMinSuccessRate: longLivedMinSuccessRate,
		SessionTTL:              sessionTTL,
	}
	normalized.NodeFilter = cloneNodeFilter(selection.Filter)
	normalized.LongLived.MinUptime = longLivedMinUptime.String()
	normalized.LongLived.MinSuccessRate = longLivedMinSuccessRate
	normalized.Session.TTL = sessionTTL.String()
	return normalized, selection, nil
}

func normalizeStrategy(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(StrategyAuto):
		return string(StrategyAuto), nil
	case string(StrategyStable):
		return string(StrategyStable), nil
	case string(StrategySession):
		return string(StrategySession), nil
	default:
		return "", fmt.Errorf("unsupported default strategy %q", value)
	}
}

func parsePolicy(value string, defaultPolicy routerule.Policy, allowBlankDefault bool) (routerule.Policy, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		if allowBlankDefault {
			return defaultPolicy, nil
		}
		return "", fmt.Errorf("policy is required")
	}
	switch strings.ToUpper(trimmed) {
	case string(routerule.PolicyDirect):
		return routerule.PolicyDirect, nil
	case string(routerule.PolicyProxy):
		return routerule.PolicyProxy, nil
	default:
		return "", fmt.Errorf("unsupported policy %q", value)
	}
}

func parseDurationWithDefault(value string, defaultValue time.Duration) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultValue, nil
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, err
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return parsed, nil
}
