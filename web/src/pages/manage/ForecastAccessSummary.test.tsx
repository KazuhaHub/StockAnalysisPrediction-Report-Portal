import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { MemoryRouter } from 'react-router'
import type { ForecastAccessOverview, UserGroupRow } from '../../api/types'
import ForecastAccessSummary from './ForecastAccessSummary'

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../../api/client', () => ({ api: apiMock, errText: (e: unknown) => (e instanceof Error ? e.message : 'failed') }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const group = (id: number, name: string, extra: Partial<UserGroupRow> = {}) =>
  ({ id, name, members: 0, weight: null, ...extra }) as UserGroupRow

const overview: ForecastAccessOverview = {
  ok: true,
  preview: false,
  affected: 0,
  counts: { all: 12, own: 5, none: 31 },
  groups: [
    { id: 1, forecast_scope: 'none', forecast_scope_effective: 'none', forecast_scope_source: 1, external: false, counts: { all: 0, own: 0, none: 9 } },
    { id: 2, forecast_scope: 'all', forecast_scope_effective: 'all', forecast_scope_source: 2, external: false, counts: { all: 12, own: 0, none: 0 } },
    { id: 3, forecast_scope: null, forecast_scope_effective: 'all', forecast_scope_source: 2, external: true, counts: { all: 0, own: 0, none: 0 } },
  ],
}
const groups = [group(1, 'Default', { is_default: true }), group(2, 'Internal'), group(3, 'Client A', { parent_id: 2 })]

function show() {
  return render(
    <MemoryRouter>
      <ForecastAccessSummary />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  apiMock.get.mockReset()
  apiMock.get.mockImplementation((url: string) =>
    Promise.resolve(url === '/api/admin/forecast/access' ? overview : { groups }))
})

describe('ForecastAccessSummary', () => {
  it('reads the server-resolved access, never computing it in the browser', async () => {
    show()
    await screen.findByText('forecast.access.summary:{"all":12,"own":5,"none":31}')
    expect(apiMock.get.mock.calls.map(([u]) => u).sort()).toEqual(['/api/admin/forecast/access', '/api/admin/groups'])
  })

  it('shows each unit with what it sets, what applies, and which unit decided', async () => {
    show()
    const row = (await screen.findByText('Client A')).closest('tr')!
    // Client A sets nothing itself: it inherits "all" from Internal, and is external.
    expect(within(row).getByText('forecast.access.inherited')).toBeTruthy()
    expect(within(row).getByText('forecast.scope.all')).toBeTruthy()
    expect(within(row).getByText('Internal')).toBeTruthy()
    expect(within(row).getByText('forecast.access.external')).toBeTruthy()
    const internal = screen.getByText('Internal', { selector: 'a' }).closest('tr')!
    expect(within(internal).getByText('12 / 0 / 0')).toBeTruthy()
  })

  it('links to the OU editor, in general and for each unit', async () => {
    show()
    expect((await screen.findByRole('link', { name: 'forecast.access.edit' })).getAttribute('href')).toBe('/manage/forecast?scope=ou')
    expect(screen.getByRole('link', { name: 'Client A' }).getAttribute('href')).toBe('/manage/forecast?scope=ou&ou=3')
  })

  it('says a unit has no deciding source when none applies', async () => {
    apiMock.get.mockImplementation((url: string) =>
      Promise.resolve(url === '/api/admin/forecast/access'
        ? { ...overview, groups: [{ ...overview.groups[0], forecast_scope: null, forecast_scope_source: 0 }] }
        : { groups }))
    show()
    const row = (await screen.findByText('Default')).closest('tr')!
    expect(within(row).getAllByText('—').length).toBeGreaterThan(0)
  })

  it('fails visibly, with a retry, instead of presenting an empty table as the truth', async () => {
    apiMock.get.mockRejectedValueOnce(new Error('offline'))
    apiMock.get.mockRejectedValueOnce(new Error('offline'))
    show()
    expect(await screen.findByText('offline')).toBeTruthy()
    expect(screen.queryByText(/forecast.access.summary/)).toBeNull()
    apiMock.get.mockImplementation((url: string) =>
      Promise.resolve(url === '/api/admin/forecast/access' ? overview : { groups }))
    fireEvent.click(screen.getByRole('button', { name: /common.retry/ }))
    await waitFor(() => expect(screen.getByRole('link', { name: 'Internal' })).toBeTruthy())
  })

  it('has no axe violations', async () => {
    const { container } = show()
    await screen.findByText('Client A')
    const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html.slice(0, 100)).join(' | ')}`)).toEqual([])
  })
})
