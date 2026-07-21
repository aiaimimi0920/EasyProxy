import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'

import { profileFixture } from '../../test/localServerFixtures'
import ProfileForm from './ProfileForm'

it('edits Profile-owned fields without server settings', async () => {
  const onChange = vi.fn()
  render(<ProfileForm value={profileFixture()} onChange={onChange} />)

  await userEvent.click(screen.getByRole('checkbox', { name: '启用此配置' }))

  expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ enabled: false }))
  expect(screen.queryByText('管理端口')).not.toBeInTheDocument()
})

it('renders and edits rules, providers, filters, thresholds, and session policy', async () => {
  const onChange = vi.fn()
  render(<ProfileForm value={profileFixture()} onChange={onChange} />)

  await userEvent.type(screen.getByLabelText('规则列表'), 'DOMAIN-SUFFIX,example.com,PROXY')
  await userEvent.click(screen.getByRole('button', { name: '添加规则订阅' }))
  await userEvent.type(screen.getByLabelText('国家筛选'), 'CN')
  await userEvent.type(screen.getByLabelText('长效最低成功率'), '0.95')
  await userEvent.type(screen.getByLabelText('会话 TTL'), '20m')

  expect(onChange).toHaveBeenCalled()
  expect(screen.getByText('远程规则订阅')).toBeInTheDocument()
})
