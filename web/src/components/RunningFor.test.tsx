import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import RunningFor from './RunningFor'

vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => {
    const unit = /^tasks\.elapsed\.([hms])$/.exec(k)
    if (unit) return `${(o as { n: number }).n}${unit[1]}`
    return typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`
  }
  return { useTranslation: () => ({ t, i18n: { language: 'en-US' } }) }
})

// The server's wall-clock "2026-10-29 09:00:00" in Asia/Shanghai is 01:00:00Z. Whatever zone this browser is in,
// elapsed time is measured between instants, so it must not move with it.
const STARTED = Date.UTC(2026, 9, 29, 1, 0, 0)

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(STARTED + 133_000)
})
afterEach(() => vi.useRealTimers())

describe('RunningFor', () => {
  it('shows the time since the run started and advances every second', () => {
    render(<RunningFor since={STARTED} />)
    expect(screen.getByText('tasks.runningFor:{"elapsed":"2m 13s"}')).toBeTruthy()
    act(() => {
      vi.advanceTimersByTime(2000)
    })
    expect(screen.getByText('tasks.runningFor:{"elapsed":"2m 15s"}')).toBeTruthy()
  })

  it('stops ticking when unmounted', () => {
    const { unmount } = render(<RunningFor since={STARTED} />)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('falls back to plain Running text when there is no start instant', () => {
    render(<RunningFor since={0} />)
    expect(screen.getByText('batch.status.running')).toBeTruthy()
  })
})
