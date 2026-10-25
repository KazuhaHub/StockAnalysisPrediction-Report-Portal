import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import AppView from './AppView'

const preferences = vi.hoisted(() => ({ dark: false, lang: 'zh-CN' }))
const translate = vi.hoisted(() => (key: string) => key)
vi.mock('../prefs', () => ({ usePrefs: () => preferences }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: translate }) }))

beforeEach(() => {
  preferences.dark = false
  preferences.lang = 'zh-CN'
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
    app: { id: 'status', name: 'Status', entry: 'index.html', version: 'fixture' },
    scopes: ['extension.read', 'extension.manage'],
    token: 'scoped-fixture-token',
    expires_in: 1800,
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function host() {
  return <MemoryRouter initialEntries={['/apps/x/status']}>
    <Routes><Route path="/apps/x/:id" element={<AppView />} /></Routes>
  </MemoryRouter>
}

test('initial app handshake supplies the selected panel language without exposing its token', async () => {
  render(host())
  const frame = await screen.findByTitle<HTMLIFrameElement>('Status')
  const post = vi.spyOn(frame.contentWindow!, 'postMessage')
  fireEvent.load(frame)
  expect(post).toHaveBeenCalledWith(expect.objectContaining({ type: 'rp:init', locale: 'zh-CN' }), '*')
  expect(frame.getAttribute('sandbox')).toBe('allow-scripts')
  expect(JSON.stringify(post.mock.calls)).not.toContain('scoped-fixture-token')
})

test('panel language changes reach the existing app frame without reloading it', async () => {
  const view = render(host())
  const frame = await screen.findByTitle<HTMLIFrameElement>('Status')
  const post = vi.spyOn(frame.contentWindow!, 'postMessage')
  fireEvent.load(frame)
  post.mockClear()
  preferences.lang = 'zh-TW'
  view.rerender(host())
  await waitFor(() => expect(post).toHaveBeenCalledWith(
    expect.objectContaining({ type: 'rp:theme', locale: 'zh-TW' }), '*',
  ))
  expect(screen.getByTitle('Status')).toBe(frame)
  preferences.lang = 'en'
  view.rerender(host())
  await waitFor(() => expect(post).toHaveBeenCalledWith(
    expect.objectContaining({ type: 'rp:theme', locale: 'en' }), '*',
  ))
})
