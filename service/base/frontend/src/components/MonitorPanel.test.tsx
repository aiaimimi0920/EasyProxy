import { render, screen } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'

import MonitorPanel from './MonitorPanel'

const apiMocks = vi.hoisted(() => ({
  fetchNodes: vi.fn(),
  fetchConfigNodes: vi.fn(),
  streamTraffic: vi.fn(),
}))

vi.mock('../api/client', () => apiMocks)

const runtimeNode = {
  tag: 'runtime-us-1',
  name: '订阅运行节点',
  uri: 'vless://runtime@example.com:443?security=tls#订阅运行节点',
  mode: 'hybrid',
  port: 25001,
  region: 'us',
  source_kind: 'subscription',
  failure_count: 0,
  success_count: 1,
  blacklisted: false,
  blacklisted_until: '',
  active_connections: 0,
  last_latency_ms: 120,
  available: true,
  initial_check_done: true,
  traffic_proven_usable: false,
  effective_available: true,
  total_upload: 0,
  total_download: 0,
}

beforeEach(() => {
  for (const mock of Object.values(apiMocks)) mock.mockReset()
  apiMocks.fetchNodes.mockResolvedValue({
    nodes: [runtimeNode],
    total_nodes: 1,
    all_total_nodes: 1,
    available_nodes: 1,
    probe_available_nodes: 1,
    traffic_proven_nodes: 0,
    total_upload: 0,
    total_download: 0,
    region_stats: { us: 1 },
    region_healthy: { us: 1 },
  })
  apiMocks.fetchConfigNodes.mockResolvedValue({ nodes: [] })
  apiMocks.streamTraffic.mockReturnValue(new AbortController())
})

it('shows runtime nodes when static configuration is empty', async () => {
  render(<MonitorPanel />)

  expect(await screen.findByText('节点监控')).toBeInTheDocument()
  expect(screen.getByText(/共 1 个运行时节点/)).toBeInTheDocument()
})

it('uses backend effective availability and separates pending from failed probes', async () => {
  apiMocks.fetchNodes.mockResolvedValue({
    nodes: [
      {
        ...runtimeNode,
        tag: 'traffic-proven',
        available: false,
        initial_check_done: false,
        traffic_proven_usable: true,
        effective_available: true,
        last_latency_ms: -1,
      },
      {
        ...runtimeNode,
        tag: 'pending',
        available: false,
        initial_check_done: false,
        traffic_proven_usable: false,
        effective_available: false,
        last_latency_ms: -1,
      },
      {
        ...runtimeNode,
        tag: 'failed',
        available: false,
        initial_check_done: true,
        traffic_proven_usable: false,
        effective_available: false,
        last_latency_ms: -1,
      },
    ],
    total_nodes: 3,
    all_total_nodes: 3,
    available_nodes: 1,
    probe_available_nodes: 0,
    traffic_proven_nodes: 1,
    total_upload: 0,
    total_download: 0,
    region_stats: { us: 3 },
    region_healthy: { us: 1 },
  })

  render(<MonitorPanel />)

  expect(await screen.findByText('可用 1')).toBeInTheDocument()
  expect(screen.getByText('不可用 1')).toBeInTheDocument()
  expect(screen.getByText('待检查 1')).toBeInTheDocument()
  expect(screen.getByText('1 / 2 已判定')).toBeInTheDocument()
})
