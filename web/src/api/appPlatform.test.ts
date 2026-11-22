import { describe, expect, it } from 'vitest'
import { parsePlatformMarketApps, parsePlatformStageStatus } from './appPlatform'
import { DESCRIPTOR_LOCALES } from '../lib/descriptor-contract.generated'

const sha = (c: string) => c.repeat(64)
const stageSha = sha('c')
const display = { icon: 'file-text', texts: DESCRIPTOR_LOCALES.map(locale => ({ locale, name: `Notes ${locale}`, description: 'Personal notes' })) }
const entry = { publisher_id: 'publisher', app_id: 'notes', versions: [{ version: '1.1.0', descriptor_sha256: sha('b') }, { version: '1.0.0', descriptor_sha256: sha('a') }], display, publisher_trusted: true }

describe('parsePlatformMarketApps', () => {
  it('reads versions newest first, the display and the trust flag', () => {
    const [app] = parsePlatformMarketApps({ apps: [entry] })
    expect(app.versions.map(v => v.version)).toEqual(['1.1.0', '1.0.0'])
    expect(app.display?.icon).toBe('file-text')
    expect(app.publisher_trusted).toBe(true)
  })
  it('treats a missing apps list, as from an older server, as empty', () => {
    expect(parsePlatformMarketApps({ releases: [] })).toEqual([])
    expect(parsePlatformMarketApps(null)).toEqual([])
  })
  it('drops a display that does not parse and an untrusted flag that is not literally true', () => {
    const [app] = parsePlatformMarketApps({ apps: [{ ...entry, display: { icon: 'nope', texts: [] }, publisher_trusted: 'yes' }] })
    expect(app.display).toBeUndefined()
    expect(app.publisher_trusted).toBe(false)
  })
  it('skips an entry with a malformed id or digest', () => {
    expect(parsePlatformMarketApps({ apps: [{ ...entry, app_id: 'Bad Id' }, { ...entry, versions: [{ version: '1.0.0', descriptor_sha256: 'short' }] }, entry] })).toHaveLength(1)
  })
})


describe('parsePlatformStageStatus', () => {
  it('reads a synchronous staging answer, which has no state, as staged', () => {
    expect(parsePlatformStageStatus({ release_sha256: stageSha, app_id: 'notes', reused: false })).toEqual({ state: 'staged', release_sha256: stageSha })
  })
  it('reads progress with and without an announced total', () => {
    expect(parsePlatformStageStatus({ state: 'downloading', release_sha256: stageSha, bytes_done: '10', bytes_total: '20' })).toEqual({ state: 'downloading', release_sha256: stageSha, bytes_done: 10, bytes_total: 20 })
    expect(parsePlatformStageStatus({ state: 'downloading', release_sha256: stageSha, bytes_done: '0', bytes_total: '' })).toEqual({ state: 'downloading', release_sha256: stageSha, bytes_done: 0, bytes_total: null })
  })
  it('reads a failure with its public code', () => {
    expect(parsePlatformStageStatus({ state: 'failed', release_sha256: stageSha, error_code: 'app_platform_busy' })).toEqual({ state: 'failed', release_sha256: stageSha, error_code: 'app_platform_busy' })
  })
  it.each([
    null, [], { state: 'staged' }, { state: 'failed', release_sha256: stageSha }, { state: 'other', release_sha256: stageSha },
    { state: 'downloading', release_sha256: stageSha, bytes_done: 5, bytes_total: '' },
    { state: 'downloading', release_sha256: stageSha, bytes_done: '-1', bytes_total: '' },
    { state: 'downloading', release_sha256: stageSha, bytes_done: '1' },
  ])('refuses a malformed answer %#', value => {
    expect(parsePlatformStageStatus(value)).toBeNull()
  })
})
