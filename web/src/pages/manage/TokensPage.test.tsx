import { fireEvent, render, screen } from '@testing-library/react'
import { App } from 'antd'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TokensPage from './TokensPage'

const api = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), del: vi.fn() }))
vi.mock('../../api/client', () => ({ api, errText: () => 'Offline' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
beforeEach(() => { api.get.mockReset().mockResolvedValue({ tokens: [
  { id: 1, name: 'Warehouse', prefix: 'abc', scope: 'ingest', expires: '', lastUsed: '' },
  { id: 2, name: 'Dashboard', prefix: 'def', scope: 'query', expires: '', lastUsed: '' },
] }) })
describe('Token directory', () => {
  it('searches names and scopes without creating or revealing a secret', async () => {
    render(<App><TokensPage /></App>)
    await screen.findByText('Warehouse')
    fireEvent.change(screen.getByLabelText('manage.listSearch'), { target: { value: 'query' } })
    expect(screen.queryByText('Warehouse')).toBeNull()
    expect(screen.getByText('Dashboard')).toBeTruthy()
    expect(api.post).not.toHaveBeenCalled()
  })
  it('retries a failed directory request instead of presenting an empty list', async () => {
    api.get.mockRejectedValueOnce(new Error('Offline'))
    render(<App><TokensPage /></App>)
    fireEvent.click(await screen.findByRole('button', { name: /common.retry/ }))
    await screen.findByText('Warehouse')
  })
})
