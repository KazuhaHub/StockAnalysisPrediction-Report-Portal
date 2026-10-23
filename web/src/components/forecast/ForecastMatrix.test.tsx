import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import type { ForecastCell, ForecastGroupsResponse } from '../../api/types'
import ForecastMatrix from './ForecastMatrix'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const mature: ForecastCell = {
  state: 'mature',
  n: 42,
  spread_bp: 310,
  spread_p: 0.012,
  spread_q: 0.041,
}

function resp(cells: Record<string, ForecastCell>): ForecastGroupsResponse {
  return {
    ok: true,
    mode: 'month',
    official: true,
    range: { from: '2026-09-01', to: '2026-09-30' },
    checkpoints: ['d5', 'd20', 'd60', 'd120', 'h12'],
    unrecorded: 0,
    groups: [
      {
        label: '2026-09',
        start: '2026-09-01',
        end: '2026-09-30',
        official: true,
        bench: 'sh000852',
        coverage: {
          reports: 50,
          member: 42,
          duplicate: 5,
          absent: 1,
          rejected: 2,
          backtest: 0,
          degraded: 0,
          mismatch: 0,
          pending_baseline: 0,
          pre_live: 0,
          test: 0,
          off_benchmark: 0,
        },
        cells,
      },
    ],
  }
}

const empty = { d5: { state: 'open' }, d20: { state: 'open' }, d60: { state: 'open' }, d120: { state: 'open' }, h12: { state: 'open' } } as Record<string, ForecastCell>

function show(r: ForecastGroupsResponse, mode: 'month' | 'custom' = 'month', extra: { auditLink?: boolean } = {}) {
  return render(
    <MemoryRouter>
      <ForecastMatrix resp={r} metric="spread" mode={mode} onOpen={() => undefined} {...extra} />
    </MemoryRouter>,
  )
}

// What an external reader with the all scope receives: no coverage, no unrecorded, no metadata, and
// cells that carry a state, or the metrics plus the n >= 30 flag, but never a count.
function withheld(cells: Record<string, ForecastCell>): ForecastGroupsResponse {
  const r = resp(cells)
  delete r.unrecorded
  for (const g of r.groups) delete g.coverage
  return r
}

