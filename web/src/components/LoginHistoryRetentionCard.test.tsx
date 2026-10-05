import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import LoginHistoryRetentionCard from './LoginHistoryRetentionCard'

const { apiMock } = vi.hoisted(() => ({ apiMock: { get: vi.fn(), put: vi.fn() } }))
vi.mock('../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
describe('Administrator login history retention', () => {
  beforeEach(() => {
    apiMock.get.mockReset().mockResolvedValue({ keep: 100 })
    apiMock.put.mockReset().mockResolvedValue({ ok: true })
  })
  it('offers retention even before any history exists, and saves a confirmed count', async () => {
    render(<App><LoginHistoryRetentionCard /></App>)
    const input = await screen.findByRole('spinbutton', { name: 'loginActivity.keep' })
    expect((input as HTMLInputElement).value).toBe('100')
    fireEvent.change(input, { target: { value: '3' } })
    fireEvent.blur(input)
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))
    expect(apiMock.put).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByRole('button', { name: 'OK' }))
    await waitFor(() => expect(apiMock.put).toHaveBeenCalledWith('/api/admin/login-activity/retention', { keep: 3 }))
    await waitFor(() => expect((screen.getByRole('button', { name: /common.save/ }) as HTMLButtonElement).disabled).toBe(true))
  })
  it('reports a failed save without changing the displayed history', async () => {
    apiMock.put.mockRejectedValue(new Error('offline'))
    render(<App><LoginHistoryRetentionCard /></App>)
    const input = await screen.findByRole('spinbutton', { name: 'loginActivity.keep' })
    fireEvent.change(input, { target: { value: '5' } })
    fireEvent.blur(input)
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))
    fireEvent.click(await screen.findByRole('button', { name: 'OK' }))
    expect(await screen.findByText('Request failed')).toBeTruthy()
    expect(apiMock.get).toHaveBeenCalledTimes(1)
  })
  it('does not offer an editable default when loading the global policy failed', async () => {
    apiMock.get.mockRejectedValueOnce(new Error('offline'))
    render(<App><LoginHistoryRetentionCard /></App>)
    expect(await screen.findByText('loginActivity.loadFailed')).toBeTruthy()
    expect(screen.queryByRole('spinbutton')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'common.retry' }))
    expect(await screen.findByRole('spinbutton')).toBeTruthy()
  })
})
