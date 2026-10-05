import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import LoginActivityPanel from './LoginActivityPanel'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../api/client', () => ({ api: { get }, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

describe('LoginActivityPanel', () => {
  it('shows personal history without a policy editor for account owners', async () => {
    get.mockResolvedValue({ items: [], total: 0, timezone: 'UTC', keep: 100 })
    render(<LoginActivityPanel />)
    expect(await screen.findByText('loginActivity.empty')).toBeTruthy()
    expect(get).toHaveBeenCalledWith('/api/me/login-activity?limit=10')
    expect(screen.queryByRole('spinbutton')).toBeNull()
    expect(screen.queryByRole('button', { name: 'common.save' })).toBeNull()
  })
})
