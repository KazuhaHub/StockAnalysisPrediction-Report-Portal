import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import AppsAdminPage from './AppsAdminPage'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

describe('application market permission review', () => {
  it('waits for confirmation and submits the reviewed version and capabilities', async () => {
    apiMock.get.mockImplementation((url: string) => Promise.resolve(url === '/api/apps' ? { apps: [] } : {
      index_url: 'https://example.invalid/index.json',
      apps: [{ id: 'status', name: 'Status', version: '1.0.0', scopes: ['extension.read', 'extension.manage'], installed: false }],
    }))
    apiMock.post.mockResolvedValue({ ok: true })
    const user = userEvent.setup()
    render(<App><AppsAdminPage /></App>)
    await user.click(await screen.findByRole('button', { name: /apps.marketFetch/ }))
    await user.click(await screen.findByRole('button', { name: /apps.marketInstall/ }))
    await screen.findByText('apps.scopeExtensionManage')
    expect(apiMock.post).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'apps.permGrant' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/apps/market/install', {
      id: 'status', version: '1.0.0', scopes: ['extension.read', 'extension.manage'],
    }))
  })
})
