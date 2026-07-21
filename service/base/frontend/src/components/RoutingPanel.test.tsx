import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

import RoutingPanel from './RoutingPanel'

const apiMocks = vi.hoisted(() => ({
  fetchRoutingConfig: vi.fn(),
  updateRoutingConfig: vi.fn(),
  fetchRoutingStatus: vi.fn(),
  fetchGatewayStatus: vi.fn(),
  triggerReload: vi.fn(),
}))

vi.mock('../api/client', () => apiMocks)

beforeEach(() => {
  for (const mock of Object.values(apiMocks)) mock.mockReset()
  apiMocks.fetchRoutingConfig.mockResolvedValue({
    enabled: true,
    listen: '127.0.0.1:22324',
    default_strategy: 'stable',
    use_default_rules: true,
    final_policy: 'PROXY',
    rules: [],
    rule_providers: [],
    long_lived_min_uptime: '2h',
    long_lived_min_success_rate: 0.9,
    session_ttl: '10m',
    future_topology: { mode: 'mixed' },
  })
  apiMocks.fetchRoutingStatus.mockResolvedValue({
    enabled: true,
    rule_count: 0,
    sticky_buckets: { cn: 'node-a' },
    sticky_sessions: { session: 'node-b' },
  })
  apiMocks.fetchGatewayStatus.mockResolvedValue({
    enabled: true,
    applied: true,
    listen: '0.0.0.0:15001',
    direct_connections: 4,
    proxy_connections: 7,
    direct_fallbacks: 2,
    active_connections: 1,
  })
  apiMocks.updateRoutingConfig.mockResolvedValue({ message: 'saved', need_reload: false })
})

it('uses ProfileForm while preserving legacy listener settings on save', async () => {
  render(<RoutingPanel />)

  await userEvent.click(await screen.findByRole('checkbox', { name: '启用此配置' }))
  await userEvent.click(screen.getByRole('button', { name: '保存配置' }))

  expect(apiMocks.updateRoutingConfig).toHaveBeenCalledWith(expect.objectContaining({
    enabled: false,
    listen: '127.0.0.1:22324',
    future_topology: { mode: 'mixed' },
  }))
  expect(screen.getByText('当前粘性绑定')).toBeInTheDocument()
  expect(screen.getByText('node-a')).toBeInTheDocument()
  expect(screen.getByText('node-b')).toBeInTheDocument()
  expect(screen.getByText('透明网关')).toBeInTheDocument()
  expect(screen.getByText('0.0.0.0:15001')).toBeInTheDocument()
  expect(screen.getByText('2 次 DIRECT 回退')).toBeInTheDocument()
})
