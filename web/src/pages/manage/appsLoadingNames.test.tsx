import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, renderHook } from '@testing-library/react'
import { installedDisplay, installedPublisher, useAppLabels, type AdminApp } from './appsAdminModel'

const language = vi.hoisted(() => ({ value: 'zh-CN' }))
vi.mock('../../prefs', () => ({ useOptionalPrefs: () => null }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key, i18n: { language: language.value } }) }))

const display = { icon: 'dashboard', texts: [
  { locale: 'en-US', name: 'Market Monitor', description: 'Live watchlists' },
  { locale: 'zh-CN', name: '市场监控', description: '实时看板' },
  { locale: 'zh-TW', name: '市場監控', description: '即時看板' },
] } as unknown as AdminApp['display']
const app: AdminApp = {
  key: 'platform:11111111-1111-4111-8111-111111111111', builtin: false, platform: true,
  installationId: '11111111-1111-4111-8111-111111111111', appId: 'market-monitor', publisherId: 'kazuha-hub',
  name: 'Market Monitor', version: '1.4.0', scopes: [], access: [], state: 'installed', display, publisherName: 'Kazuha Hub',
}

afterEach(cleanup)

describe('an installed app shows what the layout carries, from the first paint', () => {
  it('names the publisher from the layout only; the catalogue never supplies it', () => {
    expect(installedPublisher(app)).toBe('Kazuha Hub')
    expect(installedPublisher({ ...app, publisherName: undefined })).toBeUndefined()
  })

  it('takes the name and description in the reader’s language from the layout display', () => {
    expect(installedDisplay(app, 'zh-CN', 'Market Monitor')).toEqual({ name: '市场监控', description: '实时看板' })
    expect(installedDisplay(app, 'en-US', 'Market Monitor')).toEqual({ name: 'Market Monitor', description: 'Live watchlists' })
    // Without a verified display the layout's own name stands, and the id is never the name.
    expect(installedDisplay({ ...app, display: undefined }, 'zh-CN', 'Market Monitor')).toEqual({ name: 'Market Monitor', description: '' })
  })
})

describe('every console place that names a platform app speaks in the reader’s language', () => {
  // The layout's own `name` is the English display name; the verified display carries every language.
  const named: AdminApp = { ...app, name: 'Market Monitor' }
  it.each([['zh-CN', '市场监控'], ['zh-TW', '市場監控'], ['en-US', 'Market Monitor']])('titles the app in %s from its verified display', (lang, expected) => {
    language.value = lang
    const { result } = renderHook(() => useAppLabels())
    expect(result.current.title(named)).toBe(expected)
  })

  it('keeps the layout name for an app without a verified display, and a built-in or v1 app as it was', () => {
    language.value = 'zh-CN'
    const { result } = renderHook(() => useAppLabels())
    expect(result.current.title({ ...named, display: undefined })).toBe('Market Monitor')
    expect(result.current.title({ ...named, platform: false, display: undefined, name: 'Hello' })).toBe('Hello')
  })
})
