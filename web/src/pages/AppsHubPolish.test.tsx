import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import AppsHub from './AppsHub'
vi.mock('../auth', () => ({ useAuth: () => ({ user: 'fixture', can: () => true }) }))
vi.mock('../lib/builtinApps', () => ({ BUILTIN_APPS: [], BUILTIN_PIN_PREFIX: 'builtin:' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
const id = '00000000-0000-4000-8000-000000000001'
const item = { installationId: id, applicationId: 'notes-sample', state: 'installed', openable: true }
const host = () => <MemoryRouter initialEntries={['/apps']}><AppsHub /></MemoryRouter>
const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 })
beforeEach(() => { vi.stubGlobal('fetch', vi.fn()) })
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

test('built-ins wait for the directory: the first settled render already has the platform card', async () => {
  let release: (r: Response) => void = () => {}
  vi.stubGlobal('fetch', vi.fn((input: unknown) => String(input) === '/api/apps'
    ? Promise.resolve(json({ apps: [{ id: 'legacy', name: 'Legacy app' }] }))
    : new Promise<Response>(r => { release = r })))
  render(host())
  await new Promise(r => setTimeout(r, 30))
  expect(screen.queryByText('Legacy app')).toBeNull()
  release(json({ items: [item], nextCursor: '' }))
  await screen.findByText('Legacy app')
  expect(screen.getByText('notes-sample')).toBeTruthy()
})

test('a rejected directory settles with built-ins and an inline retry', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: unknown) => String(input) === '/api/apps'
    ? json({ apps: [{ id: 'legacy', name: 'Legacy app' }] })
    : new Response('boom', { status: 500 })))
  render(host())
  await screen.findByText('Legacy app')
  await screen.findByText('apps.v2ListUnavailable')
  expect(screen.getAllByRole('button', { name: 'common.retry' }).length).toBeGreaterThan(0)
})

test('the normal installed state shows no tag; other states keep theirs', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: unknown) => String(input) === '/api/apps'
    ? json({ apps: [] })
    : json({ items: [item, { ...item, installationId: '00000000-0000-4000-8000-000000000002', applicationId: 'broken', state: 'attention', openable: false }], nextCursor: '' })))
  render(host())
  await screen.findByText('broken')
  await waitFor(() => expect(screen.getByText('apps.v2State.attention')).toBeTruthy())
  expect(screen.queryByText('apps.v2State.installed')).toBeNull()
})

test('a legacy icon key is not printed as text on the launcher', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: unknown) => String(input) === '/api/apps'
    ? json({ apps: [{ id: 'legacy', name: 'Legacy app', icon: 'dashboard' }] })
    : json({ items: [], nextCursor: '' })))
  render(host())
  await screen.findByText('Legacy app')
  expect(screen.queryByText('dashboard')).toBeNull()
})
