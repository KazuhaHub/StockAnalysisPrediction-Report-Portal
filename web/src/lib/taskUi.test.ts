import { describe, expect, it } from 'vitest'
import enJson from '../locales/en-US.json'
import zhCNJson from '../locales/zh-CN.json'
import zhTWJson from '../locales/zh-TW.json'
import { formatElapsed, formatTaskTime, runOutcome } from './taskUi'

// The unit strings come from the locale files, so the test reads the real ones.
const tFor = (strings: Record<string, string>) => (key: string, o: { n: number }) => strings[key].replace('{{n}}', String(o.n))
const en = tFor(enJson as Record<string, string>)
const zhCN = tFor(zhCNJson as Record<string, string>)
const zhTW = tFor(zhTWJson as Record<string, string>)

describe('formatElapsed', () => {
  it('reads seconds, minutes and hours compactly in English', () => {
    expect(formatElapsed(0, en)).toBe('0s')
    expect(formatElapsed(59_400, en)).toBe('59s')
    expect(formatElapsed(133_000, en)).toBe('2m 13s')
    expect(formatElapsed(3_900_000, en)).toBe('1h 5m')
    expect(formatElapsed(3_723_000, en)).toBe('1h 2m')
    expect(formatElapsed(3_600_000, en)).toBe('1h')
  })
  it('is localised in Simplified and Traditional Chinese', () => {
    expect(formatElapsed(133_000, zhCN)).toBe('2 分 13 秒')
    expect(formatElapsed(3_900_000, zhCN)).toBe('1 小时 5 分')
    expect(formatElapsed(45_000, zhCN)).toBe('45 秒')
    expect(formatElapsed(133_000, zhTW)).toBe('2 分 13 秒')
    expect(formatElapsed(3_900_000, zhTW)).toBe('1 小時 5 分')
  })
  it('never goes negative', () => {
    expect(formatElapsed(-5_000, en)).toBe('0s')
  })
})

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
    expect(formatTaskTime(now - 5_000, 'en-US', now, 'just now')).toBe('just now')
    expect(formatTaskTime(now - 3 * 60_000, 'en-US', now)).toMatch(/3 minutes ago/)
  })
  it('reads minutes naturally in Chinese', () => {
    expect(formatTaskTime(now - 3 * 60_000, 'zh-CN', now, '刚刚')).toBe('3分钟前')
    expect(formatTaskTime(now - 3 * 60_000, 'zh-TW', now, '剛剛')).toMatch(/3\s?分鐘前/)
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
