import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConfigProvider, theme } from 'antd'
import axe from 'axe-core'
import AppPlatformMarketPanel from './AppPlatformMarketPanel'

const mock = vi.hoisted(() => ({ post: vi.fn(), get: vi.fn(), user: 'admin', t: (key: string) => key }))
vi.mock('../../api/client', () => ({ api: { post: mock.post, get: mock.get }, errText: (e: unknown) => e instanceof Error ? e.message : String(e) }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: mock.user }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: mock.t }) }))
const digest = 'a'.repeat(64)
const catalogue = 'https://market.example/index.json'
const entry = { app_id: 'notes', publisher_id: 'publisher', version: '1.0.0', descriptor_sha256: digest, descriptor_url: 'https://market.example/app.json', signature_url: 'https://market.example/app.sig' }
const discovery = { catalogue_sha256: 'b'.repeat(64), releases: [entry], publisher_verified: false }
const verified = {
  app_id: 'notes',
  publisher_id: 'publisher',
  version: '1.0.0',
  release_sha256: digest,
  trust_revision: '1',
  installable: true,
  compatibility: { platform_api: 'portal.app-platform/1', runner_protocol: 'portal.runner/1' },
  platforms: ['linux/amd64', 'linux/arm64'],
  required_capabilities: ['reports.read'],
  optional_capabilities: ['notifications.publish'],
  resources: { memory_mib: 256, cpu_millis: 500, pids: 128 },
  data_schema: 1,
  backend: true,
}
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
    expect(screen.getByText('portal.app-platform/1 · portal.runner/1')).toBeTruthy()
    expect(screen.getByText('reports.read')).toBeTruthy()
    expect(screen.getByText('notifications.publish')).toBeTruthy()
    expect(screen.getByText('appPlatform.resourceSummary')).toBeTruthy()
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
    // Only the mount-time read of the catalogue sources; nothing for the late staging response.
    expect(mock.get.mock.calls.every(([url]) => String(url).endsWith('/market/sources'))).toBe(true)
    expect(ready).not.toHaveBeenCalled()
  })

  it('discovers from the catalogue input with the keyboard', async () => {
    const user = userEvent.setup()
    render(<AppPlatformMarketPanel disabled={false} onReady={() => {}} onBusyChange={() => {}} />)
    const input = screen.getByRole('textbox', { name: 'appMarket.catalogueURL' })
    await user.type(input, `${catalogue}{Enter}`)
    expect(await screen.findByText('notes')).toBeTruthy()
  })

  it.each([
    ['light', theme.defaultAlgorithm],
    ['dark', theme.darkAlgorithm],
  ])('has no axe violations in %s mode', async (_mode, algorithm) => {
    render(
      <ConfigProvider theme={{ algorithm }}>
        <AppPlatformMarketPanel disabled={false} onReady={() => {}} onBusyChange={() => {}} />
      </ConfigProvider>,
    )
    await discover()
    const result = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
        region: { enabled: false },
      },
    })
    expect(result.violations).toEqual([])
  })

  it('browses the built-in official catalogue without typing a URL', async () => {
    const official = 'https://raw.githubusercontent.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/catalogue/catalogue.json'
    mock.get.mockImplementation((url: string) => Promise.resolve(url.endsWith('/market/sources') ? { official: { name: 'Kazuha Hub', url: official, enabled: true } } : { items: [verified], next_cursor: '' }))
    const user = userEvent.setup()
    render(<AppPlatformMarketPanel disabled={false} onReady={() => {}} onBusyChange={() => {}} />)
    expect(await screen.findByText('Kazuha Hub')).toBeTruthy()
    expect(screen.getByText('appMarket.builtin')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'appMarket.browseOfficial' }))
    expect(mock.post).toHaveBeenCalledWith(`${root}/market/catalogue`, { catalogue_url: official })
    await screen.findByText('notes')
    await user.click(screen.getByRole('button', { name: 'appMarket.verifyDownload' }))
    expect(mock.post).toHaveBeenCalledWith(`${root}/market/stage`, { catalogue_url: official, release_sha256: digest })
  })

  it('disables and re-enables the official catalogue explicitly', async () => {
    let enabled = true
    const official = 'https://raw.githubusercontent.com/x/y/catalogue/catalogue.json'
    mock.get.mockImplementation((url: string) => Promise.resolve(url.endsWith('/market/sources') ? { official: { name: 'Kazuha Hub', url: official, enabled } } : { items: [], next_cursor: '' }))
    mock.post.mockImplementation((url: string, body: { official_enabled?: boolean }) => { if (url.endsWith('/market/sources')) enabled = !!body.official_enabled; return Promise.resolve({ official_enabled: enabled }) })
    const user = userEvent.setup()
    render(<AppPlatformMarketPanel disabled={false} onReady={() => {}} onBusyChange={() => {}} />)
    await user.click(await screen.findByRole('switch', { name: 'appMarket.officialEnabled' }))
    expect(mock.post).toHaveBeenCalledWith(`${root}/market/sources`, { official_enabled: false })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'appMarket.browseOfficial' })).toBeNull())
    expect(screen.getByText('appMarket.officialDisabled')).toBeTruthy()
    await user.click(screen.getByRole('switch', { name: 'appMarket.officialEnabled' }))
    expect(mock.post).toHaveBeenLastCalledWith(`${root}/market/sources`, { official_enabled: true })
    expect(await screen.findByRole('button', { name: 'appMarket.browseOfficial' })).toBeTruthy()
  })

  describe('a package that downloads in the background', () => {
    const downloading = (done: string) => ({ state: 'downloading', release_sha256: digest, bytes_done: done, bytes_total: '2097152' })
    const stageCalls = () => mock.get.mock.calls.filter(([url]) => String(url).includes('/market/stage'))
    afterEach(() => { vi.useRealTimers() })
    async function startStage(ready: () => void) {
      render(<AppPlatformMarketPanel disabled={false} onReady={ready} onBusyChange={() => {}} />)
      await discover()
      vi.useFakeTimers({ toFake: ['setTimeout'] })
      await act(async () => { screen.getByRole('button', { name: 'appMarket.verifyDownload' }).click() })
    }
    const tick = () => act(async () => { await vi.advanceTimersByTimeAsync(2000) })

    it('shows progress and polls until the package is staged, then reads the verified release', async () => {
      const replies = [downloading('1048576'), { state: 'staged', release_sha256: digest }]
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/catalogue') ? discovery : downloading('0')))
      mock.get.mockImplementation((url: string) => Promise.resolve(String(url).includes('/market/stage') ? replies.shift() : { items: [verified], next_cursor: '' }))
      const ready = vi.fn()
      await startStage(ready)
      expect(screen.getByText('appMarket.downloading')).toBeTruthy()
      expect(screen.getByRole('progressbar').getAttribute('aria-valuenow')).toBe('0')
      await tick()
      expect(stageCalls()).toEqual([[`${root}/market/stage?release_sha256=${digest}`]])
      expect(screen.getByRole('progressbar').getAttribute('aria-valuenow')).toBe('50')
      expect(ready).not.toHaveBeenCalled()
      await tick()
      expect(ready).toHaveBeenCalledWith(verified)
      expect(screen.queryByRole('progressbar')).toBeNull()
      expect(mock.post.mock.calls.filter(([url]) => url.endsWith('/stage'))).toHaveLength(1)
    })

    it('shows the failure of the background download and installs nothing', async () => {
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/catalogue') ? discovery : downloading('0')))
      mock.get.mockResolvedValue({ state: 'failed', release_sha256: digest, error_code: 'app_platform_unavailable' })
      const ready = vi.fn()
      await startStage(ready)
      await tick()
      expect(screen.getByText('err.app_platform_unavailable')).toBeTruthy()
      expect(screen.queryByRole('progressbar')).toBeNull()
      expect(ready).not.toHaveBeenCalled()
    })

    it('keeps polling through a transient poll error', async () => {
      const replies: unknown[] = [Object.assign(new Error('bad gateway'), { status: 502 }), { state: 'staged', release_sha256: digest }]
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/catalogue') ? discovery : downloading('0')))
      mock.get.mockImplementation((url: string) => {
        if (!String(url).includes('/market/stage')) return Promise.resolve({ items: [verified], next_cursor: '' })
        const next = replies.shift()
        return next instanceof Error ? Promise.reject(next) : Promise.resolve(next)
      })
      const ready = vi.fn()
      await startStage(ready)
      await tick()
      expect(screen.queryByText('bad gateway')).toBeNull()
      await tick()
      await tick()
      expect(ready).toHaveBeenCalledWith(verified)
    })

    it('joins the download again once when the server no longer knows it', async () => {
      const unknown = Object.assign(new Error('gone'), { status: 404, code: 'app_platform_stage_unknown' })
      let starts = 0
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/catalogue') ? discovery : ++starts === 1 ? downloading('0') : { release_sha256: digest }))
      mock.get.mockImplementation((url: string) => String(url).includes('/market/stage') ? Promise.reject(unknown) : Promise.resolve({ items: [verified], next_cursor: '' }))
      const ready = vi.fn()
      await startStage(ready)
      await tick()
      expect(starts).toBe(2)
      expect(ready).toHaveBeenCalledWith(verified)
    })

    it('stops polling when the panel goes away', async () => {
      mock.post.mockImplementation((url: string) => Promise.resolve(url.endsWith('/catalogue') ? discovery : downloading('0')))
      mock.get.mockResolvedValue(downloading('10'))
      const ready = vi.fn()
      await startStage(ready)
      cleanup()
      await tick()
      await tick()
      expect(stageCalls()).toHaveLength(0)
      expect(ready).not.toHaveBeenCalled()
    })
  })
})
