import { describe, it, expect, vi, afterEach } from 'vitest'
import { buildKey, classifyChange, compareCalVer, formatBuildTime, pageBuildIdentity, parseCalVer } from './buildIdentity'

afterEach(() => vi.unstubAllEnvs())

const build = (over: Partial<{ version: string; commit: string; buildDate: string }> = {}) => ({
  version: 'v2026.38.1',
  commit: 'aaaaaaa',
  buildDate: '2026-08-10T00:00:00Z',
  ...over,
})

describe('pageBuildIdentity', () => {
  it('reads the identity Vite inlined at build time', () => {
    vi.stubEnv('VITE_BUILD_VERSION', 'v2026.38.1')
    vi.stubEnv('VITE_BUILD_COMMIT', 'abc1234')
    vi.stubEnv('VITE_BUILD_DATE', '2026-08-10T00:00:00Z')
    expect(pageBuildIdentity()).toEqual({ version: 'v2026.38.1', commit: 'abc1234', buildDate: '2026-08-10T00:00:00Z' })
  })

  // A dev server, a plain `npm run build`, and a test run all compile without -ldflags-equivalent
  // values. Saying "dev" is the same diagnostic triple the Go binary reports in that case, and it is
  // the honest answer — inventing a version for a bundle nobody stamped would let a page claim to
  // be a release it is not.
  it('reports a diagnostic identity when nothing was stamped in', () => {
    expect(pageBuildIdentity()).toEqual({ version: 'dev', commit: 'none', buildDate: 'unknown' })
  })
})

describe('buildKey', () => {
  it('combines version, commit and build date into a per-binary identity', () => {
    expect(buildKey(build())).toBe('v2026.38.1@aaaaaaa@2026-08-10T00:00:00Z')
    expect(buildKey(build({ commit: 'bbbbbbb' }))).not.toBe(buildKey(build()))
    expect(buildKey(build({ buildDate: '2026-08-11T00:00:00Z' }))).not.toBe(buildKey(build()))
  })
})

describe('parseCalVer / compareCalVer', () => {
  it('parses both spellings, with and without the git tag prefix', () => {
    expect(parseCalVer('v2026.38')).toEqual([2026, 38, 0])
    expect(parseCalVer('v2026.38.1')).toEqual([2026, 38, 1])
    expect(parseCalVer('2026.38.1')).toEqual([2026, 38, 1])
  })

  it('refuses anything that is not a CalVer number', () => {
    for (const v of ['dev', 'ci', 'none', 'unknown', '', 'v0.4.72', 'v2026.38.1-rc']) {
      expect(parseCalVer(v), v).toBeNull()
    }
  })

  it('orders numerically, never lexically', () => {
    expect(compareCalVer('v2026.38', 'v2026.38.1')).toBe(-1) // the week's first release, then its revision
    expect(compareCalVer('v2026.38.1', 'v2026.38')).toBe(1)
    expect(compareCalVer('v2026.9', 'v2026.38')).toBe(-1) // 9 < 38 numerically, "9" > "38" lexically
    expect(compareCalVer('v2026.38.2', 'v2027.1')).toBe(-1) // the year dominates
    expect(compareCalVer('v2026.38.1', 'v2026.38.1')).toBe(0)
  })

  // The one-digit boundaries. A week has no leading zero, so "v2026.9" is a string PREFIX of
  // "v2026.90" and sorts AFTER "v2026.10" — the whole class of bug that a lexical compare, or a
  // contain/prefix test, gets wrong for a third of the year and a tenth of the revisions.
  it('orders a single digit below the two-digit number it looks like a prefix of', () => {
    expect(compareCalVer('v2026.9', 'v2026.10')).toBe(-1)
    expect(compareCalVer('v2026.1', 'v2026.10')).toBe(-1)
    expect(compareCalVer('v2026.2', 'v2026.10')).toBe(-1)
    // …in the revision position too.
    expect(compareCalVer('v2026.38.9', 'v2026.38.10')).toBe(-1)
    expect(compareCalVer('v2026.38.1', 'v2026.38.10')).toBe(-1)
    expect(compareCalVer('v2026.38.10', 'v2026.38.9')).toBe(1)
    // And across the year: week 52 of 2026 still precedes week 1 of 2027.
    expect(compareCalVer('v2026.52', 'v2027.1')).toBe(-1)
  })

  it('cannot order a version it cannot parse', () => {
    expect(compareCalVer('dev', 'v2026.38.1')).toBeNull()
    expect(compareCalVer('v2026.38.1', 'dev')).toBeNull()
  })
})

describe('classifyChange', () => {
  it('reports nothing when the page is the build the server is serving', () => {
    expect(classifyChange(build(), build())).toBeNull()
  })

  it('calls a higher number newer and a lower one a rollback', () => {
    expect(classifyChange(build({ version: 'v2026.38' }), build({ version: 'v2026.38.1' }))).toBe('newer')
    expect(classifyChange(build({ version: 'v2026.38.1' }), build({ version: 'v2026.38' }))).toBe('rollback')
  })

  it('calls a different build of the same number neither newer nor a rollback', () => {
    expect(classifyChange(build(), build({ commit: 'bbbbbbb' }))).toBe('same')
  })

  // A rollback must never be announced as a new release, and "dev" cannot be ordered against a
  // release tag — so anything unorderable gets the generic wording rather than a wrong claim.
  it('falls back to the generic wording when the numbers cannot be ordered', () => {
    expect(classifyChange(build({ version: 'dev', commit: 'none', buildDate: 'unknown' }), build())).toBe('same')
    expect(classifyChange(build({ version: 'v0.4.72' }), build())).toBe('same')
  })

  // The wording follows the numeric order across the one-digit boundary, so week 10 is a newer
  // release than week 9 rather than an older one.
  it('calls the week-9 → week-10 move newer, and its reverse a rollback', () => {
    const nine = build({ version: 'v2026.9' })
    const ten = build({ version: 'v2026.10' })
    expect(classifyChange(nine, ten)).toBe('newer')
    expect(classifyChange(ten, nine)).toBe('rollback')
  })
})

describe('formatBuildTime', () => {
  it('labels the zone the timestamp is shown in', () => {
    const out = formatBuildTime('2026-08-10T12:00:00Z', 'en-US')
    expect(out).toContain('2026')
    // The zone name is the requirement: a bare timestamp is ambiguous across the people who read it.
    expect(out).toMatch(/\(.+\)$/)
  })

  it('passes an unusable value through rather than inventing a date', () => {
    expect(formatBuildTime('unknown', 'en-US')).toBe('unknown')
  })
})
