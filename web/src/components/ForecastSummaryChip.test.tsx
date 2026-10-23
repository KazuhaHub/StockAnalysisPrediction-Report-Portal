import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import ForecastSummaryChip from './ForecastSummaryChip'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

describe('ForecastSummaryChip', () => {
  it('shows stance, weight and signed upside with the disclaimer tooltip', async () => {
    render(
      <MemoryRouter>
        <ForecastSummaryChip prediction={{ has_prediction: true, display_set_id: 7, stance: 'long', weight_bp: 6500, upside_bp: 1234, body_changed: false }} />
      </MemoryRouter>,
    )
    expect(screen.getByTestId('forecast-summary-chip').textContent).toContain('+12.3%')
    expect(screen.getByTestId('forecast-summary-chip').textContent).toContain('65%')
    fireEvent.mouseOver(screen.getByTestId('forecast-summary-chip'))
    expect(await screen.findByText('forecast.disclaimer')).toBeTruthy()
  })

  it('links an edition without a set to its readable sibling', () => {
    render(
      <MemoryRouter>
        <ForecastSummaryChip prediction={{ has_prediction: false, sibling: { report_id: 9, set_id: 2, version: 'internal', cohort: '2026-10', url: '/stock/600519?r=9#forecast' } }} />
      </MemoryRouter>,
    )
    expect(screen.getByRole('link').getAttribute('href')).toBe('/stock/600519?r=9#forecast')
  })
})
