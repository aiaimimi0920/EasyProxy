import type {
  AuthResponse,
  NodesResponse,
  DebugResponse,
  SettingsData,
  SettingsUpdateResponse,
  ConfigNodesResponse,
  ConfigNodePayload,
  ConfigNodeMutationResponse,
  SubscriptionStatus,
  SourceSyncStatus,
  ProbeSSEEvent,
  TrafficStreamEvent,
} from '../types'

// ---- Token management ----

let authToken: string | null = localStorage.getItem('auth_token')

export function getToken(): string | null {
  return authToken
}

export function setToken(token: string | null) {
  authToken = token
  if (token) {
    localStorage.setItem('auth_token', token)
  } else {
    localStorage.removeItem('auth_token')
  }
}

export function clearToken() {
  setToken(null)
}

// ---- Base request helper ----

export async function apiRequest<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string> || {}),
  }

  // Add auth header if we have a token
  if (authToken) {
    headers['Authorization'] = `Bearer ${authToken}`
  }

  // Set JSON content type for non-GET requests with body
  if (options.body && typeof options.body === 'string') {
    headers['Content-Type'] = 'application/json'
  }

  const res = await fetch(path, {
    ...options,
    headers,
    credentials: 'include', // send cookies
  })

  const text = await res.text()
  let payload: Record<string, unknown> = {}
  if (text) {
    try {
      const parsed: unknown = JSON.parse(text)
      if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
        payload = parsed as Record<string, unknown>
      }
    } catch {
      // Preserve the HTTP status even when the body is not JSON.
    }
  }

  if (!res.ok) {
    if (res.status === 401) {
      clearToken()
      window.dispatchEvent(new CustomEvent('auth:unauthorized'))
    }
    const message = typeof payload.error === 'string'
      ? payload.error
      : res.status === 401
        ? '未授权，请重新登录'
        : `HTTP ${res.status}`
    throw new ApiError(message, res.status, payload)
  }

  if (!text) return {} as T
  return JSON.parse(text) as T
}

export class ApiError extends Error {
  readonly status: number
  readonly payload: Record<string, unknown>

  constructor(
    message: string,
    status: number,
    payload: Record<string, unknown> = {},
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.payload = payload
  }
}

const request = apiRequest

// ---- Auth API ----

/** Check if password is required & login */
export async function checkAuth(): Promise<AuthResponse> {
  const res = await fetch('/api/auth', { credentials: 'include' })
  const payload = await res.json() as AuthResponse & { auth_mode?: string; error?: string }
  if (!res.ok) {
    throw new ApiError(payload.error ?? `HTTP ${res.status}`, res.status, payload as Record<string, unknown>)
  }
  return {
    ...payload,
    auth_mode: payload.auth_mode === 'canonical_pair' ? 'canonical_pair' : 'legacy_password',
  }
}

export async function login(username: string, password: string): Promise<AuthResponse> {
  const res = await fetch('/api/auth', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
    credentials: 'include',
  })
  const payload = await res.json() as AuthResponse & { error?: string }
  if (!res.ok) {
    throw new ApiError(
      payload.error ?? '登录失败',
      res.status,
      payload as Record<string, unknown>,
    )
  }
  if (payload.token) setToken(payload.token)
  return payload
}

export function logout() {
  clearToken()
}

// ---- Nodes API ----

export async function fetchNodes(): Promise<NodesResponse> {
  return request<NodesResponse>('/api/nodes')
}

export async function probeNode(tag: string): Promise<{ message: string; latency_ms: number }> {
  return request(`/api/nodes/${encodeURIComponent(tag)}/probe`, { method: 'POST' })
}

export async function releaseNode(tag: string): Promise<{ message: string }> {
  return request(`/api/nodes/${encodeURIComponent(tag)}/release`, { method: 'POST' })
}

/** Probe all nodes with SSE progress updates */
export function probeAllNodes(
  onEvent: (event: ProbeSSEEvent) => void,
  onError?: (error: Error) => void
): AbortController {
  const controller = new AbortController()

  const doFetch = async () => {
    try {
      const headers: Record<string, string> = {}
      if (authToken) {
        headers['Authorization'] = `Bearer ${authToken}`
      }

      const res = await fetch('/api/nodes/probe-all', {
        method: 'POST',
        headers,
        credentials: 'include',
        signal: controller.signal,
      })

      if (!res.ok) {
        throw new ApiError(`探测失败: HTTP ${res.status}`, res.status)
      }

      const reader = res.body?.getReader()
      if (!reader) throw new Error('No response body')

      const decoder = new TextDecoder()
      let buffer = ''

      while (true) {
        const { done, value } = await reader.read()
        if (done) break

        buffer += decoder.decode(value, { stream: true })
        const lines = buffer.split('\n')
        buffer = lines.pop() || ''

        for (const line of lines) {
          const trimmed = line.trim()
          if (trimmed.startsWith('data: ')) {
            try {
              const data = JSON.parse(trimmed.slice(6)) as ProbeSSEEvent
              onEvent(data)
            } catch { /* skip malformed events */ }
          }
        }
      }
    } catch (err) {
      if ((err as Error).name !== 'AbortError') {
        onError?.(err as Error)
      }
    }
  }

  doFetch()
  return controller
}

// ---- Traffic Stream API ----

