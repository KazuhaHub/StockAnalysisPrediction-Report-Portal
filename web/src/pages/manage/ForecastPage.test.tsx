import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import ForecastPage from './ForecastPage'

// The page's two jobs, both of which are invisible in review: it must not present a default as a
// setting, and its header control must actually carry a save through.
//
// The evaluator's four counters start at 0 in this file's initial state, which is also a perfectly
// plausible real answer for a portal that has settled everything. That is why the load gate matters
// here rather than being a formality: a page that rendered its zeros before the server answered
// would be indistinguishable from a caught-up portal on a slow link.

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('../../api/client', () => ({ api: apiMock, errText: () => 'Request failed' }))

// Every entry point the shell might have warmed is cold in this test: the page must not depend on a
// hover having happened.
vi.mock('../../lib/prefetch', () => ({
  readPrefetched: () => undefined,
  prefetch: () => Promise.resolve(),
}))

// Echo the i18n key, with its values, so a test can assert which label a control carries.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const on = {
  enabled: true,
  bench: 'sh000852',
  due_sets: 3,
  pending: 12,
  refused: 0,
  unsettled_baseline: 0,
}

const show = () =>
  render(
    <App>
      <ForecastPage />
    </App>,
  )

describe('ForecastPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.get.mockResolvedValue(on)
  })

  it('shows neither the status nor the settings button until the server has answered', async () => {
    let resolveGet: ((v: unknown) => void) | undefined
    apiMock.get.mockReturnValue(
      new Promise((resolve) => {
        resolveGet = resolve
      }),
    )
    show()
    await screen.findByText('common.loading')
    expect(screen.queryByText('forecast.eval.title')).toBeNull()
    expect(screen.queryByText('forecast.settings.button')).toBeNull()

    resolveGet?.(on)
    await waitFor(() => expect(screen.getByText('forecast.eval.title')).toBeTruthy())
  })

  it('reports a failed load instead of presenting zero counts as the state', async () => {
    apiMock.get.mockRejectedValue(new Error('offline'))
    show()
    await waitFor(() => expect(screen.getByText('common.loadFailed')).toBeTruthy())
    // The four backlog numbers are exactly what must not be invented: "0 due" is a real and
    // reassuring answer, so a page that printed it after a failed request would be lying.
    expect(screen.queryByText('forecast.eval.due')).toBeNull()
  })

  it('says the evaluator is off, and shows the counts beside it', async () => {
    apiMock.get.mockResolvedValue({ ...on, enabled: false, due_sets: 0, pending: 0 })
    show()
    await waitFor(() => expect(screen.getByText('forecast.eval.off')).toBeTruthy())
    // Off is the shipped state and reads as a fault unless named as intentional.
    expect(screen.getByText('forecast.eval.offHint')).toBeTruthy()
    expect(screen.getByText('forecast.eval.due')).toBeTruthy()
  })

  it('marks a non-zero refusal count as a number to act on, not as another counter', async () => {
    apiMock.get.mockResolvedValue({ ...on, refused: 7 })
    show()
    await waitFor(() => expect(screen.getByText('forecast.eval.refused')).toBeTruthy())
    // The tag is how a stuck evaluator is told apart from an idle one at a glance.
    expect(screen.getByText('7')).toBeTruthy()
  })

  it('opens the settings modal and posts what it was edited to', async () => {
    show()
    await waitFor(() => expect(screen.getByText('forecast.settings.button')).toBeTruthy())
    fireEvent.click(screen.getByText('forecast.settings.button'))

    const bench = await screen.findByLabelText('forecast.settings.bench')
    fireEvent.change(bench, { target: { value: 'sh000300' } })
    apiMock.post.mockResolvedValue({ ok: true })
    fireEvent.click(screen.getByText('common.save'))

    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/forecast', {
        enabled: true,
        bench: 'sh000300',
      }),
    )
    // And the page reloads, so the counts shown are the ones the save left behind rather than the
    // ones that were on screen before it.
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(2))
  })

  it('surfaces the server refusal of a bad benchmark instead of a generic failure', async () => {
    show()
    await waitFor(() => expect(screen.getByText('forecast.settings.button')).toBeTruthy())
    fireEvent.click(screen.getByText('forecast.settings.button'))

    // The page's errText is stubbed to a fixed string here; what matters is that the save's failure
    // path keeps the modal open and does not report success.
    apiMock.post.mockRejectedValue(new Error('forecast_bad_bench'))
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(screen.getByText('common.save')).toBeTruthy()
  })
})
