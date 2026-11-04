import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AppInstallationStatus, { parseAppStatus } from './AppInstallationStatus'

const apiMock = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../../api/client', () => ({ api: apiMock }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      const labels: Record<string, string> = {
        'apps.status.open': 'View status',
        'apps.status.openFor': `View status for ${String(options?.name ?? '')}`,
        'apps.status.title': `${String(options?.name ?? '')} status`,
        'apps.status.loading': 'Loading application status',
        'apps.status.unavailable': 'The application status is unavailable.',
        'apps.status.refresh': 'Refresh status',
        'apps.status.close': 'Close',
        'apps.status.state': 'Current state',
        'apps.status.action': 'Current operation',
        'apps.status.stage': 'Current step',
        'apps.status.paused': 'Application control is paused.',
        'apps.v2State.pending': 'Not ready',
        'apps.v2Hint.pending': 'The application is still being prepared.',
        'appPlatform.install': 'Install',
        'appPlatform.stage.prepare_data': 'Checking data policy',
      }
      return labels[key] ?? String(options?.defaultValue ?? key)
    },
  }),
}))

const installation = '11111111-1111-4111-8111-111111111111'

beforeEach(() => apiMock.get.mockReset())

describe('AppInstallationStatus', () => {
  it('loads a bounded reader projection only after the status action is opened', async () => {
    apiMock.get.mockResolvedValue({
      state: 'pending',
      openable: false,
      action: 'install',
      stage: 'prepare_data',
      control_paused: false,
    })
    render(<AppInstallationStatus installationId={installation} name="Notes" />)
    expect(apiMock.get).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'View status for Notes' }))
    expect(await screen.findByText('Checking data policy')).toBeTruthy()
    expect(screen.getByText('Not ready')).toBeTruthy()
    expect(apiMock.get).toHaveBeenCalledWith(
      `/api/app-platform/installations/${installation}/status`,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
  })

  it('shows a paused control without exposing raw server diagnostics', async () => {
    apiMock.get.mockResolvedValue({
      state: 'attention',
      openable: false,
      control_paused: true,
      private_endpoint: '/run/private.sock',
    })
    render(<AppInstallationStatus installationId={installation} name="Digest" />)
    fireEvent.click(screen.getByRole('button', { name: 'View status for Digest' }))
    expect(await screen.findByText('Application control is paused.')).toBeTruthy()
    expect(screen.queryByText('/run/private.sock')).toBeNull()
  })

  it('refuses unknown states, stages and diagnostics at the browser boundary', () => {
    const valid = { state: 'attention', openable: false, control_paused: false }
    expect(parseAppStatus(valid)).toEqual(valid)
    expect(parseAppStatus({ ...valid, state: 'ready' })).toBeNull()
    expect(parseAppStatus({ ...valid, stage: 'raw-shell' })).toBeNull()
    expect(parseAppStatus({ ...valid, error_code: 'private_sql_error' })).toBeNull()
    expect(parseAppStatus({ ...valid, openable: true })).toBeNull()
  })
})
