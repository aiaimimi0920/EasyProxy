import type { ForwardingProfile, RoutingConfig } from '../../types'

const emptyNodeFilter: ForwardingProfile['node_filter'] = {
  countries: [],
  regions: [],
  long_lived: null,
}

type LegacyRouting = RoutingConfig & {
  node_filter?: ForwardingProfile['node_filter']
}

export function routingConfigToProfile(config: LegacyRouting): ForwardingProfile {
  return {
    schema_version: 1,
    enabled: config.enabled,
    default_strategy: normalizeStrategy(config.default_strategy),
    use_default_rules: config.use_default_rules,
    final_policy: normalizePolicy(config.final_policy),
    rules: [...(config.rules ?? [])],
    rule_providers: (config.rule_providers ?? []).map((provider) => ({
      url: provider.url,
      policy: normalizePolicy(provider.policy),
      behavior: normalizeBehavior(provider.behavior),
      interval: provider.interval,
    })),
    node_filter: cloneNodeFilter(config.node_filter ?? emptyNodeFilter),
    long_lived: {
      min_uptime: config.long_lived_min_uptime,
      min_success_rate: config.long_lived_min_success_rate,
    },
    session: { ttl: config.session_ttl },
  }
}

export function profileToRoutingConfig<T extends LegacyRouting>(profile: ForwardingProfile, previous: T): T {
  return {
    ...previous,
    enabled: profile.enabled,
    default_strategy: profile.default_strategy,
    use_default_rules: profile.use_default_rules,
    final_policy: profile.final_policy,
    rules: [...profile.rules],
    rule_providers: profile.rule_providers.map((provider) => ({ ...provider })),
    long_lived_min_uptime: profile.long_lived.min_uptime,
    long_lived_min_success_rate: profile.long_lived.min_success_rate,
    session_ttl: profile.session.ttl,
  }
}

function normalizeStrategy(value: string): ForwardingProfile['default_strategy'] {
  return value === 'auto' || value === 'session' ? value : 'stable'
}

function normalizePolicy(value: string): 'DIRECT' | 'PROXY' {
  return value === 'DIRECT' ? 'DIRECT' : 'PROXY'
}

function normalizeBehavior(value: string): 'domain' | 'ipcidr' | 'classical' {
  return value === 'ipcidr' || value === 'classical' ? value : 'domain'
}

function cloneNodeFilter(filter: ForwardingProfile['node_filter']): ForwardingProfile['node_filter'] {
  return {
    countries: [...filter.countries],
    regions: [...filter.regions],
    long_lived: filter.long_lived,
  }
}
