import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App, ConfigProvider } from 'antd'
import AppInstall from './AppInstall'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../../api/client', () => ({ api: apiMock, errText: (e: unknown) => String(e) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
const pending = vi.hoisted(() => vi.fn())
vi.mock('./AdminDraftBoundary', () => ({ usePendingSettings: (dirty: boolean, saving?: boolean) => pending(dirty, saving) }))
const lastPending = () => pending.mock.calls[pending.mock.calls.length - 1]

async function openMarket() {
  render(<ConfigProvider theme={{ token: { motion: false } }}><App><AppInstall onInstalled={() => {}} /></App></ConfigProvider>)
  fireEvent.click(screen.getByRole('button', { name: /apps\.install/ }))
  fireEvent.click(await screen.findByText('apps.fromMarket'))
  return await screen.findByRole('textbox', { name: 'apps.marketIndex' }) as HTMLInputElement
}

describe('AppInstall market index', () => {
  beforeEach(() => {
    pending.mockReset()
    apiMock.get.mockReset().mockResolvedValue({ apps: [], index_url: 'https://example.test/index.json' })
    apiMock.post.mockReset().mockImplementation(async (_url: string, body: { index_url: string }) => ({ index_url: body.index_url }))
  })

  it('is not pending after the drawer opens or after the saved index is loaded', async () => {
    await openMarket()
    expect(lastPending()).toEqual([false, false])
    fireEvent.click(screen.getByRole('button', { name: /apps\.marketFetch/ }))
    await waitFor(() => expect((screen.getByRole('textbox', { name: 'apps.marketIndex' }) as HTMLInputElement).value).toBe('https://example.test/index.json'))
    expect(lastPending()).toEqual([false, false])
  })

  it('is pending while the index differs from the saved one', async () => {
    const input = await openMarket()
    fireEvent.change(input, { target: { value: 'https://other.test/index.json' } })
    expect(lastPending()).toEqual([true, false])
    fireEvent.change(input, { target: { value: '' } })
    expect(lastPending()).toEqual([false, false])
  })

  it('does not overwrite an unsaved index when the list is fetched', async () => {
    const input = await openMarket()
    fireEvent.change(input, { target: { value: 'https://typed.test/index.json' } })
    fireEvent.click(screen.getByRole('button', { name: /apps\.marketFetch/ }))
    await waitFor(() => expect(apiMock.get).toHaveBeenCalled())
    await waitFor(() => expect(screen.queryByRole('button', { name: /apps\.marketFetch/ })?.className).not.toContain('loading'))
    expect((screen.getByRole('textbox', { name: 'apps.marketIndex' }) as HTMLInputElement).value).toBe('https://typed.test/index.json')
  })

  it('is clean again after a successful save', async () => {
    const input = await openMarket()
    fireEvent.change(input, { target: { value: 'https://other.test/index.json' } })
    expect(lastPending()).toEqual([true, false])
    fireEvent.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/apps/market/index', { index_url: 'https://other.test/index.json' }))
    await waitFor(() => expect(lastPending()).toEqual([false, false]))
  })

  it('closes the drawer without asking when nothing changed', async () => {
    await openMarket()
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(screen.queryByRole('textbox', { name: 'apps.marketIndex' })).toBeNull())
    expect(screen.queryAllByText('manage.leaveTitle')).toHaveLength(0)
  })

  it('asks before closing the drawer with an unsaved index, and discards it on leave', async () => {
    const input = await openMarket()
    fireEvent.change(input, { target: { value: 'https://other.test/index.json' } })
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    fireEvent.click(await screen.findByRole('button', { name: 'manage.stay' }))
    await waitFor(() => expect(screen.queryAllByText('manage.leaveTitle')).toHaveLength(0))
    expect((screen.getByRole('textbox', { name: 'apps.marketIndex' }) as HTMLInputElement).value).toBe('https://other.test/index.json')
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    fireEvent.click(await screen.findByRole('button', { name: 'manage.leave' }))
    await waitFor(() => expect(screen.queryByRole('textbox', { name: 'apps.marketIndex' })).toBeNull())
    expect(lastPending()).toEqual([false, false])
  })
})
