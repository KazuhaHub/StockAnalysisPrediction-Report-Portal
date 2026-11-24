import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { resetCatalogueHint, useUpdateHint } from './useUpdateHint'

const mock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: { get: mock.get, post: mock.post } }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: 'admin' }) }))

const root = '/api/admin/app-platform'
const sha = (c: string) => c.repeat(64)
const notes = (versions: string[]) => ({ publisher_id: 'kazuha', app_id: 'notes', publisher_trusted: true, versions: versions.map((version, i) => ({ version, descriptor_sha256: sha('abcdef'[i])})) })
const catalogue = (apps: unknown[]) => ({ catalogue_sha256: sha('9'), publisher_verified: false, apps, releases: [] })
const app = { publisherId: 'kazuha', appId: 'notes', version: '1.0.0' }

function Probe({ name, of = app }: { name: string; of?: Parameters<typeof useUpdateHint>[0] }) {
  return <output data-testid={name}>{useUpdateHint(of) ?? 'none'}</output>
}
const catalogueReads = () => mock.post.mock.calls.filter(([url]) => url === `${root}/market/catalogue`).length

beforeEach(() => {
  resetCatalogueHint()
  mock.get.mockReset(); mock.post.mockReset()
  mock.get.mockResolvedValue({ official: { name: 'Hub', url: 'https://market.example/index.json', enabled: true } })
  mock.post.mockResolvedValue(catalogue([notes(['1.1.0', '1.0.0'])]))
})
afterEach(cleanup)

describe('the update hint', () => {
  it('names the newest newer release of the same publisher and app', async () => {
    render(<Probe name="a" />)
    await waitFor(() => expect(screen.getByTestId('a').textContent).toBe('1.1.0'))
  })

  it('has nothing to say when nothing is newer, the app is not listed, or the source is off', async () => {
    mock.post.mockResolvedValue(catalogue([notes(['1.0.0'])]))
    render(<Probe name="a" />)
    await waitFor(() => expect(catalogueReads()).toBe(1))
    expect(screen.getByTestId('a').textContent).toBe('none')
    cleanup(); resetCatalogueHint(); mock.post.mockClear()
    mock.post.mockResolvedValue(catalogue([{ ...notes(['2.0.0']), publisher_id: 'stranger' }]))
    render(<Probe name="a" />)
    await waitFor(() => expect(catalogueReads()).toBe(1))
    expect(screen.getByTestId('a').textContent).toBe('none')
    cleanup(); resetCatalogueHint(); mock.post.mockClear()
    mock.get.mockResolvedValue({ official: { name: 'Hub', url: 'https://market.example/index.json', enabled: false } })
    render(<Probe name="a" />)
    await waitFor(() => expect(mock.get).toHaveBeenCalled())
    expect(catalogueReads()).toBe(0)
    expect(screen.getByTestId('a').textContent).toBe('none')
  })

  it('shows nothing and raises nothing when the catalogue cannot be read', async () => {
    mock.post.mockRejectedValue(new Error('catalogue down'))
    const unhandled = vi.fn()
    process.on('unhandledRejection', unhandled)
    render(<Probe name="a" />)
    await waitFor(() => expect(catalogueReads()).toBe(1))
    expect(screen.getByTestId('a').textContent).toBe('none')
    process.off('unhandledRejection', unhandled)
    expect(unhandled).not.toHaveBeenCalled()
  })

  it('reads the catalogue once for every card and page that asks', async () => {
    render(<><Probe name="a" /><Probe name="b" /><Probe name="c" of={{ publisherId: 'kazuha', appId: 'notes', version: '0.9.0' }} /></>)
    await waitFor(() => expect(screen.getByTestId('c').textContent).toBe('1.1.0'))
    expect(catalogueReads()).toBe(1)
    expect(mock.get).toHaveBeenCalledTimes(1)
    cleanup()
    // A page opened later in the same session reuses it.
    render(<Probe name="d" />)
    await waitFor(() => expect(screen.getByTestId('d').textContent).toBe('1.1.0'))
    expect(catalogueReads()).toBe(1)
  })

  it('asks for nothing for an app with no publisher, id or version (a built-in app)', async () => {
    render(<Probe name="a" of={{ appId: 'batch' }} />)
    expect(screen.getByTestId('a').textContent).toBe('none')
    expect(mock.get).not.toHaveBeenCalled()
  })
})