/** Subscribe real-time traffic speeds via SSE */
export function streamTraffic(
  onEvent: (event: TrafficStreamEvent) => void,
  onError?: (error: Error) => void
): AbortController {
  const controller = new AbortController()

  const doFetch = async () => {
    try {
      const headers: Record<string, string> = {}
      if (authToken) {
        headers['Authorization'] = `Bearer ${authToken}`
      }

      const res = await fetch('/api/nodes/traffic/stream', {
        method: 'GET',
        headers,
        credentials: 'include',
        signal: controller.signal,
      })

      if (!res.ok) {
        throw new ApiError(`流量流订阅失败: HTTP ${res.status}`, res.status)
      }

      const reader = res.body?.getReader()
      if (!reader) throw new Error('No response body')

      const decoder = new TextDecoder()
      let buffer = ''

      while (true) {
        const { done, value } = await reader.read()
        if (done) break

        buffer += decoder.decode(value, { stream: true })
        const lines = buffer.split('\n')
        buffer = lines.pop() || ''

        for (const line of lines) {
          const trimmed = line.trim()
          if (trimmed.startsWith('data: ')) {
            try {
              const data = JSON.parse(trimmed.slice(6)) as TrafficStreamEvent
              if (data.type === 'traffic') {
                onEvent(data)
              }
            } catch { /* skip malformed events */ }
          }
        }
      }
    } catch (err) {
      if ((err as Error).name !== 'AbortError') {
        onError?.(err as Error)
      }
    }
  }

  doFetch()
  return controller
}

// ---- Debug API ----

export async function fetchDebug(): Promise<DebugResponse> {
  return request<DebugResponse>('/api/debug')
}

// ---- Settings API ----

export async function fetchSettings(): Promise<SettingsData> {
  return request<SettingsData>('/api/settings')
}

export async function updateSettings(settings: SettingsData): Promise<SettingsUpdateResponse> {
  return request<SettingsUpdateResponse>('/api/settings', {
    method: 'PUT',
    body: JSON.stringify(settings),
  })
}

// ---- Config Nodes CRUD API ----

export async function fetchConfigNodes(): Promise<ConfigNodesResponse> {
  return request<ConfigNodesResponse>('/api/nodes/config')
}

export async function createConfigNode(payload: ConfigNodePayload): Promise<ConfigNodeMutationResponse> {
  return request<ConfigNodeMutationResponse>('/api/nodes/config', {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export async function updateConfigNode(name: string, payload: ConfigNodePayload): Promise<ConfigNodeMutationResponse> {
  return request<ConfigNodeMutationResponse>(`/api/nodes/config/${encodeURIComponent(name)}`, {
    method: 'PUT',
    body: JSON.stringify(payload),
  })
}

export async function deleteConfigNode(name: string): Promise<ConfigNodeMutationResponse> {
  return request<ConfigNodeMutationResponse>(`/api/nodes/config/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  })
}

export async function toggleConfigNode(name: string, enabled: boolean): Promise<ConfigNodeMutationResponse> {
  return request<ConfigNodeMutationResponse>(`/api/nodes/config/${encodeURIComponent(name)}`, {
    method: 'PATCH',
    body: JSON.stringify({ enabled }),
  })
}

export async function batchToggleConfigNodes(names: string[], enabled: boolean): Promise<{ message: string; success: number; total: number; errors?: string[] }> {
  return request('/api/nodes/config/batch-toggle', {
    method: 'POST',
    body: JSON.stringify({ names, enabled }),
  })
}

export async function batchDeleteConfigNodes(names: string[]): Promise<{ message: string; success: number; total: number; errors?: string[] }> {
  return request('/api/nodes/config/batch-delete', {
    method: 'POST',
    body: JSON.stringify({ names }),
  })
}

// ---- Reload API ----

export async function triggerReload(): Promise<{ message: string }> {
  return request('/api/reload', { method: 'POST' })
}

// ---- Subscription API ----

export async function fetchSubscriptionStatus(): Promise<SubscriptionStatus> {
  return request<SubscriptionStatus>('/api/subscription/status')
}

export async function refreshSubscription(): Promise<{ message: string; node_count: number }> {
  return request('/api/subscription/refresh', { method: 'POST' })
}

export async function fetchSourceSyncStatus(): Promise<SourceSyncStatus> {
  return request<SourceSyncStatus>('/api/source-sync/status')
}

// ---- Export API ----

export async function exportProxies(): Promise<string> {
  const headers: Record<string, string> = {}
  if (authToken) {
    headers['Authorization'] = `Bearer ${authToken}`
  }
  const res = await fetch('/api/export', {
    headers,
    credentials: 'include',
  })
  if (!res.ok) throw new ApiError('导出失败', res.status)
  return res.text()
}

// ---- Import API ----

export async function importNodes(content: string): Promise<{ message: string; imported: number; errors?: string[] }> {
  return request('/api/import', {
    method: 'POST',
    body: JSON.stringify({ content }),
  })
}

// ---- Smart Routing API ----

export async function fetchRoutingStatus(): Promise<import('../types').RoutingStatus> {
  return request<import('../types').RoutingStatus>('/api/routing/status')
}

export async function fetchRoutingConfig(): Promise<import('../types').RoutingConfig> {
  return request<import('../types').RoutingConfig>('/api/routing/config')
}

export async function updateRoutingConfig(
  cfg: import('../types').RoutingConfig
): Promise<import('../types').RoutingConfigUpdateResponse> {
  return request<import('../types').RoutingConfigUpdateResponse>('/api/routing/config', {
    method: 'PUT',
    body: JSON.stringify(cfg),
  })
}
