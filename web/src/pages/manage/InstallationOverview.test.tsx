import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Outlet, Route, Routes } from 'react-router'
import InstallationOverview, { gapTab, readiness } from './InstallationOverview'
import { controlState, runState } from './installationState'

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: apiMock, errText: (e: unknown) => (e instanceof Error ? e.message : String(e)) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string, o?: unknown) => (o === undefined ? k : `${k}:${JSON.stringify(o)}`) }) }))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
vi.mock('../../auth', () => ({ useAuth: () => ({ user: 'admin' }) }))

const id = '11111111-1111-4111-8111-111111111111'
const root = '/api/admin/app-platform'
const base = {
  installation_id: id, app_id: 'notes', publisher_id: 'kazuha', desired_state: 'enabled', phase: 'ready', generation: '2', revision: '5', policy_revision: '3',
  current_release_sha256: 'a'.repeat(64), version: '1.0.0', audience: [],
  capabilities: [], backend: { required: true, registered: true, enabled: true },
  data: { id: 'd', state: 'active', format: 1, quota_bytes: '1048576', quota_inodes: '65536', observed_bytes: '524288', revision: '1' },
  recovery: { count: 0, points: [], pending: false }, configuration_gaps: [], repair_eligible: false, control_paused: false,
}
const op = (extra: Record<string, unknown> = {}) => ({ operation_id: '22222222-2222-4222-8222-222222222222', installation_id: id, action: 'update', stage: 'complete', managed: false, revision: '9', terminal: true, created_ms: '1000', updated_ms: '66000', completed_ms: '66000', from_version: '0.9.0', to_version: '1.0.0', ...extra })

const flow = { busy: false, busyReason: '' as const, acting: '', pickVersion: vi.fn(), act: vi.fn(async () => undefined) }
let rows: unknown[] = []
const mount = (settings: Record<string, unknown> = {}) => render(
  <MemoryRouter initialEntries={['/o']}><Routes>
    <Route element={<Outlet context={{ settings: { ...base, ...settings }, reload: () => undefined, replace: () => undefined, headed: true, name: 'Notes', flow, operationsKey: 0, openOperation: () => undefined }} />}>
      <Route path="/o" element={<InstallationOverview />} />
    </Route>
  </Routes></MemoryRouter>,
)

beforeEach(() => {
  rows = [op()]
  flow.pickVersion.mockReset(); flow.act.mockReset()
  apiMock.get.mockReset()
  apiMock.get.mockImplementation(async (url: string) => {
    if (url.startsWith(`${root}/operations?`)) return { items: rows }
    if (url.endsWith('/network-proxy')) return { today: { day: 'x', bytes: 512, budget: 2048, reported: true } }
    return { items: [] }
  })
})
afterEach(cleanup)

describe('run state and control state', () => {
  it('names running, stopped, disabled, recovery, pending and setup', () => {
    const s = (phase: string, desired = 'enabled', backend = base.backend) => runState({ phase, desired_state: desired, backend })
    expect(s('ready')).toBe('running')
    expect(s('ready', 'enabled', { required: true, registered: true, enabled: false })).toBe('stopped')
    expect(s('ready', 'disabled')).toBe('disabled')
    expect(s('recovery_required')).toBe('recovery')
    expect(s('pending')).toBe('pending')
    expect(s('setup_required')).toBe('setup')
  })
  it('maps an installation to the state the app cards use', () => {
    expect(controlState({ phase: 'ready', desired_state: 'enabled' })).toBe('installed')
    expect(controlState({ phase: 'ready', desired_state: 'disabled' })).toBe('disabled')
    expect(controlState({ phase: 'quarantined', desired_state: 'enabled' })).toBe('attention')
  })
})

describe('readiness and where a gap is fixed', () => {
  it('says ready, paused, installing, not ready or off', () => {
    const r = (phase: string, desired = 'enabled', paused = false) => readiness({ phase, desired_state: desired, control_paused: paused }).key
    expect(r('ready')).toBe('appPlatform.overview.healthReady')
    expect(r('ready', 'enabled', true)).toBe('appPlatform.overview.readyPaused')
    expect(r('pending')).toBe('appPlatform.overview.readyPending')
    expect(r('recovery_required')).toBe('appPlatform.overview.readyNot')
    expect(r('ready', 'disabled')).toBe('appPlatform.overview.readyOff')
  })
  it('sends a gap to the tab that fixes it', () => {
    expect(gapTab('capability:network.http')).toBe('permissions')
    expect(gapTab('secret_binding:feed')).toBe('permissions')
    expect(gapTab('data_review_required')).toBe('data')
  })
})

