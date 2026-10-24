import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConfigProvider, theme } from 'antd'
import axe from 'axe-core'
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
    expect(screen.getByRole('link').getAttribute('href')).toBe('#forecast')
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

  it('shows the disclaimer when the chip takes keyboard focus, not only on hover', async () => {
    render(
      <MemoryRouter>
        <ForecastSummaryChip prediction={{ has_prediction: true, display_set_id: 7, stance: 'long', weight_bp: 6500, upside_bp: 1234, body_changed: false }} />
      </MemoryRouter>,
    )
    expect(screen.queryByText('forecast.disclaimer')).toBeNull()
    await userEvent.tab()
    const link = screen.getByRole('link')
    expect(document.activeElement).toBe(link)
    const disclaimer = await screen.findByText('forecast.disclaimer')
    // And it is the link's description, so a screen reader reads it with the chip.
    const described = (link.getAttribute('aria-describedby') ?? '').split(' ').map((id) => document.getElementById(id)?.textContent)
    expect(described).toContain(disclaimer.textContent)
  })

  it('shows the disclaimer on focus for the edition-without-a-set link too', async () => {
    render(
      <MemoryRouter>
        <ForecastSummaryChip prediction={{ has_prediction: false, sibling: { report_id: 9, set_id: 2, version: 'internal', cohort: '2026-10', url: '/stock/600519?r=9#forecast' } }} />
      </MemoryRouter>,
    )
    await act(async () => { screen.getByRole('link').focus() })
    expect(await screen.findByText('forecast.disclaimer')).toBeTruthy()
  })

  it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('has no axe violations with the disclaimer open in each theme', async (algorithm) => {
    const { container, baseElement } = render(
      <ConfigProvider theme={{ algorithm }}>
        <MemoryRouter>
          <ForecastSummaryChip prediction={{ has_prediction: true, display_set_id: 7, stance: 'long', weight_bp: 6500, upside_bp: 1234, body_changed: false }} />
        </MemoryRouter>
      </ConfigProvider>,
    )
    await userEvent.tab()
    await screen.findByText('forecast.disclaimer')
    // jsdom cannot measure layout or contrast; the palette suite measures contrast separately.
    const result = await axe.run(baseElement, { rules: { 'color-contrast': { enabled: false }, region: { enabled: false } } })
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html.slice(0, 120)).join(' | ')}`)).toEqual([])
    expect(container.querySelector('a[href="#forecast"]')).toBeTruthy()
  })
})
