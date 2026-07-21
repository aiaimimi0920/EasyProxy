import { expect, it, vi } from 'vitest'

import { apiRequest, checkAuth, getToken, login, setToken } from './client'
import { jsonResponse, mockFetch } from '../test/http'

it('preserves structured 409 payloads', async () => {
  mockFetch(jsonResponse({
    error: 'profile_revision_conflict',
    current_revision: 4,
    need_reload: false,
  }, 409))

  await expect(apiRequest('/api/local-server/profiles/shared')).rejects.toMatchObject({
    status: 409,
    payload: {
      error: 'profile_revision_conflict',
      current_revision: 4,
      need_reload: false,
    },
  })
})

it('clears stale tokens and dispatches the unauthorized event on 401', async () => {
  const unauthorized = vi.fn()
  window.addEventListener('auth:unauthorized', unauthorized)
  setToken('stale-token')
  mockFetch(jsonResponse({ error: 'unauthorized', reason: 'expired' }, 401))

  await expect(apiRequest('/api/local-server/status')).rejects.toMatchObject({ status: 401 })

  expect(getToken()).toBeNull()
  expect(unauthorized).toHaveBeenCalledOnce()
  window.removeEventListener('auth:unauthorized', unauthorized)
})

it('normalizes the legacy password discovery response', async () => {
  mockFetch(jsonResponse({ auth_mode: 'password', username_required: false, no_password: false }))

  await expect(checkAuth()).resolves.toMatchObject({ auth_mode: 'legacy_password' })
})

it('submits the canonical username and password pair', async () => {
  const fetchSpy = mockFetch(jsonResponse({ message: 'ok', token: 'session-token' }))

  await login('easyproxy', 'secret')

  expect(fetchSpy).toHaveBeenCalledWith('/api/auth', expect.objectContaining({
    method: 'POST',
    body: JSON.stringify({ username: 'easyproxy', password: 'secret' }),
  }))
})

it('exposes structured login failures', async () => {
  mockFetch(jsonResponse({ error: '密码错误', retry_after: 1 }, 401))

  await expect(login('easyproxy', 'wrong')).rejects.toMatchObject({
    message: '密码错误',
    status: 401,
    payload: { error: '密码错误', retry_after: 1 },
  })
})
