import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

import { ApiError } from '../../api/client'
import { profileFixture, profileResourceFixture } from '../../test/localServerFixtures'
import ProfileEditor from './ProfileEditor'

const apiMocks = vi.hoisted(() => ({
  fetchSharedProfile: vi.fn(),
  putSharedProfile: vi.fn(),
  fetchDevice: vi.fn(),
  putDeviceProfile: vi.fn(),
}))

vi.mock('../../api/localServer', () => apiMocks)

beforeEach(() => {
  for (const mock of Object.values(apiMocks)) mock.mockReset()
  apiMocks.fetchSharedProfile.mockResolvedValue(profileResourceFixture({
    profile_scope: 'shared',
    device_id: undefined,
    revision: 4,
    profile: profileFixture(),
  }))
  apiMocks.putSharedProfile.mockResolvedValue({
    revision: 5,
    registry_revision: 6,
    need_reload: false,
    resource: profileResourceFixture({ profile_scope: 'shared', device_id: undefined, revision: 5 }),
  })
})

it('saves the explicit scope with the server revision', async () => {
  const onClose = vi.fn()
  render(<ProfileEditor scope={{ kind: 'shared' }} onClose={onClose} />)

  await userEvent.click(await screen.findByRole('checkbox', { name: '启用此配置' }))
  await userEvent.click(screen.getByRole('button', { name: '保存配置' }))

  expect(apiMocks.putSharedProfile).toHaveBeenCalledWith(expect.objectContaining({ enabled: false }), 4)
  expect(onClose).not.toHaveBeenCalled()
})

it('keeps local input when the server reports a revision conflict', async () => {
  apiMocks.putSharedProfile.mockRejectedValue(new ApiError('revision_conflict', 409, { current_revision: 9 }))
  render(<ProfileEditor scope={{ kind: 'shared' }} onClose={vi.fn()} />)

  const enabled = await screen.findByRole('checkbox', { name: '启用此配置' })
  await userEvent.click(enabled)
  await userEvent.click(screen.getByRole('button', { name: '保存配置' }))

  expect(await screen.findByRole('alert')).toHaveTextContent('配置已被其他操作修改')
  expect(enabled).not.toBeChecked()
})

it('does not discard dirty input when reload confirmation is rejected', async () => {
  vi.spyOn(window, 'confirm').mockReturnValue(false)
  render(<ProfileEditor scope={{ kind: 'shared' }} onClose={vi.fn()} />)

  const enabled = await screen.findByRole('checkbox', { name: '启用此配置' })
  await userEvent.click(enabled)
  await userEvent.click(screen.getByRole('button', { name: '重新加载' }))

  expect(apiMocks.fetchSharedProfile).toHaveBeenCalledOnce()
  expect(enabled).not.toBeChecked()
})


it('keeps tier edits and dirty state after CAS conflict or rejected navigation', async () => {
  vi.spyOn(window, 'confirm').mockReturnValue(false)
  const onClose = vi.fn()
  apiMocks.putSharedProfile.mockRejectedValue(new ApiError('revision_conflict', 409, { current_revision: 9 }))
  render(<ProfileEditor scope={{ kind: 'shared' }} onClose={onClose} />)
  await userEvent.selectOptions(await screen.findByLabelText('第一级策略'), 'latency')
  await userEvent.selectOptions(screen.getByLabelText('第二级策略'), 'stability')
  await userEvent.click(screen.getByRole('button', { name: '保存配置' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('配置已被其他操作修改')
  expect(apiMocks.putSharedProfile).toHaveBeenCalledWith(expect.objectContaining({ selection_tiers: [{ kind: 'latency' }, { kind: 'stability' }] }), 4)
  await userEvent.click(screen.getByRole('button', { name: '重新加载' }))
  await userEvent.click(screen.getByRole('button', { name: '关闭' }))
  expect(apiMocks.fetchSharedProfile).toHaveBeenCalledOnce()
  expect(onClose).not.toHaveBeenCalled()
  expect(screen.getByLabelText('第一级策略')).toHaveValue('latency')
  expect(screen.getByLabelText('第二级策略')).toHaveValue('stability')
  expect(screen.getByRole('button', { name: '保存配置' })).toBeEnabled()
})

it('saves independent tiers with the device revision and preserves other settings', async () => {
  const profile = profileFixture({ default_strategy: 'session', selection_tiers: [{ kind: 'country', country: 'JP' }] })
  apiMocks.fetchDevice.mockResolvedValue({ profile: profileResourceFixture({ revision: 7, profile }) })
  apiMocks.putDeviceProfile.mockResolvedValue({ revision: 8, registry_revision: 9, need_reload: false })
  render(<ProfileEditor scope={{ kind: 'device', deviceId: 'laptop' }} onClose={vi.fn()} />)
  await userEvent.selectOptions(await screen.findByLabelText('第二级策略'), 'stability')
  await userEvent.click(screen.getByRole('button', { name: '保存配置' }))
  expect(apiMocks.putDeviceProfile).toHaveBeenCalledWith('laptop', { ...profile, selection_tiers: [{ kind: 'country', country: 'JP' }, { kind: 'stability' }] }, 7)
  expect(await screen.findByText('配置已保存')).toBeVisible()
  expect(screen.getByRole('button', { name: '保存配置' })).toBeDisabled()
})
