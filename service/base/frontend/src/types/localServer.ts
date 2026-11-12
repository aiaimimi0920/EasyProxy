export interface RuleProvider {
  url: string
  policy: 'DIRECT' | 'PROXY'
  behavior: 'domain' | 'ipcidr' | 'classical'
  interval: string
}

export interface SelectionTier {
  kind: 'country' | 'latency' | 'stability'
  country?: string
}

export interface ForwardingProfile {
  schema_version: 1
  enabled: boolean
  default_strategy: 'auto' | 'stable' | 'session'
  selection_tiers?: SelectionTier[]
  use_default_rules: boolean
  final_policy: 'DIRECT' | 'PROXY'
  rules: string[]
  rule_providers: RuleProvider[]
  node_filter: {
    countries: string[]
    regions: string[]
    long_lived: boolean | null
  }
  long_lived: {
    min_uptime: string
    min_success_rate: number
  }
  session: {
    ttl: string
  }
}

export interface ProviderStatus {
  degraded: boolean
  last_error?: string
  updated_at?: string
}

export interface ProfileResource {
  profile_scope: 'shared' | 'device'
  device_id?: string
  revision: number
  registry_revision: number
  need_reload: boolean
  profile: ForwardingProfile
  provider_status: ProviderStatus
}

export interface LocalServerConfig {
  enabled: boolean
  listen: string
  auth_username: string
  password_set: boolean
  shared_revision: number
  credential_generation: number
}

export interface DeviceSummary {
  device_id: string
  display_name: string
  revision: number
  profile_mode: 'shared' | 'independent'
  profile_revision?: number
  effective_enabled: boolean
  effective_state: 'DIRECT' | 'PROFILE'
  identity_source?: 'explicit' | 'ip_mapping' | 'shared_fallback'
  last_seen_ip?: string
  last_seen_at?: string
  mapping_count: number
}

export interface DeviceResource extends DeviceSummary {
  profile?: ProfileResource
  mappings: IPMapping[]
}

export interface IPMapping {
  mapping_id: string
  cidr: string
  device_id: string
  priority: number
  enabled: boolean
  revision: number
}

export interface MutationResponse<T> {
  revision: number
  registry_revision: number
  need_reload: boolean
  profile_scope?: 'shared' | 'device'
  resource?: T
  message?: string
}
