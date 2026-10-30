import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { App } from 'antd'
import RunAnalysisModal from './RunAnalysisModal'

// A non-administrator's queue summary tiles are their own, but whether a run starts at once depends on
// the whole queue (ADR 0047): the banner reads the global capacity fields.
const queueAnswer = vi.hoisted(() => ({ value: {} as Record<string, unknown> }))
vi.mock('../api/client', () => ({
  api: {
    get: (url: string) => Promise.resolve(url.includes('/batch/queue') ? queueAnswer.value : {}),
    post: () => Promise.resolve({ job_id: 9 }),
    upload: () => Promise.resolve({}),
  },
  errText: (e: unknown) => String(e),
}))
vi.mock('../auth', () => ({ useAuth: () => ({ email: '', mailEnabled: false, admin: false, user: 'op' }) }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))
vi.mock('react-router', () => ({ useNavigate: () => vi.fn() }))
vi.mock('../lib/prefetch', () => ({
  readPrefetched: (url: string) =>
    url.endsWith('/targets')
      ? { targets: [{ id: 1, name: 'Daily', plugin_slug: 'dify', created_at: '', mode: 'workflow', inputs: [] }] }
      : { presets: [], default_mode: 'now' },
  prefetch: () => Promise.resolve(),
}))

const show = async (queue: Record<string, unknown>) => {
  queueAnswer.value = queue
  render(
    <App>
      <RunAnalysisModal open onClose={() => {}} />
    </App>,
  )
  await screen.findByText('run.workflow')
}

describe('RunAnalysisModal queue banner', () => {
  afterEach(() => {
    queueAnswer.value = {}
  })

  it('does not call the queue idle when others fill the cap and the caller has nothing of their own', async () => {
    await show({ running: 0, running_rows: 0, waiting: 0, scheduled: 0, budget: 4, queue_running_rows: 4, queue_waiting: 2 })
    expect(await screen.findByText('run.queueBusy:{"n":4,"ahead":2}')).toBeTruthy()
    expect(screen.queryByText('run.queueIdle')).toBeNull()
  })

  it('counts free slots against the global running rows', async () => {
    await show({ running: 0, running_rows: 0, waiting: 0, scheduled: 0, budget: 4, queue_running_rows: 1, queue_waiting: 0 })
    expect(await screen.findByText('run.queueFree:{"n":3}')).toBeTruthy()
  })

  it('still reads an older server that sends no capacity fields', async () => {
    await show({ running: 0, running_rows: 2, waiting: 0, scheduled: 0, budget: 2 })
    expect(await screen.findByText('run.queueBusy:{"n":2,"ahead":0}')).toBeTruthy()
  })
})
