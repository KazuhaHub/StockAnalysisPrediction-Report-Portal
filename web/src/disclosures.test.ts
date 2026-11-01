import { describe, expect, it } from 'vitest'

// Every expand/collapse animates and honours reduced motion: antd's Collapse does both, a native
// <details> pops open with neither.
const sources = import.meta.glob('./**/*.tsx', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const production = Object.entries(sources).filter(([p]) => !/\.test\.tsx$/.test(p))

describe('disclosures', () => {
  it('has files to check', () => {
    expect(production.length).toBeGreaterThan(50)
  })

  it('never use a native <details>', () => {
    const offenders = production.filter(([, src]) => /<details\b/.test(src)).map(([f]) => f)
    expect(offenders, 'use antd Collapse').toEqual([])
  })
})
