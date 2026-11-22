import { describe, expect, it } from 'vitest'
import { DESCRIPTOR_LOCALES } from './descriptor-contract.generated'
import { platformPinOptions } from './appPins'
import type { AppLayoutEntry } from '../api/types'

const id = (n: number) => `${String(n).repeat(8)}-${String(n).repeat(4)}-4${String(n).repeat(3)}-8${String(n).repeat(3)}-${String(n).repeat(12)}`
const display = (names: Record<string, string>) => ({ icon: 'file-text', texts: DESCRIPTOR_LOCALES.map(locale => ({ locale, name: names[locale] ?? 'Notes', description: 'd' })) })
const platform = (n: number, extra: Partial<AppLayoutEntry> = {}): AppLayoutEntry => ({ key: `platform:${id(n)}`, kind: 'platform', name: 'notes', builtin: false, installation_id: id(n), app_id: 'notes', state: 'installed', access: [], ...extra })

describe('platformPinOptions', () => {
  it('lists platform installations as pin choices by their key, with the display name in the reader’s language', () => {
    const entries = [platform(1, { display: display({ 'zh-CN': '笔记', 'en-US': 'Notes' }) })]
    expect(platformPinOptions(entries, 'zh-CN')).toEqual([{ value: `platform:${id(1)}`, label: '笔记' }])
    expect(platformPinOptions(entries, 'en-US')).toEqual([{ value: `platform:${id(1)}`, label: 'Notes' }])
  })

  it('falls back to the layout’s own name, then the application id, when there is no usable display', () => {
    expect(platformPinOptions([platform(1, { name: 'Notes (en)' })], 'zh-CN')[0].label).toBe('Notes (en)')
    expect(platformPinOptions([platform(1, { name: '' })], 'zh-CN')[0].label).toBe('notes')
    expect(platformPinOptions([platform(1, { display: { icon: 'nope', texts: [] } })], 'zh-CN')[0].label).toBe('notes')
  })

  it('leaves out built-ins, retired v1 apps and anything without an installation id', () => {
    const entries: AppLayoutEntry[] = [
      { key: 'builtin:batch', kind: 'builtin', name: 'batch', builtin: true, access: [] },
      { key: 'hello', kind: 'app', name: 'Hello', builtin: false, access: [] },
      platform(2, { installation_id: undefined }),
      platform(3),
    ]
    expect(platformPinOptions(entries, 'en-US').map(o => o.value)).toEqual([`platform:${id(3)}`])
  })

  it('keeps the administrator’s order', () => {
    expect(platformPinOptions([platform(2), platform(1)], 'en-US').map(o => o.value)).toEqual([`platform:${id(2)}`, `platform:${id(1)}`])
  })
})
