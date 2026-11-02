import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import AppPlatformSettingsPage from './AppPlatformSettingsPage'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
const translate = vi.hoisted(() => (key: string, options?: Record<string, unknown>) => options ? `${key}:${JSON.stringify(options)}` : key)
vi.mock('../../api/client', async original => ({
  ...await original<typeof import('../../api/client')>(),
  api: apiMock,
  errText: (failure: unknown) => failure instanceof Error ? failure.message : String(failure),
}))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: translate }),
}))

const installation = '11111111-1111-4111-8111-111111111111'
const release = 'a'.repeat(64)
const root = '/api/admin/app-platform'
const settings = {
  installation_id: installation,
  app_id: 'notes',
  publisher_id: 'publisher',
  desired_state: 'enabled',
  phase: 'ready',
  generation: '2',
  revision: '5',
  policy_revision: '3',
  current_release_sha256: release,
  version: '2.0.0',
  audience: [{ type: 'user', id: 'reader', name: 'Reader' }],
  capabilities: [
    { capability: 'storage.files', granted: true, required: true },
    { capability: 'network.http', granted: false, required: true },
  ],
  backend: { required: true, registered: true, enabled: true },
  data: { id: '22222222-2222-4222-8222-222222222222', state: 'active', format: 2, quota_bytes: '268435456', observed_bytes: '128', revision: '4' },
  recovery: { count: 1, state: 'verified', pending: false },
  configuration_gaps: ['capability:network.http'],
  repair_eligible: false,
}

function mount(path = `/manage/apps/installations/${installation}`) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/manage/apps/installations/:installation" element={<AppPlatformSettingsPage />} />
        <Route path="/manage/apps" element={<div>apps</div>} />
        <Route path="/apps/v2/:installation" element={<div>application</div>} />
        <Route path="/manage/apps/:key/credentials" element={<div>credentials</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  apiMock.get.mockReset()
  apiMock.post.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url.endsWith('/settings')) return settings
    if (url.endsWith('/secret-bindings')) {
      return { bindings: [{ id: 'binding', name: 'upstream-api', credential_label: 'Provider key', operation: 'http_bearer', state: 'active', revision: 2 }] }
    }
    throw new Error(`unexpected ${url}`)
  })
})
afterEach(cleanup)

