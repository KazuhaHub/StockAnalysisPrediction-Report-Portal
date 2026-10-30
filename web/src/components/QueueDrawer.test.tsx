import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import QueueDrawer from './QueueDrawer'

const store = vi.hoisted(() => ({ urls: [] as string[], admin: true }))
vi.mock('../api/client', () => ({
  api: { get: () => Promise.resolve({ targets: [] }), post: () => Promise.resolve({}) },
  errText: (e: unknown) => String(e),
}))
vi.mock('../lib/conditionalGet', () => ({
  UNCHANGED: Symbol('unchanged'),
  forgetTags: () => {},
  getIfChanged: (url: string) => {
    store.urls.push(String(url))
    return String(url).includes('/queue')
      ? Promise.resolve({ running: 0, waiting: 0, scheduled: 0, budget: 3 })
      : Promise.resolve({ jobs: [] })
  },
}))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: store.admin, user: 'alice' }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const mount = () =>
  render(
    <App>
      <MemoryRouter>
        <QueueDrawer open onClose={() => {}} />
      </MemoryRouter>
    </App>,
  )

describe('QueueDrawer mine / all (ADR 0047)', () => {
  beforeEach(() => {
    store.urls = []
    localStorage.clear()
  })

  it('lets an administrator switch to their own runs', async () => {
    store.admin = true
    mount()
    await waitFor(() => expect(store.urls.length).toBeGreaterThan(0))
    fireEvent.click(screen.getByText('queue.scope.mine'))
    await waitFor(() => expect(store.urls.some((u) => u.includes('/jobs') && u.includes('scope=mine'))).toBe(true))
    await waitFor(() => expect(store.urls.some((u) => u.includes('/queue') && u.includes('scope=mine'))).toBe(true))
  })

  it('offers no toggle to a non-administrator', async () => {
    store.admin = false
    mount()
    await waitFor(() => expect(store.urls.length).toBeGreaterThan(0))
    expect(screen.queryByText('queue.scope.mine')).toBeNull()
  })
})
