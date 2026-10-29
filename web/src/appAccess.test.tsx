import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { AppAccessProvider, RequireAppOpen, useAppAccess } from './appAccess'
import { shortcutAppKey, shortcutOfUrl } from './lib/shortcuts'

// Access to an app is decided per OU on the server (ADR 0048). The server cannot gate a built-in
// app's own API, so the SPA is the gate for those: the route guard, the hub and the entry buttons all
// read this one answer.

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('./api/client', () => ({ api: apiMock }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const answer = { apps: [{ id: 'hello', name: 'Hello' }], builtin_apps: ['batch'], order: ['builtin:batch', 'hello'] }

beforeEach(() => {
  apiMock.get.mockReset()
  apiMock.get.mockResolvedValue(answer)
})

function Probe({ keys }: { keys: string[] }) {
  const a = useAppAccess()
  return <div>{a.ready ? keys.map((k) => `${k}=${a.isOpen(k)}`).join(',') : 'loading'}</div>
}

describe('useAppAccess', () => {
  it('is open for everything without a provider, so a bare page is unrestricted', () => {
    render(<Probe keys={['builtin:quotes']} />)
    expect(screen.getByText('builtin:quotes=true')).toBeTruthy()
  })

  it('opens exactly the keys the server listed', async () => {
    render(
      <AppAccessProvider user="rita">
        <Probe keys={['builtin:batch', 'builtin:quotes', 'hello', 'gone']} />
      </AppAccessProvider>,
    )
    expect(await screen.findByText('builtin:batch=true,builtin:quotes=false,hello=true,gone=false')).toBeTruthy()
    expect(apiMock.get).toHaveBeenCalledWith('/api/apps')
  })

  it('fails open when the list cannot be read: the server still gates installed apps', async () => {
    apiMock.get.mockRejectedValue(new Error('down'))
    render(
      <AppAccessProvider user="rita">
        <Probe keys={['builtin:quotes']} />
      </AppAccessProvider>,
    )
    expect(await screen.findByText('builtin:quotes=true')).toBeTruthy()
  })
})

describe('RequireAppOpen', () => {
  const guarded = (appKey: string) => (
    <MemoryRouter>
      <AppAccessProvider user="rita">
        <RequireAppOpen appKey={appKey}>
          <div>the console</div>
        </RequireAppOpen>
      </AppAccessProvider>
    </MemoryRouter>
  )

  it('renders the route for an open built-in app', async () => {
    render(guarded('builtin:batch'))
    expect(await screen.findByText('the console')).toBeTruthy()
  })

  it('shows a not-available result instead of a closed built-in app', async () => {
    render(guarded('builtin:quotes'))
    expect(await screen.findByText('apps.unavailable')).toBeTruthy()
    expect(screen.queryByText('the console')).toBeNull()
  })

  it('does not flash the route while the answer is on its way', async () => {
    let resolve: (v: unknown) => void = () => {}
    apiMock.get.mockReturnValue(new Promise((r) => (resolve = r)))
    render(guarded('builtin:quotes'))
    expect(screen.queryByText('the console')).toBeNull()
    expect(screen.queryByText('apps.unavailable')).toBeNull()
    resolve(answer)
    await waitFor(() => expect(screen.getByText('apps.unavailable')).toBeTruthy())
  })
})

describe('shortcutAppKey', () => {
  const key = (url: string) => shortcutAppKey(shortcutOfUrl(url)!)

  it('names the app an apps entry button is pinned to', () => {
    expect(key('rp:apps:builtin:quotes')).toBe('builtin:quotes')
    expect(key('rp:apps:hello')).toBe('hello')
  })

  it('has none for the hub itself or any other shortcut', () => {
    expect(key('rp:apps')).toBeUndefined()
    expect(key('rp:queue')).toBeUndefined()
  })
})