describe('application installation settings', () => {
  it('shows the current package, audience, permissions, data, recovery and credential metadata', async () => {
    mount()
    expect(await screen.findByText('notes')).toBeTruthy()
    expect(screen.getByText('appPlatform.settings.title')).toBeTruthy()
    expect(screen.getByText('2.0.0')).toBeTruthy()
    expect(screen.getByText(release)).toBeTruthy()
    expect(screen.getByText(/appPlatform\.settings\.principal\.user:.*Reader/)).toBeTruthy()
    expect(screen.getByText(/appPlatform\.capability\.storage\.files/)).toBeTruthy()
    expect(screen.getByText(/appPlatform\.settings\.configurationNeeded/)).toBeTruthy()
    expect(screen.getByText(/appPlatform\.settings\.missingCapability/)).toBeTruthy()
    expect(screen.getByText(/appPlatform\.settings\.backendEnabled/)).toBeTruthy()
    expect(screen.getByText(/268,435,456/)).toBeTruthy()
    expect(screen.getByText('verified')).toBeTruthy()
    expect(screen.getByText('upstream-api')).toBeTruthy()
    expect(screen.getByText('Provider key · appPlatform.settings.bindingRevision:{"revision":2}')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'appPlatform.settings.openApplication' }).getAttribute('href')).toBe(`/apps/v2/${installation}`)
    expect(screen.getByRole('link', { name: /appPlatform\.settings\.lifecycleActions/ }).getAttribute('href')).toBe(`/manage/apps?installation=${installation}`)
    expect(apiMock.get).toHaveBeenCalledWith(`/api/admin/app-platform/installations/${installation}/settings`, expect.objectContaining({ signal: expect.any(AbortSignal) }))
  })

  it('does not call the server for an invalid installation identifier', async () => {
    mount('/manage/apps/installations/not-a-uuid')
    expect(await screen.findAllByText('appPlatform.settings.notFound')).toHaveLength(2)
    expect(apiMock.get).not.toHaveBeenCalled()
  })

  it('reviews and confirms permanent deletion of the exact retained data namespace', async () => {
    const user = userEvent.setup()
    const namespace = '33333333-3333-4333-8333-333333333333'
    const plan = '44444444-4444-4444-8444-444444444444'
    const operation = '55555555-5555-4555-8555-555555555555'
    apiMock.get.mockImplementation(async (url: string) => {
      if (url.endsWith('/settings')) {
        return {
          ...settings,
          desired_state: 'disabled',
          phase: 'disabled',
          backend: { required: true, registered: true, enabled: false },
          data: { ...settings.data, id: namespace },
        }
      }
      return { bindings: [] }
    })
    apiMock.post.mockImplementation(async (url: string) => {
      if (url.endsWith('/plans')) {
        return {
          plan_id: plan,
          plan_sha256: 'b'.repeat(64),
          expires_ms: String(Date.now() + 900000),
          review: {
            action: 'delete_data',
            installation_id: installation,
            app_id: 'notes',
            publisher_id: 'publisher',
            data_namespace_id: namespace,
            release_sha256: release,
            source_generation: '2',
            target_generation: '3',
            data_format: '2',
            writer_fence: '4',
            scope: 'exact namespace and recorded recovery sets',
          },
        }
      }
      return { operation_id: operation }
    })
    mount()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.deleteData' }))
    expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, {
      action: 'delete_data',
      installation_id: installation,
      data_namespace_id: namespace,
    })
    const dialog = await screen.findByRole('dialog')
    expect(dialog.textContent).toContain('appPlatform.settings.deleteDataWarning')
    expect(dialog.textContent).toContain(namespace)
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.deleteDataAction' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/operations`, expect.objectContaining({
      plan_id: plan,
      plan_sha256: 'b'.repeat(64),
      idempotency_key: expect.stringMatching(/^[0-9a-f-]{36}$/),
    })))
    expect(await screen.findByText('apps')).toBeTruthy()
  })

  it('reviews and confirms repair of the current managed release without changing grants', async () => {
    const user = userEvent.setup()
    const plan = '66666666-6666-4666-8666-666666666666'
    const operation = '77777777-7777-4777-8777-777777777777'
    apiMock.get.mockImplementation(async (url: string) => {
      if (url.endsWith('/settings')) {
        return {
          ...settings,
          desired_state: 'disabled',
          phase: 'disabled',
          generation: '2',
          backend: { required: true, registered: true, enabled: true },
          repair_eligible: true,
        }
      }
      return { bindings: [] }
    })
    apiMock.post.mockImplementation(async (url: string) => {
      if (url.endsWith('/plans')) {
        return {
          plan_id: plan,
          plan_sha256: 'c'.repeat(64),
          expires_ms: String(Date.now() + 900000),
          review: {
            action: 'repair',
            installation_id: installation,
            app_id: 'notes',
            publisher_id: 'publisher',
            release_sha256: release,
            source_generation: '2',
            target_generation: '3',
            policy_revision: '3',
            grants_sha256: 'd'.repeat(64),
            data_effect: 'checkpoint_and_retain',
          },
        }
      }
      return { operation_id: operation }
    })
    mount()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.repair' }))
    expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, {
      action: 'repair',
      installation_id: installation,
    })
    const dialog = await screen.findByRole('dialog')
    expect(dialog.textContent).toContain('appPlatform.settings.repairWarning')
    expect(dialog.textContent).toContain(release)
    expect(dialog.textContent).toContain('2 → 3')
    expect(dialog.textContent).toContain('appPlatform.settings.repairGrantsUnchanged')
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.repairAction' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/operations`, expect.objectContaining({
      plan_id: plan,
      plan_sha256: 'c'.repeat(64),
      idempotency_key: expect.stringMatching(/^[0-9a-f-]{36}$/),
    })))
    expect(await screen.findByText('apps')).toBeTruthy()
  })
})
