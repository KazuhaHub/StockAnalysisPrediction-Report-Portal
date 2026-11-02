import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import AppPlatformSettingsPage from './AppPlatformSettingsPage'

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
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
  data: { state: 'active', format: 2, quota_bytes: '268435456', observed_bytes: '128', revision: '4' },
  recovery: { count: 1, state: 'verified', pending: false },
  configuration_gaps: ['capability:network.http'],
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
})
