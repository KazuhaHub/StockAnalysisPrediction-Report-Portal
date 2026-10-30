import { act, renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/client'
import { usePendingSettings } from '../pages/manage/AdminDraftBoundary'
import { hasPendingDrafts } from './pendingDrafts'
import { useLoadFailure } from './useOnReconnect'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

const down = () => new ApiError(0, 'network_unavailable', 'network_unavailable')
const restore = () => act(() => { window.dispatchEvent(new Event('rp:connection-restored')) })

describe('reconnect reload and unsaved edits', () => {
  it('tracks dirty drafts and clears them on unmount', () => {
    const { unmount } = renderHook(() => usePendingSettings(true))
    expect(hasPendingDrafts()).toBe(true)
    unmount()
    expect(hasPendingDrafts()).toBe(false)
  })

  it('reloads an unrelated view on reconnect but holds a guarded config load while a form is dirty', () => {
    const page = renderHook(() => ({ history: useLoadFailure(), config: useLoadFailure({ guardDrafts: true }) }))
    const h0 = page.result.current.history.retryKey
    const c0 = page.result.current.config.retryKey
    const draft = renderHook(() => usePendingSettings(true))
    act(() => { page.result.current.history.failure(down()); page.result.current.config.failure(down()) })
    restore()
    expect(page.result.current.history.retryKey).toBe(h0 + 1)
    expect(page.result.current.config.retryKey).toBe(c0)
    draft.unmount()
  })

  it('runs the held config reload once the form becomes clean (discard or save)', () => {
    const page = renderHook(() => useLoadFailure({ guardDrafts: true }))
    const before = page.result.current.retryKey
    const draft = renderHook(({ dirty }) => usePendingSettings(dirty), { initialProps: { dirty: true } })
    act(() => { page.result.current.failure(down()) })
    restore()
    expect(page.result.current.retryKey).toBe(before)
    draft.rerender({ dirty: false })
    expect(page.result.current.retryKey).toBe(before + 1)
    draft.rerender({ dirty: true })
    draft.rerender({ dirty: false })
    expect(page.result.current.retryKey).toBe(before + 1)
  })

  it('reloads a guarded load at once when no form is dirty', () => {
    const page = renderHook(() => useLoadFailure({ guardDrafts: true }))
    const before = page.result.current.retryKey
    act(() => { page.result.current.failure(down()) })
    restore()
    expect(page.result.current.retryKey).toBe(before + 1)
  })
})
