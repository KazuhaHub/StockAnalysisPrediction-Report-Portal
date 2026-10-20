import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import ForecastCellDrawer from './ForecastCellDrawer'
import type { ForecastMembersResponse } from '../../api/types'

// P16a: the drawer renders the four-state direction as TEXT, not colour alone.

const res = vi.hoisted(() => ({ value: undefined as ForecastMembersResponse | undefined }))
const getMock = vi.hoisted(() => vi.fn(async () => res.value))

vi.mock('../../api/client', () => ({
  api: { get: getMock },
  qs: () => '',
}))

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

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

function show(direction?: string) {
  res.value = {
    ok: true,
    label: '2026-09',
    start: '2026-09-01',
    end: '2026-09-30',
    checkpoint: 'd5',
    state: 'mature',
    resolved: 4,
    total: 4,
    refused: 0,
    corp_action: 0,
    n: 4,
    members: [member(1, direction)],
  }
  return render(
    <MemoryRouter>
      <ForecastCellDrawer
        open
        onClose={() => undefined}
        mode="month"
        from=""
        to=""
        group="month"
        label="2026-09"
        checkpoint="d5"
        cell={null}
      />
    </MemoryRouter>,
  )
}

describe('ForecastCellDrawer direction', () => {
  it.each([
    ['agree', 'forecast.direction.agree'],
    ['disagree', 'forecast.direction.disagree'],
    ['flat', 'forecast.direction.flat'],
    ['untested', 'forecast.direction.untested'],
  ])('renders %s as its translated text', async (dir, key) => {
    show(dir)
    await waitFor(() => expect(screen.getByText(key)).toBeTruthy())
  })

  it('renders a dash when the non-mature payload carries no direction', async () => {
    show(undefined)
    await waitFor(() => expect(screen.getByText('600001')).toBeTruthy())
    expect(screen.queryByText('forecast.direction.agree')).toBeNull()
  })
})
