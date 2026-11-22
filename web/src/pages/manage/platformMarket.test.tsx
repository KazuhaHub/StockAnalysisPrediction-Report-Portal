import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { loadInstalledRelease, usePlatformMarket } from './platformMarket'
import type { PlatformMarketApp } from '../../api/appPlatform'

const mock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), postBytes: vi.fn(), user: 'admin' }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: { get: mock.get, post: mock.post, postBytes: mock.postBytes }, errText: (e: unknown) => e instanceof Error ? e.message : String(e) }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: mock.user }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

const root = '/api/admin/app-platform'
const url = 'https://market.example/index.json'
const digest = 'a'.repeat(64)
const app: PlatformMarketApp = { publisher_id: 'publisher', app_id: 'notes', versions: [{ version: '1.0.0', descriptor_sha256: digest }], publisher_trusted: true }
const hints = (id: string) => [{ app_id: id, publisher_id: 'publisher', version: '1.0.0', descriptor_sha256: digest, descriptor_url: 'https://market.example/d.json', signature_url: 'https://market.example/d.sig' }]
const catalogueFor = (id: string) => ({ catalogue_sha256: 'b'.repeat(64), publisher_verified: false, releases: hints(id), apps: [{ publisher_id: 'publisher', app_id: id, versions: [{ version: '1.0.0', descriptor_sha256: digest }], publisher_trusted: true }] })
const verified = { release_sha256: digest, app_id: 'notes', publisher_id: 'publisher', version: '1.0.0', trust_revision: '1', installable: true }
const deferred = <T,>() => { let resolve!: (v: T) => void; let reject!: (e: unknown) => void; const promise = new Promise<T>((a, b) => { resolve = a; reject = b }); return { promise, resolve, reject } }

beforeEach(() => {
  mock.user = 'admin'
  for (const m of [mock.get, mock.post, mock.postBytes]) m.mockReset()
  mock.get.mockImplementation(async (path: string) => path === `${root}/market/sources` ? { official: { name: 'Hub', url, enabled: true } } : { items: [verified], next_cursor: '' })
  mock.post.mockImplementation(async (path: string) => path.endsWith('/catalogue') ? catalogueFor('notes') : { release_sha256: digest })
})
afterEach(cleanup)

describe('downloading and verifying a catalogue version', () => {
  it('reads the verified release by exact digest', async () => {
    const view = renderHook(() => usePlatformMarket())
    await waitFor(() => expect(view.result.current.catalogue?.status).toBe('ready'))
    let result: unknown
    await act(async () => { result = await view.result.current.stage(app, '1.0.0') })
    expect(result).toEqual({ release: verified })
    expect(mock.post).toHaveBeenCalledWith(`${root}/market/stage`, { catalogue_url: url, release_sha256: digest })
    expect(mock.get).toHaveBeenCalledWith(`${root}/releases?release_sha256=${digest}`)
  })

  it('does not publish a late staging response to a replacement account', async () => {
    const view = renderHook(() => usePlatformMarket())
    await waitFor(() => expect(view.result.current.catalogue?.status).toBe('ready'))
    const late = deferred<{ release_sha256: string }>()
    mock.post.mockImplementation(async (path: string) => path.endsWith('/stage') ? late.promise : catalogueFor('notes'))
    let pending!: Promise<unknown>
    act(() => { pending = view.result.current.stage(app, '1.0.0') })
    await waitFor(() => expect(view.result.current.staging).toEqual({ appId: 'notes', version: '1.0.0' }))
    mock.user = 'other'
    view.rerender()
    await act(async () => { late.resolve({ release_sha256: digest }); expect(await pending).toBeNull() })
    expect(view.result.current.staging).toBeNull()
    expect(view.result.current.stageError).toBe('')
    expect(mock.get.mock.calls.some(([path]) => String(path).includes('release_sha256='))).toBe(false)
  })

  it('does not report a late failure to a replacement account either', async () => {
    const view = renderHook(() => usePlatformMarket())
    await waitFor(() => expect(view.result.current.catalogue?.status).toBe('ready'))
    const late = deferred<never>()
    mock.post.mockImplementation(async (path: string) => path.endsWith('/stage') ? late.promise : catalogueFor('notes'))
    let pending!: Promise<unknown>
    act(() => { pending = view.result.current.stage(app, '1.0.0') })
    mock.user = 'other'
    view.rerender()
    await act(async () => { late.reject(new Error('boom')); expect(await pending).toBeNull() })
    expect(view.result.current.stageError).toBe('')
  })

  it('retries an uncertain download with the same source and digest, and never installs', async () => {
    const view = renderHook(() => usePlatformMarket())
    await waitFor(() => expect(view.result.current.catalogue?.status).toBe('ready'))
    let attempts = 0
    mock.post.mockImplementation(async (path: string) => {
      if (path.endsWith('/catalogue')) return catalogueFor('notes')
      if (++attempts === 1) throw new Error('offline')
      return { release_sha256: digest }
    })
    let first: unknown, second: unknown
    await act(async () => { first = await view.result.current.stage(app, '1.0.0') })
    expect(first).toEqual({ error: 'offline' })
    expect(view.result.current.staging).toBeNull()
    await act(async () => { second = await view.result.current.stage(app, '1.0.0') })
    expect(second).toEqual({ release: verified })
    const stages = mock.post.mock.calls.filter(([path]) => path.endsWith('/stage'))
    expect(stages).toHaveLength(2)
    expect(stages[0][1]).toEqual(stages[1][1])
    expect(mock.post.mock.calls.some(([path]) => path.endsWith('/operations') || path.endsWith('/plans'))).toBe(false)
  })

  it('refuses a verified release that is not the one asked for', async () => {
    const view = renderHook(() => usePlatformMarket())
    await waitFor(() => expect(view.result.current.catalogue?.status).toBe('ready'))
    mock.get.mockImplementation(async (path: string) => path === `${root}/market/sources` ? { official: { name: 'Hub', url, enabled: true } } : { items: [{ ...verified, version: '9.9.9' }], next_cursor: '' })
    let result: unknown
    await act(async () => { result = await view.result.current.stage(app, '1.0.0') })
    expect(result).toEqual({ error: 'appMarket.verificationChanged' })
  })

  it('does one download at a time', async () => {
    const view = renderHook(() => usePlatformMarket())
    await waitFor(() => expect(view.result.current.catalogue?.status).toBe('ready'))
    const slow = deferred<{ release_sha256: string }>()
    mock.post.mockImplementation(async (path: string) => path.endsWith('/stage') ? slow.promise : catalogueFor('notes'))
    let first!: Promise<unknown>
    act(() => { first = view.result.current.stage(app, '1.0.0') })
    let second: unknown = 'unset'
    await act(async () => { second = await view.result.current.stage(app, '1.0.0') })
    expect(second).toBeNull()
    await act(async () => { slow.resolve({ release_sha256: digest }); await first })
    expect(mock.post.mock.calls.filter(([path]) => path.endsWith('/stage'))).toHaveLength(1)
  })
})

