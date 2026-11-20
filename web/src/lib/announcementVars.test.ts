import { describe, expect, it } from 'vitest'
import { checkAnnouncementVars, hasClockVars, renderAnnouncementVars } from './announcementVars'

// Mirrors TestCheckAnnouncementVars in internal/app/announcement_vars_test.go: the editor and the
// server must accept exactly the same text, or an operator sees "valid" and the save is refused.
const OK = [
  '',
  'plain text with no variables',
  'a lone {{ without a close',
  '{{now}}',
  '{{ now }}',
  '{{now|date}}',
  '{{now|time}}',
  '{{now|datetime}}',
  '{{now|Asia/Tokyo}}',
  '{{now|time|America/New_York}}',
  '{{now|UTC}}',
  '{{time:2026-11-20T14:00:00Z}}',
  '{{time:2026-11-20T14:00Z}}',
  '{{time:2026-11-20T22:00:00+08:00}}',
  '{{time:2026-11-20T14:00:00.500Z}}',
  '{{time:2026-11-20T14:00:00Z|date}}',
  '{{time:2026-11-20T14:00:00Z|Asia/Shanghai}}',
  '{{time:2026-11-20T14:00:00Z|Asia/Shanghai|time}}',
  '{{time:2026-11-20T14:00:00Z|America/Argentina/Buenos_Aires}}',
  '{{countdown:2026-11-20T14:00:00Z}}',
  '{{site}}',
  '{{time:2026-11-20T14:00+14:00}}',
  '{{time:2026-11-20T14:00-12:00}}',
  '{{time:2028-02-29T00:00Z}}',
  'Maintenance {{time:2026-11-20T14:00Z}} ({{countdown:2026-11-20T14:00Z}}) on {{site}}',
]

const REFUSED: Record<string, string> = {
  '{{}}': '{{}}',
  '{{user}}': '{{user}}',
  '{{tiem:2026-11-20T14:00Z}}': '{{tiem:2026-11-20T14:00Z}}',
  '{{NOW}}': '{{NOW}}',
  '{{now:2026-11-20T14:00Z}}': '{{now:2026-11-20T14:00Z}}',
  '{{site|date}}': '{{site|date}}',
  '{{site:x}}': '{{site:x}}',
  '{{time}}': '{{time}}',
  '{{time:}}': '{{time:}}',
  '{{time:2026-11-20}}': '{{time:2026-11-20}}',
  '{{time:2026-11-20T14:00}}': '{{time:2026-11-20T14:00}}',
  '{{time:2026-11-20 14:00Z}}': '{{time:2026-11-20 14:00Z}}',
  '{{time:2026-13-40T14:00Z}}': '{{time:2026-13-40T14:00Z}}',
  '{{time:tomorrow}}': '{{time:tomorrow}}',
  '{{time:2026-11-20T14:00Z|Mars/Olympus}}': '{{time:2026-11-20T14:00Z|Mars/Olympus}}',
  '{{time:2026-11-20T14:00Z|asia/shanghai}}': '{{time:2026-11-20T14:00Z|asia/shanghai}}',
  '{{time:2026-11-20T14:00Z|Local}}': '{{time:2026-11-20T14:00Z|Local}}',
  '{{time:2026-11-20T14:00Z|CST}}': '{{time:2026-11-20T14:00Z|CST}}',
  '{{time:2026-11-20T14:00Z|date|time}}': '{{time:2026-11-20T14:00Z|date|time}}',
  '{{time:2026-11-20T14:00Z|UTC|Asia/Tokyo}}': '{{time:2026-11-20T14:00Z|UTC|Asia/Tokyo}}',
  '{{time:2026-11-20T14:00Z|}}': '{{time:2026-11-20T14:00Z|}}',
  '{{countdown:2026-11-20T14:00Z|date}}': '{{countdown:2026-11-20T14:00Z|date}}',
  '{{countdown}}': '{{countdown}}',
  '{{time:2026-11-20T14:00+24:00}}': '{{time:2026-11-20T14:00+24:00}}',
  '{{time:2026-11-20T14:00+12:60}}': '{{time:2026-11-20T14:00+12:60}}',
  '{{time:0099-01-01T00:00Z}}': '{{time:0099-01-01T00:00Z}}',
  '{{time:2027-02-29T00:00Z}}': '{{time:2027-02-29T00:00Z}}',
  '{{site\u0085}}': '{{site\u0085}}',
  '{{site\ufeff}}': '{{site\ufeff}}',
  'ok {{now}} then {{bogus}}': '{{bogus}}',
}

