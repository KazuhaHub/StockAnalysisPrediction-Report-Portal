import { describe, expect, it } from 'vitest'
import en from '../../locales/en-US.json'
import zhCN from '../../locales/zh-CN.json'
import zhTW from '../../locales/zh-TW.json'
import { parseAppStatus } from './AppInstallationStatus'

// One failure code per dimension the runner names in a capacity_exhausted receipt, plus the Portal's own
// installation cap. Each needs a message in every language.
const dimensions = ['memory', 'cpu', 'disk', 'image_store', 'running_backends']
// data_quota_capacity is the namespace quota the runner cannot reserve (doc 185).
const codes = [...dimensions.map((d) => `capacity_exhausted_${d}`), 'installation_capacity', 'data_quota_capacity']

describe('capacity failure messages', () => {
  for (const [name, bundle] of [['en-US', en], ['zh-CN', zhCN], ['zh-TW', zhTW]] as const) {
    it(`${name} has a message for every dimension and the installation cap`, () => {
      const messages = bundle as Record<string, string>
      for (const code of codes) {
        expect(messages[`err.${code}`], `${name} err.${code}`).toBeTruthy()
      }
      // One distinct message per code, so an administrator can tell the limits apart.
      expect(new Set(codes.map((code) => messages[`err.${code}`])).size).toBe(codes.length)
    })
  }

  it('the installation status accepts exactly the six capacity codes the server can send', () => {
    const base = { state: 'attention', openable: false, control_paused: false, timeline: [] }
    for (const dimension of dimensions) {
      const code = `capacity_exhausted_${dimension}`
      expect(parseAppStatus({ ...base, error_code: code })).toEqual({ ...base, error_code: code })
      expect(parseAppStatus({ ...base, timeline: [{ at: '2026-11-04T12:00:00Z', event: 'failed', error_code: code }] })).not.toBeNull()
    }
    expect(parseAppStatus({ ...base, error_code: 'data_quota_capacity' })).toEqual({ ...base, error_code: 'data_quota_capacity' })
    // An update's raise that failed for good, or whose answer is not known yet, shows under its own code.
    for (const code of ['data_quota_unavailable', 'data_quota_recovery_required']) {
      expect(parseAppStatus({ ...base, error_code: code })).toEqual({ ...base, error_code: code })
      for (const bundle of [en, zhCN, zhTW] as Array<Record<string, string>>) expect(bundle[`err.${code}`], code).toBeTruthy()
    }
    for (const bundle of [en, zhCN, zhTW] as Array<Record<string, string>>) expect(bundle['err.app_platform_quota_raise_unsettled']).toBeTruthy()
    expect(parseAppStatus({ ...base, error_code: 'capacity_exhausted_swap' })).toBeNull()
    expect(parseAppStatus({ ...base, error_code: 'capacity_exhausted' })).toBeNull()
  })

  it('the installation status shows an update the runner could not checkpoint under its own code, with a message in every language', () => {
    const base = { state: 'attention', openable: false, control_paused: false, timeline: [] }
    for (const code of ['checkpoint_unsafe', 'checkpoint_too_large', 'checkpoint_incomplete', 'plan_expired']) {
      expect(parseAppStatus({ ...base, error_code: code })).toEqual({ ...base, error_code: code })
      expect(parseAppStatus({ ...base, timeline: [{ at: '2026-11-04T12:00:00Z', event: 'failed', error_code: code }] })).not.toBeNull()
      for (const bundle of [en, zhCN, zhTW] as Array<Record<string, string>>) expect(bundle[`err.${code}`], code).toBeTruthy()
    }
    // The unsafe text names the cause the operator has to remove.
    expect(en['err.checkpoint_unsafe']).toMatch(/symlink/i)
    expect(zhCN['err.checkpoint_unsafe']).toMatch(/符号链接/)
    expect(zhTW['err.checkpoint_unsafe']).toMatch(/符號連結/)
  })

  it('the installation status accepts the code of a start the runner proved never ran, with a message in every language', () => {
    const base = { state: 'attention', openable: false, control_paused: false, timeline: [] }
    expect(parseAppStatus({ ...base, error_code: 'start_never_ran' })).toEqual({ ...base, error_code: 'start_never_ran' })
    for (const bundle of [en, zhCN, zhTW] as Array<Record<string, string>>) {
      expect(bundle['err.start_never_ran']).toBeTruthy()
    }
  })

  it('the installation status accepts the code of a start that did not run but is not proven to have done nothing', () => {
    const base = { state: 'attention', openable: false, control_paused: false, timeline: [] }
    expect(parseAppStatus({ ...base, error_code: 'start_never_ran_review_required' })).toEqual({ ...base, error_code: 'start_never_ran_review_required' })
    for (const bundle of [en, zhCN, zhTW] as Array<Record<string, string>>) {
      expect(bundle['err.start_never_ran_review_required']).toBeTruthy()
    }
  })
})
