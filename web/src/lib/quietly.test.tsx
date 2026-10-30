import { act, render, renderHook, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, clearConnectionError } from '../api/client'
import { setConnectionError } from '../api/connection'
import WaitingForConnection from '../components/WaitingForConnection'
import { settleOnAnswer } from './loadFailure'
import { useLoadFailure } from './useOnReconnect'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

const down = () => new ApiError(0, 'network_unavailable', 'network_unavailable')
const restore = () => act(() => { window.dispatchEvent(new Event('rp:connection-restored')) })
afterEach(() => act(() => clearConnectionError()))

describe('a quiet load (a refresh behind a successful action)', () => {
  it('does not flag the view for a reload on reconnect, so drafts typed since are not overwritten', () => {
    const { result } = renderHook(() => useLoadFailure())
    const before = result.current.retryKey
    act(() => { result.current.failure(down(), { quiet: true }) })
    restore()
    expect(result.current.retryKey).toBe(before)
  })

  it('does not hide an unrelated normal load that fails at the same time', () => {
    const { result } = renderHook(() => useLoadFailure())
    const before = result.current.retryKey
    act(() => {
      result.current.failure(down(), { quiet: true })
      result.current.failure(down())
    })
    restore()
    expect(result.current.retryKey).toBe(before + 1)
  })

  it('ends its own loading flag on a connection failure, and a normal load keeps its flag up', () => {
    const quietDone = vi.fn()
    const normalDone = vi.fn()
    for (const [done, opts] of [[quietDone, { quiet: true }], [normalDone, undefined]] as const) {
      try { settleOnAnswer(done, opts)[1](down()) } catch { /* rethrown for the caller */ }
    }
    expect(quietDone).toHaveBeenCalledTimes(1)
    expect(normalDone).not.toHaveBeenCalled()
  })
})

describe('WaitingForConnection', () => {
  it('shows its line only while the connection is down', () => {
    render(<WaitingForConnection />)
    expect(screen.queryByText('common.waitingForConnection')).toBeNull()
    act(() => setConnectionError('network_unavailable'))
    expect(screen.getByText('common.waitingForConnection')).toBeTruthy()
  })
})
