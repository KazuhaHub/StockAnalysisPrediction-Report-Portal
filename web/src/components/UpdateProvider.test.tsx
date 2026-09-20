import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { UpdateBanner, UpdateProvider } from './UpdateProvider'
import VersionLabel from './VersionLabel'
import type { UpdateState } from '../lib/updateState'
import type { BuildIdentity } from '../lib/buildIdentity'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${Object.values(o).join(',')}` : k),
    i18n: { language: 'en-US' },
  }),
}))

const page: BuildIdentity = { version: 'v2026.38', commit: 'aaaaaaa', buildDate: '2026-08-01T00:00:00Z' }
const target: BuildIdentity = { version: 'v2026.38.1', commit: 'bbbbbbb', buildDate: '2026-08-10T00:00:00Z' }

const updateState = { value: {} as UpdateState }
vi.mock('../lib/updateState', async (orig) => {
  const actual = await orig<typeof import('../lib/updateState')>()
  return { ...actual, useUpdateState: () => updateState.value }
})

const applied = vi.fn()
vi.mock('../lib/swUpdate', () => ({ applyUpdate: () => applied() }))

// A stub standing in for the dialog: this file is about which presentation the policy calls for and
// that there is never more than one. The dialog's own behaviour is covered by its own test.
vi.mock('./ReleaseNotesModal', () => ({
  default: (p: { open: boolean; policy: string; target: BuildIdentity | null; onRefresh?: () => void }) => (
    <div
      data-testid="notes-modal"
      data-open={String(p.open)}
      data-policy={p.policy}
      data-target={p.target?.version ?? ''}
      data-refresh={String(!!p.onRefresh)}
    />
  ),
}))

const state = (over: Partial<UpdateState> = {}): UpdateState => ({
  page,
  target,
  kind: 'newer',
  policy: 'dismissible',
  workerReady: false,
  failed: false,
  ...over,
})

const show = (ui: React.ReactNode) => render(<UpdateProvider>{ui}</UpdateProvider>)
const banner = () => show(<UpdateBanner maxWidth={1240} compact={false} />)
const modal = () => screen.getByTestId('notes-modal')

beforeEach(() => {
  applied.mockReset()
  sessionStorage.clear()
  updateState.value = state()
})
afterEach(() => vi.unstubAllEnvs())

describe('UpdateBanner', () => {
  it('says nothing while the page matches the server', () => {
    updateState.value = state({ target: null, kind: null })
    const { container } = banner()
    expect(container.querySelector('.rp-update-banner')).toBeNull()
  })

  it('names the version a newer build would bring', () => {
    banner()
    expect(screen.getByText(/update\.newTitle:2026\.38\.1/)).toBeTruthy()
    expect(screen.getByText(/update\.newDesc/)).toBeTruthy()
  })

  it('describes a rollback as a change, not as a new release', () => {
    updateState.value = state({ kind: 'rollback', target: { ...target, version: 'v2026.37.4' } })
    banner()
    expect(screen.getByText(/update\.rollbackTitle:2026\.37\.4/)).toBeTruthy()
    expect(screen.queryByText(/update\.newTitle/)).toBeNull()
  })

  it('uses generic wording for a worker-only signal and offers no notes to open', () => {
    updateState.value = state({ target: null, kind: 'same', workerReady: true })
    banner()
    expect(screen.getByText(/update\.sameTitle/)).toBeTruthy()
    // There is no version behind a worker notification, so there is nothing to look up.
    expect(screen.queryByRole('button', { name: 'update.viewNotes' })).toBeNull()
  })

  // One click is one handover. A second click while the first is in flight must not post SKIP_WAITING
  // again or arm a second reload listener — two clicks, one reload.
  it('refreshes through the one shared handover when the reader asks', async () => {
    banner()
    const btn = screen.getByRole('button', { name: 'update.refresh' })
    await userEvent.click(btn)
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(applied).toHaveBeenCalledTimes(1)
  })
})

describe('deferral under a dismissible policy', () => {
  it('hides the banner for that target only, and only for this tab session', async () => {
    banner()
    await userEvent.click(screen.getByRole('button', { name: 'update.later' }))
    expect(screen.queryByText(/update\.newTitle/)).toBeNull()
    expect(sessionStorage.getItem('rp.update.deferred')).toBe('v2026.38.1@bbbbbbb@2026-08-10T00:00:00Z')

    // A different target is news again.
    updateState.value = state({ target: { version: 'v2026.38.2', commit: 'ccccccc', buildDate: '2026-08-11T00:00:00Z' } })
    banner()
    expect(screen.getByText(/update\.newTitle:2026\.38\.2/)).toBeTruthy()
  })
})

describe('the administrator’s policy', () => {
  it('persistent: no way to hide the reminder, and the page stays usable', () => {
    updateState.value = state({ policy: 'persistent' })
    banner()
    expect(screen.getByText(/update\.newTitle/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'update.later' })).toBeNull()
    expect(screen.getByRole('button', { name: 'update.refresh' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'update.viewNotes' })).toBeTruthy()
  })

  it('required: the dialog IS the prompt, with no banner beside it', () => {
    updateState.value = state({ policy: 'required' })
    const { container } = banner()
    expect(container.querySelector('.rp-update-banner')).toBeNull()
    // Mounting the provider alone is enough for the escalation to happen.
    expect(modal().dataset.open).toBe('true')
    expect(modal().dataset.policy).toBe('required')
    expect(modal().dataset.target).toBe('v2026.38.1')
    expect(modal().dataset.refresh).toBe('true')
    // Exactly one overlay: the dialog and the banner are never both drawn.
    expect(screen.getAllByTestId('notes-modal')).toHaveLength(1)
  })

  it('relaxing the policy stops insisting without trapping the reader', async () => {
    updateState.value = state({ policy: 'required' })
    const { rerender } = show(<UpdateBanner maxWidth={1240} compact={false} />)
    expect(modal().dataset.policy).toBe('required')

    updateState.value = state({ policy: 'dismissible' })
    rerender(
      <UpdateProvider>
        <UpdateBanner maxWidth={1240} compact={false} />
      </UpdateProvider>,
    )
    expect(modal().dataset.policy).toBe('dismissible')
  })

  it('a second target re-points the same dialog rather than stacking another', () => {
    updateState.value = state({ policy: 'required' })
    const { rerender } = show(<UpdateBanner maxWidth={1240} compact={false} />)
    updateState.value = state({ policy: 'required', target: { version: 'v2026.38.2', commit: 'ccccccc', buildDate: '2026-08-11T00:00:00Z' } })
    rerender(
      <UpdateProvider>
        <UpdateBanner maxWidth={1240} compact={false} />
      </UpdateProvider>,
    )
    expect(screen.getAllByTestId('notes-modal')).toHaveLength(1)
    expect(modal().dataset.target).toBe('v2026.38.2')
  })
})

describe('VersionLabel', () => {
  it('shows the build this page is running, with its version prefix', () => {
    show(<VersionLabel />)
    expect(screen.getByRole('button', { name: 'version.label:2026.38' })).toBeTruthy()
  })

  // The label names the loaded build, never the server's: the difference between them is what the
  // update prompt is for.
  it('opens the notes for the loaded build without offering a refresh', async () => {
    updateState.value = state({ target: null, kind: null })
    show(<VersionLabel />)
    await userEvent.click(screen.getByRole('button', { name: 'version.label:2026.38' }))
    expect(modal().dataset.open).toBe('true')
    expect(modal().dataset.target).toBe('v2026.38')
    expect(modal().dataset.refresh).toBe('false')
  })

  it('renders as plain text outside a provider', () => {
    // The identity is still the bundle's own; only the dialog it would open is missing.
    vi.stubEnv('VITE_BUILD_VERSION', 'v2026.38')
    render(<VersionLabel />)
    expect(screen.getByText('version.label:2026.38')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
  })
})

describe('update history in the label tooltip', () => {
  it('reveals commit and build time, including the zone, on hover', async () => {
    show(<VersionLabel />)
    fireEvent.mouseEnter(screen.getByRole('button', { name: 'version.label:2026.38' }))
    const tip = await screen.findByRole('tooltip')
    expect(tip.textContent).toMatch(/version\.tip\.commit aaaaaaa/)
    // A bare timestamp reads as two different dates to a maintainer and a reader; the zone is what
    // makes it mean one instant.
    expect(tip.textContent).toMatch(/version\.tip\.built .*\(.+\)/)
  })

  // Hover is not the only way in. A keyboard user reaches the same details by focusing the label —
  // antd's default trigger is hover alone, which would leave them with nothing.
  it('reveals the same details on keyboard focus', async () => {
    show(<VersionLabel />)
    fireEvent.focus(screen.getByRole('button', { name: 'version.label:2026.38' }))
    const tip = await screen.findByRole('tooltip')
    expect(tip.textContent).toMatch(/version\.tip\.version 2026\.38/)
  })
})
