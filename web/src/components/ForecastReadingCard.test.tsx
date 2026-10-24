import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { ConfigProvider, theme } from 'antd'
import axe from 'axe-core'
import { MemoryRouter } from 'react-router'
import type { PredictionMark, PredictionSet } from '../api/types'
import en from '../locales/en-US.json'
import ForecastReadingCard from './ForecastReadingCard'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, values?: { date?: string; months?: number }) => {
      if (values?.date && k.startsWith('forecast.phase.')) return `${k}: ${values.date}`
      if (k === 'forecast.reading.horizon_until_standard') return `${k}: ${values?.date} (${values?.months})`
      return k
    },
  }),
}))

const accepted: PredictionSet = {
  id: 7,
  report_id: 14088,
  state: 'accepted',
  symbol: '600519',
  rtype: '投资决策建议',
  title: '',
  version: 'V3.17',
  made_at: '2026-10-18T13:00:00Z',
  made_on: '2026-10-18',
  cohort: '2026-10',
  zone: 'Asia/Shanghai',
  bench_code: 'sh000852',
  horizon_std: true,
  baseline_check: 'match',
  body_changed: false,
  claims: [
    { key: 'stance', kind: 'stance', stance: 'long' },
    { key: 'expected_terminal', kind: 'terminal_value', price_fen: 149775, prob_bp: 7500 },
    { key: 'scenario.bear', kind: 'scenario', label: 'bear', price_fen: 100000, prob_bp: 2500 },
    { key: 'scenario.base', kind: 'scenario', label: 'base', price_fen: 154700, prob_bp: 5000 },
    { key: 'scenario.bull', kind: 'scenario', label: 'bull', price_fen: 189700, prob_bp: 2500 },
    { key: 'entry_price', kind: 'entry_price', price_fen: 132900 },
  ],
  s0_fen: 19004,
  s0_date: '2026-09-18',
  marks: [{ checkpoint: 'd5', status: 'resolved', bar_date: '2026-09-28', close_fen: 21128, ret_bp: 1117, bench_ret_bp: -511 }],
}

function show(sets: PredictionSet[], extra: Partial<React.ComponentProps<typeof ForecastReadingCard>> = {}) {
  return render(
    <MemoryRouter>
      <ForecastReadingCard sets={sets} otherSets={[]} symbol="600519" {...extra} />
    </MemoryRouter>,
  )
}

