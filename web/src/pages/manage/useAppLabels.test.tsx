import { describe, expect, it, vi } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useAppLabels, type AdminApp } from './appsAdminModel'

const mock = vi.hoisted(() => ({ prefs: null as { lang: string } | null, i18n: 'en-US' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k, i18n: { language: mock.i18n } }) }))
vi.mock('../../prefs', () => ({ useOptionalPrefs: () => mock.prefs }))

const texts = [
  { locale: 'en-US', name: 'Market Monitor', description: '' },
  { locale: 'zh-CN', name: '市场监控', description: '' },
  { locale: 'zh-TW', name: '市場監控', description: '' },
]
const app = { key: 'platform:x', builtin: false, platform: true, appId: 'market-monitor', name: 'Market Monitor', display: { icon: 'dashboard', texts }, scopes: [], access: [] } as unknown as AdminApp

describe('useAppLabels', () => {
  it('names a platform app in the language the preferences hold, even before i18next has caught up', () => {
    // Right after a switch the provider's language is the new one while i18next still reports the old one.
    mock.prefs = { lang: 'zh-CN' }; mock.i18n = 'en-US'
    const { result } = renderHook(() => useAppLabels())
    expect(result.current.title(app)).toBe('市场监控')
  })

  it('agrees with the language the other parts of the page read from the preferences', () => {
    mock.prefs = { lang: 'zh-TW' }; mock.i18n = 'zh-CN'
    const { result } = renderHook(() => useAppLabels())
    expect(result.current.title(app)).toBe('市場監控')
  })

  it('works outside the preferences provider, from i18next', () => {
    mock.prefs = null; mock.i18n = 'zh-CN'
    const { result } = renderHook(() => useAppLabels())
    expect(result.current.title(app)).toBe('市场监控')
  })
})
