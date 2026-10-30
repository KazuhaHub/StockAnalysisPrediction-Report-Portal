import { describe, expect, it } from 'vitest'
import { formatTaskTime, runOutcome } from './taskUi'

describe('runOutcome', () => {
  it('is ok when no row failed or came back partial', () => {
    expect(runOutcome({ succeeded: 3, partial: 0, failed: 0 })).toBe('ok')
    expect(runOutcome(undefined)).toBe('ok')
  })
  it('separates partial-only, mixed and total failure', () => {
    expect(runOutcome({ succeeded: 2, partial: 1, failed: 0 })).toBe('partialOnly')
    expect(runOutcome({ succeeded: 1, partial: 0, failed: 2 })).toBe('someFailed')
    expect(runOutcome({ succeeded: 0, partial: 0, failed: 3 })).toBe('failed')
  })
})

describe('formatTaskTime', () => {
  const now = new Date(2026, 9, 29, 12, 0, 0).getTime()
  it('reads a fresh instant relatively for the first hour', () => {
    expect(formatTaskTime(now - 5_000, 'en-US', now)).toMatch(/now/i)
    expect(formatTaskTime(now - 3 * 60_000, 'en-US', now)).toMatch(/3 minutes ago/)
  })
  it('shows the clock for earlier today and the date for another day', () => {
    expect(formatTaskTime(new Date(2026, 9, 29, 9, 30).getTime(), 'en-US', now)).toBe('09:30')
    expect(formatTaskTime(new Date(2026, 9, 28, 22, 36).getTime(), 'en-US', now)).toBe('10-28 22:36')
  })
  it('reads a wall-clock string the same way, without a relative form', () => {
    expect(formatTaskTime('2026-10-29 11:58:00', 'en-US', now)).toBe('11:58')
    expect(formatTaskTime('2026-10-27 08:05:00', 'en-US', now)).toBe('10-27 08:05')
  })
  it('is empty for nothing', () => {
    expect(formatTaskTime(0, 'en-US', now)).toBe('')
    expect(formatTaskTime('', 'en-US', now)).toBe('')
    expect(formatTaskTime(undefined, 'en-US', now)).toBe('')
  })
})
