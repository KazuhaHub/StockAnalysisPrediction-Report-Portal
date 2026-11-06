import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import AppPlatformTasksPanel from './AppPlatformTasksPanel'

const mock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), t: (key: string) => key }))
vi.mock('../../api/client', () => ({
  api: { get: mock.get, post: mock.post },
  errText: (failure: unknown) => failure instanceof Error ? failure.message : String(failure),
}))
vi.mock('../../prefs', () => ({ usePrefs: () => ({ lang: 'en-US' }) }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: mock.t }) }))

const task = {
  id: '11111111-1111-4111-8111-111111111111',
  installationId: '22222222-2222-4222-8222-222222222222',
  appId: 'scheduled-digest',
  occurrenceId: 'digest:2026-11-03',
  handler: 'scheduled-digest.run',
  state: 'uncertain',
  generation: 1,
  policyRevision: 1,
  attempts: 1,
  maxAttempts: 3,
  nextAttemptMs: Date.now(),
  expiresMs: Date.now() + 60_000,
  errorCode: 'effect_unknown',
  updatedMs: Date.now(),
  attemptHistory: [{ attempt: 1, state: 'uncertain', startedMs: Date.now(), finishedMs: Date.now(), errorCode: 'effect_unknown' }],
}

beforeEach(() => {
  mock.get.mockReset()
  mock.post.mockReset()
  mock.get.mockResolvedValue({ tasks: [task] })
  mock.post.mockResolvedValue({ ok: true })
})
afterEach(cleanup)

describe('application task administration', () => {
  it('lists safe task evidence and requires an explicit review decision', async () => {
    const user = userEvent.setup()
    render(<AppPlatformTasksPanel />)
    expect(await screen.findByText('digest:2026-11-03')).toBeTruthy()
    expect(screen.getByText('effect_unknown')).toBeTruthy()
    expect(screen.queryByText(/"payload"/)).toBeNull()
    await waitFor(() => expect(document.querySelector('.ant-spin-spinning')).toBeNull())
    await user.click(screen.getByRole('button', { name: 'appPlatform.tasks.reviewFailed' }))
    await waitFor(() => expect(mock.post).toHaveBeenCalledWith(
      '/api/admin/app-platform/tasks/11111111-1111-4111-8111-111111111111/review',
      { decision: 'failed' },
    ))
  })

  it('shows a bounded error without dropping the current task list', async () => {
    mock.post.mockRejectedValue(new Error('task changed'))
    const user = userEvent.setup()
    render(<AppPlatformTasksPanel />)
    const button = await screen.findByRole('button', { name: 'appPlatform.tasks.reviewCancelled' })
    await waitFor(() => expect(document.querySelector('.ant-spin-spinning')).toBeNull())
    await user.click(button)
    await waitFor(() => expect(mock.post).toHaveBeenCalledTimes(1))
    expect(await screen.findByText('task changed')).toBeTruthy()
    expect(screen.getByText('digest:2026-11-03')).toBeTruthy()
  })
})