describe('ForecastMatrix', () => {
  it('stretches every checkpoint to its row height across mixed states', () => {
    show(resp({
      d5: { ...mature, refused: 3 },
      d20: { ...mature, spread_bp: -210, spread_q: 0.2 },
      d60: { state: 'pending', resolved: 31, total: 42 },
      d120: { state: 'small', n: 12 },
      h12: { state: 'open' },
    }))
    const rows = screen.getAllByRole('row')
    expect(rows[1].style.display).toBe('grid')
    expect(rows[1].style.gridTemplateColumns).toBe(rows[0].style.gridTemplateColumns)
    const cells = rows[1].querySelectorAll('td')
    for (const cell of Array.from(cells).slice(1, 6)) {
      expect(cell.style.display).toBe('flex')
      expect(cell.style.alignItems).toBe('stretch')
      const box = cell.firstElementChild as HTMLElement
      expect(box.style.flex).toBe('1 1 0%')
      expect(box.style.height).toBe('')
    }
  })

  it('renders a mature cell with its value, q and n', () => {
    show(resp({ ...empty, d5: mature }))
    expect(screen.getByText('+3.1 pt')).toBeTruthy()
    expect(screen.getByText(/q=0.041/)).toBeTruthy()
    expect(document.body.textContent).toContain('forecast.cov.summary:')
  })

  it.each([
    ['pending', { state: 'pending', resolved: 31, total: 42, spread_bp: 999 } as ForecastCell],
    ['small', { state: 'small', n: 12, spread_bp: 999 } as ForecastCell],
    ['open', { state: 'open', spread_bp: 999 } as ForecastCell],
  ])('shows no number on a %s cell even when the payload carries one', (_state, cell) => {
    show(resp({ ...empty, d5: cell }))
    expect(screen.queryByText('10.0 pt')).toBeNull()
    expect(screen.queryByText('9.99 pt')).toBeNull()
    expect(screen.queryByText(/pt$/)).toBeNull()
  })

  it('shows the progress of a pending checkpoint', () => {
    show(resp({ ...empty, d5: { state: 'pending', resolved: 31, total: 42 } }))
    expect(screen.getByText('forecast.cell.pending:{"done":31,"total":42}')).toBeTruthy()
  })

  it('marks an exploratory group and keeps the p* convention', () => {
    show(
      {
        ...resp({ ...empty, d5: mature }),
        mode: 'custom',
        official: false,
        range: { from: '2026-09-01', to: '2026-09-30', group: 'month' },
        groups: [{ ...resp({ ...empty, d5: mature }).groups[0], official: false }],
      },
      'custom',
    )
    expect(screen.getByText('forecast.tag.exploratory')).toBeTruthy()
    expect(screen.getByText(/p\*=0.012/)).toBeTruthy()
    const labelCell = screen.getByText('2026-09').closest('td')!
    expect(labelCell.style.whiteSpace).not.toBe('nowrap')
    expect(labelCell.style.overflowWrap).toBe('anywhere')
    expect(screen.getByText('forecast.tag.exploratory').parentElement!.style.flexWrap).toBe('wrap')
  })

  it('highlights a rejected count and links it to the audit log', () => {
    show(resp({ ...empty, d5: mature }))
    const link = screen.getByRole('link', { name: /forecast.cov.rejected/ })
    expect(link.getAttribute('href')).toBe('/manage/audit?action=predictions.reject')
  })

  it('expresses significance in text and glyphs, and removes it when q fails the gate', () => {
    const { unmount } = show(resp({ ...empty, d5: mature }))
    expect(screen.getByRole('button').textContent).toContain('▲')
    expect(screen.getByRole('button').textContent).toContain('q<0.05')
    expect(screen.getByRole('button').textContent).toContain('forecast.signal.positive')
    unmount()
    show(resp({ ...empty, d5: { ...mature, spread_q: 0.05 } }))
    expect(screen.getByRole('button').textContent).not.toContain('q<0.05')
    expect(screen.getByRole('button').textContent).not.toContain('▲')
  })

  describe('for a reader who may not see the ledger volume', () => {
    const gated: ForecastCell = { state: 'mature', min_n_met: true, spread_bp: 310, spread_p: 0.012, spread_q: 0.041 }

    it('renders without coverage, without crashing, and says n >= 30 instead of a count', () => {
      show(withheld({
        d5: gated,
        d20: { state: 'pending' },
        d60: { state: 'small' },
        d120: { state: 'open' },
        h12: { state: 'purged' },
      }))
      expect(screen.getByText('+3.1 pt')).toBeTruthy()
      expect(document.body.textContent).toContain('forecast.cell.min_n_met')
      // No coverage column, no coverage cell, no count of any kind.
      expect(screen.queryByText('forecast.col.coverage')).toBeNull()
      expect(document.body.textContent).not.toContain('forecast.cov.')
      expect(document.body.textContent).not.toMatch(/forecast\.cell\.n:/)
      expect(document.body.textContent).not.toContain('"n":0')
      expect(document.body.textContent).not.toContain('"done":0')
      expect(document.body.textContent).not.toContain('"total":0')
    })

    it('states the publication gate once per cell, as its own badge, and not inside the metric line', () => {
      show(withheld({ ...empty, d5: gated }))
      const badge = screen.getByText('forecast.cell.min_n_met')
      // The explanation is visible text and not a hover title, which a keyboard or a phone never sees.
      expect(badge.hasAttribute('title')).toBe(false)
      expect(screen.getAllByText('forecast.cell.min_n_met')).toHaveLength(1)
      // The line under the value is the corrected value alone: no sample is claimed for the metric.
      expect(screen.getByText('q=0.041')).toBeTruthy()
    })

    it('explains the gate in one visible line, tied to every flagged cell', () => {
      show(withheld({ ...empty, d5: gated, d20: gated, d60: { ...gated, spread_bp: -200 } }))
      const hint = screen.getByText('forecast.cell.min_n_met_hint')
      expect(hint.tagName).not.toBe('TITLE')
      expect(hint.id).not.toBe('')
      const buttons = screen.getAllByRole('button')
      expect(buttons).toHaveLength(3)
      for (const button of buttons) expect(button.getAttribute('aria-describedby')).toBe(hint.id)
      expect(screen.getAllByText('forecast.cell.min_n_met_hint')).toHaveLength(1)
    })

    it('says nothing about the gate where every mature cell carries its count', () => {
      show(resp({ ...empty, d5: mature }))
      expect(screen.queryByText('forecast.cell.min_n_met_hint')).toBeNull()
      expect(screen.getByRole('button').hasAttribute('aria-describedby')).toBe(false)
    })

    it('does not explain a gate that no cell shows', () => {
      show(withheld({ ...empty, d5: { state: 'pending' } }))
      expect(screen.queryByText('forecast.cell.min_n_met_hint')).toBeNull()
    })

    it.each(['ic', 'hit', 'fill'] as const)('never reads the cell gate as the %s sample size', (metric) => {
      const cell: ForecastCell = { ...gated, ic: 0.21, ic_q: 0.047, hit: 0.61, hit_null: 0.55, hit_q: 0.3, fill: 0.33, fill_ret_bp: -120 }
      render(
        <MemoryRouter>
          <ForecastMatrix resp={withheld({ ...empty, d5: cell })} metric={metric} mode="month" onOpen={() => undefined} />
        </MemoryRouter>,
      )
      const button = screen.getByRole('button')
      expect(screen.getAllByText('forecast.cell.min_n_met')).toHaveLength(1)
      // The badge is a sibling of the metric line, never part of it.
      const sub = Array.from(button.querySelectorAll('span')).find((n) => n.textContent?.includes('q=') || n.textContent?.includes('after_fill'))!
      expect(sub.textContent).not.toContain('forecast.cell.min_n_met')
    })

    it('adds no badge to a cell that carries its count', () => {
      show(resp({ ...empty, d5: mature }))
      expect(screen.queryByText('forecast.cell.min_n_met')).toBeNull()
    })

    it('shows a pending cell with no progress bar when the server sent no counts', () => {
      const { container } = show(withheld({ ...empty, d5: { state: 'pending' } }))
      expect(screen.getByText('forecast.cell.pending_plain')).toBeTruthy()
      expect(container.querySelector('[style*="height: 4px"]')).toBeNull()
    })

    it('says a small cell is below the gate without stating how many names it has', () => {
      show(withheld({ ...empty, d5: { state: 'small' } }))
      expect(screen.getByText('forecast.cell.small_nocount')).toBeTruthy()
      expect(screen.getByText('forecast.cell.small_hint')).toBeTruthy()
    })

    it('names a purged cell instead of leaving it blank', () => {
      show(resp({ ...empty, d5: { state: 'purged' } }))
      expect(screen.getByText('forecast.cell.purged')).toBeTruthy()
    })

    it('keeps the coverage column for a reader who is allowed volume', () => {
      show(resp({ ...empty, d5: mature }))
      expect(screen.getByText('forecast.col.coverage')).toBeTruthy()
    })

    it('leaves the audit link to administrators', () => {
      show(resp({ ...empty, d5: mature }), 'month', { auditLink: false })
      expect(screen.queryByRole('link', { name: /forecast.cov.rejected/ })).toBeNull()
      expect(document.body.textContent).toContain('forecast.cov.rejected')
    })
  })
})
