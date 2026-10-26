import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import AppPlatformMarketPanel from './AppPlatformMarketPanel'

const mock = vi.hoisted(() => ({ post: vi.fn(), get: vi.fn(), user: 'admin', t: (key: string) => key }))
vi.mock('../../api/client', () => ({ api: { post: mock.post, get: mock.get }, errText: (e: unknown) => e instanceof Error ? e.message : String(e) }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: mock.user }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: mock.t }) }))
const digest = 'a'.repeat(64)
const catalogue = 'https://market.example/index.json'
const entry = { app_id: 'notes', publisher_id: 'publisher', version: '1.0.0', descriptor_sha256: digest, descriptor_url: 'https://market.example/app.json', signature_url: 'https://market.example/app.sig' }
const discovery = { catalogue_sha256: 'b'.repeat(64), releases: [entry], publisher_verified: false }
const verified = { app_id: 'notes', publisher_id: 'publisher', version: '1.0.0', release_sha256: digest, trust_revision: '1', installable: true }
const root = '/api/admin/app-platform'

beforeEach(() => {
  mock.user = 'admin'; mock.get.mockReset(); mock.post.mockReset()
  mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/catalogue') ? discovery : { release_sha256: digest }))
  mock.get.mockResolvedValue({ items: [verified], next_cursor: '' })
})
afterEach(cleanup)
async function discover() {
  const user = userEvent.setup()
  await user.type(screen.getByRole('textbox', { name: 'appMarket.catalogueURL' }), catalogue)
  await user.click(screen.getByRole('button', { name: 'appMarket.fetch' }))
  await screen.findByText('notes')
  return user
}