describe('reading a catalogue', () => {
  it('does not reuse a previous account’s response after switching away and back', async () => {
    const stale = deferred<unknown>()
    let reads = 0
    mock.post.mockImplementation(async (path: string) => path.endsWith('/catalogue') ? (++reads === 1 ? stale.promise : catalogueFor('fresh')) : { release_sha256: digest })
    const view = renderHook(() => usePlatformMarket())
    await waitFor(() => expect(reads).toBe(1))
    mock.user = 'other'
    view.rerender()
    mock.user = 'admin'
    view.rerender()
    await waitFor(() => expect(view.result.current.catalogue?.apps[0]?.app_id).toBe('fresh'))
    await act(async () => { stale.resolve(catalogueFor('stale')) })
    expect(view.result.current.catalogue?.apps.map(a => a.app_id)).toEqual(['fresh'])
  })

  it('does not report a previous account’s failed read', async () => {
    const stale = deferred<unknown>()
    let reads = 0
    mock.post.mockImplementation(async (path: string) => path.endsWith('/catalogue') ? (++reads === 1 ? stale.promise : catalogueFor('fresh')) : { release_sha256: digest })
    const view = renderHook(() => usePlatformMarket())
    await waitFor(() => expect(reads).toBe(1))
    mock.user = 'other'
    view.rerender()
    await waitFor(() => expect(view.result.current.catalogue?.status).toBe('ready'))
    await act(async () => { stale.reject(new Error('late failure')) })
    expect(view.result.current.catalogue?.status).toBe('ready')
  })

  it('reads the sources again when the page shows again', async () => {
    let enabled = true
    mock.get.mockImplementation(async (path: string) => path === `${root}/market/sources` ? { official: { name: 'Hub', url, enabled } } : { items: [], next_cursor: '' })
    const view = renderHook(({ visible }) => usePlatformMarket(visible), { initialProps: { visible: true } })
    await waitFor(() => expect(view.result.current.sources).toHaveLength(1))
    view.rerender({ visible: false })
    enabled = false
    view.rerender({ visible: true })
    await waitFor(() => expect(view.result.current.sources).toHaveLength(0))
  })
})

describe('staging an offline package', () => {
  it('does not publish a late response to a replacement account', async () => {
    const view = renderHook(() => usePlatformMarket())
    await waitFor(() => expect(view.result.current.catalogue?.status).toBe('ready'))
    const late = deferred<{ release_sha256: string }>()
    mock.postBytes.mockReturnValue(late.promise)
    let pending!: Promise<unknown>
    act(() => { pending = view.result.current.stageOffline(new Blob(['zip'])) })
    await waitFor(() => expect(view.result.current.uploading).toBe(true))
    mock.user = 'other'
    view.rerender()
    await act(async () => { late.resolve({ release_sha256: digest }); expect(await pending).toBeNull() })
    expect(view.result.current.uploading).toBe(false)
    expect(mock.get.mock.calls.some(([path]) => String(path).includes('release_sha256='))).toBe(false)
  })

  it('refuses a response that is not a release digest', async () => {
    const view = renderHook(() => usePlatformMarket())
    await waitFor(() => expect(view.result.current.catalogue?.status).toBe('ready'))
    mock.postBytes.mockResolvedValue({ release_sha256: 'nope' })
    let result: unknown
    await act(async () => { result = await view.result.current.stageOffline(new Blob(['zip'])) })
    expect(result).toEqual({ error: 'appMarket.verificationChanged' })
  })
})

describe('reading an installation’s current release', () => {
  it('fails with a translated message when there is none', async () => {
    mock.get.mockResolvedValue({})
    await expect(loadInstalledRelease('33333333-3333-4333-8333-333333333333', key => key)).rejects.toThrow('appStore.releaseMissing')
  })
})
