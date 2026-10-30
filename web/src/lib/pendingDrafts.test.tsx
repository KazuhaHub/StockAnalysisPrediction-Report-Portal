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

  it('skips the reload while a form is dirty, and reloads once it is not', () => {
    const { result } = renderHook(() => useLoadFailure())
    const before = result.current.retryKey
    const draft = renderHook(() => usePendingSettings(true))
    act(() => { result.current.failure(down()) })
    restore()
    expect(result.current.retryKey).toBe(before)
    draft.unmount()
    act(() => { result.current.failure(down()) })
    restore()
    expect(result.current.retryKey).toBe(before + 1)
  })
})
