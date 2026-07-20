import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

import LoginPage from './LoginPage'

const apiMocks = vi.hoisted(() => ({ login: vi.fn() }))

vi.mock('../api/client', () => ({ login: apiMocks.login }))

beforeEach(() => {
  apiMocks.login.mockReset()
  apiMocks.login.mockResolvedValue({ message: 'ok', token: 'token' })
})

it('submits the canonical username and password', async () => {
  const onLogin = vi.fn()
  render(<LoginPage authMode="canonical_pair" onLogin={onLogin} />)

  await userEvent.type(screen.getByRole('textbox', { name: '用户名' }), 'easyproxy')
  await userEvent.type(screen.getByLabelText('管理密码'), 'secret')
  await userEvent.click(screen.getByRole('button', { name: '登录系统' }))

  expect(apiMocks.login).toHaveBeenCalledWith('easyproxy', 'secret')
  expect(onLogin).toHaveBeenCalledOnce()
})

it('keeps the legacy login password-only', () => {
  render(<LoginPage authMode="legacy_password" onLogin={vi.fn()} />)

  expect(screen.queryByRole('textbox', { name: '用户名' })).not.toBeInTheDocument()
  expect(screen.getByLabelText('管理密码')).toBeInTheDocument()
})

it('shows a 401 login error', async () => {
  apiMocks.login.mockRejectedValue(new Error('密码错误'))
  render(<LoginPage authMode="canonical_pair" onLogin={vi.fn()} />)

  await userEvent.type(screen.getByRole('textbox', { name: '用户名' }), 'easyproxy')
  await userEvent.type(screen.getByLabelText('管理密码'), 'wrong')
  await userEvent.click(screen.getByRole('button', { name: '登录系统' }))

  expect(await screen.findByRole('alert')).toHaveTextContent('密码错误')
})
