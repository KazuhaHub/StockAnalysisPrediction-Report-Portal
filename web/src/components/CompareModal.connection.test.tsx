import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import CompareModal from './CompareModal'

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const lost = () => Object.assign(new Error('network_unavailable'), { code: 'network_unavailable' })
const CANDIDATES = { items: [{ id: 11, date: '2026-06-30', title: 'June', version: '' }, { id: 10, date: '2026-05-31', title: 'May', version: '' }] }
const diffWith = (heading: string, a: number) => ({
  a: { id: a, title: 'A', date: '2026-06-30', symbol: '', name: '', rtype: '', version: '' },
  b: { id: 12, title: 'B', date: '2026-07-31', symbol: '', name: '', rtype: '', version: '' },
  changed: 1,
  sections: [{ heading, level: 2, status: 'changed' as const, lines: [{ op: '+' as const, text: 'x' }] }],
})
const mount = () => render(<App><CompareModal reportId={12} open onClose={vi.fn()} /></App>)

describe('CompareModal while the connection is down', () => {
  beforeEach(() => { apiMock.get.mockReset() })

  it('retries only the diff on reconnect, leaving the list (and the chosen edition) alone', async () => {
    let diffs = 0
    apiMock.get.mockImplementation((url: string) => {
      if (url.includes('/comparable')) return Promise.resolve(CANDIDATES)
      diffs += 1
      return diffs === 1 ? Promise.reject(lost()) : Promise.resolve(diffWith('Valuation', 11))
    })
    mount()
    await waitFor(() => expect(diffs).toBe(1))
    await act(async () => { await Promise.resolve() })
    expect(screen.queryByRole('alert')).toBeNull()
    await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
    expect(await screen.findByText('Valuation')).toBeTruthy()
    expect(apiMock.get.mock.calls.filter(([u]) => String(u).includes('/comparable'))).toHaveLength(1)
  })

  it('applies only the newest diff answer when two requests overlap', async () => {
    const pending: Record<string, (v: unknown) => void> = {}
    apiMock.get.mockImplementation((url: string) => {
      if (url.includes('/comparable')) return Promise.resolve(CANDIDATES)
      return new Promise((resolve) => { pending[url.includes('a=11') ? '11' : '10'] = resolve })
    })
    mount()
    await waitFor(() => expect(pending['11']).toBeTruthy())
    fireEvent.mouseDown(screen.getAllByRole('combobox')[0])
    fireEvent.click(await screen.findByText(/May/, { selector: '.ant-select-item-option-content *, .ant-select-item-option-content' }))
    await waitFor(() => expect(pending['10']).toBeTruthy())
    await act(async () => { pending['10'](diffWith('Newest', 10)); pending['11'](diffWith('Stale', 11)) })
    expect(await screen.findByText('Newest')).toBeTruthy()
    expect(screen.queryByText('Stale')).toBeNull()
  })
})
