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

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date(2026, 9, 29, 9, 2, 13))
})
afterEach(() => vi.useRealTimers())

describe('RunningFor', () => {
  it('shows the time since the run started and advances every second', () => {
    render(<RunningFor since="2026-10-29 09:00:00" />)
    expect(screen.getByText('tasks.runningFor:{"elapsed":"2m 13s"}')).toBeTruthy()
    act(() => {
      vi.advanceTimersByTime(2000)
    })
    expect(screen.getByText('tasks.runningFor:{"elapsed":"2m 15s"}')).toBeTruthy()
  })

  it('stops ticking when unmounted', () => {
    const { unmount } = render(<RunningFor since="2026-10-29 09:00:00" />)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('falls back to plain Running text when there is no start time', () => {
    render(<RunningFor since="" />)
    expect(screen.getByText('batch.status.running')).toBeTruthy()
  })
})
