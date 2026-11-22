import { describe, expect, it } from 'vitest'
import { compareVersions } from './semver'

describe('compareVersions', () => {
  const cases: [string, string, number][] = [
    ['1.0.0', '1.0.0', 0],
    ['1.0.0', '2.0.0', -1],
    ['1.10.0', '1.2.0', 1],
    ['1.2.10', '1.2.9', 1],
    ['0.1.0-beta.2', '0.1.0-beta.1', 1],
    ['0.1.0', '0.1.0-beta.2', 1],
    ['1.0.0-beta.10', '1.0.0-beta.2', 1],
    ['1.0.0-alpha', '1.0.0-alpha.1', -1],
    ['1.0.0-alpha.1', '1.0.0-alpha.beta', -1],
    ['1.0.0-alpha.beta', '1.0.0-beta', -1],
    ['1.0.0-1', '1.0.0-alpha', -1],
    ['1.0.0+build.1', '1.0.0+build.2', 0],
    ['1.0.0-rc.1', '1.0.0-rc.1.0', -1],
    ['1.0.0-2', '1.0.0-11', -1],
  ]
  for (const [a, b, want] of cases) {
    it(`${a} vs ${b}`, () => {
      expect(compareVersions(a, b)).toBe(want)
      expect(compareVersions(b, a)).toBe(want === 0 ? 0 : -want)
    })
  }
})
