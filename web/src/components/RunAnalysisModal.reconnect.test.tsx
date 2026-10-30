import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import RunAnalysisModal from './RunAnalysisModal'

const get = vi.hoisted(() => vi.fn())
vi.mock('../api/client', () => ({ api: { get, post: vi.fn(), upload: vi.fn() }, errText: (e: unknown) => String((e as Error).message) }))
vi.mock('../auth', () => ({ useAuth: () => ({ email: '', mailEnabled: false, admin: true, user: 'alice' }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('react-router', () => ({ useNavigate: () => vi.fn() }))
vi.mock('../lib/prefetch', () => ({ readPrefetched: () => undefined, prefetch: () => Promise.resolve() }))

const lost = () => Object.assign(new Error('network_unavailable'), { code: 'network_unavailable' })
const targets = { targets: [{ id: 1, name: 'Daily', plugin_slug: 'dify', created_at: '', mode: 'workflow', inputs: [] }] }
const presets = { presets: [], default_mode: 'now' }
const restore = () => act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
const mount = (open = true) => <App><RunAnalysisModal open={open} onClose={() => {}} /></App>

describe('RunAnalysisModal re-fetch after the connection returns', () => {
  let targetAnswers: Array<() => Promise<unknown>>
  beforeEach(() => {
    get.mockReset().mockImplementation((url: string) => {
      if (url.endsWith('/targets')) return (targetAnswers.shift() ?? (() => Promise.resolve(targets)))()
      if (url.endsWith('/presets')) return Promise.resolve(presets)
      return Promise.resolve({})
    })
  })

  it('stays loading (no empty-picker notice) while the re-fetch cannot connect, then opens the form', async () => {
    targetAnswers = [() => Promise.reject(lost()), () => Promise.reject(lost())]
    render(mount())
    await act(async () => { await Promise.resolve() })
    await restore()
    await act(async () => { await Promise.resolve() })
    expect(screen.queryByText('run.noTargets')).toBeNull()
    expect(screen.getByText('run.loading')).toBeTruthy()
    await restore()
    expect(await screen.findByText('run.workflow')).toBeTruthy()
    expect(screen.queryByText('run.noTargets')).toBeNull()
  })

  it('reports a re-fetch failure that is not the connection', async () => {
    targetAnswers = [() => Promise.reject(lost()), () => Promise.reject(Object.assign(new Error('boom'), { code: 'internal' }))]
    render(mount())
    await act(async () => { await Promise.resolve() })
    await restore()
    expect(await screen.findByText('boom')).toBeTruthy()
    expect(screen.queryByText('run.noTargets')).toBeNull()
  })

  it('does not re-fetch for a dialog that was closed in the meantime', async () => {
    targetAnswers = [() => Promise.reject(lost())]
    const view = render(mount())
    await act(async () => { await Promise.resolve() })
    view.rerender(mount(false))
    const before = get.mock.calls.filter(([u]) => String(u).endsWith('/targets')).length
    await restore()
    await waitFor(() => expect(get.mock.calls.filter(([u]) => String(u).endsWith('/targets')).length).toBe(before))
  })
})
