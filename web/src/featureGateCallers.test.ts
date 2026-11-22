import { describe, expect, it } from 'vitest'

// Closing Scheduled tasks for an OU makes every /api/admin/batch/recurring* route answer 403 app_closed
// (doc 190, D2; the Go route table pins that those are the only feature-gated routes). So no page
// outside the Scheduled tasks console may call one: the header run dialog, the queue page and the
// reading pages must keep working for a closed OU (acceptance A6).
const sources = import.meta.glob('./**/*.{ts,tsx}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const callers = Object.entries(sources).filter(([p]) => !/\.test\.tsx?$/.test(p) && !/\/Recurring[^/]*\.tsx?$/.test(p))

describe('feature-gated routes', () => {
  it('has files to check, including the shared batch callers', () => {
    expect(callers.length).toBeGreaterThan(100)
    expect(callers.some(([, src]) => src.includes('/api/admin/batch/tickets'))).toBe(true)
  })

  it('are called only from the Scheduled tasks console', () => {
    const offenders = callers.filter(([, src]) => /\/api\/admin\/batch\/recurring/.test(src)).map(([p]) => p)
    expect(offenders, 'a page outside the Scheduled tasks console calls a gated recurring route').toEqual([])
  })
})
