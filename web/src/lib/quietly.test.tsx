import { act, render, renderHook, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, clearConnectionError } from '../api/client'
import { setConnectionError } from '../api/connection'
import WaitingForConnection from '../components/WaitingForConnection'
import { settleOnAnswer, quietly } from './loadFailure'
import { useLoadFailure } from './useOnReconnect'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

const down = () => new ApiError(0, 'network_unavailable', 'network_unavailable')
afterEach(() => act(() => clearConnectionError()))

describe('quietly (a refresh behind a successful action)', () => {
  it('does not flag the view for a reload on reconnect, so drafts typed since are not overwritten', async () => {
    const { result } = renderHook(() => useLoadFailure())
    const before = result.current.retryKey
    await quietly(async () => { act(() => { result.current.failure(down()) }) })
    act(() => { window.dispatchEvent(new Event('rp:connection-restored')) })
    expect(result.current.retryKey).toBe(before)
  })

  it('still ends a loading flag on a connection failure', async () => {
    const done = vi.fn()
    const [, onFail] = settleOnAnswer(done)
    await quietly(async () => { try { onFail(down()) } catch { /* rethrown for the caller */ } })
    expect(done).toHaveBeenCalledTimes(1)
    try { onFail(down()) } catch { /* outside quietly it keeps the flag up */ }
    expect(done).toHaveBeenCalledTimes(1)
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