describe('checkAnnouncementVars', () => {
  it.each(OK)('accepts %j', (s) => {
    expect(checkAnnouncementVars(s)).toBe('')
  })
  it.each(Object.entries(REFUSED))('refuses %j', (s, want) => {
    expect(checkAnnouncementVars(s)).toBe(want)
  })
})

describe('renderAnnouncementVars', () => {
  const ctx = {
    now: new Date('2026-11-20T12:00:00Z'),
    zone: 'America/Los_Angeles',
    site: 'Research Portal',
    lang: 'en-US',
  }
  const r = (s: string, over: Partial<typeof ctx> = {}) => renderAnnouncementVars(s, { ...ctx, ...over })

  it('renders a fixed instant in the reader zone, tagged with the full zone name', () => {
    expect(r('{{time:2026-11-20T14:00Z}}')).toBe('2026-11-20 06:00 (America/Los_Angeles)')
    expect(r('{{time:2026-11-20T14:00Z}}', { zone: 'Asia/Shanghai' })).toBe('2026-11-20 22:00 (Asia/Shanghai)')
  })

  it('follows daylight saving through the zone database', () => {
    expect(r('{{time:2026-07-01T14:00Z}}')).toBe('2026-07-01 07:00 (America/Los_Angeles)')
  })

  it('pins the zone when the operator names one, whatever the reader zone', () => {
    expect(r('{{time:2026-11-20T14:00Z|Asia/Shanghai}}')).toBe('2026-11-20 22:00 (Asia/Shanghai)')
  })

  it('honours an offset in the instant', () => {
    expect(r('{{time:2026-11-20T22:00:00+08:00|UTC}}')).toBe('2026-11-20 14:00 (UTC)')
  })

  it('formats as date or time only, and the date follows the zone', () => {
    expect(r('{{time:2026-11-20T20:00Z|date}}', { zone: 'Asia/Tokyo' })).toBe('2026-11-21 (Asia/Tokyo)')
    expect(r('{{time:2026-11-20T14:00Z|time|Asia/Shanghai}}')).toBe('22:00 (Asia/Shanghai)')
  })

  it('renders now from the clock it is given', () => {
    expect(r('{{now}}')).toBe('2026-11-20 04:00 (America/Los_Angeles)')
    expect(r('{{now|time|Asia/Shanghai}}')).toBe('20:00 (Asia/Shanghai)')
  })

  it('renders a countdown in the reader language, in a unit that fits the distance', () => {
    expect(r('{{countdown:2026-11-20T12:45Z}}')).toBe('in 45 minutes')
    expect(r('{{countdown:2026-11-20T17:00Z}}')).toBe('in 5 hours')
    expect(r('{{countdown:2026-11-25T12:00Z}}')).toBe('in 5 days')
    expect(r('{{countdown:2026-11-20T09:00Z}}')).toBe('3 hours ago')
    expect(r('{{countdown:2026-11-20T17:00Z}}', { lang: 'zh-CN' })).toBe('5小时后')
  })

  it('renders the site name', () => {
    expect(r('Welcome to {{site}}')).toBe('Welcome to Research Portal')
  })

  it('leaves an unrecognised placeholder as written rather than swallowing it', () => {
    expect(r('a {{bogus}} b {{time:tomorrow}}')).toBe('a {{bogus}} b {{time:tomorrow}}')
  })

  it('leaves text without placeholders untouched', () => {
    expect(r('line one\n  line two {')).toBe('line one\n  line two {')
  })
})

describe('hasClockVars', () => {
  it('is true only when the text moves with the clock', () => {
    expect(hasClockVars('{{now}}')).toBe(true)
    expect(hasClockVars('x {{countdown:2026-11-20T14:00Z}}')).toBe(true)
    expect(hasClockVars('{{time:2026-11-20T14:00Z}} {{site}}')).toBe(false)
    expect(hasClockVars('plain')).toBe(false)
  })
})
