import { expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { ConfigProvider, theme } from 'antd'
import axe from 'axe-core'
import QuoteLedgerStatus, { type QuoteLedgerState } from './QuoteLedgerStatus'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, options?: Record<string, unknown>) => options ? `${key}:${JSON.stringify(options)}` : key }),
}))

const configured: QuoteLedgerState = {
  ledgerChain: ['tencent', 'peer'], ledgerPrimary: 'tencent', ledgerShadowPeer: 'peer',
}
const sourceLabel = (name: string) => `Source ${name}`
const stamp = (value: string) => value ? `Time ${value}` : ''

it('distinguishes unavailable telemetry from an observed empty history', () => {
  const view = render(<QuoteLedgerStatus state={configured} sourceLabel={sourceLabel} stamp={stamp} />)
  expect(screen.getByText('quoteAdmin.ledgerUnavailable')).toBeTruthy()
  expect(screen.queryByText('quoteAdmin.ledgerNoHistory')).toBeNull()
  expect(screen.getByText('quoteAdmin.ledgerConfigOnly')).toBeTruthy()
  expect(screen.getByText('quoteAdmin.ledgerChain:{"sources":"Source tencent, Source peer"}')).toBeTruthy()
  expect(screen.getByText('quoteAdmin.ledgerShadow:{"source":"Source peer"}')).toBeTruthy()
  view.rerender(<QuoteLedgerStatus state={{ ...configured, ledgerHealth: [] }} sourceLabel={sourceLabel} stamp={stamp} />)
  expect(screen.getByText('quoteAdmin.ledgerNoHistory')).toBeTruthy()
  expect(screen.queryByText('quoteAdmin.ledgerUnavailable')).toBeNull()
})

it('renders source-specific failures and never-successful telemetry without claiming readiness', () => {
  render(<QuoteLedgerStatus state={{ ...configured, ledgerHealth: [
    { source: 'tencent', lastSuccess: '', lastError: 'upstream refused', lastErrorAt: '2026-10-22', consecutiveFailures: 3 },
    { source: 'peer', lastSuccess: '2026-10-21', lastError: '', lastErrorAt: '', consecutiveFailures: 0 },
  ] }} sourceLabel={sourceLabel} stamp={stamp} />)
  const rows = within(screen.getByTestId('quote-ledger-health')).getAllByRole('listitem')
  expect(rows).toHaveLength(2)
  expect(within(rows[0]).getByText('quoteAdmin.lastSuccess: quoteAdmin.never')).toBeTruthy()
  expect(within(rows[0]).getByText('quoteAdmin.failures: 3')).toBeTruthy()
  expect(within(rows[0]).getByText('upstream refused')).toBeTruthy()
  expect(within(rows[1]).getByText('quoteAdmin.lastSuccess: Time 2026-10-21')).toBeTruthy()
  expect(within(rows[1]).queryByText('upstream refused')).toBeNull()
})

it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('has accessible diagnostic markup in each theme', async (algorithm) => {
  const { container } = render(<ConfigProvider theme={{ algorithm }}>
    <QuoteLedgerStatus state={{ ledgerChain: [], ledgerPrimary: '', ledgerHealth: [] }} sourceLabel={sourceLabel} stamp={stamp} />
  </ConfigProvider>)
  expect(screen.getByRole('alert').textContent).toContain('quoteAdmin.ledgerNoPrimary')
  // jsdom cannot measure visual contrast or layout.
  const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
  expect(result.violations).toEqual([])
})
