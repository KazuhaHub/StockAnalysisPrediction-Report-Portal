import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { MarketCard, MarketDetailModal } from './appStoreParts'
import { AppDisplayIcon, parseAppDisplay } from '../../lib/appDisplayV2'
import { parsePlatformMarketApps, type PlatformMarketApp } from '../../api/appPlatform'

vi.mock('react-i18next', () => {
  const t = (k: string, o?: Record<string, unknown>) => (o === undefined ? k : `${k}:${o.publisher ?? JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

const sha = 'a'.repeat(64)
const texts = [
  { locale: 'en-US', name: 'Notes', description: 'Personal notes' },
  { locale: 'zh-CN', name: '笔记', description: '个人笔记' },
  { locale: 'zh-TW', name: '筆記', description: '個人筆記' },
]
const app = (extra: Partial<PlatformMarketApp> = {}): PlatformMarketApp => ({
  publisher_id: 'kazuha', app_id: 'notes', publisher_trusted: true,
  versions: [{ version: '1.0.0', descriptor_sha256: 'b'.repeat(64) }],
  display: { icon: 'file-text', texts: texts as never }, ...extra,
})
const card = (a: PlatformMarketApp) => render(<MemoryRouter><MarketCard app={a} name="Notes" description="d" staging={undefined} blocked={false} onOpen={() => {}} onInstall={() => {}} /></MemoryRouter>)

describe('publisher name', () => {
  it('shows the declared name on the market card, not the ID', () => {
    card(app({ publisher_name: 'Kazuha Hub' }))
    expect(screen.getByText('appStore.publishedBy:Kazuha Hub')).toBeTruthy()
    expect(screen.queryByText('appStore.publishedBy:kazuha')).toBeNull()
  })
  it('falls back to the publisher ID without a name', () => {
    card(app())
    expect(screen.getByText('appStore.publishedBy:kazuha')).toBeTruthy()
  })
  it('keeps the ID visible in the details view beside the name', () => {
    render(<MarketDetailModal app={app({ publisher_name: 'Kazuha Hub' })} lang="en-US" staging={undefined} blocked={false} onClose={() => {}} onInstall={() => {}} onOpenTrust={() => {}} />)
    expect(screen.getByText('Kazuha Hub (kazuha)')).toBeTruthy()
  })
  it('reads publisher_name from the catalogue and drops an oversized one', () => {
    const base = { publisher_id: 'kazuha', app_id: 'notes', versions: [{ version: '1.0.0', descriptor_sha256: 'b'.repeat(64) }], publisher_trusted: true }
    const apps = parsePlatformMarketApps({ apps: [{ ...base, publisher_name: 'Kazuha Hub' }, { ...base, app_id: 'long', publisher_name: 'x'.repeat(65) }] })
    expect(apps.map(a => a.publisher_name)).toEqual(['Kazuha Hub', undefined])
  })
})

describe('custom icon', () => {
  it('parses a display that carries only a hash, and refuses bytes or a bad hash', () => {
    expect(parseAppDisplay({ icon: 'file-text', icon_sha256: sha, texts })?.icon_sha256).toBe(sha)
    expect(parseAppDisplay({ icon: 'file-text', icon_png: 'iVBORw0KGgo=', texts })).toBeNull()
    expect(parseAppDisplay({ icon: 'file-text', icon_sha256: 'nothex', texts })).toBeNull()
  })
  it('renders the hashed image and falls back to the built-in icon when it fails to load', () => {
    const display = parseAppDisplay({ icon: 'file-text', icon_sha256: sha, texts })
    const { container } = render(<AppDisplayIcon display={display} />)
    const img = container.querySelector('img') as HTMLImageElement
    expect(img.getAttribute('src')).toBe(`/api/app-platform/icons/${sha}.png`)
    expect(img.getAttribute('alt')).toBe('')
    fireEvent.error(img)
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('.anticon-file-text')).not.toBeNull()
  })
  it('renders only the built-in icon without a hash', () => {
    const { container } = render(<AppDisplayIcon display={parseAppDisplay({ icon: 'cloud', texts })} />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('.anticon-cloud')).not.toBeNull()
  })
})
