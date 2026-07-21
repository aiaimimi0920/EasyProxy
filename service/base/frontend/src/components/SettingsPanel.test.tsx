import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

import SettingsPanel from './SettingsPanel'

const apiMocks = vi.hoisted(() => ({
  fetchSettings: vi.fn(),
  updateSettings: vi.fn(),
  triggerReload: vi.fn(),
  fetchSubscriptionStatus: vi.fn(),
  fetchSourceSyncStatus: vi.fn(),
  refreshSubscription: vi.fn(),
}))

vi.mock('../api/client', () => apiMocks)
vi.mock('./local-server/LocalServerSettingsCard', () => ({
  default: ({ onModeChange }: { onModeChange?: (enabled: boolean) => void }) => (
    <button type="button" onClick={() => onModeChange?.(true)}>启用本地服务器测试</button>
  ),
}))

beforeEach(() => {
  for (const mock of Object.values(apiMocks)) mock.mockReset()
  apiMocks.fetchSettings.mockResolvedValue({
    local_server_enabled: true,
    subscriptions: [],
    source_sync_fallback_subscriptions: [],
  })
  apiMocks.fetchSubscriptionStatus.mockResolvedValue({ enabled: false })
  apiMocks.fetchSourceSyncStatus.mockResolvedValue({ enabled: false })
})

it('hides derived legacy credentials without hiding pool scheduling', async () => {
  render(<SettingsPanel />)

  expect(await screen.findByText('系统设置')).toBeInTheDocument()
  expect(screen.queryByText('代理用户名')).not.toBeInTheDocument()
  expect(screen.queryByText('代理密码')).not.toBeInTheDocument()
  expect(screen.queryByText('默认用户名')).not.toBeInTheDocument()
  expect(screen.queryByText('默认密码')).not.toBeInTheDocument()
  expect(screen.queryByText('WebUI 密码')).not.toBeInTheDocument()
  expect(screen.getByText('调度模式')).toBeInTheDocument()
})

it('updates legacy credential visibility after the card enables Local Server', async () => {
  apiMocks.fetchSettings.mockResolvedValue({
    local_server_enabled: false,
    subscriptions: [],
    source_sync_fallback_subscriptions: [],
  })

  render(<SettingsPanel />)

  expect(await screen.findByText('代理用户名')).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: '启用本地服务器测试' }))

  expect(screen.queryByText('代理用户名')).not.toBeInTheDocument()
  expect(screen.queryByText('WebUI 密码')).not.toBeInTheDocument()
})