describe('remote application catalogue', () => {
  it('keeps discovery unverified, stages the chosen digest, then reads verified metadata', async () => {
    const ready = vi.fn()
    render(<AppPlatformMarketPanel disabled={false} onReady={ready} onBusyChange={() => {}} />)
    expect(mock.post).not.toHaveBeenCalled()
    const user = await discover()
    expect(await screen.findByText('appMarket.unverified')).toBeTruthy()
    expect(ready).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'appMarket.verifyDownload' }))
    expect(mock.post).toHaveBeenCalledWith(`${root}/market/stage`, { catalogue_url: catalogue, release_sha256: digest })
    expect(mock.get).toHaveBeenCalledWith(`${root}/releases?release_sha256=${digest}`)
    await screen.findByText('appMarket.ready')
    expect(ready).toHaveBeenCalledWith(verified)
    expect(mock.post.mock.calls.some(([url]) => url.endsWith('/operations') || url.endsWith('/plans'))).toBe(false)
  })

  it('does not upgrade an unverified hint when staging or verified metadata fails', async () => {
    const ready = vi.fn()
    mock.get.mockResolvedValue({ items: [], next_cursor: '' })
    render(<AppPlatformMarketPanel disabled={false} onReady={ready} onBusyChange={() => {}} />)
    const user = await discover()
    await user.click(screen.getByRole('button', { name: 'appMarket.verifyDownload' }))
    await screen.findByText('appMarket.verificationChanged')
    expect(ready).not.toHaveBeenCalled()
    expect(screen.queryByText('appMarket.ready')).toBeNull()
  })

  it('invalidates discovery if the catalogue URL is edited while it is loading', async () => {
    let resolve!: (value: typeof discovery) => void
    mock.post.mockReturnValue(new Promise(done => { resolve = done }))
    render(<AppPlatformMarketPanel disabled={false} onReady={() => {}} onBusyChange={() => {}} />)
    const user = userEvent.setup()
    const input = screen.getByRole('textbox', { name: 'appMarket.catalogueURL' })
    await user.type(input, catalogue)
    await user.click(screen.getByRole('button', { name: 'appMarket.fetch' }))
    await user.clear(input)
    await user.type(input, 'https://other.example/index.json')
    await act(async () => resolve(discovery))
    expect(screen.queryByText('notes')).toBeNull()
    expect(screen.queryByRole('button', { name: 'appMarket.verifyDownload' })).toBeNull()
  })

  it('retains an explicit failure rather than claiming an empty market', async () => {
    mock.post.mockRejectedValue(new Error('offline'))
    render(<AppPlatformMarketPanel disabled={false} onReady={() => {}} onBusyChange={() => {}} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: 'appMarket.catalogueURL' }), catalogue)
    await user.click(screen.getByRole('button', { name: 'appMarket.fetch' }))
    await screen.findByText('offline')
    expect(screen.queryByText('appMarket.empty')).toBeNull()
  })

  it('blocks fetch while the host has an installation confirmation in progress', () => {
    render(<AppPlatformMarketPanel disabled onReady={() => {}} onBusyChange={() => {}} />)
    expect(screen.getByRole('button', { name: 'appMarket.fetch' })).toHaveProperty('disabled', true)
    expect(mock.post).not.toHaveBeenCalled()
  })

  it('does not reuse a previous account response after switching away and back', async () => {
    let resolve!: (value: typeof discovery) => void
    mock.post.mockReturnValue(new Promise(done => { resolve = done }))
    const ready = vi.fn()
    const page = render(<AppPlatformMarketPanel disabled={false} onReady={ready} onBusyChange={() => {}} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: 'appMarket.catalogueURL' }), catalogue)
    await user.click(screen.getByRole('button', { name: 'appMarket.fetch' }))
    mock.user = 'other'
    page.rerender(<AppPlatformMarketPanel disabled={false} onReady={ready} onBusyChange={() => {}} />)
    mock.user = 'admin'
    page.rerender(<AppPlatformMarketPanel disabled={false} onReady={ready} onBusyChange={() => {}} />)
    await act(async () => resolve(discovery))
    expect(screen.queryByText('notes')).toBeNull()
    expect(ready).not.toHaveBeenCalled()
  })

  it('refuses a catalogue response that claims publisher verification', async () => {
    mock.post.mockResolvedValue({ ...discovery, publisher_verified: true })
    render(<AppPlatformMarketPanel disabled={false} onReady={() => {}} onBusyChange={() => {}} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: 'appMarket.catalogueURL' }), catalogue)
    await user.click(screen.getByRole('button', { name: 'appMarket.fetch' }))
    await screen.findByText('appMarket.invalidResponse')
    expect(screen.queryByRole('button', { name: 'appMarket.verifyDownload' })).toBeNull()
  })

  it('retries an uncertain download with the same source and digest without installing', async () => {
    let stages = 0
    mock.post.mockImplementation((url: string) => url.endsWith('/catalogue') ? Promise.resolve(discovery) : ++stages === 1 ? Promise.reject(new Error('connection lost')) : Promise.resolve({ release_sha256: digest }))
    const ready = vi.fn()
    render(<AppPlatformMarketPanel disabled={false} onReady={ready} onBusyChange={() => {}} />)
    const user = await discover()
    await user.click(screen.getByRole('button', { name: 'appMarket.verifyDownload' }))
    await screen.findByText('connection lost')
    // Ant retains its loading icon briefly while the exit animation settles;
    // wait for the actual retry control rather than matching its transient name.
    await waitFor(() => expect(screen.getByRole('button', { name: /appMarket.verifyDownload/ }).className).not.toContain('ant-btn-loading'))
    await user.click(screen.getByRole('button', { name: /appMarket.verifyDownload/ }))
    await screen.findByText('appMarket.ready')
    const writes = mock.post.mock.calls.filter(([url]) => url.endsWith('/stage'))
    expect(writes).toHaveLength(2)
    expect(writes[0][1]).toEqual(writes[1][1])
    expect(ready).toHaveBeenCalledTimes(1)
  })

  it('does not publish a late staging response to a replacement account', async () => {
    let resolve!: (value: { release_sha256: string }) => void
    mock.post.mockImplementation((url: string) => url.endsWith('/catalogue') ? Promise.resolve(discovery) : new Promise(done => { resolve = done }))
    const ready = vi.fn()
    const page = render(<AppPlatformMarketPanel disabled={false} onReady={ready} onBusyChange={() => {}} />)
    const user = await discover()
    await user.click(screen.getByRole('button', { name: 'appMarket.verifyDownload' }))
    mock.user = 'other'
    page.rerender(<AppPlatformMarketPanel disabled={false} onReady={ready} onBusyChange={() => {}} />)
    mock.user = 'admin'
    page.rerender(<AppPlatformMarketPanel disabled={false} onReady={ready} onBusyChange={() => {}} />)
    await act(async () => resolve({ release_sha256: digest }))
    expect(mock.get).not.toHaveBeenCalled()
    expect(ready).not.toHaveBeenCalled()
  })
})
