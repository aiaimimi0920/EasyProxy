import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

import DevicesPanel from './DevicesPanel'
import { deviceFixtures, mutationFixture, profileFixture, profileResourceFixture } from '../test/localServerFixtures'
import type { DeviceSummary } from '../types'

const apiMocks = vi.hoisted(() => ({
  fetchSharedProfile: vi.fn(),
  fetchDevices: vi.fn(),
  fetchIPMappings: vi.fn(),
  putDeviceProfile: vi.fn(),
  copySharedProfile: vi.fn(),
  deleteDeviceProfile: vi.fn(),
  setDeviceProfileEnabled: vi.fn(),
  createIPMapping: vi.fn(),
  updateIPMapping: vi.fn(),
  deleteIPMapping: vi.fn(),
}))

vi.mock('../api/localServer', () => apiMocks)

function mockDeviceAPIs(state: {
  mode?: 'shared' | 'independent'
  independentEnabled?: boolean
  sharedEnabled?: boolean
  devices?: DeviceSummary[]
} = {}) {
  const sharedEnabled = state.sharedEnabled ?? true
  const mode = state.mode ?? 'shared'
  const independentEnabled = state.independentEnabled ?? true
  const devices = state.devices ?? [{
    ...deviceFixtures()[0],
    profile_mode: mode,
    profile_revision: mode === 'independent' ? 1 : undefined,
    effective_enabled: mode === 'independent' ? independentEnabled : sharedEnabled,
    effective_state: (mode === 'independent' ? independentEnabled : sharedEnabled) ? 'PROFILE' : 'DIRECT',
  }]
  apiMocks.fetchSharedProfile.mockResolvedValue(profileResourceFixture({
    profile_scope: 'shared',
    device_id: undefined,
    profile: profileFixture({ enabled: sharedEnabled }),
  }))
  apiMocks.fetchDevices.mockResolvedValue({ devices })
  apiMocks.fetchIPMappings.mockResolvedValue({ mappings: [] })
  apiMocks.putDeviceProfile.mockResolvedValue(mutationFixture(profileResourceFixture()))
  return apiMocks
}

beforeEach(() => {
  for (const mock of Object.values(apiMocks)) mock.mockReset()
})

it.each([
  ['shared', false, false, 'DIRECT'],
  ['shared', true, true, '共享配置'],
  ['independent', true, false, '独立配置'],
  ['independent', false, true, 'DIRECT'],
] as const)('renders %s mode with effective state', async (mode, independentEnabled, sharedEnabled, label) => {
  mockDeviceAPIs({ mode, independentEnabled, sharedEnabled })
  render(<DevicesPanel />)
  expect(await screen.findByRole('status', { name: `有效状态 ${label}` })).toBeInTheDocument()
})

it('creates blank Profile with revision zero and explains copy-shared is one-time', async () => {
  const calls = mockDeviceAPIs({ devices: deviceFixtures() })
  render(<DevicesPanel />)

  await userEvent.click(await screen.findByRole('button', { name: '创建独立配置' }))
  await userEvent.click(screen.getByRole('button', { name: '使用默认配置' }))

  expect(calls.putDeviceProfile).toHaveBeenCalledWith('laptop', expect.anything(), 0)
  expect(screen.getByText(/复制当前值，后续不联动/)).toBeInTheDocument()
})

it('warns that IP mapping is best-effort behind Docker or NAT', async () => {
  mockDeviceAPIs({ devices: deviceFixtures() })
  render(<DevicesPanel />)

  expect(await screen.findByText(/IP 映射仅作为回退/)).toBeInTheDocument()
})
