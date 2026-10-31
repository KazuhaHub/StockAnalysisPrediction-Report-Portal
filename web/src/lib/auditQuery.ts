import dayjs from 'dayjs'
import utc from 'dayjs/plugin/utc'
import timezone from 'dayjs/plugin/timezone'

import type { AuditFilterInput } from './exportFilter'

dayjs.extend(utc)
dayjs.extend(timezone)

// The audit log's query, apart from the page: the conditions an admin added, the time range, and
// how both become the API's parameters and the Export hand-off.

export type AuditField = 'action' | 'actor' | 'ip' | 'target'
export type AuditOp = 'is' | 'is_not'
export interface AuditCondition {
  field: AuditField
  op: AuditOp
  value: string
}

export const AUDIT_FIELDS: AuditField[] = ['action', 'actor', 'ip', 'target']

export type AuditPreset = '1h' | '24h' | '7d' | '30d' | '90d' | 'all'
export const AUDIT_PRESETS: AuditPreset[] = ['1h', '24h', '7d', '30d', '90d', 'all']

/** A preset is read back from now each time the log is asked; an absolute range is two instants. */
export type AuditRange = { preset: AuditPreset } | { from: string; to: string }

const PRESET_SPAN: Record<Exclude<AuditPreset, 'all'>, [number, dayjs.ManipulateType]> = {
  '1h': [1, 'hour'],
  '24h': [24, 'hour'],
  '7d': [7, 'day'],
  '30d': [30, 'day'],
  '90d': [90, 'day'],
}

export function rangeBounds(range: AuditRange, now: Date = new Date()): { from?: string; to?: string } {
  if ('from' in range) return { from: range.from, to: range.to }
  if (range.preset === 'all') return {}
  const [n, unit] = PRESET_SPAN[range.preset]
  return { from: dayjs(now).subtract(n, unit).toISOString() }
}

const WALL = 'YYYY-MM-DD HH:mm:ss'

/** A wall-clock time in the panel zone ('' means the reader's own) as a UTC instant. */
export function wallToInstant(wall: string, tz: string): string {
  // ISO-shaped so neither form needs the custom-format parser plugin.
  const iso = wall.replace(' ', 'T')
  return (tz ? dayjs.tz(iso, tz) : dayjs(iso)).toISOString()
}

/** A UTC instant as a wall-clock time in the panel zone ('' means the reader's own). */
export function instantToWall(instant: string, tz: string): string {
  return (tz ? dayjs(instant).tz(tz) : dayjs(instant)).format(WALL)
}

const PARAM: Record<AuditField, string> = { action: 'action', actor: 'actor', ip: 'ip', target: 'target_id' }

export function auditQueryParams(conditions: AuditCondition[], range: AuditRange, now: Date = new Date()): URLSearchParams {
  const qs = new URLSearchParams()
  for (const c of conditions) {
    const value = c.value.trim()
    if (value) qs.set(c.op === 'is' ? PARAM[c.field] : `not_${PARAM[c.field]}`, value)
  }
  const { from, to } = rangeBounds(range, now)
  if (from) qs.set('from', from)
  if (to) qs.set('to', to)
  return qs
}

export function auditExportInput(conditions: AuditCondition[], range: AuditRange, now: Date = new Date()): AuditFilterInput {
  const out: AuditFilterInput = { ...rangeBounds(range, now) }
  // Export reads an open "after" as strictly later than from, where the page reads from-on; closing
  // a preset at now makes it an inclusive between, so Export keeps exactly the rows on the page.
  if (out.from && !out.to) out.to = now.toISOString()
  const key = { action: 'action', actor: 'actor', ip: 'ip', target: 'target' } as const
  const notKey = { action: 'notAction', actor: 'notActor', ip: 'notIp', target: 'notTarget' } as const
  for (const c of conditions) {
    const value = c.value.trim()
    if (value) out[c.op === 'is' ? key[c.field] : notKey[c.field]] = value
  }
  return out
}
