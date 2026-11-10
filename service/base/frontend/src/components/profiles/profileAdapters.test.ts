import { expect, it } from 'vitest'

import { profileFixture } from '../../test/localServerFixtures'
import type { ForwardingProfile, RoutingConfig } from '../../types'
import { profileToRoutingConfig, routingConfigToProfile } from './profileAdapters'

type LegacyRoutingFixture = RoutingConfig & {
  node_filter: ForwardingProfile['node_filter']
}

function legacyRoutingFixture(): LegacyRoutingFixture {
  return {
    enabled: true,
    listen: '127.0.0.1:22324',
    default_strategy: 'stable',
    use_default_rules: true,
    final_policy: 'PROXY',
    rules: [],
    rule_providers: [],
    node_filter: { countries: [], regions: [], long_lived: null },
    long_lived_min_uptime: '2h',
    long_lived_min_success_rate: 0.9,
    session_ttl: '10m',
  }
}

it('round-trips the legacy flattened routing payload', () => {
  const legacy = legacyRoutingFixture()
  const nested = routingConfigToProfile(legacy)

  expect(profileToRoutingConfig(nested, legacy)).toEqual(legacy)
})

it('preserves topology-only fields when replacing Profile-owned fields', () => {
  const previous = { ...legacyRoutingFixture(), future_topology: { mode: 'mixed' } }
  const profile = profileFixture({ enabled: false, final_policy: 'DIRECT' })

  expect(profileToRoutingConfig(profile, previous)).toEqual({
    ...previous,
    enabled: false,
    final_policy: 'DIRECT',
  })
})


it('retains ordered tiers and clones them across adapter boundaries', () => {
  const legacy = { ...legacyRoutingFixture(), selection_tiers: [{ kind: 'country' as const, country: 'JP' }, { kind: 'stability' as const }] }
  const profile = routingConfigToProfile(legacy)
  expect(profile.selection_tiers).toEqual(legacy.selection_tiers)
  expect(profile.selection_tiers?.[0]).not.toBe(legacy.selection_tiers[0])
  const result = profileToRoutingConfig(profile, legacy)
  expect(result.selection_tiers).toEqual(legacy.selection_tiers)
  expect(result.selection_tiers?.[0]).not.toBe(profile.selection_tiers?.[0])
  expect(profileToRoutingConfig({ ...profile, selection_tiers: [] }, legacy).selection_tiers).toEqual([])
})

it('does not introduce defaults for legacy profiles or keep removed tiers', () => {
  expect(routingConfigToProfile(legacyRoutingFixture())).not.toHaveProperty('selection_tiers')
  const previous = { ...legacyRoutingFixture(), selection_tiers: [{ kind: 'latency' as const }] }
  expect(profileToRoutingConfig(profileFixture(), previous).selection_tiers).toBeUndefined()
})
