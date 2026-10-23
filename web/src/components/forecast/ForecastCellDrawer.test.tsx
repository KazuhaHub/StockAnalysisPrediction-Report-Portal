import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import ForecastCellDrawer from './ForecastCellDrawer'
import { ApiError } from '../../api/client'
import type { ForecastCell, ForecastMember, ForecastMembersResponse } from '../../api/types'

// P16a: the drawer renders the four-state direction as TEXT, not colour alone.
// P27: the drawer reads only the reader members route, pages by cursor and never states a total.

const getMock = vi.hoisted(() => vi.fn())

vi.mock('../../api/client', async (original) => ({
  ...(await original<typeof import('../../api/client')>()),
  api: { get: getMock },
  errText: (e: unknown) => (e instanceof Error ? e.message : 'failed'),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

function member(set_id: number, direction?: string): ForecastMember {
  return {
    set_id,
    symbol: `60000${set_id}`,
    report_date: '2026-09-10',
    made_at: '2026-09-10T02:00:00Z',
    made_on: '2026-09-10',
    stance: 'long' as const,
    status: 'resolved',
    upside: 0.06,
    direction: direction as ForecastMember['direction'],
  }
}

function page(members: ForecastMember[], extra: Partial<ForecastMembersResponse> = {}): ForecastMembersResponse {
  return {
    ok: true,
    label: '2026-09',
    start: '2026-09-01',
    end: '2026-09-30',
    checkpoint: 'd5',
    state: 'mature',
    members,
    ...extra,
  }
}

function show(res: ForecastMembersResponse, props: { cell?: ForecastCell | null; label?: string; mode?: 'month' | 'custom'; rtype?: string } = {}) {
  getMock.mockResolvedValue(res)
  return render(
    <MemoryRouter>
      <ForecastCellDrawer
        open
        onClose={() => undefined}
        mode={props.mode ?? 'month'}
        from=""
        to=""
        group="month"
        label={props.label ?? '2026-09'}
        checkpoint="d5"
        cell={props.cell ?? null}
        rtype={props.rtype}
      />
    </MemoryRouter>,
  )
}

beforeEach(() => getMock.mockReset())

describe('ForecastCellDrawer direction', () => {
  it.each([
    ['agree', 'forecast.direction.agree'],
    ['disagree', 'forecast.direction.disagree'],
    ['flat', 'forecast.direction.flat'],
    ['untested', 'forecast.direction.untested'],
  ])('renders %s as its translated text', async (dir, key) => {
    show(page([member(1, dir)]))
    await waitFor(() => expect(screen.getByText(key)).toBeTruthy())
  })

  it('draws the agreeing and the halted tags in explicit, readable colours rather than antd\'s presets', async () => {
    // antd's green preset measures 3.4:1 and its warning preset 1.8:1 in the light theme.
    getMock.mockResolvedValue(page([{ ...member(1, 'agree'), halted: true }]))
    render(
      <MemoryRouter>
        <ForecastCellDrawer open onClose={() => undefined} mode="month" from="" to="" group="month" label="2026-09" checkpoint="d5" cell={{ state: 'mature', n: 42 }} />
      </MemoryRouter>,
    )
    const agree = await screen.findByText('forecast.direction.agree')
    const halted = screen.getByText('forecast.mark.halted')
    for (const tag of [agree, halted]) {
      expect(tag.className).not.toMatch(/ant-tag-(green|success|warning|orange)/)
      expect(tag.style.color).not.toBe('')
    }
  })

  it('renders no verdict when the payload carries no direction', async () => {
    show(page([member(1, undefined)]))
    await waitFor(() => expect(screen.getByText('600001')).toBeTruthy())
    for (const key of ['agree', 'disagree', 'flat', 'untested']) {
      expect(screen.queryByText(`forecast.direction.${key}`)).toBeNull()
    }
  })
})

describe('ForecastCellDrawer reader route', () => {
  it('asks only the reader members route, with a bounded page, and never an admin route', async () => {
    show(page([member(1, 'agree')]), { mode: 'custom' })
    await waitFor(() => expect(getMock).toHaveBeenCalled())
    const url = String(getMock.mock.calls[0][0])
    expect(url.startsWith('/api/forecast/groups/members?')).toBe(true)
    expect(url).not.toContain('/api/admin/')
    const query = new URLSearchParams(url.split('?')[1])
    expect(query.get('label')).toBe('2026-09')
    expect(query.get('checkpoint')).toBe('d5')
    expect(query.get('mode')).toBe('custom')
    expect(query.get('limit')).toBe('100')
    expect(query.has('cursor')).toBe(false)
  })

  it('loads the next page by cursor, appends it, and states no total', async () => {
    getMock.mockResolvedValueOnce(page([member(1, 'agree'), member(2, 'agree')], { next_cursor: 'abc_-9' }))
    getMock.mockResolvedValueOnce(page([member(3, 'agree')]))
    render(
      <MemoryRouter>
        <ForecastCellDrawer open onClose={() => undefined} mode="month" from="" to="" group="month" label="2026-09" checkpoint="d5" cell={null} />
      </MemoryRouter>,
    )
    await screen.findByText('600001')
    expect(screen.queryByText('600003')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'forecast.drawer.load_more' }))
    await screen.findByText('600003')
    expect(screen.getByText('600001')).toBeTruthy()
    expect(new URLSearchParams(String(getMock.mock.calls[1][0]).split('?')[1]).get('cursor')).toBe('abc_-9')
    // The last page has no cursor, so there is nothing more to offer.
    expect(screen.queryByRole('button', { name: 'forecast.drawer.load_more' })).toBeNull()
    expect(document.body.textContent).not.toMatch(/forecast\.drawer\.total/)
  })

  it('does not show one cell\'s rows while another cell is loading, and discards the late answer', async () => {
    let resolveFirst!: (value: ForecastMembersResponse) => void
    getMock.mockImplementationOnce(() => new Promise<ForecastMembersResponse>((done) => { resolveFirst = done }))
    getMock.mockResolvedValueOnce(page([member(7, 'agree')], { label: '2026-08' }))
    const view = render(
      <MemoryRouter>
        <ForecastCellDrawer open onClose={() => undefined} mode="month" from="" to="" group="month" label="2026-09" checkpoint="d5" cell={null} />
      </MemoryRouter>,
    )
    view.rerender(
      <MemoryRouter>
        <ForecastCellDrawer open onClose={() => undefined} mode="month" from="" to="" group="month" label="2026-08" checkpoint="d5" cell={null} />
      </MemoryRouter>,
    )
    await screen.findByText('600007')
    await act(async () => resolveFirst(page([member(1, 'agree')], { label: '2026-09' })))
    expect(screen.queryByText('600001')).toBeNull()
    expect(screen.getByText('600007')).toBeTruthy()
  })

  it('shows a refusal instead of an empty list, with a way to try again', async () => {
    getMock.mockRejectedValueOnce(new ApiError(429, 'slow down', 'forecast_rate_limited'))
    render(
      <MemoryRouter>
        <ForecastCellDrawer open onClose={() => undefined} mode="month" from="" to="" group="month" label="2026-09" checkpoint="d5" cell={null} />
      </MemoryRouter>,
    )
    await screen.findByText('slow down')
    expect(getMock).toHaveBeenCalledTimes(1)
    getMock.mockResolvedValueOnce(page([member(1, 'agree')]))
    fireEvent.click(screen.getByRole('button', { name: 'common.retry' }))
    await screen.findByText('600001')
  })
})

describe('ForecastCellDrawer without counts', () => {
  const gated: ForecastCell = { state: 'mature', min_n_met: true, spread_bp: 310, spread_p: 0.012, spread_q: 0.041 }

  it('shows the n >= 30 flag, no headline counts and no zero, for a cell that carries no count', async () => {
    show(page([member(1, 'agree')]), { cell: gated })
    await screen.findByText('600001')
    expect(screen.getAllByText('forecast.cell.min_n_met').length).toBeGreaterThan(0)
    expect(document.body.textContent).not.toContain('forecast.drawer.headline')
    expect(document.body.textContent).not.toContain('"members":0')
  })

  it('says in the drawer that the flag belongs to the cell and a metric may rest on fewer', async () => {
    show(page([member(1, 'agree')]), { cell: gated })
    await screen.findByText('600001')
    expect(screen.getByText('forecast.cell.min_n_met_hint')).toBeTruthy()
  })

  it('has no gate explanation for a cell that carries its count', async () => {
    show(page([member(1, 'agree')]), { cell: { ...gated, min_n_met: undefined, n: 42 } })
    await screen.findByText('600001')
    expect(screen.queryByText('forecast.cell.min_n_met_hint')).toBeNull()
  })

  it('puts the flag only where the denominator is the cell\'s own n, and a dash on every other metric', async () => {
    // The hit, IC, fill, band and calibration denominators are smaller subsets with no minimum of
    // their own, so "n >= 30" beside them would be a statement the server never made.
    const full: ForecastCell = {
      state: 'mature', min_n_met: true,
      spread_bp: 310, spread_p: 0.012, spread_q: 0.041,
      ic: 0.2, ic_p: 0.1, ic_q: 0.2,
      hit: 0.6, hit_null: 0.5, hit_p: 0.1, hit_q: 0.2,
      fill: 0.3, band: 0.5, calib: 0.55,
    }
    show(page([member(1, 'agree')]), { cell: full })
    await screen.findByText('600001')
    const lastCell = (metric: string) => {
      const row = screen.getByText(`forecast.metric.${metric}`).closest('tr')!
      const cellsInRow = within(row).getAllByRole('cell')
      return cellsInRow[cellsInRow.length - 1].textContent
    }
    expect(lastCell('spread')).toBe('forecast.cell.min_n_met')
    for (const metric of ['ic', 'hit', 'fill', 'band', 'calib']) expect(lastCell(metric)).toBe('—')
  })

  it('keeps the headline counts for a reader who is allowed them', async () => {
    show(page([member(1, 'agree')]), { cell: { ...gated, min_n_met: undefined, n: 42, refused: 2, corp_action: 1 } })
    await screen.findByText('600001')
    expect(screen.getByText('forecast.drawer.headline:{"members":42,"refused":2,"corp":1}')).toBeTruthy()
  })
})

// A cell that is not mature has no outcome to show, but its members are settled: who is in the
// cohort and what each is waiting on. The drawer therefore has two modes, and the second must not
// leak a number the first would show.
describe('ForecastCellDrawer membership mode', () => {
  const pending: ForecastCell = { state: 'pending', resolved: 3, total: 5 }
  const row = (set_id: number, excluded_reason?: string): ForecastMember => ({
    ...member(set_id, 'agree'),
    status: excluded_reason === 'pending' ? 'pending' : 'resolved',
    excluded_reason: excluded_reason as ForecastMember['excluded_reason'],
    excess_bp: 250,
    upside: 0.08,
    close_fen: 10400,
  })

  it('asks for the whole cohort, with the report type, and not for scored members', async () => {
    show(page([row(1, 'pending')], { population: 'all', state: 'pending' }), { cell: pending, rtype: 'Decision memo' })
    await screen.findByText('600001')
    const query = new URLSearchParams(String(getMock.mock.calls[0][0]).split('?')[1])
    expect(query.get('population')).toBe('all')
    expect(query.get('rtype')).toBe('Decision memo')
  })

  it('asks for scored members only, with no population, for a mature cell, and no report type unless one is chosen', async () => {
    show(page([member(1, 'agree')]), { cell: { state: 'mature', n: 42 } })
    await screen.findByText('600001')
    const query = new URLSearchParams(String(getMock.mock.calls[0][0]).split('?')[1])
    expect(query.has('population')).toBe(false)
    expect(query.has('rtype')).toBe(false)
  })

  it('lists each member with the reason it is not yet a scored result, and no outcome column', async () => {
    show(page([row(1, 'pending'), row(2, 'pending_baseline'), row(3, 'refused'), row(4, 'non_standard_horizon'), row(5, 'corp_action'), row(6)], { population: 'all', state: 'pending' }), { cell: pending })
    await screen.findByText('600001')
    for (const code of ['pending', 'pending_baseline', 'refused', 'non_standard_horizon', 'corp_action']) {
      expect(screen.getByText(`forecast.drawer.reason.${code}`)).toBeTruthy()
    }
    expect(screen.getByText('forecast.drawer.reason.settled')).toBeTruthy()
    for (const header of ['upside', 'excess', 'agreement']) expect(screen.queryByText(`forecast.drawer.${header}`)).toBeNull()
  })

  it('renders no outcome value even when the payload carries one on a member of a non-mature cell', async () => {
    show(page([row(1, 'pending')], { population: 'all', state: 'pending' }), { cell: pending })
    await screen.findByText('600001')
    expect(document.body.textContent).not.toMatch(/8\.0%|\+2\.5%|forecast\.direction\./)
  })

  it('has no metrics table, and says what the mode is for', async () => {
    show(page([row(1, 'pending')], { population: 'all', state: 'pending' }), { cell: pending })
    await screen.findByText('600001')
    expect(screen.queryByText('forecast.drawer.metric')).toBeNull()
    expect(screen.getByText('forecast.drawer.membership_note')).toBeTruthy()
    expect(screen.getByText('forecast.drawer.mode_membership')).toBeTruthy()
  })

  it('treats a reason it does not know as an unknown status rather than echoing it', async () => {
    show(page([row(1, 'something_new')], { population: 'all', state: 'pending' }), { cell: pending })
    await screen.findByText('600001')
    expect(screen.getByText('forecast.drawer.reason.unknown_mark_status')).toBeTruthy()
    expect(document.body.textContent).not.toContain('something_new')
  })

  it('does not show the previous mode\'s rows while the next mode loads, and discards its late answer', async () => {
    let resolveFirst!: (value: ForecastMembersResponse) => void
    getMock.mockImplementationOnce(() => new Promise<ForecastMembersResponse>((done) => { resolveFirst = done }))
    getMock.mockResolvedValueOnce(page([row(9, 'pending')], { population: 'all', state: 'pending' }))
    const tree = (cell: ForecastCell) => (
      <MemoryRouter>
        <ForecastCellDrawer open onClose={() => undefined} mode="month" from="" to="" group="month" label="2026-09" checkpoint="d5" cell={cell} />
      </MemoryRouter>
    )
    const view = render(tree({ state: 'mature', n: 42 }))
    view.rerender(tree(pending))
    await screen.findByText('600009')
    await act(async () => resolveFirst(page([member(1, 'agree')])))
    expect(screen.queryByText('600001')).toBeNull()
    expect(screen.getByText('600009')).toBeTruthy()
  })

  it('lists readable members without a total for a reader who may not know the volume', async () => {
    show(page([row(1, 'pending')], { population: 'all', state: 'pending' }), { cell: { state: 'pending' } })
    await screen.findByText('600001')
    expect(screen.getByText('forecast.drawer.reason.pending')).toBeTruthy()
    expect(document.body.textContent).not.toMatch(/"done"|"total"|"members":0/)
  })
})

describe('ForecastCellDrawer mature content', () => {
  const full: ForecastCell = {
    state: 'mature', n: 42, refused: 1, corp_action: 2,
    spread_bp: 310, spread_p: 0.012, spread_q: 0.041,
    ic: 0.21, ic_n: 40, ic_p: 0.02, ic_q: 0.047,
    hit: 0.61, hit_n: 38, hit_null: 0.55, hit_p: 0.18, hit_q: 0.3,
    fill: 0.33, fill_ret_bp: 230,
  }

  it('signs the spread and the IC, and states the return after a fill as its own row', async () => {
    show(page([member(1, 'agree')]), { cell: full })
    await screen.findByText('600001')
    const rowOf = (name: string) => screen.getByText(name).closest('tr')!
    expect(within(rowOf('forecast.metric.spread')).getByText('+3.1 pt')).toBeTruthy()
    expect(within(rowOf('forecast.metric.ic')).getByText('+0.21')).toBeTruthy()
    expect(within(rowOf('forecast.drawer.after_fill')).getByText('+2.3%')).toBeTruthy()
  })

  it('says a one-armed spread has no spread, rather than leaving the row out', async () => {
    show(page([member(1, 'agree')]), { cell: { ...full, spread_bp: undefined, spread_p: undefined, spread_q: undefined, spread_one_arm: true } })
    await screen.findByText('600001')
    const row = screen.getByText('forecast.metric.spread').closest('tr')!
    expect(within(row).getByText('forecast.cell.one_arm')).toBeTruthy()
    expect(within(row).getAllByText('—').length).toBeGreaterThan(0)
  })

  it('lists the policy slices with their own size and their own gate', async () => {
    const cell: ForecastCell = {
      ...full,
      slices: [
        { state: 'mature', n: 36, spread_bp: 120, spread_p: 0.2, spread_q: 0.3, ic: -0.05, ic_p: 0.6, ic_q: 0.7, hit: 0.5, hit_null: 0.5, hit_p: 0.9, hit_q: 0.9, policy_version: 'v3-1' },
        { state: 'small', n: 12, policy_version: 'v3-2' },
      ],
    }
    show(page([member(1, 'agree')]), { cell })
    await screen.findByText('600001')
    expect(screen.getByText('forecast.drawer.slices')).toBeTruthy()
    const mature = screen.getByText('v3-1').closest('tr')!
    expect(within(mature).getByText('+1.2 pt')).toBeTruthy()
    expect(within(mature).getByText('-0.05')).toBeTruthy()
    const small = screen.getByText('v3-2').closest('tr')!
    expect(small.textContent).toContain('forecast.cell.small:{"n":12}')
    expect(small.textContent).not.toMatch(/pt/)
  })

  it('draws no slice section for a cell with a single policy', async () => {
    show(page([member(1, 'agree')]), { cell: full })
    await screen.findByText('600001')
    expect(screen.queryByText('forecast.drawer.slices')).toBeNull()
  })

  it('states a slice\'s gate where its count is withheld, and no count', async () => {
    const cell: ForecastCell = { ...full, n: undefined, min_n_met: true, slices: [{ state: 'mature', min_n_met: true, spread_bp: 120, spread_p: 0.2, spread_q: 0.3, policy_version: 'v3-1' }, { state: 'small', policy_version: 'v3-2' }] }
    show(page([member(1, 'agree')]), { cell })
    await screen.findByText('600001')
    expect(screen.getByText('v3-1').closest('tr')!.textContent).toContain('forecast.cell.min_n_met')
    expect(screen.getByText('v3-2').closest('tr')!.textContent).toContain('forecast.cell.small_nocount')
    expect(document.body.textContent).not.toContain('forecast.cell.n:')
  })

  it('signs a member\'s upside and excess, and tags a halted row', async () => {
    getMock.mockResolvedValue(page([{ ...member(1, 'agree'), excess_bp: -125, upside: 0.06, halted: true }, member(2, 'agree')]))
    render(
      <MemoryRouter>
        <ForecastCellDrawer open onClose={() => undefined} mode="month" from="" to="" group="month" label="2026-09" checkpoint="d5" cell={full} />
      </MemoryRouter>,
    )
    await screen.findByText('600001')
    const halted = screen.getByText('600001').closest('tr')!
    expect(within(halted).getByText('-1.3%')).toBeTruthy()
    expect(within(halted).getByText('+6.0%')).toBeTruthy()
    expect(within(halted).getByText('forecast.mark.halted')).toBeTruthy()
    expect(within(screen.getByText('600002').closest('tr')!).queryByText('forecast.mark.halted')).toBeNull()
  })

  it('marks an exploratory range in the title and says its p is not corrected', async () => {
    show(page([member(1, 'agree')]), { cell: full, mode: 'custom' })
    await screen.findByText('600001')
    expect(screen.getByRole('dialog').textContent).toContain('forecast.tag.exploratory')
    expect(screen.getByText('forecast.custom.banner')).toBeTruthy()
  })

  it('reports p* alone in an exploratory range, because a q there would claim a correction it does not make', async () => {
    show(page([member(1, 'agree')]), { cell: full, mode: 'custom' })
    await screen.findByText('600001')
    // A horizontally scrolling table repeats its header for measurement, so count rather than find one.
    expect(screen.getAllByText('forecast.drawer.p_star').length).toBeGreaterThan(0)
    expect(screen.queryAllByText('forecast.drawer.q')).toHaveLength(0)
  })

  it('reports p and the corrected q on the record', async () => {
    show(page([member(1, 'agree')]), { cell: full })
    await screen.findByText('600001')
    expect(screen.getAllByText('forecast.drawer.p').length).toBeGreaterThan(0)
    expect(screen.getAllByText('forecast.drawer.q').length).toBeGreaterThan(0)
    expect(screen.queryAllByText('forecast.drawer.p_star')).toHaveLength(0)
  })

  it('does not mark the monthly record as exploratory', async () => {
    show(page([member(1, 'agree')]), { cell: full })
    await screen.findByText('600001')
    expect(screen.getByRole('dialog').textContent).not.toContain('forecast.tag.exploratory')
    expect(screen.queryByText('forecast.custom.banner')).toBeNull()
  })

  it('links a member to its exact report by id', async () => {
    show(page([{ ...member(1, 'agree'), report_id: 77, kind: 'decision', name: 'Moutai' }]), { cell: full })
    const link = await screen.findByRole('link', { name: 'Moutai' })
    const href = link.getAttribute('href')!
    expect(href.startsWith('/stock/600001?')).toBe(true)
    const query = new URLSearchParams(href.split('?')[1])
    expect(query.get('r')).toBe('77')
    expect(query.get('date')).toBe('2026-09-10')
    expect(query.get('kind')).toBe('decision')
  })
})

