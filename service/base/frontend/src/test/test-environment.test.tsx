import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it } from 'vitest'

import { jsonResponse, mockFetch } from './http'

const originalFetch = globalThis.fetch

function Probe() {
  return <button onClick={() => localStorage.setItem('clicked', 'yes')}>ready</button>
}

it('provides jsdom, jest-dom, and isolated localStorage', async () => {
  const user = userEvent.setup()
  render(<Probe />)
  expect(screen.getByRole('button', { name: 'ready' })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'ready' }))
  expect(localStorage.getItem('clicked')).toBe('yes')
})

it('clears localStorage between tests', () => {
  expect(localStorage.getItem('clicked')).toBeNull()
})

it('can stub the global fetch implementation', () => {
  mockFetch(jsonResponse({ ok: true }))
  expect(globalThis.fetch).not.toBe(originalFetch)
})

it('restores the global fetch implementation between tests', () => {
  expect(globalThis.fetch).toBe(originalFetch)
})
