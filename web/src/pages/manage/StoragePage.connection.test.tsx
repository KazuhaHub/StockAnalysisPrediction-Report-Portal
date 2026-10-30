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
// The page also holds the login-history retention field, so pick the schedule field by the value the
// config answer gave it.
const numberInputs = () => Array.from(document.body.querySelectorAll<HTMLInputElement>('.ant-input-number input'))

describe('StoragePage on reconnect', () => {
  it('reloads only the history table when only history failed, leaving unsaved schedule edits alone', async () => {
    let history = 0
    let answerConfig!: (value: unknown) => void
    const configAnswer = new Promise((resolve) => { answerConfig = resolve })
    apiMock.get.mockReset().mockImplementation((url: string) => {
      if (url === '/api/admin/login-activity/retention') return Promise.resolve({ keep: 100 })
      if (url.includes('/cleanup/config')) return configAnswer
      if (url.includes('/cleanup/usage')) return Promise.resolve({ ...usage })
      if (url.includes('/cleanup/history')) { history += 1; return history === 1 ? Promise.reject(lost()) : Promise.resolve({ runs: [] }) }
      return Promise.resolve({})
    })
    render(<App><StoragePage /></App>)
    // The config answer is the one write that fills the form; hold it until the rest has settled,
    // then release it in a known order so nothing can overwrite the edit made afterwards.
    await waitFor(() => expect(calls('/cleanup/config')).toBe(1))
    await waitFor(() => expect(history).toBe(1))
    await act(async () => { answerConfig({ ...config, batch_days: 45 }) })
    await waitFor(() => expect(numberInputs().some((i) => i.value === '45')).toBe(true))
    const days = numberInputs().find((i) => i.value === '45')!
    fireEvent.change(days, { target: { value: '120' } })
    expect(days.value).toBe('120')
    await act(async () => { window.dispatchEvent(new Event('rp:connection-restored')) })
    await waitFor(() => expect(history).toBe(2))
    expect(calls('/cleanup/config')).toBe(1)
    expect(days.value).toBe('120')
  })
})
