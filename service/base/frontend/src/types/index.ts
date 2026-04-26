// ---- Node & Snapshot types (maps to monitor.Snapshot) ----

export interface NodeInfo {
  tag: string
  name: string
  uri: string
  mode: string
  listen_address?: string
  port?: number
  region?: string
  country?: string
}

export interface TimelineEvent {
  time: string
  success: boolean
  latency_ms: number
  error?: string
  destination?: string
}

export interface NodeSnapshot extends NodeInfo {
  failure_count: number
  success_count: number
  blacklisted: boolean
  blacklisted_until: string
  active_connections: number
  last_error?: string
  last_failure?: string
  last_success?: string
  last_probe_latency?: number
  last_latency_ms: number
  available: boolean
  initial_check_done: boolean
  total_upload: number
  total_download: number
  timeline?: TimelineEvent[]
}

// ---- API Response types ----

export interface NodesResponse {
  nodes: NodeSnapshot[]
  total_nodes: number
  total_upload: number
  total_download: number
  upload_speed?: number
  download_speed?: number
  traffic_sampled?: string
  region_stats: Record<string, number>
  region_healthy: Record<string, number>
}

export interface DebugNode {
  tag: string
  name: string
  mode: string
  port: number
  failure_count: number
  success_count: number
  active_connections: number
  last_latency_ms: number
  last_success: string
  last_failure: string
  last_error: string
  blacklisted: boolean
  total_upload: number
  total_download: number
  timeline: TimelineEvent[]
}

export interface DebugResponse {
  nodes: DebugNode[]
  total_calls: number
  total_success: number
  success_rate: number
}

export interface SettingsData {
  // Global
  mode: string
  log_level: string
  external_ip: string
  skip_cert_verify: boolean

  // Listener
  listener_address: string
  listener_port: number
  listener_protocol: string
  listener_username: string
  listener_password: string

  // Multi-port
  multi_port_address: string
  multi_port_base_port: number
  multi_port_protocol: string
  multi_port_username: string
  multi_port_password: string

  // Pool
  pool_mode: string
  pool_failure_threshold: number
  pool_blacklist_duration: string

  // Management
  management_enabled: boolean
  management_listen: string
  management_probe_target: string
  management_password: string
  management_health_check_interval: string

  // Subscription refresh
  sub_refresh_enabled: boolean
  sub_refresh_interval: string
  sub_refresh_timeout: string
  sub_refresh_health_check_timeout: string
  sub_refresh_drain_timeout: string
  sub_refresh_min_available_nodes: number

  // Source sync
  source_sync_enabled: boolean
  source_sync_manifest_url: string
  source_sync_manifest_token: string
  source_sync_refresh_interval: string
  source_sync_request_timeout: string
  source_sync_fallback_subscriptions: string[]
  source_sync_default_direct_proxy_scheme: string

  // GeoIP
  geoip_enabled: boolean
  geoip_database_path: string
  geoip_auto_update_enabled: boolean
  geoip_auto_update_interval: string

  // Subscriptions
  subscriptions: string[]
}

export interface SettingsUpdateResponse {
  message: string
  need_reload: boolean
}

// ---- Auth types ----

export interface AuthResponse {
  message: string
  token?: string
  no_password?: boolean
}

export interface ErrorResponse {
  error: string
}

// ---- Config Node CRUD types ----

export interface ConfigNodePayload {
  name: string
  uri: string
  port: number
  username: string
  password: string
}

export interface ConfigNodeConfig {
  name: string
  uri: string
  port: number
  username: string
  password: string
  source?: string
  disabled?: boolean
}

export interface ConfigNodesResponse {
  nodes: ConfigNodeConfig[]
}

export interface ConfigNodeMutationResponse {
  node?: ConfigNodeConfig
  message: string
}

// ---- Subscription types ----

export interface SubscriptionStatus {
  enabled: boolean
  has_subscriptions?: boolean
  last_refresh?: string
  next_refresh?: string
  node_count?: number
  last_error?: string
  refresh_count?: number
  is_refreshing?: boolean
  message?: string
}

export interface SourceSyncStatus {
  enabled: boolean
  manifest_url?: string
  manifest_healthy?: boolean
  last_sync?: string
  last_success?: string
  last_error?: string
  fallback_active?: boolean
  local_source_count?: number
  manifest_source_count?: number
  fallback_source_count?: number
}

// ---- SSE Probe types ----

export interface ProbeSSEStart {
  type: 'start'
  total: number
}

export interface ProbeSSEProgress {
  type: 'progress'
  tag: string
  name: string
  latency: number
  status: 'success' | 'error'
  error: string
  current: number
  total: number
  progress: number
}

export interface ProbeSSEComplete {
  type: 'complete'
  total: number
  success: number
  failed: number
}

export type ProbeSSEEvent = ProbeSSEStart | ProbeSSEProgress | ProbeSSEComplete

// ---- SSE Traffic stream types ----

export interface TrafficStreamNode {
  tag: string
  upload_speed: number
  download_speed: number
  total_upload: number
  total_download: number
}

export interface TrafficStreamEvent {
  type: 'traffic'
  node_count: number
  total_upload: number
  total_download: number
  upload_speed: number
  download_speed: number
  sampled_at: string
  nodes: TrafficStreamNode[]
}
