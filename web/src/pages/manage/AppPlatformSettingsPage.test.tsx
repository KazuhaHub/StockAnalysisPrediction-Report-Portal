import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useCallback, useState } from 'react'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter, Outlet, Route, Routes } from 'react-router'
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
  data: { id: '22222222-2222-4222-8222-222222222222', state: 'active', format: 2, quota_bytes: '268435456', quota_inodes: '65536', observed_bytes: '128', revision: '4' },
  recovery: { count: 1, state: 'verified', pending: false, points: [] },
  configuration_gaps: ['capability:network.http'],
  repair_eligible: false,
  control_paused: false,
}

function mount(path = `/manage/apps/installations/${installation}`) {
  return render(
    <App><MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/manage/apps/installations/:installation" element={<AppPlatformSettingsPage />} />
        <Route path="/manage/apps" element={<div>apps</div>} />
        <Route path="/apps/v2/:installation" element={<div>application</div>} />
        <Route path="/manage/apps/:key/credentials" element={<div>credentials</div>} />
      </Routes>
    </MemoryRouter></App>,
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
  it('shows the audience, permissions, data, recovery and credential metadata', async () => {
    mount()
    expect(await screen.findByText('appPlatform.settings.capabilities')).toBeTruthy()
    expect(screen.getByText(/appPlatform\.settings\.principal\.user:.*Reader/)).toBeTruthy()
    expect(screen.getByText(/appPlatform\.capability\.storage\.files/)).toBeTruthy()
    expect(screen.getByText(/268,435,456/)).toBeTruthy()
    expect(screen.getByText('verified')).toBeTruthy()
    expect(screen.getByText('upstream-api')).toBeTruthy()
    expect(screen.getByText('Provider key · appPlatform.settings.bindingRevision:{"revision":2}')).toBeTruthy()
    expect(apiMock.get).toHaveBeenCalledWith(`/api/admin/app-platform/installations/${installation}/settings`, expect.objectContaining({ signal: expect.any(AbortSignal) }))
  })

  describe('app administrators (delegated admin page)', () => {
    const admins = `${root}/installations/${installation}/app-admins`
    const serve = (value: typeof settings & { admin_page?: boolean }) => apiMock.get.mockImplementation(async (url: string) => {
      if (url.endsWith('/settings')) return value
      if (url.endsWith('/app-admins')) return { admins: [{ username: 'ada', displayName: 'Ada Lovelace', grantedBy: 'root', grantedAt: 1767268800000 }] }
      return { bindings: [] }
    })

    it('lists the delegated administrators in the settings tab of an app that has an admin page', async () => {
      serve({ ...settings, admin_page: true })
      mount()
      expect(await screen.findByText('Ada Lovelace')).toBeTruthy()
      expect(screen.getByText('appAdmin.admins.title')).toBeTruthy()
      expect(apiMock.get).toHaveBeenCalledWith(admins, expect.anything())
    })

    it('keeps the administrators card when the app has no admin page, so stale delegations can be seen and revoked', async () => {
      serve({ ...settings, admin_page: false })
      mount()
      expect(await screen.findByText('Ada Lovelace')).toBeTruthy()
      expect(screen.getByText('appAdmin.admins.title')).toBeTruthy()
      expect(screen.getByText('appAdmin.admins.noAdminPage')).toBeTruthy()
      expect(screen.queryByRole('button', { name: /appAdmin.admins.add/ })).toBeNull()
      expect(screen.getByRole('button', { name: /appAdmin.admins.remove/ })).toBeTruthy()
      expect(apiMock.get).toHaveBeenCalledWith(admins, expect.anything())
    })
  })


  describe('principals.read grant (doc 181 section 4)', () => {
    const route = `${root}/installations/${installation}/principals-read`
    const withPrincipals = (granted: boolean, required = false) => ({ ...settings, capabilities: [...settings.capabilities, { capability: 'principals.read', granted, required }] })
    const serve = (value: typeof settings) => apiMock.get.mockImplementation(async (url: string) => url.endsWith('/settings') ? value : { bindings: [] })

    it('shows the disclosure before an administrator enables the grant, then sends the reviewed revision', async () => {
      serve(withPrincipals(false))
      apiMock.post.mockResolvedValue({ ok: true, granted: true })
      mount()
      const user = userEvent.setup()
      await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.principals.enable' }))
      const dialog = await screen.findByRole('dialog')
      expect(within(dialog).getByText('appPlatform.capability.principals.read')).toBeTruthy()
      expect(apiMock.post).not.toHaveBeenCalled()
      await user.click(within(dialog).getByRole('button', { name: 'appPlatform.settings.principals.confirmEnable' }))
      await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(route, { enabled: true, policyRevision: '3' }))
      await waitFor(() => expect(apiMock.get.mock.calls.filter(([url]) => String(url).endsWith('/settings')).length).toBeGreaterThan(1))
    })

    it('withdraws immediately without a second confirmation', async () => {
      serve(withPrincipals(true))
      apiMock.post.mockResolvedValue({ ok: true, granted: false })
      mount()
      const user = userEvent.setup()
      await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.principals.withdraw' }))
      await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(route, { enabled: false, policyRevision: '3' }))
    })

    it('shows a refusal and leaves the grant alone', async () => {
      serve(withPrincipals(false))
      apiMock.post.mockRejectedValue(new Error('plan changed'))
      mount()
      const user = userEvent.setup()
      await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.principals.enable' }))
      await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'appPlatform.settings.principals.confirmEnable' }))
      expect(await screen.findByText('plan changed')).toBeTruthy()
    })

    it('disables both controls with an explanation unless the installation is ready with no operation in progress', async () => {
      for (const [name, override] of [['operation', { operation_pending: true }], ['phase', { phase: 'pending' }], ['disabled', { desired_state: 'disabled' }]] as const) {
        for (const granted of [false, true]) {
          serve({ ...withPrincipals(granted), ...override } as typeof settings)
          const view = mount()
          const button = await screen.findByRole('button', { name: granted ? 'appPlatform.settings.principals.withdraw' : 'appPlatform.settings.principals.enable' })
          expect(button.hasAttribute('disabled'), `${name} ${granted}`).toBe(true)
          expect(screen.getByText('appPlatform.settings.principals.notReady')).toBeTruthy()
          view.unmount()
        }
      }
    })

    it('offers the public projection only for an optional public.projection capability', async () => {
      apiMock.get.mockImplementation(async (url: string) => url.endsWith('/settings') ? { ...settings, capabilities: [...settings.capabilities, { capability: 'public.projection', granted: false, required: false }] } : { bindings: [] })
      mount()
      expect(await screen.findByRole('button', { name: 'appPlatform.settings.public.enable' })).toBeTruthy()
      cleanup()
      apiMock.get.mockImplementation(async (url: string) => url.endsWith('/settings') ? { ...settings, capabilities: [...settings.capabilities, { capability: 'public.projection', granted: false, required: true }] } : { bindings: [] })
      mount()
      await screen.findByText('appPlatform.settings.capabilities')
      expect(screen.queryByRole('button', { name: /appPlatform\.settings\.public\./ })).toBeNull()
    })

    it('offers machine access only for an optional machine.read capability', async () => {
      apiMock.get.mockImplementation(async (url: string) => url.endsWith('/settings') ? { ...settings, capabilities: [...settings.capabilities, { capability: 'machine.read', granted: false, required: false }] } : { bindings: [] })
      mount()
      expect(await screen.findByRole('button', { name: 'appPlatform.settings.machine.enable' })).toBeTruthy()
      cleanup()
      apiMock.get.mockImplementation(async (url: string) => url.endsWith('/settings') ? { ...settings, capabilities: [...settings.capabilities, { capability: 'machine.read', granted: false, required: true }] } : { bindings: [] })
      mount()
      await screen.findByText('appPlatform.settings.capabilities')
      expect(screen.queryByRole('button', { name: /appPlatform\.settings\.machine\./ })).toBeNull()
    })

    it('shows the PostgreSQL probe panel only when the capability exists and the preview declares it', async () => {
      const preview = { policy_revision: '1', declared: true, granted: false, ready: true, previous_policy_sha256: '', targets: [], deadline_ms: 10000, concurrency: 1, private_networks_sha256: '', disclosure: 'd' }
      apiMock.get.mockImplementation(async (url: string) => url.endsWith('/postgres-policy') ? preview : url.endsWith('/settings') ? { ...settings, capabilities: [...settings.capabilities, { capability: 'network.postgres', granted: false, required: false }] } : { bindings: [] })
      mount()
      expect(await screen.findByText('appPostgres.title')).toBeTruthy()
      cleanup()
      apiMock.get.mockImplementation(async (url: string) => url.endsWith('/postgres-policy') ? { ...preview, declared: false } : url.endsWith('/settings') ? { ...settings, capabilities: [...settings.capabilities, { capability: 'network.postgres', granted: false, required: false }] } : { bindings: [] })
      mount()
      await screen.findByText('appPlatform.settings.capabilities')
      expect(screen.queryByText('appPostgres.title')).toBeNull()
    })

    it('offers no control for other capabilities or without the capability', async () => {
      mount()
      await screen.findByText('appPlatform.settings.capabilities')
      expect(screen.queryByRole('button', { name: /appPlatform\.settings\.principals\./ })).toBeNull()
    })
  })


  it('hands a quiet re-read up to the tabs, so switching tabs and back does not show the spent action again', async () => {
    const user = userEvent.setup()
    const plan = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd'
    const operation = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'
    let resolved = false
    const current = () => ({ ...settings, admin_page: true, desired_state: 'disabled', phase: 'recovery_required', repair_eligible: resolved, resolve_keep_data_eligible: !resolved })
    apiMock.get.mockImplementation(async (url: string) => url.endsWith('/settings') ? current() : { bindings: [] })
    apiMock.post.mockImplementation(async (url: string) => {
      if (url.endsWith('/plans')) return { plan_id: plan, plan_sha256: 'c'.repeat(64), expires_ms: String(Date.now() + 900000), review: { action: 'resolve_keep_data', installation_id: installation, operation_id: operation, operation_action: 'import_data', release_sha256: release, observed_bytes: '1', checkpoint_recovery_set_id: operation, abandoned_effects: [], keeps_data: true, keeps_checkpoint: true, runs_rollback: false } }
      resolved = true
      return { resolved: true, operation_id: operation, installation_id: installation, abandoned_effects: '0' }
    })
    const replaced: { resolve_keep_data_eligible?: boolean }[] = []
    function Host() {
      const [shared, setShared] = useState<Record<string, unknown>>(current())
      const replace = useCallback((next: Record<string, unknown>) => { replaced.push(next); setShared(next) }, [])
      return <Outlet context={{ settings: shared, reload: () => {}, replace, headed: true }} />
    }
    render(
      <App><MemoryRouter initialEntries={[`/manage/apps/installations/${installation}`]}>
        <Routes>
          <Route path="/manage/apps/installations/:installation" element={<Host />}>
            <Route index element={<AppPlatformSettingsPage />} />
          </Route>
        </Routes>
      </MemoryRouter></App>,
    )
    await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.resolve' }))
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('checkbox', { name: 'appPlatform.settings.resolveConfirm' }))
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.resolveAction' }))
    expect(await screen.findByRole('button', { name: 'appPlatform.settings.repair' })).toBeTruthy()
    await waitFor(() => expect(replaced.map(value => value.resolve_keep_data_eligible)).toEqual([false]))
    expect(screen.queryByRole('button', { name: 'appPlatform.settings.resolve' })).toBeNull()
  })

  it('asks the tabs to read again too when their first read failed and the page retries', async () => {
    const user = userEvent.setup()
    let reads = 0
    apiMock.get.mockImplementation(async (url: string) => {
      if (url.endsWith('/settings') && ++reads === 1) throw new Error('down')
      return url.endsWith('/settings') ? settings : { bindings: [] }
    })
    const reload = vi.fn()
    render(
      <App><MemoryRouter initialEntries={[`/manage/apps/installations/${installation}`]}>
        <Routes>
          <Route path="/manage/apps/installations/:installation" element={<Outlet context={{ settings: null, reload, replace: () => {}, headed: false }} />}>
            <Route index element={<AppPlatformSettingsPage />} />
          </Route>
        </Routes>
      </MemoryRouter></App>,
    )
    await user.click((await screen.findAllByRole('button', { name: /common\.retry/ }))[0])
    expect(reload).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(document.querySelector('.rp-platform-settings')).not.toBeNull())
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
    expect(dialog.textContent).toContain(release.slice(0, 12))
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

  it('reviews and accepts the data a failed import left, only after an explicit acknowledgement', async () => {
    const user = userEvent.setup()
    const plan = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd'
    const operation = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'
    const recovery = 'ffffffff-ffff-4fff-8fff-ffffffffffff'
    let resolved = false
    apiMock.get.mockImplementation(async (url: string) => {
      if (url.endsWith('/settings')) {
        return {
          ...settings,
          desired_state: 'disabled',
          phase: 'recovery_required',
          generation: '1',
          repair_eligible: resolved,
          resolve_keep_data_eligible: !resolved,
          operation_pending: !resolved,
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
            action: 'resolve_keep_data',
            installation_id: installation,
            operation_id: operation,
            operation_action: 'import_data',
            release_sha256: release,
            observed_bytes: '780615680',
            checkpoint_recovery_set_id: recovery,
            abandoned_effects: [
              { operation_id: operation, operation_action: 'import_data', operation_completed_ms: '1795345430805', effect_id: operation, stage: 'import', kind: 'restore' },
              { operation_id: recovery, operation_action: 'import_data', operation_completed_ms: '1795339211708', effect_id: plan, stage: 'snapshot', kind: 'snapshot', recovery_set_id: 'abcdef12-aaaa-4aaa-8aaa-aaaaaaaaaaaa' },
            ],
            keeps_data: true,
            keeps_checkpoint: true,
            runs_rollback: false,
          },
        }
      }
      resolved = true
      return { resolved: true, operation_id: operation, installation_id: installation, abandoned_effects: '1' }
    })
    mount()
    expect(screen.queryByRole('button', { name: 'appPlatform.settings.repair' })).toBeNull()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.resolve' }))
    expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, { action: 'resolve_keep_data', installation_id: installation })
    const dialog = await screen.findByRole('dialog')
    expect(dialog.textContent).toContain('appPlatform.settings.resolveWarning')
    expect(dialog.textContent).toContain(operation)
    expect(dialog.textContent).toContain(recovery)
    // Each unfinished item is listed with its operation, kind and the recovery set closed with it.
    expect(dialog.textContent).toContain('appPlatform.settings.resolveEffectRestore')
    expect(dialog.textContent).toContain('appPlatform.settings.resolveEffectSnapshot')
    expect(dialog.textContent).toContain('appPlatform.settings.resolveEffectSet')
    expect(dialog.textContent).toContain(operation.slice(0, 8))
    expect(dialog.textContent).toContain('appPlatform.settings.resolveEffects:{"count":2}')
    const confirm = screen.getByRole('button', { name: 'appPlatform.settings.resolveAction' }) as HTMLButtonElement
    expect(confirm.disabled).toBe(true)
    await user.click(screen.getByRole('checkbox', { name: 'appPlatform.settings.resolveConfirm' }))
    expect(confirm.disabled).toBe(false)
    await user.click(confirm)
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/operations`, expect.objectContaining({
      plan_id: plan,
      plan_sha256: 'c'.repeat(64),
      idempotency_key: expect.stringMatching(/^[0-9a-f-]{36}$/),
    })))
    // The page reads the settings again: the data stays, and a repair is now the way forward.
    expect(await screen.findByRole('button', { name: 'appPlatform.settings.repair' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'appPlatform.settings.resolve' })).toBeNull()
  })

  it('refreshes the page in place after the data is accepted, without blanking it, and closes the dialog when the answer was lost but the data was accepted', async () => {
    const user = userEvent.setup()
    const plan = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd'
    const operation = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'
    let resolved = false
    let settingsReads = 0
    apiMock.get.mockImplementation(async (url: string) => {
      if (url.endsWith('/settings')) {
        settingsReads++
        return { ...settings, desired_state: 'disabled', phase: 'recovery_required', repair_eligible: resolved, resolve_keep_data_eligible: !resolved }
      }
      return { bindings: [] }
    })
    let operations = 0
    apiMock.post.mockImplementation(async (url: string) => {
      if (url.endsWith('/plans')) {
        return { plan_id: plan, plan_sha256: 'c'.repeat(64), expires_ms: String(Date.now() + 900000), review: { action: 'resolve_keep_data', installation_id: installation, operation_id: operation, operation_action: 'import_data', release_sha256: release, observed_bytes: '1', checkpoint_recovery_set_id: operation, abandoned_effects: [], keeps_data: true, keeps_checkpoint: true, runs_rollback: false } }
      }
      operations++
      resolved = true
      // The first confirmation commits on the server, but its answer is lost.
      if (operations === 1) throw new Error('network lost')
      return { resolved: true, operation_id: operation, installation_id: installation, abandoned_effects: '0' }
    })
    mount()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.resolve' }))
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('checkbox', { name: 'appPlatform.settings.resolveConfirm' }))
    const before = settingsReads
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.resolveAction' }))
    // The page reads what the server holds now; the data is accepted, so the spent plan's dialog closes.
    await waitFor(() => expect(settingsReads).toBe(before + 1))
    expect(await screen.findByRole('button', { name: 'appPlatform.settings.repair' })).toBeTruthy()
    await waitFor(() => expect(screen.queryByText('network lost')).toBeNull())
    expect(screen.queryByRole('button', { name: 'appPlatform.settings.resolveAction' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'appPlatform.settings.resolve' })).toBeNull()
  })

  it('keeps the dialog and its message when a failed confirmation did not accept the data', async () => {
    const user = userEvent.setup()
    const plan = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd'
    const operation = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'
    apiMock.get.mockImplementation(async (url: string) => url.endsWith('/settings')
      ? { ...settings, desired_state: 'disabled', phase: 'recovery_required', repair_eligible: false, resolve_keep_data_eligible: true }
      : { bindings: [] })
    apiMock.post.mockImplementation(async (url: string) => {
      if (url.endsWith('/plans')) return { plan_id: plan, plan_sha256: 'c'.repeat(64), expires_ms: String(Date.now() + 900000), review: { action: 'resolve_keep_data', installation_id: installation, operation_id: operation, operation_action: 'import_data', release_sha256: release, observed_bytes: '1', checkpoint_recovery_set_id: operation, abandoned_effects: [], keeps_data: true, keeps_checkpoint: true, runs_rollback: false } }
      throw new Error('refused')
    })
    mount()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.resolve' }))
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('checkbox', { name: 'appPlatform.settings.resolveConfirm' }))
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.resolveAction' }))
    expect(await screen.findByText('refused')).toBeTruthy()
    await waitFor(() => expect(screen.getByRole('button', { name: /appPlatform\.settings\.resolveAction/ })).toBeTruthy())
  })


  it('reviews and admits restore from the exact verified recovery point', async () => {
    const user = userEvent.setup()
    const recovery = '88888888-8888-4888-8888-888888888888'
    const checkpointOperation = '99999999-9999-4999-8999-999999999999'
    const checkpoint = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
    const plan = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
    const operation = 'cccccccc-cccc-4ccc-8ccc-cccccccccccc'
    apiMock.get.mockImplementation(async (url: string) => {
      if (url.endsWith('/settings')) {
        return {
          ...settings,
          desired_state: 'enabled',
          phase: 'recovery_required',
          recovery: {
            count: 1,
            state: 'verified',
            pending: false,
            points: [{
              id: recovery,
              state: 'verified',
              checkpoint_id: checkpoint,
              release_sha256: release,
              format: 2,
              bytes: '4096',
              verified_ms: String(Date.now()),
              restore_eligible: true,
            }],
          },
        }
      }
      return { bindings: [] }
    })
    apiMock.post.mockImplementation(async (url: string) => {
      if (url.endsWith('/plans')) {
        return {
          plan_id: plan,
          plan_sha256: 'e'.repeat(64),
          expires_ms: String(Date.now() + 900000),
          review: {
            action: 'restore',
            installation_id: installation,
            recovery_set_id: recovery,
            checkpoint_operation_id: checkpointOperation,
            checkpoint_effect_id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
            checkpoint_id: checkpoint,
            release_sha256: release,
            manifest_sha256: 'f'.repeat(64),
            archive_bytes: '4096',
            source_generation: '1',
            target_generation: '2',
            data_effect: 'restore_checkpoint',
          },
        }
      }
      return { operation_id: operation }
    })
    mount()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.restore' }))
    expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, {
      action: 'restore',
      installation_id: installation,
      recovery_set_id: recovery,
    })
    const dialog = await screen.findByRole('dialog')
    expect(dialog.textContent).toContain('appPlatform.settings.restoreWarning')
    expect(dialog.textContent).toContain(recovery)
    expect(dialog.textContent).toContain(checkpoint)
    expect(dialog.textContent).toContain('1 → 2')
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.restoreAction' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/operations`, expect.objectContaining({
      plan_id: plan,
      plan_sha256: 'e'.repeat(64),
      idempotency_key: expect.stringMatching(/^[0-9a-f-]{36}$/),
    })))
    expect(await screen.findByText('apps')).toBeTruthy()
  })

  it('shows the recovery pause reason and disables restore admission', async () => {
    apiMock.get.mockImplementation(async (url: string) => {
      if (url.endsWith('/settings')) {
        return {
          ...settings,
          desired_state: 'enabled',
          phase: 'recovery_required',
          control_paused: true,
          configuration_gaps: ['control_paused'],
          recovery: {
            count: 1,
            state: 'verified',
            pending: false,
            points: [{
              id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
              state: 'verified',
              checkpoint_id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',
              release_sha256: release,
              format: 2,
              bytes: '4096',
              restore_eligible: false,
            }],
          },
        }
      }
      return { bindings: [] }
    })
    mount()
    expect(await screen.findByText('err.app_platform_control_paused')).toBeTruthy()
    expect(screen.getByText('appPlatform.settings.restorePaused')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'appPlatform.settings.restore' }) as HTMLButtonElement).disabled).toBe(true)
  })
  describe('data migration recovery', () => {
    const recoverySet = '88888888-8888-4888-8888-888888888888'
    const failedOperation = '77777777-7777-4777-8777-777777777777'
    const plan = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
    const settled = {
      state: 'settled',
      operation_id: failedOperation,
      choices: [
        { kind: 'rollback', recovery_set_id: recoverySet },
        { kind: 'retry', recovers_operation_id: failedOperation, release_sha256: 'c'.repeat(64), trust_revision: '9' },
      ],
    }
    const withRecovery = (migration_recovery: unknown) => apiMock.get.mockImplementation(async (url: string) =>
      url.endsWith('/settings') ? { ...settings, migration_recovery } : { bindings: [] })
    const buttons = () => screen.queryAllByRole('button', { name: /appPlatform\.settings\.migrationRecovery\.(rollback|retry)$/ })

    it('says the package must be uploaded again when the image blobs are gone, and names the release', async () => {
      apiMock.get.mockImplementation(async (url: string) =>
        url.endsWith('/settings') ? { ...settings, image_delivery: { state: 'reupload_required', release_sha256: 'd'.repeat(64), reason: 'blobs_missing' } } : { bindings: [] })
      mount()
      expect((await screen.findByTestId('image-delivery-reupload')).textContent).toContain('appPlatform.settings.imageDelivery.reuploadRequired')
    })

    it('shows no re-upload notice when the image is intact', async () => {
      mount()
      await screen.findByText('appPlatform.settings.capabilities')
      expect(screen.queryByTestId('image-delivery-reupload')).toBeNull()
    })

    it('renders nothing when the field is absent', async () => {
      mount()
      await screen.findByText('appPlatform.settings.capabilities')
      expect(screen.queryByText('appPlatform.settings.migrationRecovery.title')).toBeNull()
    })

    it.each(['settling', 'blocked'])('shows %s without action buttons', async (state) => {
      withRecovery({ state, operation_id: failedOperation, choices: [] })
      mount()
      expect(await screen.findByText('appPlatform.settings.migrationRecovery.title')).toBeTruthy()
      expect(screen.getByText(`appPlatform.settings.migrationRecovery.state.${state}`)).toBeTruthy()
      expect(screen.getByText(`appPlatform.settings.migrationRecovery.detail.${state}`)).toBeTruthy()
      expect(buttons()).toHaveLength(0)
    })

    it('says why a blocked settlement is stuck, with a sentence for a known reason', async () => {
      withRecovery({ state: 'blocked', reason: 'foreign_container', operation_id: failedOperation, choices: [] })
      mount()
      expect(await screen.findByText('appPlatform.settings.migrationRecovery.reason.foreign_container')).toBeTruthy()
      expect(buttons()).toHaveLength(0)
    })

    it('falls back to a generic sentence for a reason it does not know, without translating the value', async () => {
      withRecovery({ state: 'blocked', reason: 'something_new', operation_id: failedOperation, choices: [] })
      mount()
      expect(await screen.findByTestId('migration-blocked-reason')).toBeTruthy()
      expect(screen.queryByText('appPlatform.settings.migrationRecovery.reason.foreign_container')).toBeNull()
      expect(screen.getByTestId('migration-blocked-reason').textContent).toContain('appPlatform.settings.migrationRecovery.reason.unknown')
    })

    it('shows no reason when the state is not blocked', async () => {
      withRecovery({ state: 'settling', reason: 'foreign_container', operation_id: failedOperation, choices: [] })
      mount()
      await screen.findByText('appPlatform.settings.migrationRecovery.title')
      expect(screen.queryByTestId('migration-blocked-reason')).toBeNull()
    })

    it('shows both choices when settled', async () => {
      withRecovery(settled)
      mount()
      expect(await screen.findByRole('button', { name: 'appPlatform.settings.migrationRecovery.rollback' })).toBeTruthy()
      expect(screen.getByRole('button', { name: 'appPlatform.settings.migrationRecovery.retry' })).toBeTruthy()
      expect(buttons()).toHaveLength(2)
    })

    it('starts the restore plan for the rollback recovery set', async () => {
      const user = userEvent.setup()
      withRecovery(settled)
      apiMock.post.mockRejectedValue(new Error('stop'))
      mount()
      await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.migrationRecovery.rollback' }))
      expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, {
        action: 'restore',
        recovery_set_id: recoverySet,
        installation_id: installation,
      })
    })

    it('reviews and admits the update retry', async () => {
      const user = userEvent.setup()
      const operation = 'cccccccc-cccc-4ccc-8ccc-cccccccccccc'
      withRecovery(settled)
      apiMock.post.mockImplementation(async (url: string) => url.endsWith('/plans')
        ? { plan_id: plan, plan_sha256: 'e'.repeat(64), expires_ms: '1', review: { action: 'update', installation_id: installation, release_sha256: 'c'.repeat(64) } }
        : { operation_id: operation })
      mount()
      await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.migrationRecovery.retry' }))
      expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, {
        action: 'update',
        installation_id: installation,
        release_sha256: 'c'.repeat(64),
        trust_revision: '9',
        recovers_operation_id: failedOperation,
      })
      const dialog = await screen.findByRole('dialog')
      expect(dialog.textContent).toContain('appPlatform.settings.migrationRecovery.retryWarning')
      await user.click(screen.getByRole('button', { name: 'appPlatform.settings.migrationRecovery.retryAction' }))
      await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/operations`, expect.objectContaining({ plan_id: plan })))
      expect(await screen.findByText('apps')).toBeTruthy()
    })

    it('refuses a review for another action', async () => {
      const user = userEvent.setup()
      withRecovery(settled)
      apiMock.post.mockResolvedValue({ plan_id: plan, plan_sha256: 'e'.repeat(64), expires_ms: '1', review: { action: 'repair', installation_id: installation } })
      mount()
      await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.migrationRecovery.retry' }))
      expect(await screen.findByText('appPlatform.invalidOperation')).toBeTruthy()
      expect(screen.queryByRole('dialog')).toBeNull()
    })
  })
})

describe('data quota raise and data import', () => {
  const plan = '99999999-9999-4999-8999-999999999999'
  const operation = '77777777-7777-4777-8777-777777777777'
  const quotaReview = (capacity: 'fits' | 'short') => ({
    action: 'set_data_quota',
    installation_id: installation,
    app_id: 'notes',
    publisher_id: 'publisher',
    current_data_quota_bytes: '268435456',
    current_data_quota_inodes: '65536',
    data_quota_bytes: '536870912',
    data_quota_inodes: '131072',
    observed_bytes: '128',
    capacity,
    ...(capacity === 'short' ? { capacity_dimension: 'disk', capacity_requested: '1000', capacity_remaining: '10' } : {}),
  })
  const importReview = {
    action: 'import_data',
    installation_id: installation,
    app_id: 'notes',
    publisher_id: 'publisher',
    archive_sha256: 'f'.repeat(64),
    archive_bytes: '2048',
    archive_entries: '12',
    content_bytes: '4096',
    archive_data_format: '2',
    release_data_format: '2',
    data_quota_bytes: '268435456',
    data_quota_inodes: '65536',
    replaces_contents: true,
    keeps_checkpoint: true,
    requires_disabled: true,
  }
  const serve = (extra: Record<string, unknown>, review: unknown) => {
    apiMock.get.mockImplementation(async (url: string) => url.endsWith('/settings') ? { ...settings, ...extra } : { bindings: [] })
    apiMock.post.mockImplementation(async (url: string) => url.endsWith('/plans')
      ? { plan_id: plan, plan_sha256: 'e'.repeat(64), expires_ms: '1', review }
      : { operation_id: operation })
  }
  const fillRaise = async (user: ReturnType<typeof userEvent.setup>, bytes: string, inodes: string) => {
    const bytesInput = screen.getByLabelText('appPlatform.settings.raiseQuotaBytes')
    await user.clear(bytesInput)
    await user.type(bytesInput, bytes)
    const inodesInput = screen.getByLabelText('appPlatform.settings.raiseQuotaInodes')
    await user.clear(inodesInput)
    await user.type(inodesInput, inodes)
  }

  it('posts the raise plan, refuses a lower value, and commits a fitting plan', async () => {
    const user = userEvent.setup()
    serve({ quota_raise_eligible: true }, quotaReview('fits'))
    mount()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.raiseQuota' }))
    await fillRaise(user, '134217728', '65536')
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.reviewPlan' }))
    expect(await screen.findByText('err.data_quota_lower_refused')).toBeTruthy()
    expect(apiMock.post).not.toHaveBeenCalled()

    await fillRaise(user, '536870912', '131072')
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.reviewPlan' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, {
      action: 'set_data_quota',
      installation_id: installation,
      data_quota_bytes: '536870912',
      data_quota_inodes: '131072',
    }))
    await user.click(await screen.findByRole('button', { name: /appPlatform\.settings\.raiseQuotaAction$/ }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/operations`, expect.objectContaining({
      plan_id: plan,
      plan_sha256: 'e'.repeat(64),
      idempotency_key: expect.stringMatching(/^[0-9a-f-]{36}$/),
    })))
    expect(await screen.findByText('apps')).toBeTruthy()
  })

  it('disables confirmation when the runner capacity is short', async () => {
    const user = userEvent.setup()
    serve({ quota_raise_eligible: true }, quotaReview('short'))
    mount()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.raiseQuota' }))
    await fillRaise(user, '536870912', '131072')
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.reviewPlan' }))
    expect((await screen.findByTestId('raise-capacity-short')).textContent).toContain('disk')
    const confirm = screen.getByRole('button', { name: /appPlatform\.settings\.raiseQuotaAction$/ }) as HTMLButtonElement
    expect(confirm.disabled).toBe(true)
  })

  it('validates the import digest, states the replace, checkpoint and disabled facts, and commits', async () => {
    const user = userEvent.setup()
    serve({ desired_state: 'disabled', phase: 'disabled', import_eligible: true }, importReview)
    mount()
    await user.click(await screen.findByRole('button', { name: 'appPlatform.settings.importData' }))
    const digest = screen.getByLabelText('appPlatform.settings.importDigest')
    await user.type(digest, 'NOT-A-DIGEST')
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.reviewPlan' }))
    expect(await screen.findByText('appPlatform.settings.importDigestInvalid')).toBeTruthy()
    expect(apiMock.post).not.toHaveBeenCalled()

    await user.clear(digest)
    await user.type(digest, 'f'.repeat(64))
    await user.click(screen.getByRole('button', { name: 'appPlatform.settings.reviewPlan' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/plans`, {
      action: 'import_data',
      installation_id: installation,
      archive_sha256: 'f'.repeat(64),
    }))
    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(dialog.textContent).toContain('appPlatform.settings.importReplaces'))
    expect(dialog.textContent).toContain('appPlatform.settings.importCheckpoint')
    expect(dialog.textContent).toContain('appPlatform.settings.importDisabled')
    expect(dialog.textContent).toContain('f'.repeat(64))
    await user.click(await screen.findByRole('button', { name: /appPlatform\.settings\.importAction$/ }))
    // The destructive commit asks once more, with the shared confirmation rules.
    expect(apiMock.post).not.toHaveBeenCalledWith(`${root}/operations`, expect.anything())
    expect((await screen.findAllByText('appPlatform.settings.importConfirmTitle')).length).toBeGreaterThan(0)
    const buttons = screen.getAllByRole('button', { name: /appPlatform\.settings\.importAction$/ })
    await user.click(buttons[buttons.length - 1])
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith(`${root}/operations`, expect.objectContaining({
      plan_id: plan,
      plan_sha256: 'e'.repeat(64),
      idempotency_key: expect.stringMatching(/^[0-9a-f-]{36}$/),
    })))
    expect(await screen.findByText('apps')).toBeTruthy()
  })

  it('does not offer import when the installation is not eligible and says it must be disabled', async () => {
    serve({ import_eligible: false }, importReview)
    mount()
    const button = await screen.findByRole('button', { name: 'appPlatform.settings.importData' }) as HTMLButtonElement
    expect(button.disabled).toBe(true)
    expect(screen.getByTestId('import-disabled-hint').textContent).toContain('appPlatform.settings.importDisabledHint')
  })
})
