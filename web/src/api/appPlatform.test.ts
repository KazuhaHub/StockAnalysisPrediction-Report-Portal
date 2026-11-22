import { describe, expect, it } from 'vitest'
import { parsePlatformStageStatus } from './appPlatform'

const sha = 'c'.repeat(64)

describe('parsePlatformStageStatus', () => {
  it('reads a synchronous staging answer, which has no state, as staged', () => {
    expect(parsePlatformStageStatus({ release_sha256: sha, app_id: 'notes', reused: false })).toEqual({ state: 'staged', release_sha256: sha })
  })
  it('reads progress with and without an announced total', () => {
    expect(parsePlatformStageStatus({ state: 'downloading', release_sha256: sha, bytes_done: '10', bytes_total: '20' })).toEqual({ state: 'downloading', release_sha256: sha, bytes_done: 10, bytes_total: 20 })
    expect(parsePlatformStageStatus({ state: 'downloading', release_sha256: sha, bytes_done: '0', bytes_total: '' })).toEqual({ state: 'downloading', release_sha256: sha, bytes_done: 0, bytes_total: null })
  })
  it('reads a failure with its public code', () => {
    expect(parsePlatformStageStatus({ state: 'failed', release_sha256: sha, error_code: 'app_platform_busy' })).toEqual({ state: 'failed', release_sha256: sha, error_code: 'app_platform_busy' })
  })
  it.each([
    null, [], { state: 'staged' }, { state: 'failed', release_sha256: sha }, { state: 'other', release_sha256: sha },
    { state: 'downloading', release_sha256: sha, bytes_done: 5, bytes_total: '' },
    { state: 'downloading', release_sha256: sha, bytes_done: '-1', bytes_total: '' },
    { state: 'downloading', release_sha256: sha, bytes_done: '1' },
  ])('refuses a malformed answer %#', value => {
    expect(parsePlatformStageStatus(value)).toBeNull()
  })
})
