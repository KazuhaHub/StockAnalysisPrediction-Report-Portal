import { describe, expect, it } from 'vitest'
import { clientInfoFromDetail, clientLabel } from './clientInfo'

describe('clientInfo', () => {
  it('builds a readable summary from the fields that are present', () => {
    expect(clientLabel({ browser: 'Chrome', browser_version: '128', os: 'macOS', os_version: '14.6', device: 'Mac' }))
      .toBe('Chrome 128 · macOS 14.6 · Mac')
    expect(clientLabel({ browser: 'Safari', os: 'iOS' })).toBe('Safari · iOS')
    expect(clientLabel({ browser: 'Firefox', os: 'Linux', device_type: 'desktop' }, 'Desktop')).toBe('Firefox · Linux · Desktop')
  })

  it('reads full client evidence from an audit payload', () => {
    expect(clientInfoFromDetail('{"method":"password","client":{"browser":"Firefox","user_agent":"raw"}}'))
      .toEqual({ browser: 'Firefox', user_agent: 'raw' })
    expect(clientInfoFromDetail('not json')).toBeUndefined()
  })
})
