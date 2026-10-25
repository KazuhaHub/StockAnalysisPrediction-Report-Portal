import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import RunQueueSettingsPage from './RunQueueSettingsPage'

const server = vi.hoisted(() => ({ resolve: null as null | ((v: unknown) => void), reject: null as null | ((e: unknown) => void), post: vi.fn().mockResolvedValue({}) }))
vi.mock('../../api/client', () => ({
  api: {
    get: () =>
      new Promise((resolve, reject) => {
        server.resolve = resolve
        server.reject = reject
      }),
    post: server.post,
  },
  errText: () => 'boom',
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))
vi.mock('./RunPresetsEditor', () => ({ default: () => <div>presets</div> }))

// The page links across to the run-form defaults, so it needs a router around it.
const renderPage = () =>
  render(
    <MemoryRouter>
      <App>
        <RunQueueSettingsPage />
      </App>
    </MemoryRouter>,
  )

describe('RunQueueSettingsPage before its configuration arrives', () => {
  it('preserves edits across sections, discards them together and saves only queue settings', async () => {
    server.post.mockClear()
    renderPage()
    await screen.findByText('common.loading')
    server.resolve?.({ max_jobs: 8, max_concurrency: 12, prio_w_base: 500, prio_w_age: 1000, prio_w_fair: 900, prio_age_hours: 24, prio_fair_halflife_hours: 168 })
    const jobs = await screen.findByRole('spinbutton', { name: 'batch.admin.maxJobs' })
    fireEvent.change(jobs, { target: { value: '9' } })
    fireEvent.click(screen.getByRole('button', { name: 'batch.admin.prioWeightsTitle' }))
    fireEvent.change(screen.getByRole('spinbutton', { name: 'batch.admin.wBase' }), { target: { value: '600' } })
    fireEvent.click(screen.getByRole('button', { name: 'manage.discard' }))
    expect((screen.getByRole('spinbutton', { name: 'batch.admin.wBase' }) as HTMLInputElement).value).toBe('500')
    fireEvent.click(screen.getByRole('button', { name: 'manage.queue.capacity' }))
    expect((jobs as HTMLInputElement).value).toBe('8')
    fireEvent.change(jobs, { target: { value: '9' } })
    fireEvent.click(screen.getByRole('button', { name: /common.save/ }))
    await waitFor(() => expect(server.post).toHaveBeenCalledOnce())
    const body = server.post.mock.calls[0][1]
    expect(body).toMatchObject({ max_jobs: 9, max_concurrency: 12, prio_w_base: 500, prio_w_fair: 900 })
    expect(body).not.toHaveProperty('run_default_target_id')
    expect(body).toHaveProperty('urgent_enabled', false)
  })
  // The fields are initialised to plausible-looking numbers — 1 run at a time, priority 50,
  // weights of 1000 — and the form is live. Showing them as if they were the stored settings
  // means an admin on a slow link can read a value the server never sent, and save it back.
  it('shows neither the settings nor the save button until the server has answered', async () => {
    renderPage()
    await screen.findByText('common.loading')
    expect(screen.queryByText('batch.admin.maxJobs')).toBeNull()
    expect(screen.queryByText('common.save')).toBeNull()

    server.resolve?.({ max_jobs: 8, prio_w_base: 1000, prio_w_age: 1000, prio_w_fair: 1000, prio_age_hours: 24, prio_fair_halflife_hours: 168 })
    await waitFor(() => expect(screen.getByText('batch.admin.maxJobs')).toBeTruthy())
  })

  it('reports a failed load instead of presenting its defaults as the configuration', async () => {
    renderPage()
    await screen.findByText('common.loading')
    server.reject?.(new Error('offline'))
    await waitFor(() => expect(screen.getByText('common.loadFailed')).toBeTruthy())
    expect(screen.queryByText('common.save')).toBeNull()
  })
})
