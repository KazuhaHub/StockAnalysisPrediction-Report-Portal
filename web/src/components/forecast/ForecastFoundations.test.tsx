import { expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ConfigProvider, theme } from 'antd'
import axe from 'axe-core'
import en from '../../locales/en-US.json'
import ForecastLegend from './ForecastLegend'
import ForecastMethodology from './ForecastMethodology'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => en[key as keyof typeof en] ?? key }),
}))

it.each([theme.defaultAlgorithm, theme.darkAlgorithm])('renders accessible shared guidance in each theme', async (algorithm) => {
  const { container } = render(
    <ConfigProvider theme={{ algorithm }}>
      <ForecastLegend />
      <ForecastMethodology />
    </ConfigProvider>,
  )
  expect(screen.getByRole('list', { name: 'Significance legend' })).toBeTruthy()
  expect(screen.getByText(en['forecast.disclaimer'])).toBeTruthy()
  // jsdom cannot measure layout or contrast; the palette suite measures contrast separately.
  const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
  expect(result.violations).toEqual([])
})
