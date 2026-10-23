import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { App } from 'antd'
import OrgUnitDetail from './OrgUnitDetail'
import type { UserGroupRow } from '../../api/types'

const put = vi.hoisted(() => vi.fn())
const get = vi.hoisted(() => vi.fn())
const del = vi.hoisted(() => vi.fn())
vi.mock('../../api/client', () => ({
  api: { get, put, post: vi.fn(), del },
  errText: (e: unknown) => String(e),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k),
  }),
}))

const g = (o: Partial<UserGroupRow>): UserGroupRow =>
  ({ id: 2, name: 'Clients', members: 0, weight: null, ...o }) as UserGroupRow

const DEF = g({ id: 1, name: 'Default', is_default: true, weight: 2, allow_urgent: true, max_queued: 5, run_window: '' })

const mount = (target: UserGroupRow, groups: UserGroupRow[] = [DEF, target]) =>
  render(
    <App>
      <OrgUnitDetail group={target} groups={groups} onChanged={vi.fn()} onDeleted={vi.fn()} />
    </App>,
  )

describe('OrgUnitDetail', () => {
  beforeEach(() => {
    put.mockReset().mockResolvedValue({})
    del.mockReset().mockResolvedValue({})
    get.mockReset().mockResolvedValue({ targets: [], granted: [] })
  })

  // The whole point of the rewrite: an inheriting setting must show WHAT it inherits and FROM
  // WHERE. The old form showed a switch labelled "inherit from the default group" above a greyed
  // field with no value in it — and for the run window, a greyed field showing "9-18", which is the
  // example from the hint text and reads exactly like a real value.
  it('names the inherited value and its source, and never shows a bare example as a value', () => {
    mount(g({ id: 2 }))
    const labels = screen.getAllByText(/ou\.inheritedAs/).map((n) => n.textContent ?? '')
    // Max queued inherits 5 from Default; the run window inherits "no restriction", not "9-18".
    expect(labels.some((l) => l.includes('"value":"5"') && l.includes('"from":"Default"'))).toBe(true)
    expect(labels.some((l) => l.includes('ou.anyHour'))).toBe(true)
    expect(labels.some((l) => l.includes('"value":"9-18"'))).toBe(false)
  })

  it('priority names the SYSTEM default, which is a different source from the Default group', () => {
    mount(g({ id: 2 }))
    // And it says it ONCE: "inherit the system default — the system default" is what naming both a
    // source and a value gets you when the source has no value worth printing.
    const label = screen.getByText(/ou\.inheritedFrom/).textContent ?? ''
    expect(label).toContain('ou.systemDefault')
    expect(screen.queryByText(/ou\.inheritedAs.*ou\.systemDefault/)).toBeNull()
  })

  // Choosing "set here" is what reveals the input. A disabled control next to a selected "inherit"
  // is the ambiguity the old form had.
  it('reveals the override input only once the override is chosen', () => {
    mount(g({ id: 2, max_queued: 9 }))
    expect(screen.getByDisplayValue('9')).toBeTruthy()
    // Switch that one back to inheriting and the input goes away.
    const inheritRadios = screen.getAllByText(/ou\.inheritedAs.*"value":"5"/)
    fireEvent.click(inheritRadios[0])
    expect(screen.queryByDisplayValue('9')).toBeNull()
  })

  it('saves nulls for the settings left inheriting, and values for the ones set here', async () => {
    mount(g({ id: 2, max_queued: 9 }))
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(put).toHaveBeenCalled())
    const body = put.mock.calls[0][1] as Record<string, unknown>
    expect(body.max_queued).toBe(9) // set here
    expect(body.run_window).toBeNull() // inheriting
    expect(body.weight).toBeNull() // inheriting
  })

  // The urgent policy is one control mapped onto three stored fields, so "allowed" and "unlimited"
  // can never contradict each other the way the two tags did.
  it('collapses the urgent policy into three consistent fields', async () => {
    mount(g({ id: 2, allow_urgent: true, urgent_unlimited: true, weight: 3 }))
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(put).toHaveBeenCalled())
    const body = put.mock.calls[0][1] as Record<string, unknown>
    expect(body.allow_urgent).toBe(true)
    expect(body.urgent_unlimited).toBe(true)
    expect(body.weight).toBe(0) // a ticket count under "unlimited" would be the contradiction
  })

  it('the Default OU has no parent, no restriction and no delete', () => {
    mount(DEF, [DEF])
    expect(screen.queryByText('users.parentOu')).toBeNull()
    expect(screen.queryByText('ou.sectionTenancy')).toBeNull()
    expect(screen.queryByText('ou.deleteOu')).toBeNull()
  })

  // The second-factor overrides ride with the OU, because that is where they are edited. null is
  // "inherit the parent OU", which is what the InheritField's radio means.
  it('saves the second-factor overrides, with null for the ones left inheriting', async () => {
    mount(g({ id: 2, totp_enroll: false }))
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(put).toHaveBeenCalled())
    const body = put.mock.calls[0][1] as Record<string, unknown>
    expect(body.totp_enroll).toBe(false)
    expect(body.passkey_enroll).toBeNull()
  })

  // The card renders on every OU, including the Default one: the portal-wide switches are global,
  // but the Default OU is still an OU and its members still resolve through it.
  it('offers the second-factor card on the Default OU too', () => {
    mount(DEF, [DEF])
    expect(screen.getByText('ou.sectionSecurity')).toBeTruthy()
  })

  // The mandate rides with the OU too, and its "inherit" is an ancestor rather than the Default group.
  it('saves the second-factor requirement', async () => {
    mount(g({ id: 2, require_2fa: true }))
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(put).toHaveBeenCalled())
    const body = put.mock.calls[0][1] as Record<string, unknown>
    expect(body.require_2fa).toBe(true)
  })

  // The quota is a number AND a window, and both have to survive the round trip together — the
  // server refuses to inherit one without the other, so a panel that sent only the number would
  // silently reset a monthly cap to daily.
  it('saves the quota period alongside the number', async () => {
    mount(g({ id: 2, restricted: true, restricted_effective: true, daily_run_quota: 20, run_quota_period: 'month' }))
    fireEvent.click(screen.getAllByText('common.save')[0]) // a restricted OU also renders the allow-list's save
    await waitFor(() => expect(put).toHaveBeenCalled())
    const body = put.mock.calls[0][1] as Record<string, unknown>
    expect(body.daily_run_quota).toBe(20)
    expect(body.run_quota_period).toBe('month')
  })

  // The quota inherits down the OU TREE, so the hint has to name the ancestor's cap. It used to
  // fall back to 0 and offer "inherit — unlimited" under a parent that caps its subtree at 20.
  it('offers the ancestor’s cap, with its window, as what inheriting would mean', () => {
    const clients = g({ id: 2, name: 'Clients', parent_id: 1, daily_run_quota: 20, run_quota_period: 'month' })
    const acme = g({ id: 3, name: 'Acme', parent_id: 2, restricted: true, restricted_effective: true })
    mount(acme, [DEF, clients, acme])
    const labels = screen.getAllByText(/ou\.inheritedAs/).map((n) => n.textContent ?? '')
    expect(labels.some((l) => l.includes('20 ou.period.month'))).toBe(true)
    expect(labels.some((l) => l.includes('ou.unlimited') && l.includes('users.parentOu'))).toBe(false)
  })

  // The column header is the only way to revoke a surface across a dozen workflows, and it was
  // computed over ALL rows while acting on only the enabled ones. One workflow switched off left
  // the header permanently unchecked, so clicking it could grant the surface and never take it back.
  it('the column toggle reflects — and can clear — the rows it actually governs', async () => {
    get.mockResolvedValue({
      targets: [
        { id: 1, name: 'A', surfaces: ['run', 'batch'] },
        { id: 2, name: 'B', surfaces: ['run', 'batch'] }, // left OFF: the toggle must ignore it
      ],
      granted: [{ target_id: 1, surfaces: ['run'] }],
    })
    mount(g({ id: 3, restricted: true, restricted_effective: true }))
    await screen.findByText('A')

    // Every row the toggle governs (only workflow A) already has 'run', so it reads as checked.
    const runHeader = screen.getByText('users.surface.run').closest('th')!
    const box = runHeader.querySelector('input[type="checkbox"]') as HTMLInputElement
    expect(box.checked).toBe(true)

    // And unticking it revokes, rather than being a no-op that can only ever grant.
    fireEvent.click(box)
    await waitFor(() => expect((runHeader.querySelector('input[type="checkbox"]') as HTMLInputElement).checked).toBe(false))
    fireEvent.click(screen.getAllByText('common.save')[1])
    await waitFor(() => expect(put).toHaveBeenCalled())
    const body = put.mock.calls[put.mock.calls.length - 1][1] as { granted: { target_id: number; surfaces: string[] }[] }
    expect(body.granted.find((x) => x.target_id === 1)?.surfaces).toEqual([])
  })

  it('shows the allow-list only for a restricted OU, where it governs anything', () => {
    mount(g({ id: 2 }))
    expect(screen.queryByText('ou.sectionTargets')).toBeNull()
    mount(g({ id: 3, restricted: true, restricted_effective: true }))
    expect(screen.getAllByText('ou.sectionTargets').length).toBeGreaterThan(0)
  })
})

