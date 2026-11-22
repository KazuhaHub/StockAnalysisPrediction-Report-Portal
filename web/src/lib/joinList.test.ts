import { describe, expect, it } from 'vitest'
import { joinList } from './joinList'

describe('joinList', () => {
  it('uses the Chinese enumeration mark in both Chinese locales', () => {
    expect(joinList(['运行', '定时'], 'zh-CN')).toBe('运行、定时')
    expect(joinList(['運行', '定時'], 'zh-TW')).toBe('運行、定時')
  })
  it('uses a comma and a space elsewhere, never a Chinese mark', () => {
    expect(joinList(['Run in the background', 'Schedule background tasks'], 'en-US')).toBe('Run in the background, Schedule background tasks')
  })
  it('leaves one item and none alone', () => {
    expect(joinList(['a'], 'en-US')).toBe('a')
    expect(joinList([], 'en-US')).toBe('')
  })
})
