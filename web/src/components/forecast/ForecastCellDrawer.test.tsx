import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import ForecastCellDrawer from './ForecastCellDrawer'
import { ApiError } from '../../api/client'
import type { ForecastCell, ForecastMembersResponse } from '../../api/types'

// P16a: the drawer renders the four-state direction as TEXT, not colour alone.
// P27: the drawer reads only the reader members route, pages by cursor and never states a total.

const getMock = vi.hoisted(() => vi.fn())

vi.mock('../../api/client', async (original) => ({
  ...(await original<typeof import('../../api/client')>()),
  api: { get: getMock },
  qs: () => '',
  errText: (e: unknown) => (e instanceof Error ? e.message : 'failed'),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

function member(set_id: number, direction?: string) {
  return {
    set_id,
    symbol: `60000${set_id}`,
    report_date: '2026-09-10',
    made_at: '2026-09-10T02:00:00Z',
    made_on: '2026-09-10',
    stance: 'long' as const,
    status: 'resolved',
    upside: 0.06,
    direction: direction as 'agree' | undefined,
  }
}

function page(members: ReturnType<typeof member>[], extra: Partial<ForecastMembersResponse> = {}): ForecastMembersResponse {
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

function show(res: ForecastMembersResponse, props: { cell?: ForecastCell | null; label?: string; mode?: 'month' | 'custom' } = {}) {
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
