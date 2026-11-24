import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import InstallationHeaderActions, { installationApp } from './InstallationHeaderActions'
import type { Settings } from './AppPlatformSettingsPage'
import type { AppsConsoleFlow } from './appsConsoleFlow'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('../../api/client', async original => ({ ...await original<typeof import('../../api/client')>(), api: { get: vi.fn(async () => ({ items: [] })) } }))

const id = '11111111-1111-4111-8111-111111111111'
const base = {
  installation_id: id, app_id: 'notes', publisher_id: 'kazuha', desired_state: 'enabled', phase: 'ready', generation: '2', revision: '5', policy_revision: '3',
  version: '1.0.0', audience: [], capabilities: [], backend: { required: true, registered: true, enabled: true },
  recovery: { count: 0, points: [], pending: false }, configuration_gaps: [], repair_eligible: false, control_paused: false,
} as unknown as Settings
const flow = { busy: false, busyReason: '' as const, acting: '', pickVersion: vi.fn(), act: vi.fn(async () => undefined) }
const mount = (extra: Record<string, unknown> = {}, update?: string) => {
  const settings = { ...base, ...extra } as Settings
  return render(<MemoryRouter initialEntries={['/h']}><Routes>
    <Route path="/h" element={<InstallationHeaderActions settings={settings} app={installationApp(settings, 'Notes')!} name="Notes" flow={flow as unknown as AppsConsoleFlow} update={update} />} />
    <Route path="/apps/v2/:id" element={<div>application</div>} />
  </Routes></MemoryRouter>)
}
const group = () => screen.getByRole('group', { name: 'appPlatform.settings.lifecycleActions' })

beforeEach(() => { flow.pickVersion.mockReset(); flow.act.mockReset() })
afterEach(cleanup)

describe('the header actions of an installation', () => {
  it('offers only Switch version, Disable and Uninstall when no newer release is known, and Update (naming the version) when one is', () => {
    mount()
    expect(within(group()).getAllByRole('button').map(b => b.textContent)).toEqual(['appStore.switchVersion', 'appStore.disable', 'appStore.uninstall'])
    cleanup()
    mount({}, '1.1.0')
    expect(within(group()).getAllByRole('button').map(b => b.textContent)).toEqual(['appStore.updateTo', 'appStore.switchVersion', 'appStore.disable', 'appStore.uninstall'])
  })

  it('offers Repair only when the server offers it', () => {
    mount()
    cleanup()
    mount({ repair_eligible: true })
    expect(within(group()).getByRole('button', { name: 'appPlatform.repair' })).toBeTruthy()
  })

  it('offers Enable instead of Disable for a disabled app, and no Open application', () => {
    mount({ desired_state: 'disabled', phase: 'disabled' })
    expect(within(group()).getByRole('button', { name: 'appStore.enable' })).toBeTruthy()
    expect(within(group()).queryByRole('button', { name: 'appStore.disable' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'appPlatform.settings.openApplication' })).toBeNull()
  })

  it('runs the same flows as the card: the version step for Update and Switch version, a review for the rest', async () => {
    mount({ repair_eligible: true }, '1.1.0')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'appStore.updateTo' }))
    expect(flow.pickVersion).toHaveBeenLastCalledWith(expect.objectContaining({ installationId: id, key: `platform:${id}` }), 'Notes', 'update', expect.anything())
    await user.click(screen.getByRole('button', { name: 'appStore.switchVersion' }))
    expect(flow.pickVersion).toHaveBeenLastCalledWith(expect.anything(), 'Notes', 'switch', expect.anything())
    await user.click(screen.getByRole('button', { name: 'appStore.disable' }))
    expect(flow.act).toHaveBeenLastCalledWith(expect.objectContaining({ installationId: id }), 'disable', expect.anything())
    await user.click(screen.getByRole('button', { name: 'appPlatform.repair' }))
    expect(flow.act).toHaveBeenLastCalledWith(expect.anything(), 'repair', expect.anything())
  })

  it('has Open application as a normal button for a running app', async () => {
    mount()
    await userEvent.click(screen.getByRole('button', { name: 'appPlatform.settings.openApplication' }))
    expect(await screen.findByText('application')).toBeTruthy()
  })
})
