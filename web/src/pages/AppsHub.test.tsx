import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import AppsHub from './AppsHub'

// The hub shows what the server says is open, in the administrator's order (ADR 0048), with built-in
// and installed apps in one sequence. A built-in app also still needs its own permission.

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
const authState = vi.hoisted(() => ({ can: (_p: string): boolean => true }))
vi.mock('../api/client', () => ({ api: apiMock, errText: () => 'failed' }))
vi.mock('../auth', () => ({ useAuth: () => ({ can: (p: string) => authState.can(p) }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const renderHub = () =>
  render(
    <MemoryRouter>
      <AppsHub />
    </MemoryRouter>,
  )

// The card titles, top to bottom.
const titles = () => screen.getAllByRole('button').map((b) => b.getAttribute('aria-label'))

beforeEach(() => {
  apiMock.get.mockReset()
  authState.can = () => true
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
})
