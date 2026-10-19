import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import ForecastLedgerStart from './ForecastLedgerStart'
import type { ForecastPopulation } from '../../api/types'

// The ledger-start dialog (P5). Four things here are invisible in review and each has a failure mode:
//
//   - the confirmation must require the admin to type the instant AGAIN, because the value is
//     write-once and survives restores;
//   - a set value must render read-only — there is no edit affordance, because there is no edit;
//   - a refusal shows its own mapped message, not a generic failure;
//   - the wire value is the UTC instant, not the Shanghai wall clock that was typed.

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))

vi.mock('../../api/client', () => ({ api: apiMock, errText: (_e: unknown, t: (k: string) => string) => t('err.forecast_population_set') }))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))

function renderIt(value: ForecastPopulation) {
  const onSaved = vi.fn()
  const onClose = vi.fn()
  render(
    <App>
      <MemoryRouter>
        <ForecastLedgerStart open value={value} onClose={onClose} onSaved={onSaved} />
      </MemoryRouter>
    </App>,
  )
  return { onSaved, onClose }
}

const unset: ForecastPopulation = {
  set: false,
  bound: '2026-10-19T07:00:00Z',
  candidates: [
    { id: 1, symbol: '600160', rtype: '投资决策建议', made_at: '2026-10-18T13:00:00Z', state: 'accepted', orphan: false },
  ],
}

const set: ForecastPopulation = { set: true, from: '2026-10-18T16:00:00Z', candidates: [] }

beforeEach(() => {
  apiMock.get.mockReset()
  apiMock.post.mockReset()
})

describe('ForecastLedgerStart', () => {
  it('requires the typed value before the confirm button enables', async () => {
    renderIt(unset)
    fireEvent.change(screen.getByLabelText('forecast.population.instant'), { target: { value: '2026-10-19T00:00:00' } })
    fireEvent.click(screen.getByText('forecast.population.save'))

    const confirm = await screen.findByText('forecast.population.confirm_save')
    // The confirm button is disabled until the instant is retyped exactly.
    expect((confirm.closest('button') as HTMLButtonElement).disabled).toBe(true)

    const retype = screen.getByLabelText('forecast.population.type_again')
    fireEvent.change(retype, { target: { value: '2026-10-19T00:00:00' } })
    await waitFor(() => expect((confirm.closest('button') as HTMLButtonElement).disabled).toBe(false))
  })

  it('posts the UTC instant derived from the Shanghai wall clock typed', async () => {
    apiMock.post.mockResolvedValue({ ok: true })
    const { onSaved } = renderIt(unset)

    // The Shanghai 2026-10-19 00:00 is UTC 2026-10-18T16:00:00Z.
    fireEvent.change(screen.getByLabelText('forecast.population.instant'), { target: { value: '2026-10-19T00:00:00' } })
    fireEvent.click(screen.getByText('forecast.population.save'))
    fireEvent.change(screen.getByLabelText('forecast.population.type_again'), { target: { value: '2026-10-19T00:00:00' } })
    fireEvent.click(screen.getByText('forecast.population.confirm_save'))

    await waitFor(() => expect(apiMock.post).toHaveBeenCalled())
    const [url, body] = apiMock.post.mock.calls[0]
    expect(url).toBe('/api/admin/forecast')
    expect(body.population_from).toBe('2026-10-18T16:00:00Z')
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
  })

  it('renders a set value read-only with no save affordance', () => {
    renderIt(set)
    expect(screen.getByText('forecast.population.from')).toBeTruthy()
    expect(screen.queryByText('forecast.population.save')).toBeNull()
    expect(screen.queryByLabelText('forecast.population.instant')).toBeNull()
  })

  it('shows the mapped message for a refusal', async () => {
    apiMock.post.mockRejectedValue(new Error('409'))
    renderIt(unset)
    fireEvent.change(screen.getByLabelText('forecast.population.instant'), { target: { value: '2026-10-19T00:00:00' } })
    fireEvent.click(screen.getByText('forecast.population.save'))
    fireEvent.change(screen.getByLabelText('forecast.population.type_again'), { target: { value: '2026-10-19T00:00:00' } })
    fireEvent.click(screen.getByText('forecast.population.confirm_save'))
    await waitFor(() => expect(screen.getByText('err.forecast_population_set')).toBeTruthy())
  })
})
