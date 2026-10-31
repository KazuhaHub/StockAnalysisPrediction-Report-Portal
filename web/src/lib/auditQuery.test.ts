import { describe, expect, it } from 'vitest'

import { auditExportInput, auditQueryParams, instantToWall, rangeBounds, wallToInstant } from './auditQuery'

const NOW = new Date('2026-10-31T09:30:00Z')

describe('rangeBounds', () => {
  it('reads a preset back from now, with no end', () => {
    expect(rangeBounds({ preset: '7d' }, NOW)).toEqual({ from: '2026-10-24T09:30:00.000Z' })
    expect(rangeBounds({ preset: '1h' }, NOW)).toEqual({ from: '2026-10-31T08:30:00.000Z' })
  })

  it('leaves both ends open for all time', () => {
    expect(rangeBounds({ preset: 'all' }, NOW)).toEqual({})
  })

  it('passes an absolute range through', () => {
    const r = { from: '2026-10-30T11:00:00.000Z', to: '2026-10-30T13:00:00.000Z' }
    expect(rangeBounds(r, NOW)).toEqual(r)
  })
})

// An absolute range is picked as wall-clock times in the panel time zone, the zone the table shows
// stamps in, and sent as instants.
describe('panel wall clock', () => {
  it('turns a Shanghai wall clock into the UTC instant and back', () => {
    expect(wallToInstant('2026-10-30 19:00:00', 'Asia/Shanghai')).toBe('2026-10-30T11:00:00.000Z')
    expect(instantToWall('2026-10-30T11:00:00.000Z', 'Asia/Shanghai')).toBe('2026-10-30 19:00:00')
  })

  it('uses the offset in force on that day, not today', () => {
    // New York is on EDT (UTC-4) in October and EST (UTC-5) after 2026-11-01.
    expect(wallToInstant('2026-10-15 08:00:00', 'America/New_York')).toBe('2026-10-15T12:00:00.000Z')
    expect(wallToInstant('2026-11-15 08:00:00', 'America/New_York')).toBe('2026-11-15T13:00:00.000Z')
  })
})

describe('auditQueryParams', () => {
  it('sends an is as the field and an is not as its negation', () => {
    const qs = auditQueryParams(
      [
        { field: 'action', op: 'is', value: 'auth.login' },
        { field: 'actor', op: 'is_not', value: 'admin' },
        { field: 'ip', op: 'is', value: '10.0.0.1' },
        { field: 'target', op: 'is_not', value: '42' },
      ],
      { preset: 'all' },
      NOW,
    )
    expect(Object.fromEntries(qs)).toEqual({ action: 'auth.login', not_actor: 'admin', ip: '10.0.0.1', not_target_id: '42' })
  })

  it('adds the range as instants', () => {
    const qs = auditQueryParams([], { preset: '24h' }, NOW)
    expect(qs.get('from')).toBe('2026-10-30T09:30:00.000Z')
    expect(qs.has('to')).toBe(false)
  })
})

describe('auditExportInput', () => {
  // Export reads "after" as strictly later, the page as from-on; a closed range keeps the same rows.
  it('closes a preset range at now, so Export keeps the rows the page shows', () => {
    expect(auditExportInput([], { preset: '7d' }, NOW)).toEqual({ from: '2026-10-24T09:30:00.000Z', to: '2026-10-31T09:30:00.000Z' })
  })

  it('hands Export the same conditions and range the page is showing', () => {
    const input = auditExportInput(
      [
        { field: 'actor', op: 'is_not', value: 'admin' },
        { field: 'target', op: 'is', value: '42' },
      ],
      { from: '2026-10-30T11:00:00.000Z', to: '2026-10-30T13:00:00.000Z' },
      NOW,
    )
    expect(input).toMatchObject({ notActor: 'admin', target: '42', from: '2026-10-30T11:00:00.000Z', to: '2026-10-30T13:00:00.000Z' })
  })
})