describe('ForecastReadingCard', () => {
  it('honors the forecast deep link after asynchronous card loading', async () => {
    const original = Element.prototype.scrollIntoView
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    window.history.replaceState(null, '', '#forecast')
    try {
      const view = show([], { loading: true })
      expect(scroll).not.toHaveBeenCalled()
      view.rerender(<MemoryRouter><ForecastReadingCard sets={[accepted]} otherSets={[]} symbol="600519" /></MemoryRouter>)
      await waitFor(() => expect(scroll).toHaveBeenCalledWith({ block: 'start' }))
    } finally {
      window.history.replaceState(null, '', window.location.pathname)
      Element.prototype.scrollIntoView = original
    }
  })
  it('shows settlement paused separately from a mark phase', () => {
    show([{ ...accepted, marks: [{ checkpoint: 'd5', status: 'pending', phase: 'settling' }] }], { settlementPaused: true })
    expect(screen.getByText('forecast.phase.settlement_paused')).toBeTruthy()
    expect(screen.getByText('forecast.phase.settling')).toBeTruthy()
    expect(screen.queryByText('forecast.phase.retrying')).toBeNull()
  })
  it('qualifies only calendar-backed projections as expected', () => {
    show([{ ...accepted, marks: [
      { checkpoint: 'd20', status: 'pending', phase: 'waiting', not_before: '2026-10-26', expected: true },
      { checkpoint: 'd120', status: 'pending', phase: 'waiting', not_before: '2027-03-15' },
    ] }])
    expect(screen.getAllByText(/forecast.phase.expected/)).toHaveLength(1)
    expect(screen.getAllByText(/forecast.phase.on_or_after/)).toHaveLength(1)
    expect(screen.getByText('forecast.phase.expected: 2026-10-26')).toBeTruthy()
  })
  it('distinguishes a report without a set from unavailable forecast access', () => {
    show([{ ...accepted, state: 'absent' }])
    expect(screen.getByText('forecast.reading.no_forecast')).toBeTruthy()
  })

  it('shows the claims that were written, with the entry labelled derived', () => {
    show([accepted])
    expect(screen.getByText('forecast.panel.terminal')).toBeTruthy()
    expect(screen.getByText(/1,497\.75/)).toBeTruthy()
    expect(screen.getByText(/forecast\.panel\.entry_derived/)).toBeTruthy()
    // The counted/not-counted badge is gone until real membership exists: every set used to claim
    // "not counted", which was not a fact about that set.
    expect(screen.queryByText('forecast.panel.counted')).toBeNull()
    expect(screen.queryByText('forecast.panel.not_counted')).toBeNull()
  })

  it('translates the decision quality', () => {
    show([{ ...accepted, decision_quality: 'degraded' }])
    expect(screen.getByText('forecast.quality.degraded')).toBeTruthy()
    expect(screen.queryByText(/forecast\.panel\.quality/)).toBeNull()
  })

  it('says a pending mark is waiting, never that fetching failed', () => {
    show([
      {
        ...accepted,
        marks: [
          { checkpoint: 'd5', status: 'pending', phase: 'waiting', not_before: '2026-10-26' },
          { checkpoint: 'd20', status: 'refused', phase: 'waiting', not_before: '2026-11-16' },
        ],
      },
    ])
    expect(screen.getAllByText(/forecast\.phase\.on_or_after/).length).toBe(2)
    expect(screen.queryByText(/forecast\.mark\.refused/)).toBeNull()
  })

  it('labels a test run and shows no checkpoint table', () => {
    show([{ ...accepted, state: 'test', marks: [] }])
    expect(screen.getByText('forecast.panel.test_run')).toBeTruthy()
    expect(screen.queryByText('forecast.panel.checkpoints')).toBeNull()
    // A test run is never "counted", even if a stale payload says it was.
    expect(screen.queryByText('forecast.panel.counted')).toBeNull()
  })

  it('shows the accepted set rather than a newer test set for the same report', () => {
    show([accepted, { ...accepted, id: 8, state: 'test', marks: [] }], {
      countedSetID: accepted.id,
    })
    expect(screen.getByText('forecast.panel.checkpoints')).toBeTruthy()
    expect(screen.queryByText('forecast.panel.test_run')).toBeNull()
  })

  it('warns when the report body changed after the set was written', () => {
    show([{ ...accepted, body_changed: true }])
    expect(screen.getByText('forecast.panel.body_changed')).toBeTruthy()
  })

  it('labels the latest close as not scored', () => {
    show([accepted], { latestClose: 17101, latestDate: '2026-10-16' })
    const line = screen.getByText(/forecast\.panel\.latest_close/)
    expect(line.textContent).toContain('forecast.panel.latest_close')
    // The wording is the promise: a price printed beside the marks must not read as one of them.
    expect(line.textContent).not.toContain('forecast.panel.ret')
  })

  it('lists other sets for the same stock and links only the live ones', () => {
    show([accepted], {
      otherSets: [
        accepted,
        { ...accepted, id: 9, report_id: 14001, made_on: '2026-09-17' },
        { ...accepted, id: 10, report_id: undefined, made_on: '2026-08-15' },
      ],
    })
    expect(screen.getByText('forecast.panel.others')).toBeTruthy()
    expect(screen.getAllByRole('link').some((a) => a.getAttribute('href') === '/stock/600519?date=2026-09-17&kind=&r=14001#forecast')).toBe(true)
  })

  it('shows membership, baseline detail and the disclaimer', () => {
    show([{ ...accepted, membership: 'member', baseline_emitted_fen: 19004, baseline_as_of: '2026-09-18' }])
    expect(screen.getByText('forecast.reading.counted')).toBeTruthy()
    expect(screen.getByText('forecast.reading.reported_close')).toBeTruthy()
    expect(screen.getByText('forecast.disclaimer')).toBeTruthy()
  })

  it('shows the display revision even when another revision counted', () => {
    show([accepted, { ...accepted, id: 8, claims: [{ key: 'expected_terminal', kind: 'terminal_value', price_fen: 222200 }], made_at: '2026-10-19T13:00:00Z' }], { displaySetID: 8, countedSetID: 7 })
    expect(screen.getByText(/2,222\.00/)).toBeTruthy()
    expect(screen.queryByText(/1,497\.75/)).toBeNull()
  })

  it('hides membership, orphaned rows and payload for a protected reader', () => {
    show([{ ...accepted, membership: 'member', payload: 'private-payload' }], {
      allowMembership: false, allowPayload: false, allowOrphans: false,
      otherSets: [{ ...accepted, id: 9, report_id: undefined, report_date: '2025-01-01' }],
    })
    expect(screen.queryByText('forecast.reading.counted')).toBeNull()
    expect(screen.queryByText('private-payload')).toBeNull()
    expect(screen.queryByText('2025-01-01')).toBeNull()
  })

  it('does not expose outcome numbers on a pending mark carrying stale metric fields', () => {
    show([{ ...accepted, marks: [{ checkpoint: 'd5', status: 'pending', phase: 'waiting', ret_bp: 1234, close_fen: 888888 }] }])
    expect(screen.queryByText('+12.34%')).toBeNull()
    expect(screen.queryByText('8,888.88')).toBeNull()
  })

  describe('checkpoints that apply to the set', () => {
    const through120: PredictionMark[] = (['d5', 'd20', 'd60', 'd120'] as const).map((checkpoint) => ({ checkpoint, status: 'resolved' as const, bar_date: '2026-09-28', close_fen: 21128, ret_bp: 100, bench_ret_bp: 50 }))
    const rowKeys = () => Array.from(document.querySelectorAll('tbody tr[data-row-key]')).map((row) => row.getAttribute('data-row-key'))

    it('shows no h12 step or row when the set has no standard horizon', () => {
      show([{ ...accepted, horizon_std: false, horizon_end: '2027-01-15', marks: through120 }])
      expect(screen.queryByText('forecast.cp.h12')).toBeNull()
      expect(rowKeys()).toEqual(['d5', 'd20', 'd60', 'd120'])
      expect(document.querySelectorAll('.ant-steps-item')).toHaveLength(4)
    })

    it('ignores an old pending h12 row on a set without a standard horizon', () => {
      show([{ ...accepted, horizon_std: false, marks: [...through120, { checkpoint: 'h12', status: 'pending', phase: 'waiting' }] }])
      expect(screen.queryByText('forecast.cp.h12')).toBeNull()
      expect(rowKeys()).not.toContain('h12')
    })

    it('waits for h12 only on a standard-horizon set', () => {
      show([{ ...accepted, horizon_std: true, marks: through120 }])
      expect(rowKeys()).toEqual(['d5', 'd20', 'd60', 'd120', 'h12'])
      expect(document.querySelectorAll('.ant-steps-item')).toHaveLength(5)
    })
  })

  describe('horizon', () => {
    it('states the end date of a standard horizon in the demo form', () => {
      show([{ ...accepted, horizon_std: true, horizon_end: '2027-09-17' }])
      expect(screen.getByText('forecast.reading.horizon_until_standard: 2027-09-17 (12)')).toBeTruthy()
      expect(screen.queryByText('forecast.reading.standard_horizon')).toBeNull()
    })

    it('words the standard horizon as until the date, with months and standard', () => {
      const text = en['forecast.reading.horizon_until_standard' as keyof typeof en] as string
      expect(text.replace('{{date}}', '2027-09-17').replace('{{months}}', '12')).toBe('until 2027-09-17 (12 months, standard)')
    })

    it('still says the horizon is standard when the end date is missing', () => {
      show([{ ...accepted, horizon_std: true, horizon_end: undefined }])
      expect(screen.getByText('forecast.reading.standard_horizon')).toBeTruthy()
    })

    it('shows only the date of a non-standard horizon', () => {
      show([{ ...accepted, horizon_std: false, horizon_end: '2027-01-15' }])
      expect(screen.getByText('2027-01-15')).toBeTruthy()
    })
  })

  describe('coverage', () => {
    it('renders nothing for a report the ledger does not cover', () => {
      const { container } = show([], { covered: false })
      expect(container.querySelector('#forecast')).toBeNull()
      expect(screen.queryByText('forecast.reading.no_forecast')).toBeNull()
      expect(screen.queryByText('forecast.disclaimer')).toBeNull()
    })

    it('keeps the empty card for a covered report that made no forecast', () => {
      const { container } = show([], { covered: true })
      expect(container.querySelector('#forecast')).toBeTruthy()
      expect(screen.getByText('forecast.reading.no_forecast')).toBeTruthy()
    })

    it('keeps the empty card when the server did not say', () => {
      show([])
      expect(screen.getByText('forecast.reading.no_forecast')).toBeTruthy()
    })

    it('still shows a set that exists whatever the coverage flag says', () => {
      show([accepted], { covered: false })
      expect(screen.getByText('forecast.panel.terminal')).toBeTruthy()
    })

    it('holds the slot while loading, before coverage is known', () => {
      const { container } = show([], { loading: true, covered: false })
      expect(container.querySelector('#forecast')).toBeTruthy()
    })
  })

  it('puts the meta line inside the card', () => {
    const { container } = show([accepted], { reportDate: '2026-09-18', receivedAt: 'unparsed', edition: 'V3.17' })
    expect(container.querySelector('#forecast')?.textContent).toContain('forecast.reading.meta')
  })

  describe('legibility', () => {
    const parse = (c: string): number[] => {
      const fn = c.match(/^rgba?\(([^)]+)\)$/)
      if (fn) {
        const [r, g, b, a = 1] = fn[1].split(',').map((v) => Number(v.trim()))
        return [r, g, b, a]
      }
      const h = c.replace('#', '')
      return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16), h.length === 8 ? parseInt(h.slice(6, 8), 16) / 255 : 1]
    }
    const over = (f: number[], b: number[]) => [0, 1, 2].map((i) => f[i] * f[3] + b[i] * (1 - f[3]))
    const lum = (rgb: number[]) => {
      const ch = (v: number) => {
        const x = v / 255
        return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4
      }
      return 0.2126 * ch(rgb[0]) + 0.7152 * ch(rgb[1]) + 0.0722 * ch(rgb[2])
    }
    const contrast = (a: number[], b: number[]) => {
      const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x)
      return (hi + 0.05) / (lo + 0.05)
    }

    it.each([
      ['light', theme.defaultAlgorithm],
      ['dark', theme.darkAlgorithm],
    ] as const)('paints the scenario band visibly, falling to rising, in the %s theme', (_, algorithm) => {
      const t = theme.getDesignToken({ algorithm })
      render(
        <ConfigProvider theme={{ algorithm }}>
          <MemoryRouter>
            <ForecastReadingCard sets={[accepted]} otherSets={[]} symbol="600519" />
          </MemoryRouter>
        </ConfigProvider>,
      )
      const stops: string[] = JSON.parse(screen.getByTestId('forecast-scenario-band').getAttribute('data-stops') || '[]')
      // Bear to bull reads as fall to rise: the page's own convention is red for a rise, green for a fall.
      expect(stops).toEqual([t.colorSuccess, t.colorTextTertiary, t.colorError])
      const bg = over(parse(t.colorBgContainer), [0, 0, 0])
      for (const stop of stops) expect(contrast(over(parse(stop), bg), bg)).toBeGreaterThanOrEqual(2)
    })

    it('keeps the card content clear of its border, like the other cards on the page', () => {
      const { container } = show([accepted])
      const body = container.querySelector('#forecast .ant-card-body') as HTMLElement
      expect(body.style.padding).toBe('16px 24px')
      const head = container.querySelector('#forecast .ant-card-head') as HTMLElement
      expect(head.style.paddingInline).toBe('24px')
    })
  })

  describe('accessibility', () => {
    it('names the scenario band as a group', () => {
      show([accepted])
      expect(screen.getByRole('group', { name: 'forecast.panel.scenarios' })).toBeTruthy()
    })

    it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('has no axe violations for a full card in each theme', async (algorithm) => {
      const { container } = render(
        <ConfigProvider theme={{ algorithm }}>
          <MemoryRouter>
            <ForecastReadingCard
              sets={[{ ...accepted, horizon_end: '2027-09-17', membership: 'member', payload: '{"schema":"predictions.v1"}', marks: [
                { checkpoint: 'd5', status: 'resolved', bar_date: '2026-09-28', close_fen: 21128, ret_bp: 1117, bench_ret_bp: -511, low_fen: 120000, source: 'tencent' },
                { checkpoint: 'd20', status: 'pending', phase: 'waiting', not_before: '2026-10-26' },
              ] }]}
              otherSets={[{ ...accepted, id: 9, report_id: 14001, made_on: '2026-09-17' }]}
              symbol="600519"
              reportDate="2026-09-18"
              receivedAt="2026-09-18T05:00:00Z"
              edition="V3.17"
              latestClose={17101}
              latestDate="2026-10-16"
              settlementPaused
            />
          </MemoryRouter>
        </ConfigProvider>,
      )
      // jsdom cannot measure layout or contrast; the palette suite measures contrast separately.
      const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
      expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html.slice(0, 120)).join(' | ')}`)).toEqual([])
    })

    it('has no axe violations for the empty and test-run cards', async () => {
      const empty = show([], { covered: true })
      const emptyResult = await axe.run(empty.container, { rules: { 'color-contrast': { enabled: false } } })
      expect(emptyResult.violations.map((v) => v.id)).toEqual([])
      empty.unmount()
      const test = show([{ ...accepted, state: 'test', marks: [] }])
      const testResult = await axe.run(test.container, { rules: { 'color-contrast': { enabled: false } } })
      expect(testResult.violations.map((v) => v.id)).toEqual([])
    })
  })
})
