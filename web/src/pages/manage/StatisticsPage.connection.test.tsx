import { act, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { expect, it, vi } from 'vitest'
import StatisticsPage from './StatisticsPage'

const { get, t } = vi.hoisted(() => ({ get: vi.fn(), t: (key: string) => key }))
vi.mock('./DashboardChart', () => ({ default: ({ rows }: { rows: { name: string }[] }) => <div data-testid="statistics-chart">{rows.map((r) => <span key={r.name}>{r.name}</span>)}</div> }))
vi.mock('../../api/client', () => ({ api: { get }, errText: () => 'Failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t }) }))

const lost = () => Object.assign(new Error('network_unavailable'), { code: 'network_unavailable' })

it('stays busy with no empty state while the connection is down, and queries again on reconnect', async () => {
  get.mockReset().mockRejectedValueOnce(lost()).mockResolvedValue({ items: [{ label: 'Research', count: 12 }], total: 1, records: 12 })
  render(<App><StatisticsPage /></App>)
  await waitFor(() => expect(get).toHaveBeenCalledTimes(1))
  await act(async () => { await Promise.resolve() })
  expect(screen.queryByText('statistics.noData')).toBeNull()
  expect(screen.getByText('common.loading')).toBeTruthy()
  await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
  expect(await screen.findByText('Research')).toBeTruthy()
})
