import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import AppPlatformEventSubscriptionsPanel from './AppPlatformEventSubscriptionsPanel'

const mock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), t: (key: string) => key }))
vi.mock('../../api/client', () => ({
  api: { get: mock.get, post: mock.post },
  errText: (failure: unknown) => failure instanceof Error ? failure.message : String(failure),
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: mock.t }) }))

const installation = '22222222-2222-4222-8222-222222222222'
const subscription = {
  id: '11111111-1111-4111-8111-111111111111',
  installation_id: installation,
  app_id: 'notes-sample',
  event_type: 'report.ingested',
  resource_type: 'report',
  resource_id: '41',
  state: 'active',
  generation: 1,
  policy_revision: 1,
  max_inflight: 4,
  revision: 1,
  created_ms: Date.now(),
  updated_ms: Date.now(),
}

beforeEach(() => {
  mock.get.mockReset()
  mock.post.mockReset()
  mock.get.mockResolvedValue({ subscriptions: [subscription] })
  mock.post.mockResolvedValue({ ok: true })
})
afterEach(cleanup)

describe('application event subscriptions', () => {
  it('shows the exact resource and uses its current revision for state changes', async () => {
    const user = userEvent.setup()
    render(<AppPlatformEventSubscriptionsPanel />)
    expect(await screen.findByText('notes-sample')).toBeTruthy()
    expect(screen.getByText('report:41')).toBeTruthy()
    await waitFor(() => expect(document.querySelector('.ant-spin-spinning')).toBeNull())
    await user.click(screen.getByRole('button', { name: 'appPlatform.events.pause' }))
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(
      `/api/admin/app-platform/event-subscriptions/${subscription.id}/state?installation=${installation}`,
      { state: 'paused', expectedRevision: 1 },
    ))
  })

  it('creates an owner-scoped job subscription only with the canonical job resource', async () => {
    const user = userEvent.setup()
    render(<AppPlatformEventSubscriptionsPanel />)
    await waitFor(() => expect(document.querySelector('.ant-spin-spinning')).toBeNull())
    await user.click(screen.getByRole('button', { name: 'appPlatform.events.create' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('appPlatform.tasks.installation'), installation)
    await user.click(within(dialog).getByRole('combobox', { name: 'appPlatform.events.event' }))
    await user.click(await screen.findByText('appPlatform.events.batchFinished'))
    await user.type(within(dialog).getByLabelText('appPlatform.events.resourceId'), '7@3')
    await user.click(within(dialog).getByRole('button', { name: 'appPlatform.events.create' }))
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(
      '/api/admin/app-platform/event-subscriptions',
      {
        installationId: installation,
        eventType: 'batch.job.finished',
        resourceType: 'job',
        resourceId: '7@3',
        maxInflight: 4,
      },
    ))
  })

  it('requires confirmation before the irreversible revoke action', async () => {
    const user = userEvent.setup()
    render(<AppPlatformEventSubscriptionsPanel />)
    await waitFor(() => expect(document.querySelector('.ant-spin-spinning')).toBeNull())
    await user.click(screen.getByRole('button', { name: 'appPlatform.events.revoke' }))
    expect(await screen.findByText('appPlatform.events.revokeConfirm')).toBeTruthy()
    expect(mock.post).not.toHaveBeenCalled()
    const actions = screen.getAllByRole('button', { name: 'appPlatform.events.revoke' })
    await user.click(actions[actions.length - 1])
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(
      `/api/admin/app-platform/event-subscriptions/${subscription.id}/state?installation=${installation}`,
      { state: 'revoked', expectedRevision: 1 },
    ))
  })
})
