import { describe, it, expect, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import StoragePage from './StoragePage'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const lost = () => Object.assign(new Error('network_unavailable'), { code: 'network_unavailable' })
const config = { freq: 'daily', time: '03:00', weekday: 1, monthday: 1, batch_enabled: true, batch_days: 90, tokens_enabled: false, tokens_grace_days: 30, reports_enabled: false, reports_days: 730, batch_floor: 7, reports_floor: 365, last_run_period: '', last_result: null }
const usage = { db_bytes: 2048, categories: [{ key: 'batch', rows: 12, bytes: 1000, eligible: 3, oldest: '', newest: '' }] }
const calls = (frag: string) => apiMock.get.mock.calls.filter(([u]) => String(u).includes(frag)).length

describe('StoragePage on reconnect', () => {
  it('reloads only the history table when only history failed, leaving unsaved schedule edits alone', async () => {
    let history = 0
    apiMock.get.mockReset().mockImplementation((url: string) => {
      if (url === '/api/admin/login-activity/retention') return Promise.resolve({ keep: 100 })
      if (url.includes('/cleanup/config')) return Promise.resolve({ ...config })
      if (url.includes('/cleanup/usage')) return Promise.resolve({ ...usage })
      if (url.includes('/cleanup/history')) { history += 1; return history === 1 ? Promise.reject(lost()) : Promise.resolve({ runs: [] }) }
      return Promise.resolve({})
    })
    render(<App><StoragePage /></App>)
    await screen.findAllByText('storage.cat.batch')
    // The usage cards can paint before the config answer lands; edit only once the form holds it.
    await waitFor(() => expect((document.body.querySelector('.ant-input-number input') as HTMLInputElement).value).not.toBe(''))
    await act(async () => { await new Promise((r) => setTimeout(r, 30)) })
    const days = document.body.querySelector('.ant-input-number input') as HTMLInputElement
    fireEvent.change(days, { target: { value: '120' } })
    await act(async () => { await Promise.resolve() })
    await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
    await waitFor(() => expect(history).toBe(2))
    expect(calls('/cleanup/config')).toBe(1)
    expect((document.body.querySelector('.ant-input-number input') as HTMLInputElement).value).toBe('120')
  })
})
