import { render, screen, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import QuoteTrafficPanel from './QuoteTrafficPanel'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

it('keeps request and symbol cache ratios separate and identifies percentile bounds', () => {
  render(<QuoteTrafficPanel traffic={{
    live: { requests: 10, completed: 8, cacheHits: 4, merged: 2, loads: 2, failures: 1, stale: 1, p95Millis: 2500 },
    history: { requests: 0, completed: 0, cacheHits: 0, merged: 0, loads: 0, failures: 0, stale: 0, p95Millis: 0 },
    batch: { requests: 2, completed: 2, symbols: 20, cachedSymbols: 15, missingSymbols: 3, merged: 0, loads: 1, p95Millis: 100 },
  }} />)
  expect(within(screen.getByTestId('quote-traffic-live')).getByText('50.0%')).toBeTruthy()
  expect(within(screen.getByTestId('quote-traffic-history')).queryByText('0.0%')).toBeNull()
  expect(screen.getByText('75.0%')).toBeTruthy()
  expect(screen.getByText('2500 ms')).toBeTruthy()
  expect(screen.getByRole('columnheader', { name: 'quoteAdmin.display.p95' })).toBeTruthy()
  expect(screen.getByText('quoteAdmin.display.scope')).toBeTruthy()
})

it('does not invent metrics when an older server omits them', () => {
  const { container } = render(<QuoteTrafficPanel />)
  expect(container.textContent).toBe('')
})
