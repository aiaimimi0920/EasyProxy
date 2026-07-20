import type {
  DeviceSummary,
  ForwardingProfile,
  IPMapping,
  MutationResponse,
  ProfileResource,
} from '../types/localServer'

export function profileFixture(overrides: Partial<ForwardingProfile> = {}): ForwardingProfile {
  return {
    schema_version: 1,
    enabled: true,
    default_strategy: 'stable',
    use_default_rules: true,
    final_policy: 'PROXY',
    rules: [],
    rule_providers: [],
    node_filter: { countries: [], regions: [], long_lived: null },
    long_lived: { min_uptime: '2h', min_success_rate: 0.9 },
    session: { ttl: '10m' },
    ...overrides,
  }
}

export function profileResourceFixture(overrides: Partial<ProfileResource> = {}): ProfileResource {
  return {
    profile_scope: 'device',
    device_id: 'laptop',
    revision: 1,
    registry_revision: 2,
    need_reload: false,
    profile: profileFixture(),
    provider_status: { degraded: false },
    ...overrides,
  }
}

export function mutationFixture<T>(
  resource?: T,
  overrides: Partial<MutationResponse<T>> = {},
): MutationResponse<T> {
  return { revision: 1, registry_revision: 2, need_reload: false, resource, ...overrides }
}

export function deviceFixtures(): DeviceSummary[] {
  return [{
    device_id: 'laptop',
    display_name: 'Laptop',
    revision: 1,
    profile_mode: 'shared',
    effective_enabled: true,
    effective_state: 'PROFILE',
    mapping_count: 0,
  }]
}

export function mappingFixture(overrides: Partial<IPMapping> = {}): IPMapping {
  return {
    mapping_id: 'map-1',
    cidr: '192.168.1.10/32',
    device_id: 'laptop',
    priority: 100,
    enabled: true,
    revision: 1,
    ...overrides,
  }
}
