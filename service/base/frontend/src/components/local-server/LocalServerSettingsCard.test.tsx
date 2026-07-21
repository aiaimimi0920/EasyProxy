import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

import LocalServerSettingsCard from './LocalServerSettingsCard'

const apiMocks = vi.hoisted(() => ({
  fetchLocalServerConfig: vi.fn(),
  updateLocalServerConfig: vi.fn(),
}))

vi.mock('../../api/localServer', () => apiMocks)

beforeEach(() => {
  apiMocks.fetchLocalServerConfig.mockReset()
  apiMocks.updateLocalServerConfig.mockReset()
  apiMocks.fetchLocalServerConfig.mockResolvedValue({
    enabled: true,
    listen: '0.0.0.0:22323',
    auth_username: 'easyproxy',
    password_set: true,
    shared_revision: 2,
    credential_generation: 3,
  })
  apiMocks.updateLocalServerConfig.mockResolvedValue({
    revision: 2,
    registry_revision: 4,
    need_reload: false,
  })
})

it('never refills the write-only password and preserves it on blank save', async () => {
  render(<LocalServerSettingsCard />)

  const password = await screen.findByLabelText('新密码')
  expect(password).toHaveValue('')
  expect(screen.getByText(/旧凭证字段由本地服务器凭证派生/)).toBeInTheDocument()

  await userEvent.click(screen.getByRole('button', { name: '保存本地服务器设置' }))

  expect(apiMocks.updateLocalServerConfig).toHaveBeenCalledWith({
    enabled: true,
    listen: '0.0.0.0:22323',
    auth_username: 'easyproxy',
  })
})

it('sends a non-empty password only when the operator enters one', async () => {
  render(<LocalServerSettingsCard />)

  await userEvent.type(await screen.findByLabelText('新密码'), 'rotated-secret')
  await userEvent.click(screen.getByRole('button', { name: '保存本地服务器设置' }))

  expect(apiMocks.updateLocalServerConfig).toHaveBeenCalledWith(expect.objectContaining({
    auth_password: 'rotated-secret',
  }))
})

it('notifies the parent about the saved Local Server mode', async () => {
  const onModeChange = vi.fn()
  render(<LocalServerSettingsCard onModeChange={onModeChange} />)

  await userEvent.click(await screen.findByRole('button', { name: '保存本地服务器设置' }))

  expect(onModeChange).toHaveBeenCalledWith(true)
})
