import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render as renderPage, screen, waitFor } from '@testing-library/react'
import { ConfigProvider } from 'antd'
import userEvent from '@testing-library/user-event'
import AppPlatformRuntimePanel from './AppPlatformRuntimePanel'

const mock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), download: vi.fn(), notify: vi.fn() }))
vi.mock('../../api/client', () => ({ api: { get: mock.get, post: mock.post }, errText: (e: unknown) => e instanceof Error ? e.message : String(e) }))
vi.mock('../../lib/download', () => ({ downloadText: mock.download }))
vi.mock('../../lib/notify', () => ({ notifyError: mock.notify }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

const root = '/api/admin/app-platform'
const deployment = '11111111-2222-4333-8444-555555555555'
const runtime = { runtime_id: '99999999-2222-4333-8444-555555555555', deployment_id: deployment, controller_epoch: 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee', verification_key_fingerprint: 'f'.repeat(64) }
const enrollment = '{\n  "format": "portal.app-runner.bootstrap/1-dev.1"\n}\n'
const render = (page: React.ReactNode) => renderPage(page, { wrapper: ({ children }) => <ConfigProvider theme={{ token: { motion: false } }}>{children}</ConfigProvider> })

beforeEach(() => { mock.get.mockReset(); mock.post.mockReset(); mock.download.mockReset(); mock.notify.mockReset() })
afterEach(cleanup)

describe('runtime enrollment card', () => {
  it('asks for initialization first and offers no runtime action', async () => {
    mock.get.mockResolvedValue({ state: 'uninitialized', endpoint_configured: false, runtime: null })
    render(<AppPlatformRuntimePanel />)
    await screen.findByText('appRuntime.hint.uninitialized')
    expect(screen.queryByRole('button', { name: 'appRuntime.prepare' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'appRuntime.confirm' })).toBeNull()
  })

  it('prepares the binding and hands out the document with the host-setup command', async () => {
    mock.get.mockResolvedValueOnce({ state: 'none', endpoint_configured: false, runtime: null })
      .mockResolvedValue({ state: 'pending', endpoint_configured: false, runtime: { ...runtime, enabled: false } })
    mock.post.mockResolvedValue({ ...runtime, enabled: false, enrollment })
    const user = userEvent.setup()
    render(<AppPlatformRuntimePanel />)
    await user.click(await screen.findByRole('button', { name: 'appRuntime.prepare' }))
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(`${root}/control/runtime`, { action: 'prepare' }))
    expect(await screen.findByText(/portal\.app-runner\.bootstrap/)).toBeTruthy()
    expect(screen.getByText(/host-setup --apply --enrollment/)).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'appRuntime.download' }))
    expect(mock.download).toHaveBeenCalledWith('portal-runner-enrollment.json', enrollment, 'application/json')
    await screen.findByTitle(deployment)
  })

  it('keeps confirm unavailable until Portal has its runner endpoint', async () => {
    mock.get.mockResolvedValue({ state: 'pending', endpoint_configured: false, runtime: { ...runtime, enabled: false } })
    render(<AppPlatformRuntimePanel />)
    const confirm = await screen.findByRole('button', { name: 'appRuntime.confirm' })
    expect(confirm).toHaveProperty('disabled', true)
    expect(screen.getByText('appRuntime.hint.endpointMissing')).toBeTruthy()
    // The document can be fetched again while the binding is pending.
    expect(screen.getByRole('button', { name: 'appRuntime.showDocument' })).toHaveProperty('disabled', false)
  })

  it('confirms only after the confirmation and shows what the runner reported', async () => {
    mock.get.mockResolvedValueOnce({ state: 'pending', endpoint_configured: true, runtime: { ...runtime, enabled: false } })
      .mockResolvedValue({ state: 'confirmed', endpoint_configured: true, runtime: { ...runtime, enabled: true } })
    mock.post.mockResolvedValue({ ...runtime, enabled: true, observed: { deployment_id: deployment, controller_epoch: runtime.controller_epoch, control_revision: '1', verification_key_fingerprint: runtime.verification_key_fingerprint } })
    const user = userEvent.setup()
    render(<AppPlatformRuntimePanel />)
    await user.click(await screen.findByRole('button', { name: 'appRuntime.confirm' }))
    expect(mock.post).not.toHaveBeenCalled()
    await user.click(await screen.findByRole('button', { name: 'appRuntime.confirmAction' }))
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(`${root}/control/runtime`, { action: 'confirm', deployment_id: deployment }))
    await screen.findByText('appRuntime.observed')
    await screen.findByText('appRuntime.state.confirmed')
    expect(screen.queryByRole('button', { name: 'appRuntime.confirm' })).toBeNull()
  })

  it('reports a failed confirm and leaves the binding pending', async () => {
    mock.get.mockResolvedValue({ state: 'pending', endpoint_configured: true, runtime: { ...runtime, enabled: false } })
    mock.post.mockRejectedValue(new Error('not observed'))
    const user = userEvent.setup()
    render(<AppPlatformRuntimePanel />)
    await user.click(await screen.findByRole('button', { name: 'appRuntime.confirm' }))
    await user.click(await screen.findByRole('button', { name: 'appRuntime.confirmAction' }))
    await waitFor(() => expect(mock.notify).toHaveBeenCalledWith('not observed'))
    expect(screen.getByText('appRuntime.state.pending')).toBeTruthy()
  })

  it('names the recovery and conflict states without offering an action', async () => {
    for (const state of ['paused', 'conflict']) {
      mock.get.mockResolvedValue({ state, endpoint_configured: true, runtime: null })
      render(<AppPlatformRuntimePanel />)
      await screen.findByText(`appRuntime.hint.${state}`)
      expect(screen.queryByRole('button', { name: 'appRuntime.prepare' })).toBeNull()
      expect(screen.queryByRole('button', { name: 'appRuntime.confirm' })).toBeNull()
      cleanup()
    }
  })
})