// Forecast verification access is the one setting here the SERVER resolves: the form shows the
// server's effective value and the unit that decided it, and sends the field only when the admin
// changed it, so saving a name can never rewrite an access decision.
describe('OrgUnitDetail — forecast verification access', () => {
  const withScope = (o: Partial<UserGroupRow>) => g(o)
  const DEFAULT = withScope({
    id: 1, name: 'Default', is_default: true, weight: 2, allow_urgent: true, max_queued: 5, run_window: '',
    forecast_scope: 'none', forecast_scope_effective: 'none', forecast_scope_source: 1,
  })
  const INTERNAL = withScope({
    id: 3, name: 'Internal', parent_id: 1, forecast_scope: 'all', forecast_scope_effective: 'all', forecast_scope_source: 3,
  })

  beforeEach(() => {
    put.mockReset().mockResolvedValue({})
    get.mockReset().mockResolvedValue({ targets: [], granted: [] })
  })

  const body = () => put.mock.calls[0][1] as Record<string, unknown>
  // The field's own container, so a control is never confused with the same control in another card.
  const field = () => screen.getByText('forecast.scope.label').closest('div') as HTMLElement
  const save = async () => {
    fireEvent.click(screen.getAllByText('common.save')[0])
    await waitFor(() => expect(put).toHaveBeenCalled())
  }

  it('shows the field with its help, under the forecast verification card', () => {
    const child = withScope({ id: 2, name: 'Desk', parent_id: 3, forecast_scope: null, forecast_scope_effective: 'all', forecast_scope_source: 3 })
    mount(child, [DEFAULT, INTERNAL, child])
    expect(screen.getByText('forecast.scope.label')).toBeTruthy()
    expect(screen.getByText('forecast.scope.help')).toBeTruthy()
  })

  it('names the inherited value and the unit it comes from, as the server resolved them', () => {
    const child = withScope({ id: 2, name: 'Desk', parent_id: 3, forecast_scope: null, forecast_scope_effective: 'all', forecast_scope_source: 3 })
    mount(child, [DEFAULT, INTERNAL, child])
    const labels = screen.getAllByText(/ou\.inheritedAs/).map((n) => n.textContent ?? '')
    expect(labels.some((l) => l.includes('"from":"Internal"') && l.includes('"value":"forecast.scope.all"'))).toBe(true)
  })

  it('inherits from the Default unit when the unit has no parent', () => {
    const root = withScope({ id: 2, name: 'Root', forecast_scope: null, forecast_scope_effective: 'none', forecast_scope_source: 1 })
    mount(root, [DEFAULT, root])
    const labels = screen.getAllByText(/ou\.inheritedAs/).map((n) => n.textContent ?? '')
    expect(labels.some((l) => l.includes('"from":"Default"') && l.includes('"value":"forecast.scope.none"'))).toBe(true)
  })

  it('offers what inheriting WOULD mean, not the unit\'s own override', () => {
    // Desk overrides to none, under Internal (all): inheriting would give "all" from Internal.
    const child = withScope({ id: 2, name: 'Desk', parent_id: 3, forecast_scope: 'none', forecast_scope_effective: 'none', forecast_scope_source: 2 })
    mount(child, [DEFAULT, INTERNAL, child])
    const labels = screen.getAllByText(/ou\.inheritedAs/).map((n) => n.textContent ?? '')
    expect(labels.some((l) => l.includes('"from":"Internal"') && l.includes('"value":"forecast.scope.all"'))).toBe(true)
  })

  it('offers no inherit option on the Default unit, which inherits from nobody', () => {
    mount(DEFAULT, [DEFAULT])
    expect(screen.getByText('forecast.scope.label')).toBeTruthy()
    // Its stored value is shown in the control instead.
    expect(screen.getByText('forecast.scope.none')).toBeTruthy()
    const labels = screen.queryAllByText(/ou\.inheritedAs.*forecast\.scope/)
    expect(labels).toHaveLength(0)
  })

  it('shows the external protections only for an effectively restricted unit', () => {
    const plain = withScope({ id: 2, name: 'Desk', parent_id: 3, forecast_scope: null, forecast_scope_effective: 'all', forecast_scope_source: 3 })
    const { unmount } = mount(plain, [DEFAULT, INTERNAL, plain])
    expect(screen.queryByText('forecast.scope.external_note')).toBeNull()
    unmount()
    const external = withScope({ id: 4, name: 'Client', parent_id: 3, restricted: true, restricted_effective: true, forecast_scope: 'all', forecast_scope_effective: 'all', forecast_scope_source: 4 })
    mount(external, [DEFAULT, INTERNAL, external])
    expect(screen.getByText('forecast.scope.external_note')).toBeTruthy()
  })

  it('shows the note on a unit that is restricted only through its parent', () => {
    const ext = withScope({ id: 4, name: 'Parent', parent_id: 1, restricted: true, restricted_effective: true, forecast_scope: 'all', forecast_scope_effective: 'all', forecast_scope_source: 4 })
    const child = withScope({ id: 5, name: 'Child', parent_id: 4, restricted: false, restricted_effective: true, forecast_scope: null, forecast_scope_effective: 'all', forecast_scope_source: 4 })
    mount(child, [DEFAULT, ext, child])
    expect(screen.getByText('forecast.scope.external_note')).toBeTruthy()
  })

  it('does not send the field when it was not changed', async () => {
    const child = withScope({ id: 2, name: 'Desk', parent_id: 3, forecast_scope: null, forecast_scope_effective: 'all', forecast_scope_source: 3 })
    mount(child, [DEFAULT, INTERNAL, child])
    await save()
    expect('forecast_scope' in body()).toBe(false)
  })

  it('does not send an unchanged override either', async () => {
    const child = withScope({ id: 2, name: 'Desk', parent_id: 3, forecast_scope: 'own', forecast_scope_effective: 'own', forecast_scope_source: 2 })
    mount(child, [DEFAULT, INTERNAL, child])
    await save()
    expect('forecast_scope' in body()).toBe(false)
  })

  it('sends the chosen scope once the admin picks one', async () => {
    const child = withScope({ id: 2, name: 'Desk', parent_id: 3, forecast_scope: 'own', forecast_scope_effective: 'own', forecast_scope_source: 2 })
    mount(child, [DEFAULT, INTERNAL, child])
    fireEvent.mouseDown(within(field()).getByRole('combobox'))
    fireEvent.click(await screen.findByText('forecast.scope.none', { selector: '.ant-select-item-option-content' }))
    await save()
    expect(body().forecast_scope).toBe('none')
  })

  it('sends null to go back to inheriting, and only then', async () => {
    const child = withScope({ id: 2, name: 'Desk', parent_id: 3, forecast_scope: 'own', forecast_scope_effective: 'own', forecast_scope_source: 2 })
    mount(child, [DEFAULT, INTERNAL, child])
    fireEvent.click(within(field()).getByText(/ou\.inheritedAs/))
    await save()
    expect(body().forecast_scope).toBeNull()
  })

  it('starts overriding from a unit that inherits, sending the chosen value', async () => {
    const child = withScope({ id: 2, name: 'Desk', parent_id: 3, forecast_scope: null, forecast_scope_effective: 'all', forecast_scope_source: 3 })
    mount(child, [DEFAULT, INTERNAL, child])
    fireEvent.click(within(field()).getByText('ou.override'))
    await waitFor(() => expect(within(field()).getByRole('combobox')).toBeTruthy())
    await save()
    // Picking "set here" without choosing a value pins what is in effect, as the other fields do.
    expect(body().forecast_scope).toBe('all')
  })

  it('does not show the field, and sends nothing, when the server did not resolve access', async () => {
    const old = withScope({ id: 2, name: 'Desk', parent_id: 1 })
    mount(old, [DEFAULT, old])
    expect(screen.queryByText('forecast.scope.label')).toBeNull()
    await save()
    expect('forecast_scope' in body()).toBe(false)
  })
})

