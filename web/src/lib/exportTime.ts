import dayjs from 'dayjs'
import { auditTime, guessZone } from './auditTime'

// Time on the Export page: instants are rendered in the panel (business) time zone and civil
// dates are decided in it, exactly as the audit log does.

export function panelToday(panelTz: string, now: Date = new Date()): string {
  const zone = panelTz || guessZone()
  try {
    return zone ? dayjs(now).tz(zone).format('YYYY-MM-DD') : dayjs(now).format('YYYY-MM-DD')
  } catch {
    return dayjs(now).format('YYYY-MM-DD')
  }
}

/** A unix-millisecond instant (task timestamps) in the panel zone; '' for the 0 "unset" value. */
export function fmtMs(ms: number, panelTz: string): string {
  if (!ms) return ''
  return auditTime(new Date(ms).toISOString(), panelTz).text
}

export const isTimeColumn = (key: string, timeField?: string): boolean =>
  key === timeField || key === 'at' || key.endsWith('_at')

/** Renders one preview cell. Time columns are localised; everything else is shown as the server sent it. */
export function formatCell(key: string, value: string | number | null | undefined, panelTz: string, timeField?: string): string {
  if (value === null || value === undefined || value === '') return ''
  if (isTimeColumn(key, timeField)) {
    if (typeof value === 'number' && value > 1e11) return fmtMs(value, panelTz)
    if (typeof value === 'string' && value.includes('T')) return auditTime(value, panelTz).text
  }
  return String(value)
}
