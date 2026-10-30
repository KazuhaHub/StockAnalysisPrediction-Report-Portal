import dayjs from 'dayjs'
import type { TaskItem } from '../api/types'

export type RunOutcome = 'ok' | 'someFailed' | 'partialOnly' | 'failed'

/** A finished run is only as good as its rows: some failures make it a partial result, all of them a failure. */
export function runOutcome(c: Pick<NonNullable<TaskItem['counts']>, 'succeeded' | 'partial' | 'failed'> | undefined): RunOutcome {
  if (!c || c.failed + c.partial === 0) return 'ok'
  if (c.failed === 0) return 'partialOnly'
  return c.succeeded === 0 && c.partial === 0 ? 'failed' : 'someFailed'
}

const MINUTE = 60_000

/**
 * A duration as "2m 13s" or, from an hour up, "1h 5m" (seconds no longer matter). The unit wording comes
 * from the locale through `t` (tasks.elapsed.h / .m / .s, each taking {{n}}).
 */
export function formatElapsed(ms: number, t: (key: string, o: { n: number }) => string): string {
  const total = Math.max(0, Math.round(ms / 1000))
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const parts: string[] = []
  if (h > 0) {
    parts.push(t('tasks.elapsed.h', { n: h }))
    if (m > 0) parts.push(t('tasks.elapsed.m', { n: m }))
  } else if (m > 0) {
    parts.push(t('tasks.elapsed.m', { n: m }), t('tasks.elapsed.s', { n: s }))
  } else {
    parts.push(t('tasks.elapsed.s', { n: s }))
  }
  return parts.join(' ')
}

/**
 * A short time for a task row. An instant (epoch milliseconds) under an hour old reads relatively;
 * anything else is the clock for today or "MM-DD HH:mm" for another day. A wall-clock string (what
 * the run queue stores) carries no zone, so it is never made relative. `justNow` is the translated
 * label for an instant under a minute old.
 */
export function formatTaskTime(value: number | string | undefined | null, lang: string, now: number = Date.now(), justNow = ''): string {
  if (value === undefined || value === null || value === '' || value === 0) return ''
  const d = dayjs(value)
  if (!d.isValid()) return ''
  if (typeof value === 'number') {
    const age = now - value
    if (age >= 0 && age < 60 * MINUTE) {
      const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' })
      return age < MINUTE ? justNow || rtf.format(0, 'second') : rtf.format(-Math.floor(age / MINUTE), 'minute')
    }
  }
  return d.isSame(dayjs(now), 'day') ? d.format('HH:mm') : d.format('MM-DD HH:mm')
}
