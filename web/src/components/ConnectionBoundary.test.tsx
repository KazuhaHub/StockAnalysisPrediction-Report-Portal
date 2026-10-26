import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { Link, MemoryRouter, useLocation } from 'react-router'
import ConnectionBoundary, { ConnectionContent, ConnectionNotice } from './ConnectionBoundary'
import { api, clearConnectionError } from '../api/client'
const auth = vi.hoisted(() => ({ refresh: vi.fn().mockResolvedValue(undefined), loading: false }))
vi.mock('../auth', () => ({ useAuth: () => auth }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
afterEach(() => { vi.unstubAllGlobals(); clearConnectionError(); auth.loading = false })
function Page() {
  const location = useLocation()
  return <><header>Navigation</header><ConnectionNotice /><ConnectionContent>
    <input aria-label="draft" defaultValue="unsaved" /><p>{location.pathname}</p>
    <Link to="/next">Next page</Link>
  </ConnectionContent></>
}
function mount() {
  return render(<MemoryRouter><ConnectionBoundary><Page /></ConnectionBoundary></MemoryRouter>)
}
async function disconnect() {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
  await act(async () => { await api.get('/api/home').catch(() => {}) })
}
it('keeps the current page and draft visible after a background failure', async () => {
  mount()
  await disconnect()
  expect(screen.getByRole('textbox', { name: 'draft' })).toBeTruthy()
  expect(screen.queryByTestId('connection-page')).toBeNull()
  expect(screen.getByRole('alert').textContent).toContain('connection.notice')
})
it('keeps navigation visible and shows an in-content error only after navigation', async () => {
  mount()
  await disconnect()
  fireEvent.click(screen.getByText('Next page'))
  expect(screen.getByText('Navigation')).toBeTruthy()
  expect(screen.getByTestId('connection-page')).toBeTruthy()
  expect(screen.queryByRole('textbox', { name: 'draft' })).toBeNull()
})
it('restores the mounted draft after reconnecting without reloading', async () => {
  mount()
  await disconnect()
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"siteTitle":"Portal"}')))
  fireEvent.click(screen.getByRole('button', { name: 'connection.retry' }))
  await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
  expect((screen.getByRole('textbox', { name: 'draft' }) as HTMLInputElement).value).toBe('unsaved')
  expect(auth.refresh).toHaveBeenCalled()
})
it('shows an in-content error when the initial session check cannot connect', async () => {
  auth.loading = true
  mount()
  await disconnect()
  expect(screen.getByTestId('connection-page')).toBeTruthy()
  expect(screen.getByText('Navigation')).toBeTruthy()
})
it('mounts destination content after a successful retry', async () => {
  mount()
  await disconnect()
  fireEvent.click(screen.getByText('Next page'))
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"siteTitle":"Portal"}')))
  fireEvent.click(screen.getByRole('button', { name: 'connection.retry' }))
  await waitFor(() => expect(screen.queryByTestId('connection-page')).toBeNull())
  expect(screen.getByText('/next')).toBeTruthy()
})
it('keeps the notice and reading surface when retry fails', async () => {
  mount()
  await disconnect()
  fireEvent.click(screen.getByRole('button', { name: 'connection.retry' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'connection.retry' }).hasAttribute('disabled')).toBe(false))
  expect(screen.getByRole('alert')).toBeTruthy()
  expect(screen.getByRole('textbox', { name: 'draft' })).toBeTruthy()
})
it('recovers automatically when the browser comes back online', async () => {
  mount()
  await disconnect()
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"siteTitle":"Portal"}')))
  act(() => { window.dispatchEvent(new Event('online')) })
  await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
  expect(screen.getByRole('textbox', { name: 'draft' })).toBeTruthy()
})
