import { useCallback, useEffect } from 'react'

export const BEFORE_NAVIGATION_EVENT = 'easyproxy:before-navigation'

export function requestAppNavigation(): boolean {
  return window.dispatchEvent(new Event(BEFORE_NAVIGATION_EVENT, { cancelable: true }))
}

export function useUnsavedChangesGuard(
  isDirty: boolean,
  confirm: (message: string) => boolean = window.confirm.bind(window),
) {
  useEffect(() => {
    const handleBeforeUnload = (event: BeforeUnloadEvent) => {
      if (!isDirty) return
      event.preventDefault()
      event.returnValue = ''
    }
    const handleNavigation = (event: Event) => {
      if (isDirty && !confirm('有未保存的修改，确定要离开吗？')) event.preventDefault()
    }
    window.addEventListener('beforeunload', handleBeforeUnload)
    window.addEventListener(BEFORE_NAVIGATION_EVENT, handleNavigation)
    return () => {
      window.removeEventListener('beforeunload', handleBeforeUnload)
      window.removeEventListener(BEFORE_NAVIGATION_EVENT, handleNavigation)
    }
  }, [confirm, isDirty])

  const confirmNavigation = useCallback(() => {
    return !isDirty || confirm('有未保存的修改，确定要离开吗？')
  }, [confirm, isDirty])

  return { confirmNavigation }
}
