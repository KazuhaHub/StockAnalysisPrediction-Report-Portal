import { App, theme } from 'antd'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Group } from '../api/types'
import type { CardQuote } from '../lib/useHomeQuotes'
import ReportCard from './ReportCard'

const favoriteState = vi.hoisted(() => ({ active: false, toggle: vi.fn().mockResolvedValue(undefined) }))

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('../api/client', () => ({ errText: (cause: unknown) => String(cause) }))
vi.mock('../favorites', () => ({
  useFavorites: () => ({
    loaded: true,
    loading: false,
    error: null,
    reordering: false,
    ensureLoaded: vi.fn(),
    isFavorite: () => favoriteState.active,
    isBusy: () => false,
    toggle: favoriteState.toggle,
  }),
}))

const report: Group = {
  key: '603075|2026-09-08',
  symbol: '603075',
  market: 'sh',
  name: 'Test stock',
  date: '2026-09-08',
  kind: 'Decision',
  kinds: ['Decision'],
  src: 'new',
  n: 4,
  members: [
    { id: 1, rtype: 'Decision', kind: 'Decision', title: 'Preview report one' },
    { id: 2, rtype: 'Investment', kind: 'Investment', title: 'Preview report two' },
    { id: 3, rtype: 'Research', kind: 'Research', title: 'Preview report three' },
    { id: 4, rtype: 'Technical', kind: 'Technical', title: 'Preview report four' },
  ],
}

function mount(g: Group = report, quote?: CardQuote) {
  return render(
    <App>
      <MemoryRouter>
        <ReportCard g={g} quote={quote} />
        <LocationReadout />
      </MemoryRouter>
    </App>,
  )
}

function LocationReadout() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname}{location.search}</output>
}

describe('ReportCard favorite action', () => {
  beforeEach(() => {
    favoriteState.active = false
    favoriteState.toggle.mockClear()
  })

  it('keeps the symbol and favorite in a dedicated trailing action slot', () => {
    mount()

    const button = screen.getByRole('button', { name: 'favorite.add' })
    const slot = button.closest('.rp-card-symbol-actions')
    expect(slot).not.toBeNull()
    expect(slot?.textContent).toContain(report.symbol)
    expect(button.classList).toContain('rp-card-favorite')
  })

  it('expands the current card after a delay with three summaries and quick actions', async () => {
    mount()

    const card = screen.getByRole('button', { name: 'Test stock' })
    fireEvent.mouseEnter(card)
    expect(screen.queryByTestId('report-card-preview')).toBeNull()

    const preview = await screen.findByTestId('report-card-preview', {}, { timeout: 2000 })
    expect(card.contains(preview)).toBe(true)
    expect(card.classList).toContain('rp-report-card--expanded')
    expect(document.querySelector('.ant-popover')).toBeNull()
    expect(within(preview).getByText('Preview report one')).toBeTruthy()
    expect(within(preview).getByText('Preview report two')).toBeTruthy()
    expect(within(preview).getByText('Preview report three')).toBeTruthy()
    expect(screen.queryByText('Preview report four')).toBeNull()
    expect(within(preview).getByText('+1')).toBeTruthy()
    const viewReport = within(preview).getByRole('button', { name: 'queue.viewReport' })
    expect(viewReport).toBeTruthy()

    fireEvent.click(within(preview).getByRole('button', { name: 'favorite.add' }))
    expect(favoriteState.toggle).toHaveBeenCalledWith('sh', '603075')

    fireEvent.click(viewReport)
    expect(screen.getByTestId('location').textContent).toBe('/stock/603075?date=2026-09-08')
  })
})

describe('ReportCard market phase label', () => {
  // Before the A-share open the vendor answers last = previous close and change 0, which reads as a
  // broken feed. The label says what the number is.
  const flat: CardQuote = {
    symbol: '603289',
    market: 'sh',
    name: 'Test stock',
    currency: 'CNY',
    kind: 'stock',
    last: 943,
    change: 0,
    changePct: '0.00',
    phase: 'pre_open',
  }

  it.each([
    ['pre_open', 'quote.phase.pre_open_prev_close'],
    ['trading', 'quote.session.open'],
    ['lunch_break', 'quote.phase.lunch_break'],
    ['closed', 'quote.session.close'],
    ['holiday', 'quote.phase.holiday'],
  ])('labels the %s phase beside the numbers', (phase, key) => {
    mount(report, { ...flat, phase })
    expect(screen.getByTestId('card-quote-phase').textContent).toBe(key)
    // The close stays visible and labelled: the numbers are still there.
    expect(screen.getByTestId('card-quote').textContent).toBe('9.43')
  })

  it.each(['unknown', '', 'after_hours'])('prints no label for phase %j', (phase) => {
    mount(report, { ...flat, phase })
    expect(screen.queryByTestId('card-quote-phase')).toBeNull()
    expect(screen.getByTestId('card-quote').textContent).toBe('9.43')
  })

  // The previous-close claim is true only while the price has not moved. The call auction and Hong
  // Kong's pre-market publish an indicative last before the bell.
  it('drops the previous-close suffix once a pre-open price has moved', () => {
    mount(report, { ...flat, last: 1037, change: 94, changePct: '9.97' })
    expect(screen.getByTestId('card-quote-phase').textContent).toBe('quote.phase.pre_open')
  })

  it('keeps the suffix only for pre-open: other phases never claim a previous close', () => {
    mount(report, { ...flat, phase: 'closed', change: 0 })
    expect(screen.getByTestId('card-quote-phase').textContent).toBe('quote.session.close')
  })

  it('keeps the label in the theme secondary text colour, ellipsized, with prices that do not shrink', () => {
    let secondary = ''
    function Probe() {
      secondary = theme.useToken().token.colorTextSecondary
      return null
    }
    render(
      <App>
        <MemoryRouter>
          <Probe />
          <ReportCard g={report} quote={flat} />
        </MemoryRouter>
      </App>,
    )
    const label = screen.getByTestId('card-quote-phase')
    const line = screen.getByTestId('card-quote-line')
    expect(secondary).not.toBe('')
    const squash = (c: string) => c.replace(/\s+/g, '')
    expect(squash(label.style.color)).toBe(squash(secondary.startsWith('#') ? hexToRgb(secondary) : secondary))
    expect(label.style.whiteSpace).toBe('nowrap')
    expect(label.style.overflow).toBe('hidden')
    expect(label.style.textOverflow).toBe('ellipsis')
    expect(label.style.minWidth).toBe('0px')
    expect(screen.getByTestId('card-quote').style.flexShrink).toBe('0')
    expect(line.style.height).toBe('22px')
    expect(line.style.overflow).toBe('hidden')
  })
})

function hexToRgb(hex: string) {
  const n = parseInt(hex.slice(1), 16)
  return `rgb(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255})`
}
