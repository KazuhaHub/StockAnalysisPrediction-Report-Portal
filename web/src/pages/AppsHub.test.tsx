import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { ConfigProvider, theme } from 'antd'
import axe from 'axe-core'
import AppsHub from './AppsHub'

// The hub shows what the server says is open, in the administrator's order (ADR 0048), with built-in
// and installed apps in one sequence. A built-in app also still needs its own permission.

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
const authState = vi.hoisted(() => ({ admin: true, can: (_p: string): boolean => true }))
const directoryState = vi.hoisted(() => ({
  items: [] as Array<{
    installationId: string
    applicationId: string
    state: 'installed' | 'pending' | 'disabled' | 'attention'
    openable: boolean
  }>,
}))
vi.mock('../api/client', () => ({ api: apiMock, errText: () => 'failed' }))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: authState.admin, can: (p: string) => authState.can(p) }) }))
vi.mock('../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
vi.mock('../lib/appDirectoryV2', () => ({
  useAppDirectory: () => ({ items: directoryState.items, loading: false, failed: false, nextCursor: '', load: () => undefined }),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
  initReactI18next: { type: '3rdParty', init: () => undefined },
}))

const renderHub = (algorithm = theme.defaultAlgorithm) =>
  render(
    <ConfigProvider theme={{ algorithm }}>
      <MemoryRouter>
        <AppsHub />
      </MemoryRouter>
    </ConfigProvider>,
  )

// The card titles, top to bottom.
const titles = () => screen.getAllByRole('button').map((b) => b.getAttribute('aria-label'))

beforeEach(() => {
  apiMock.get.mockReset()
  authState.can = () => true
  authState.admin = true
  directoryState.items = []
})

describe('AppsHub', () => {
  it('interleaves built-in and installed apps in the server order', async () => {
    apiMock.get.mockResolvedValue({
      apps: [{ id: 'hello', name: 'Hello' }],
      builtin_apps: ['quotes', 'batch', 'recurring'],
      order: ['hello', 'builtin:quotes', 'builtin:recurring', 'builtin:batch'],
    })
    renderHub()
    await screen.findByText('Hello')
    expect(titles()).toEqual(['Hello', 'nav.quotes', 'nav.recurring', 'nav.batch'])
  })

  it('hides a built-in app the server did not list', async () => {
    apiMock.get.mockResolvedValue({
      apps: [],
      builtin_apps: ['batch', 'recurring'],
      order: ['builtin:batch', 'builtin:recurring'],
    })
    renderHub()
    await screen.findByLabelText('nav.batch')
    expect(screen.queryByLabelText('nav.quotes')).toBeNull()
  })

  it('keeps the permission gate on a built-in app that is open', async () => {
    authState.can = (p) => p !== 'run_batch'
    apiMock.get.mockResolvedValue({
      apps: [],
      builtin_apps: ['batch', 'quotes'],
      order: ['builtin:batch', 'builtin:quotes'],
    })
    renderHub()
    await screen.findByLabelText('nav.quotes')
    expect(screen.queryByLabelText('nav.batch')).toBeNull()
  })

  it('shows every built-in app, in the registry order, when the server sends no layout (older server)', async () => {
    apiMock.get.mockResolvedValue({ apps: [{ id: 'hello', name: 'Hello' }] })
    renderHub()
    await screen.findByText('Hello')
    expect(titles()).toEqual(['nav.batch', 'nav.recurring', 'nav.quotes', 'Hello'])
  })

  it('says there are no apps only when nothing at all is open', async () => {
    apiMock.get.mockResolvedValue({ apps: [], builtin_apps: [], order: [] })
    renderHub()
    expect(await screen.findByText('apps.empty')).toBeTruthy()
  })

  it('shows truthful directory states and an admin recovery link without exposing internals', async () => {
    directoryState.items = [
      { installationId: '11111111-1111-4111-8111-111111111111', applicationId: 'pending-app', state: 'pending', openable: false },
      { installationId: '22222222-2222-4222-8222-222222222222', applicationId: 'disabled-app', state: 'disabled', openable: false },
      { installationId: '33333333-3333-4333-8333-333333333333', applicationId: 'broken-app', state: 'attention', openable: false },
    ]
    apiMock.get.mockResolvedValue({ apps: [], builtin_apps: [], order: [] })
    renderHub()
    expect(await screen.findByText('apps.v2Hint.pending')).toBeTruthy()
    expect(screen.getByText('apps.v2Hint.disabled')).toBeTruthy()
    expect(screen.getByText('apps.v2Hint.attention')).toBeTruthy()
    expect(screen.getAllByRole('link', { name: /apps\.v2Manage/ })).toHaveLength(3)
    expect(screen.getAllByRole('link', { name: /apps\.v2Manage/ }).map((link) => link.getAttribute('href'))).toEqual([
      '/manage/apps/installations/11111111-1111-4111-8111-111111111111',
      '/manage/apps/installations/22222222-2222-4222-8222-222222222222',
      '/manage/apps/installations/33333333-3333-4333-8333-333333333333',
    ])
    expect(screen.queryByText(/11111111-1111-4111-8111-111111111111/)).toBeNull()
    expect(screen.queryByText('apps.count')).toBeNull()
    const result = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
        region: { enabled: false },
      },
    })
    expect(result.violations).toEqual([])
  })

  it('does not expose the management action to a non-admin reader', async () => {
    authState.admin = false
    directoryState.items = [
      { installationId: '11111111-1111-4111-8111-111111111111', applicationId: 'disabled-app', state: 'disabled', openable: false },
    ]
    apiMock.get.mockResolvedValue({ apps: [], builtin_apps: [], order: [] })
    renderHub()
    expect(await screen.findByText('apps.v2Hint.disabled')).toBeTruthy()
    expect(screen.queryByRole('link', { name: /apps\.v2Manage/ })).toBeNull()
  })

  it.each([
    ['light', theme.defaultAlgorithm],
    ['dark', theme.darkAlgorithm],
  ])('has no axe violations in %s mode', async (_mode, algorithm) => {
    apiMock.get.mockResolvedValue({
      apps: [{ id: 'hello', name: 'Hello' }],
      builtin_apps: ['quotes'],
      order: ['hello', 'builtin:quotes'],
    })
    renderHub(algorithm)
    await screen.findByText('Hello')
    const result = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
        region: { enabled: false },
      },
    })
    expect(result.violations).toEqual([])
  })
})