describe('the Overview tab', () => {
  it('shows run state, readiness, data against its quota and the proxy traffic against its budget', async () => {
    mount({ capabilities: [{ capability: 'network.proxy', granted: true, required: false }] })
    expect(await screen.findByText('appPlatform.overview.run.running')).toBeTruthy()
    expect(screen.getByText('appPlatform.overview.healthReady')).toBeTruthy()
    expect(screen.getByText('appPlatform.overview.usageValue:{"used":"512 KiB","total":"1 MiB"}')).toBeTruthy()
    expect(await screen.findByText('appPlatform.overview.usageValue:{"used":"512 B","total":"2 KiB"}')).toBeTruthy()
    expect(screen.getAllByRole('progressbar')).toHaveLength(2)
  })

  it('is one Status card: no separate configuration, backend or release cards, and no toolbar or policy revision in it', async () => {
    mount()
    await screen.findByText('appPlatform.overview.run.running')
    const titles = [...document.querySelectorAll('.ant-card-head-title')].map(n => n.textContent)
    expect(titles).toEqual(['appPlatform.overview.status', 'appPlatform.ops.recent'])
    expect(screen.queryByRole('group', { name: 'appPlatform.settings.lifecycleActions' })).toBeNull()
    expect(screen.queryByText(/appPlatform\.settings\.policyRevision/)).toBeNull()
    expect(screen.getByText('appPlatform.overview.backendManaged:{"status":"appPlatform.settings.backendEnabled"}')).toBeTruthy()
    expect(screen.getByText('1.0.0')).toBeTruthy()
  })

  it('shows no proxy line when the proxy is off, and says how many things need setup', async () => {
    mount({ configuration_gaps: ['capability:network.http', 'secret_binding:feed'] })
    expect(await screen.findByText('appPlatform.overview.healthGaps:{"count":2}')).toBeTruthy()
    expect(screen.queryByText('appPlatform.overview.proxyToday')).toBeNull()
    expect(apiMock.get).not.toHaveBeenCalledWith(`${root}/installations/${id}/network-proxy`, expect.anything())
  })

  it('links a missing business binding to its reviewed setup', async () => {
    mount({ app_id: 'scheduled-digest', phase: 'setup_required', configuration_gaps: ['setup_required', 'secret_binding:digest-feed'] })
    expect(await screen.findByText(/appPlatform\.settings\.missingSecretBinding/)).toBeTruthy()
    // The gap links to the tab that fixes it.
    expect(screen.getByRole('link', { name: /appPlatform\.settings\.missingSecretBinding/ }).getAttribute('href')).toBe(`/manage/apps/installations/${id}/permissions`)
  })




  it('reserves a slot for runtime metrics and shows no CPU or memory number in it', async () => {
    mount()
    await screen.findByText('appPlatform.overview.run.running')
    expect(document.body.textContent).not.toMatch(/CPU|memory|Memory/)
  })

  it('lists the recent operations with versions, duration and result, and a failure with its reason and its steps', async () => {
    rows = [
      op({ operation_id: '33333333-3333-4333-8333-333333333333', stage: 'failed', error_code: 'capacity_exhausted_disk', created_ms: '70000', updated_ms: '71000', completed_ms: '71000', revision: '5' }),
      op(),
    ]
    mount()
    const list = await screen.findByText('appPlatform.ops.recent')
    const card = list.closest('.ant-card') as HTMLElement
    await waitFor(() => expect(within(card).getAllByText('0.9.0 → 1.0.0')).toHaveLength(2))
    expect(within(card).getByText('appPlatform.ops.durationMinutes:{"m":1,"s":5}')).toBeTruthy()
    expect(within(card).getByText('appPlatform.ops.succeededShort')).toBeTruthy()
    expect(within(card).getByText('appPlatform.ops.failedShort')).toBeTruthy()
    expect(within(card).getByText(/err\.capacity_exhausted_disk/)).toBeTruthy()
    await userEvent.click(within(card).getAllByRole('button', { name: /expand/i })[0])
    expect(await screen.findByText('appPlatform.stage.validate')).toBeTruthy()
    // The steps unfold (Expand) rather than appear.
    expect(card.querySelector('.rp-fold')).not.toBeNull()
    expect(card.querySelector('.ant-steps-item-error')).not.toBeNull()
  })

  it('says there are no operations yet, and a failed read with a retry', async () => {
    rows = []
    mount()
    expect(await screen.findByText('appPlatform.ops.empty')).toBeTruthy()
    cleanup()
    apiMock.get.mockImplementation(async (url: string) => { if (url.startsWith(`${root}/operations?`)) throw new Error('ops down'); return { items: [] } })
    mount()
    expect(await screen.findByText('ops down')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'common.retry' })).toBeTruthy()
  })

  it('names the running operation and its step as the current one', async () => {
    rows = [op({ stage: 'download', terminal: false, completed_ms: undefined })]
    mount()
    expect(await screen.findByText(/appPlatform\.ops\.step:\{"step":3,"total":9\} · appPlatform\.stage\.download/)).toBeTruthy()
  })
})
