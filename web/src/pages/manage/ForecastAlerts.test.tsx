import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import ForecastAlerts from './ForecastAlerts'
import { ApiError } from '../../api/client'
import type { ForecastSettings } from '../../api/types'

// The alert card (P41). Three things are load-bearing and easy to get wrong:
//   - a blank Slack field must NOT clear the stored secret;
//   - a failed test names the channel that failed, not a generic error;
//   - with no channel configured the card says readiness item 5 cannot pass.

const apiMock = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('../../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: apiMock,
    errText: (e: unknown, t: (k: string) => string) =>
      t(e instanceof actual.ApiError && e.code ? `err.${e.code}` : 'common.error'),
  }
})

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const base: ForecastSettings = {
  enabled: false,
  mode: '0',
  bench: 'sh000852',
  bench_codes: ['sh000852'],
  gate_codes: ['sh000852'],
  population: { set: true, candidates: [] },
  due_sets: 0,
  pending: 0,
  refused: 0,
  unsettled_baseline: 0,
  unrecorded: [],
  readiness: [],
  dry_gate: {},
  alerts: { email_to: '', has_slack: false },
}

function show(value: Partial<ForecastSettings> = {}) {
  const onChanged = vi.fn()
  render(
    <App>
      <MemoryRouter>
        <ForecastAlerts settings={{ ...base, ...value }} onChanged={onChanged} />
      </MemoryRouter>
    </App>,
  )
  return { onChanged }
}

describe('ForecastAlerts', () => {
  beforeEach(() => {
    apiMock.post.mockReset()
    apiMock.post.mockResolvedValue({ ok: true })
  })

  it('warns when no channel is configured', () => {
    show()
    expect(screen.getByText('forecast.alert.none')).toBeTruthy()
  })

  it('posts the recipients when they changed', async () => {
    show()
    fireEvent.change(screen.getByLabelText('forecast.alert.email'), {
      target: { value: 'ops@example.com' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'forecast.alert.save' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', {
        alert_email_to: 'ops@example.com',
      }),
    )
  })

  it('does not clear a stored Slack URL when the field is left blank', async () => {
    show({ alerts: { email_to: 'ops@example.com', has_slack: true } })
    fireEvent.click(screen.getByRole('button', { name: 'forecast.alert.save' }))
    expect(apiMock.post).not.toHaveBeenCalled()
    expect((screen.getByLabelText('forecast.alert.slack') as HTMLInputElement).placeholder).toBe(
      'forecast.alert.slackSet',
    )
  })

  it('clears the Slack URL only through the explicit action', async () => {
    show({ alerts: { email_to: '', has_slack: true } })
    fireEvent.click(screen.getByRole('button', { name: 'forecast.alert.slackClear' }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', { alert_slack_url: '' }),
    )
  })

  it('shows the per-channel failure from a 502', async () => {
    show({ alerts: { email_to: 'ops@example.com', has_slack: true } })
    apiMock.post.mockRejectedValue(
      new ApiError(502, 'failed', 'forecast_alert_failed', {
        results: {
          email: { ok: true },
          slack: { ok: false, error: 'HTTP 404' },
        },
      }),
    )
    fireEvent.click(screen.getByRole('button', { name: 'forecast.alert.test' }))
    await waitFor(() => expect(screen.getByText('HTTP 404')).toBeTruthy())
    // The failing channel is named inline; the toast carries the translated code.
    expect(screen.getAllByText('✗').length).toBeGreaterThan(0)
  })

  it('shows a successful test as verified', async () => {
    show({ alerts: { email_to: 'ops@example.com', has_slack: false, test_ok_at: '2026-10-20T08:00:00Z' } })
    expect(screen.getByText(/forecast\.alert\.verifiedAt/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'forecast.alert.test' }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast/alert-test', {}))
  })
})