// Deleting an OU sweeps the announcement grants naming it, which can leave an announcement
// addressed to nobody: still enabled, still "live", reaching no one and reporting nothing. The
// operator is shown that while it is still avoidable, and decides. The server refuses without a
// decision too (409), so this dialog is the place to give one rather than the only thing stopping it.
describe('OrgUnitDetail — deleting an OU that announcements are addressed to', () => {
  beforeEach(() => {
    put.mockReset().mockResolvedValue({})
    del.mockReset().mockResolvedValue({})
  })

  const clickDelete = async () => {
    fireEvent.click(screen.getByRole('button', { name: /ou\.deleteOu/ }))
    // antd Popconfirm's own OK, before the impact dialog appears
    const ok = await screen.findAllByRole('button')
    fireEvent.click(ok.find((b) => /OK|确 定|确定/.test(b.textContent || '')) as HTMLElement)
  }

  it('asks what to do, and sends the answer, when an announcement would be left with nobody', async () => {
    get.mockImplementation((url: string) =>
      url.endsWith('/announcements')
        ? Promise.resolve({ affected: [{ id: 5, title: '华东停电', enabled: true, orphaned: true }] })
        : Promise.resolve({ targets: [], granted: [] }),
    )
    mount(g({ id: 2, name: '华东' }))
    await clickDelete()

    expect(await screen.findByText('华东停电')).toBeTruthy()
    expect(screen.getByText('ou.announcementOrphaned')).toBeTruthy()
    expect(del).not.toHaveBeenCalled() // nothing is deleted until the question is answered

    // The dialog's confirm carries the same label as the page's button, so scope to the dialog.
    const dialog = screen.getByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: /ou\.deleteOu/ }))
    await waitFor(() => expect(del).toHaveBeenCalledWith('/api/admin/groups/2?orphans=disable'))
  })

  it('goes straight through when every affected announcement keeps other recipients', async () => {
    get.mockImplementation((url: string) =>
      url.endsWith('/announcements')
        ? Promise.resolve({ affected: [{ id: 5, title: '两地停电', enabled: true, orphaned: false }] })
        : Promise.resolve({ targets: [], granted: [] }),
    )
    mount(g({ id: 2, name: '华东' }))
    await clickDelete()

    // No decision to make, so no dialog: the announcement goes on working.
    await waitFor(() => expect(del).toHaveBeenCalledWith('/api/admin/groups/2'))
    expect(screen.queryByText('两地停电')).toBeNull()
  })
})
