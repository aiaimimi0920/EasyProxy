import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

import App from './App'
import { BEFORE_NAVIGATION_EVENT } from './hooks/useUnsavedChangesGuard'

const apiMocks = vi.hoisted(() => ({
  checkAuth: vi.fn(),
  getToken: vi.fn(),
  logout: vi.fn(),
  login: vi.fn(),
}))

vi.mock('./api/client', () => apiMocks)
vi.mock('./components/MonitorPanel', () => ({ default: () => <div>monitor</div> }))
vi.mock('./components/ManagePanel', () => ({ default: () => <div>manage</div> }))
vi.mock('./components/RoutingPanel', () => ({ default: () => <div>routing</div> }))
vi.mock('./components/DevicesPanel', () => ({ default: () => <div>devices</div> }))
vi.mock('./components/DebugPanel', () => ({ default: () => <div>debug</div> }))
vi.mock('./components/SettingsPanel', () => ({ default: () => <div>settings</div> }))

beforeEach(() => {
  apiMocks.checkAuth.mockReset()
  apiMocks.getToken.mockReset()
  apiMocks.logout.mockReset()
  apiMocks.login.mockReset()
  apiMocks.getToken.mockReturnValue(null)
})

it('uses canonical auth discovery to render the username field', async () => {
  apiMocks.checkAuth.mockResolvedValue({
    auth_mode: 'canonical_pair',
    username_required: true,
    no_password: false,
  })

  render(<App />)

  expect(await screen.findByRole('textbox', { name: '用户名' })).toBeInTheDocument()
  expect(apiMocks.checkAuth).toHaveBeenCalledOnce()
})

it('uses legacy auth discovery without a second request', async () => {
  apiMocks.checkAuth.mockResolvedValue({
    auth_mode: 'legacy_password',
    username_required: false,
    no_password: false,
  })

  render(<App />)

  expect(await screen.findByLabelText('管理密码')).toBeInTheDocument()
  expect(screen.queryByRole('textbox', { name: '用户名' })).not.toBeInTheDocument()
  expect(apiMocks.checkAuth).toHaveBeenCalledOnce()
})

it('rediscovers canonical auth mode after logout', async () => {
  apiMocks.checkAuth.mockResolvedValue({
    auth_mode: 'canonical_pair',
    username_required: true,
    no_password: false,
  })
  apiMocks.getToken.mockReturnValue('session-token')

  render(<App />)
  expect(await screen.findByText('monitor')).toBeInTheDocument()

  await userEvent.click(screen.getByTitle('退出登录'))

  expect(await screen.findByRole('textbox', { name: '用户名' })).toBeInTheDocument()
  expect(apiMocks.checkAuth).toHaveBeenCalledTimes(2)
})

it('rediscovers canonical auth mode after an unauthorized response', async () => {
  apiMocks.checkAuth.mockResolvedValue({
    auth_mode: 'canonical_pair',
    username_required: true,
    no_password: false,
  })
  apiMocks.getToken.mockReturnValue('session-token')

  render(<App />)
  expect(await screen.findByText('monitor')).toBeInTheDocument()

  act(() => window.dispatchEvent(new CustomEvent('auth:unauthorized')))

  expect(await screen.findByRole('textbox', { name: '用户名' })).toBeInTheDocument()
  expect(apiMocks.checkAuth).toHaveBeenCalledTimes(2)
})

it('keeps the active tab and hash when sidebar navigation is rejected', async () => {
  apiMocks.checkAuth.mockResolvedValue({
    auth_mode: 'canonical_pair',
    username_required: true,
    no_password: true,
  })

  render(<App />)
  expect(await screen.findByText('monitor')).toBeInTheDocument()
  const rejectNavigation = (event: Event) => event.preventDefault()
  window.addEventListener(BEFORE_NAVIGATION_EVENT, rejectNavigation)

  try {
    await userEvent.click(screen.getByRole('button', { name: /设备策略/ }))
    expect(screen.getByText('monitor')).toBeInTheDocument()
    expect(screen.queryByText('devices')).not.toBeInTheDocument()
    expect(window.location.hash).toBe('')
  } finally {
    window.removeEventListener(BEFORE_NAVIGATION_EVENT, rejectNavigation)
  }
})

it('restores the previously accepted hash when browser navigation is rejected', async () => {
  apiMocks.checkAuth.mockResolvedValue({
    auth_mode: 'canonical_pair',
    username_required: true,
    no_password: true,
  })

  render(<App />)
  expect(await screen.findByText('monitor')).toBeInTheDocument()
  const rejectNavigation = (event: Event) => event.preventDefault()
  window.addEventListener(BEFORE_NAVIGATION_EVENT, rejectNavigation)

  try {
    window.location.hash = '#devices'
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.getByText('monitor')).toBeInTheDocument()
    expect(screen.queryByText('devices')).not.toBeInTheDocument()
    expect(window.location.hash).toBe('')
  } finally {
    window.removeEventListener(BEFORE_NAVIGATION_EVENT, rejectNavigation)
  }
})
