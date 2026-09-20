import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Grid } from 'antd'
import { MemoryRouter, Route, Routes } from 'react-router'
import AppLayout from './AppLayout'

const updateState = vi.hoisted(() => ({ value: {} as unknown }))
// The header's queue badge polls through the conditional-GET helper; `queue` decides whether it has
// a count yet. UNCHANGED is what a 304 looks like — an answer to a tag this mount never sent.
const queueState = vi.hoisted(() => ({ answer: null as unknown }))
const UNCHANGED = vi.hoisted(() => Symbol('unchanged'))
const forgetTags = vi.hoisted(() => vi.fn())
const siteState = vi.hoisted(() => ({
  settings: { footerText: '', footerShowInfo: false, versionDisplay: 'hidden' },
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    // Interpolate as i18next does, so a test can assert both the string chosen and the value put
    // into it (the version label is the string plus the number).
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${Object.values(o).join(',')}` : k),
    i18n: { language: 'en-US' },
  }),
}))
vi.mock('../site', () => ({
  SiteLogo: () => <span data-testid="site-logo" />,
  useSite: () => ({
    title: 'Report Portal',
    settings: siteState.settings,
  }),
}))
vi.mock('../prefs', () => ({
  usePrefs: () => ({ mode: 'light', setMode: vi.fn(), lang: 'en', setLang: vi.fn(), langs: [{ code: 'en', label: 'English' }] }),
}))
vi.mock('../reader', () => ({ useReaderPrefs: () => ({ wide: false }) }))
vi.mock('../auth', () => ({
  useAuth: () => ({ user: 'alice', name: 'Alice', admin: true, can: () => true, logout: vi.fn() }),
}))
vi.mock('../api/client', () => ({
  api: { get: () => Promise.resolve({ version: 'v2026.38.1', commit: 'abc1234', buildDate: '2026-08-10' }) },
}))
vi.mock('../lib/updateState', async (orig) => {
  const actual = await orig<typeof import('../lib/updateState')>()
  return { ...actual, useUpdateState: () => updateState.value }
})
vi.mock('../lib/conditionalGet', () => ({
  UNCHANGED,
  forgetTags,
  getIfChanged: () => (queueState.answer === null ? new Promise(() => {}) : Promise.resolve(queueState.answer)),
}))
vi.mock('./Omnibox', () => ({ default: () => <input aria-label="global-search" /> }))
vi.mock('./RunAnalysisModal', () => ({ default: () => null }))
vi.mock('./QueueDrawer', () => ({ default: () => null }))
vi.mock('./SiteAnnouncement', () => ({
  default: () => null,
  AnnouncementStrip: () => null,
  AnnouncementPopup: () => null,
}))

// The build this test bundle "is running", and the state the shared coordinator reports. Both the
// footer label and the banner read from here, so the page identity is stubbed once.
const page = { version: 'v2026.38.1', commit: 'abc1234', buildDate: '2026-08-10T00:00:00Z' }
const noUpdate = { page, target: null, kind: null, policy: 'dismissible', workerReady: false, failed: false }

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route element={<AppLayout />}>
          <Route path="chat" element={<div>chat-body</div>} />
          <Route path="queue" element={<div>queue-body</div>} />
          <Route path="review" element={<div>review-body</div>} />
          <Route path="apps" element={<div>apps-body</div>} />
          <Route path="apps/batch" element={<div>batch-body</div>} />
          <Route path="manage" element={<div>manage-body</div>} />
          <Route path="report/new" element={<div>write-report-body</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  updateState.value = noUpdate
  vi.stubEnv('VITE_BUILD_VERSION', page.version)
  vi.stubEnv('VITE_BUILD_COMMIT', page.commit)
  vi.stubEnv('VITE_BUILD_DATE', page.buildDate)
})
afterEach(() => vi.unstubAllEnvs())

// An absent badge is how this header says "nothing is queued". It used to say that before anything
// had been asked, and — on a summary request that keeps failing — for ever.
describe('AppLayout queue badge', () => {
  beforeEach(() => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    queueState.answer = null
    forgetTags.mockClear()
  })

  // antd keeps a hidden badge in the DOM to animate it out, so "no badge" is data-show="false"
  // rather than an absent node.
  const shown = (c: HTMLElement, sel: string) => c.querySelector(`${sel}[data-show="true"]`)

  it('shows a dot rather than no badge while the count is unknown', async () => {
    const { container } = renderAt('/queue')
    expect(await screen.findByText('queue-body')).toBeTruthy()
    expect(shown(container, '.ant-badge-dot')).not.toBeNull()
    expect(shown(container, '.ant-badge-count')).toBeNull()
  })

  it('drops the badge once the server has actually said the queue is empty', async () => {
    queueState.answer = { running: 0, waiting: 0, scheduled: 0, budget: 3 }
    const { container } = renderAt('/queue')
    expect(await screen.findByText('queue-body')).toBeTruthy()
    await vi.waitFor(() => expect(shown(container, '.ant-badge-dot')).toBeNull())
    expect(shown(container, '.ant-badge-count')).toBeNull()
  })

  it('counts the runs once it has them', async () => {
    queueState.answer = { running: 2, waiting: 1, scheduled: 0, budget: 3 }
    const { container } = renderAt('/queue')
    expect(await screen.findByText('queue-body')).toBeTruthy()
    await vi.waitFor(() => expect(shown(container, '.ant-badge-count')?.textContent).toContain('3'))
    expect(shown(container, '.ant-badge-dot')).toBeNull()
  })

  // Same module-global tag store as the queue table: the badge must not be told "unchanged" about
  // a count it has never held.
  it('drops the summary tag before its first ask', async () => {
    queueState.answer = { running: 0, waiting: 0, scheduled: 0, budget: 3 }
    renderAt('/queue')
    await vi.waitFor(() => expect(forgetTags).toHaveBeenCalledWith('/api/admin/batch/queue'))
  })
})

describe('AppLayout desktop navigation', () => {
  beforeEach(() => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
  })

  it('groups secondary destinations in a single workbench launcher', async () => {
    const user = userEvent.setup()
    renderAt('/queue')

    expect(await screen.findByText('queue-body')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'nav.chat' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'nav.review' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'nav.apps' })).toBeNull()

    await user.click(screen.getByRole('button', { name: 'nav.workbench' }))

    expect(screen.getByRole('menu', { name: 'nav.workbench' })).toBeTruthy()
    expect(screen.getByRole('menuitem', { name: 'nav.chat' })).toBeTruthy()
    expect(screen.getByRole('menuitem', { name: 'nav.review' })).toBeTruthy()
    expect(screen.getByRole('menuitem', { name: 'nav.apps' })).toBeTruthy()
    expect(screen.queryByRole('menuitem', { name: 'nav.manage' })).toBeNull()
  })

  it('navigates from the launcher and closes it', async () => {
    const user = userEvent.setup()
    renderAt('/queue')

    const trigger = await screen.findByRole('button', { name: 'nav.workbench' })
    await user.click(trigger)
    await user.click(screen.getByRole('menuitem', { name: 'nav.review' }))

    expect(await screen.findByText('review-body')).toBeTruthy()
    await vi.waitFor(() => expect(trigger.getAttribute('aria-expanded')).toBe('false'))
  })

  it('keeps management in the account menu at desktop width', async () => {
    const user = userEvent.setup()
    renderAt('/queue')

    expect(await screen.findByText('queue-body')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'nav.manage' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Alice' }))

    expect(screen.getByRole('button', { name: 'nav.manage' })).toBeTruthy()
  })

  it('moves write report into the run-analysis dropdown', async () => {
    const user = userEvent.setup()
    renderAt('/queue')

    expect(await screen.findByText('queue-body')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'nav.writeReport' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'nav.runActions' }))
    expect(screen.getByRole('menu').closest('.rp-run-actions-menu')).not.toBeNull()
    const batchItem = screen.getByRole('menuitem', { name: 'nav.batch' })
    expect(batchItem.querySelector('.anticon-play-circle')).not.toBeNull()
    expect(batchItem.querySelector('.anticon-table')).toBeNull()
    await user.click(screen.getByRole('menuitem', { name: 'nav.writeReport' }))

    expect(await screen.findByText('write-report-body')).toBeTruthy()
  })

  it('opens batch execution from the run-analysis dropdown', async () => {
    const user = userEvent.setup()
    renderAt('/queue')

    await user.click(await screen.findByRole('button', { name: 'nav.runActions' }))
    await user.click(screen.getByRole('menuitem', { name: 'nav.batch' }))

    expect(await screen.findByText('batch-body')).toBeTruthy()
  })
})

describe('AppLayout mobile chat focus mode', () => {
  beforeEach(() => {
    updateState.value = noUpdate
    siteState.settings = { footerText: '', footerShowInfo: false, versionDisplay: 'hidden' }
  })

  it('removes global search, actions, breadcrumbs, and content gutters on mobile chat', async () => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: false } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderAt('/chat')

    expect(await screen.findByText('chat-body')).toBeTruthy()
    const header = container.querySelector<HTMLElement>('.rp-app-header--chat-focus')
    expect(header).not.toBeNull()
    expect(header?.style.display).toBe('none')
    expect(container.querySelector('.rp-chat-content--mobile')).not.toBeNull()
    expect(screen.queryByLabelText('global-search')).toBeNull()
    expect(screen.queryByTitle('nav.runAnalysis')).toBeNull()
    expect(screen.queryByText('nav.home')).toBeNull()
  })

  it('keeps normal mobile portal chrome away from chat', async () => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: false } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderAt('/queue')

    expect(await screen.findByText('queue-body')).toBeTruthy()
    expect(container.querySelector('.rp-app-header--chat-focus')).toBeNull()
    expect(screen.getByLabelText('global-search')).toBeTruthy()
    expect(screen.getByTitle('nav.runAnalysis')).toBeTruthy()
    expect(screen.getByText('nav.home')).toBeTruthy()
  })

  // The footer's name and version sat at different heights: the name and logo were grouped in an
  // inline-flex box inside a flex row, an inline-flex box takes its baseline from its first flex
  // item (here a replaced <img>), and centring that taller box left its text 1.25px above the
  // version's. jsdom has no layout, so what is asserted here is the arrangement that caused it —
  // one inline flow, no part boxed off in its own flex container. The 1.25px → 0 measurement
  // itself was made in a browser.
  it('lays the whole footer out in one inline flow so its parts share a baseline', async () => {
    siteState.settings = { footerText: '', footerShowInfo: true, versionDisplay: 'footer' }
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderAt('/queue')

    // The footer shows the product version, not the git tag: 2026.38.1, not v2026.38.1 (ADR 0034),
    // behind a localized "Version" prefix, and as a button so the release notes are keyboard- and
    // touch-reachable rather than hover-only.
    const footer = await screen.findByText('version.label:2026.38.1').then((el) => el.closest('.ant-layout-footer'))
    expect(footer).not.toBeNull()
    expect(footer!.querySelector('.ant-space'), 'a flex row re-splits the baselines').toBeNull()
    const flexed = [...footer!.querySelectorAll<HTMLElement>('*')].filter((el) => el.style.display.includes('flex'))
    expect(flexed.map((el) => el.textContent)).toEqual([])
    expect(container.querySelector('[data-testid="site-logo"]')).not.toBeNull()
  })

  it('places the version beside the site name without also rendering it in the footer', async () => {
    siteState.settings = { footerText: '', footerShowInfo: true, versionDisplay: 'header' }
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderAt('/queue')

    const label = await screen.findByText('version.label:2026.38.1')
    expect(label.closest('#rp-app-header')).not.toBeNull()
    expect(container.querySelector('.ant-layout-footer .rp-version-label')).toBeNull()
  })

  it('keeps the portal version hidden while the management rail remains responsible for its own label', async () => {
    siteState.settings = { footerText: '', footerShowInfo: true, versionDisplay: 'hidden' }
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderAt('/manage')

    expect(await screen.findByText('manage-body')).toBeTruthy()
    expect(container.querySelector('#rp-app-header .rp-version-label')).toBeNull()
    expect(container.querySelector('.ant-layout-footer')).toBeNull()
  })

  it('draws the update banner from the shared coordinator, with no close affordance stacked on it', async () => {
    updateState.value = {
      ...noUpdate,
      target: { version: 'v2026.38.2', commit: 'bbbbbbb', buildDate: '2026-08-11T00:00:00Z' },
      kind: 'newer',
    }
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderAt('/queue')

    expect(await screen.findByText(/update\.newTitle:2026\.38\.2/)).toBeTruthy()
    const banner = container.querySelector<HTMLElement>('.rp-update-banner')
    expect(banner).not.toBeNull()
    expect(banner?.style.borderBottom).toBe('')
    expect(screen.queryByLabelText('common.cancel')).toBeNull()
  })
})
