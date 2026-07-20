import { renderHook } from '@testing-library/react'
import { act } from 'react'
import { expect, it, vi } from 'vitest'

import { requestAppNavigation, useUnsavedChangesGuard } from './useUnsavedChangesGuard'

it('cancels cross-component navigation when confirmation is rejected', () => {
  const confirm = vi.fn(() => false)
  renderHook(() => useUnsavedChangesGuard(true, confirm))

  let result = true
  act(() => {
    result = requestAppNavigation()
  })

  expect(result).toBe(false)
  expect(confirm).toHaveBeenCalledOnce()
})

it('allows clean navigation and beforeunload without prompting', () => {
  const confirm = vi.fn(() => false)
  renderHook(() => useUnsavedChangesGuard(false, confirm))

  let result = false
  act(() => {
    result = requestAppNavigation()
  })
  const event = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(event)

  expect(result).toBe(true)
  expect(event.defaultPrevented).toBe(false)
  expect(confirm).not.toHaveBeenCalled()
})

it('prevents beforeunload while dirty', () => {
  renderHook(() => useUnsavedChangesGuard(true, vi.fn(() => true)))
  const event = new Event('beforeunload', { cancelable: true })

  window.dispatchEvent(event)

  expect(event.defaultPrevented).toBe(true)
})
