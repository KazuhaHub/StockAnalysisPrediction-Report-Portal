import { describe, expect, it } from 'vitest'
import { compareVersions, isPrerelease, latestVersion, offeredUpdate } from './semver'

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

describe('prerelease policy', () => {
  it('tells a prerelease from a release, ignoring build metadata', () => {
    expect(isPrerelease('1.0.0-beta.1')).toBe(true)
    expect(isPrerelease('1.0.0')).toBe(false)
    expect(isPrerelease('1.0.0+build-5')).toBe(false)
  })
  it('makes the newest stable version the latest, whatever the order of the list', () => {
    expect(latestVersion(['2.0.0-beta.1', '1.1.0', '1.0.0'])).toBe('1.1.0')
    expect(latestVersion(['1.0.0', '1.1.0', '2.0.0-rc.1'])).toBe('1.1.0')
  })
  it('falls back to the newest prerelease when there is no stable version', () => {
    expect(latestVersion(['0.1.0-beta.2', '0.1.0-beta.10', '0.1.0-alpha'])).toBe('0.1.0-beta.10')
    expect(latestVersion([])).toBeUndefined()
  })
  it('offers a stable install only a newer stable version', () => {
    expect(offeredUpdate('1.0.0', ['2.0.0-beta.1', '1.1.0', '1.0.0'])).toBe('1.1.0')
    expect(offeredUpdate('1.0.0', ['2.0.0-beta.1', '1.0.0'])).toBeUndefined()
    expect(offeredUpdate('1.1.0', ['1.1.0', '1.0.0'])).toBeUndefined()
    expect(offeredUpdate('1.2.0', ['1.1.0'])).toBeUndefined()
  })
  it('offers a prerelease install the latest when it is newer, stable or not', () => {
    expect(offeredUpdate('0.1.0-beta.1', ['0.1.0-beta.2', '0.1.0-beta.1'])).toBe('0.1.0-beta.2')
    expect(offeredUpdate('0.1.0-beta.2', ['0.1.0', '0.1.0-beta.2'])).toBe('0.1.0')
    expect(offeredUpdate('0.1.0-beta.2', ['0.1.0-beta.2'])).toBeUndefined()
  })
})
