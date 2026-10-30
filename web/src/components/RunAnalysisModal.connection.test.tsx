import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import RunAnalysisModal from './RunAnalysisModal'

const get = vi.hoisted(() => vi.fn())
vi.mock('../api/client', () => ({ api: { get, post: vi.fn(), upload: vi.fn() }, errText: (e: unknown) => String(e) }))
vi.mock('../auth', () => ({ useAuth: () => ({ email: '', mailEnabled: false, admin: true, user: 'alice' }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('react-router', () => ({ useNavigate: () => vi.fn() }))

const targets = { targets: [{ id: 1, name: 'Daily', plugin_slug: 'dify', created_at: '', mode: 'workflow', inputs: [] }] }
const presets = { presets: [], default_mode: 'now', default_retries: 1 }
vi.mock('../lib/prefetch', () => ({
  readPrefetched: (url: string) => (url.endsWith('/targets') ? targets : presets),
  prefetch: () => Promise.resolve(),
}))

const lost = () => Object.assign(new Error('network_unavailable'), { code: 'network_unavailable' })

describe('RunAnalysisModal reconnect', () => {
  it('re-fetches the failed list without resetting what the user chose in the open dialog', async () => {
    let targetCalls = 0
    get.mockReset().mockImplementation((url: string) => {
      if (url.endsWith('/targets')) { targetCalls += 1; return targetCalls === 1 ? Promise.reject(lost()) : Promise.resolve(JSON.parse(JSON.stringify(targets))) }
      if (url.endsWith('/presets')) return Promise.resolve(presets)
      return Promise.resolve({})
    })
    render(<App><RunAnalysisModal open onClose={() => {}} /></App>)
    await screen.findByText('run.workflow')
    await act(async () => { await Promise.resolve() })
    const retries = document.body.querySelector('.ant-input-number input') as HTMLInputElement
    fireEvent.change(retries, { target: { value: '4' } })
    expect(retries.value).toBe('4')
    await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
    await waitFor(() => expect(targetCalls).toBe(2))
    await act(async () => { await Promise.resolve() })
    expect((document.body.querySelector('.ant-input-number input') as HTMLInputElement).value).toBe('4')
  })
})
