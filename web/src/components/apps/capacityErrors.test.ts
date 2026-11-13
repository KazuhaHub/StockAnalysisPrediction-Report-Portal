import { describe, expect, it } from 'vitest'
import en from '../../locales/en-US.json'
import zhCN from '../../locales/zh-CN.json'
import zhTW from '../../locales/zh-TW.json'
import { parseAppStatus } from './AppInstallationStatus'

// One failure code per dimension the runner names in a capacity_exhausted receipt, plus the Portal's own
// installation cap. Each needs a message in every language.
const dimensions = ['memory', 'cpu', 'disk', 'image_store', 'running_backends']
const codes = [...dimensions.map((d) => `capacity_exhausted_${d}`), 'installation_capacity']

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

  it('the installation status accepts exactly the five capacity codes the server can send', () => {
    const base = { state: 'attention', openable: false, control_paused: false, timeline: [] }
    for (const dimension of dimensions) {
      const code = `capacity_exhausted_${dimension}`
      expect(parseAppStatus({ ...base, error_code: code })).toEqual({ ...base, error_code: code })
      expect(parseAppStatus({ ...base, timeline: [{ at: '2026-11-04T12:00:00Z', event: 'failed', error_code: code }] })).not.toBeNull()
    }
    expect(parseAppStatus({ ...base, error_code: 'capacity_exhausted_swap' })).toBeNull()
    expect(parseAppStatus({ ...base, error_code: 'capacity_exhausted' })).toBeNull()
  })
})
