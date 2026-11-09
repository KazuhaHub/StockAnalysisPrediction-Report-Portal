import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import AppPlatformStorageStatus from './AppPlatformStorageStatus'

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../../api/client', () => ({ api: apiMock }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

afterEach(() => apiMock.get.mockReset())

describe('AppPlatformStorageStatus', () => {
  it('names every release whose stored image is unusable', async () => {
    apiMock.get.mockResolvedValue({ unreadable: [
      { release_sha256: 'a'.repeat(64), app_id: 'notes', version: '1.0.0', reason: 'manifest_unreadable' },
      { release_sha256: 'b'.repeat(64), app_id: 'digest', version: '2.0.0', reason: 'blobs_missing' },
    ] })
    render(<AppPlatformStorageStatus />)
    expect(await screen.findAllByText('appPlatform.storage.reuploadRequired')).toHaveLength(2)
    expect(apiMock.get).toHaveBeenCalledWith('/api/admin/app-platform/storage-status')
  })

  it('shows nothing when the store is healthy or the read fails', async () => {
    apiMock.get.mockResolvedValueOnce({ unreadable: [] })
    const { container, rerender } = render(<AppPlatformStorageStatus />)
    await waitFor(() => expect(apiMock.get).toHaveBeenCalled())
    expect(container.textContent).toBe('')
    apiMock.get.mockRejectedValueOnce(new Error('boom'))
    rerender(<AppPlatformStorageStatus key="again" />)
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(2))
    expect(container.textContent).toBe('')
  })
})
