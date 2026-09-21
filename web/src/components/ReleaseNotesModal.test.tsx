import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import ReleaseNotesModal from './ReleaseNotesModal'
import type { BuildIdentity } from '../lib/buildIdentity'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    // Keep the key visible and the interpolated values inspectable, so a test can assert both which
    // string was chosen and what was put into it.
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${Object.values(o).join(',')}` : k),
    i18n: { language: 'en-US' },
  }),
}))
vi.mock('./Markdown', () => ({ default: ({ md }: { md?: string }) => <div data-testid="md">{md}</div> }))

// A bilingual note: one release written twice, marked by locale. The dialog shows the reader their
// own language rather than both, which is what the markers exist for.
const bilingual = `<zh-CN>

## 修复

- 中文说明

</zh-CN>

<en-US>

## Fixed

- English note

</en-US>`

const get = vi.fn()
vi.mock('../api/client', () => ({ api: { get: (...a: unknown[]) => get(...a) } }))

// antd's Modal renders through a portal into document.body.
function show(ui: ReactNode) {
  return render(<div>{ui}</div>)
}

const page: BuildIdentity = { version: 'v2026.38', commit: 'aaaaaaa', buildDate: '2026-08-01T00:00:00Z' }
const target: BuildIdentity = { version: 'v2026.38.1', commit: 'bbbbbbb', buildDate: '2026-08-10T00:00:00Z' }
const notes = (over: Partial<{ tag: string; available: boolean; markdown: string; url: string; maturity: 'release' | 'beta' | 'dev' | '' }> = {}) => ({
  tag: 'v2026.38.1',
  available: true,
  markdown: '## Fixed\n\n- the thing',
  url: 'https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/releases/tag/v2026.38.1',
  maturity: 'beta' as const,
  ...over,
})

const open = (props: Partial<Parameters<typeof ReleaseNotesModal>[0]> = {}) =>
  show(
    <ReleaseNotesModal
      open
      target={target}
      current={page}
      policy="dismissible"
      refreshing={false}
      onClose={vi.fn()}
      onRefresh={vi.fn()}
      {...props}
    />,
  )

beforeEach(() => get.mockReset())

describe('ReleaseNotesModal', () => {
  it('names the target version, and the page it is being compared against', async () => {
    get.mockResolvedValue(notes())
    open()
    expect(await screen.findByText('update.notesTitle:2026.38.1')).toBeTruthy()
    expect(screen.getByText('update.beta')).toBeTruthy()
    // The reader is told what they are running as well as what they would get.
    expect(screen.getByText('update.currentVersion:2026.38')).toBeTruthy()
  })

  it('renders inline so opening it does not lock and collapse the document body', async () => {
    get.mockResolvedValue(notes())
    const { container } = open()
    await screen.findByTestId('md')
    expect(container.querySelector('.ant-modal-root')).toBeTruthy()
  })

  it('does not show an unsaved-work warning when the footer opens current-version history', async () => {
    get.mockResolvedValue(notes({ tag: page.version }))
    open({ target: page, policy: 'required', onRefresh: undefined })
    await screen.findByTestId('md')
    expect(screen.queryByText('update.requiredWarning')).toBeNull()
  })

  it('renders the fetched note and links to the published release', async () => {
    get.mockResolvedValue(notes())
    open()
    expect((await screen.findByTestId('md')).textContent).toContain('## Fixed')
    const link = screen.getByRole('link', { name: /update.viewOnGithub/ })
    expect(link.getAttribute('href')).toContain('/releases/tag/v2026.38.1')
    expect(link.getAttribute('rel')).toContain('noopener')
  })

  it('reads the note for the build it was asked about', async () => {
    get.mockResolvedValue(notes())
    open()
    await waitFor(() => expect(get).toHaveBeenCalledWith('/api/release-notes?tag=v2026.38.1'))
  })

  // Browsing what you are already running has nothing to switch to.
  it('offers no refresh when showing the current build’s own notes', async () => {
    get.mockResolvedValue(notes({ tag: 'v2026.38', available: true, markdown: 'notes' }))
    open({ target: page, onRefresh: undefined })
    expect(await screen.findByTestId('md')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /update.refreshTo/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'common.close' })).toBeTruthy()
    // …and no "current page version" line, since there is nothing to compare.
    expect(screen.queryByText(/update.currentVersion/)).toBeNull()
  })

  it('browses older packaged notes without offering a downgrade refresh', async () => {
    get.mockImplementation((url: string) => {
      if (url === '/api/release-history') {
        return Promise.resolve({ items: [
          { tag: 'v2026.38', title: 'Current', url: 'https://example/current', maturity: 'release' },
          { tag: 'v2026.37.2', title: 'Older', url: 'https://example/older', maturity: 'beta' },
        ] })
      }
      if (url?.includes('v2026.37.2')) {
        return Promise.resolve(notes({ tag: 'v2026.37.2', markdown: '# Older changes', url: 'https://example/older' }))
      }
      return Promise.resolve(notes({ tag: 'v2026.38', markdown: '# Current changes' }))
    })
    const { container } = open({ target: page, onRefresh: undefined })
    const older = await screen.findByRole('button', { name: /2026\.37\.2/ })
    expect(container.querySelector('.rp-release-notes-modal--history')).toBeTruthy()
    expect(container.querySelector('.rp-release-history')).toBeTruthy()
    expect(container.querySelector('.rp-release-notes-body')).toBeTruthy()
    await userEvent.click(older)
    await waitFor(() => expect(get).toHaveBeenCalledWith('/api/release-notes?tag=v2026.37.2'))
    expect((await screen.findByTestId('md')).textContent).toContain('Older changes')
    expect(screen.getByText('update.notesTitle:2026.37.2')).toBeTruthy()
    expect(screen.getAllByText('update.beta').length).toBeGreaterThan(0)
    expect(screen.getAllByText('update.release').length).toBeGreaterThan(0)
    expect(screen.queryByRole('button', { name: /update.refreshTo/ })).toBeNull()
  })

  // A failed note fetch must never take the way out with it.
  it('keeps the refresh action when the notes cannot be loaded, and offers a retry', async () => {
    get.mockRejectedValueOnce(new Error('offline')).mockResolvedValue(notes())
    open()
    const retry = await screen.findByRole('button', { name: 'common.retry' })
    expect(screen.getByRole('button', { name: /update.refreshTo/ })).toBeTruthy()
    await userEvent.click(retry)
    expect((await screen.findByTestId('md')).textContent).toContain('## Fixed')
  })

  it('says the notes are unavailable rather than showing an empty success', async () => {
    get.mockResolvedValue(notes({ available: false, markdown: '' }))
    open()
    expect(await screen.findByText('update.notesUnavailable')).toBeTruthy()
    expect(screen.queryByTestId('md')).toBeNull()
    // The exact release link is still offered.
    expect(screen.getByRole('link', { name: /releases\/tag\/v2026\.38\.1/ })).toBeTruthy()
  })

  it('invents neither a note nor a link for a build that has no release', async () => {
    get.mockResolvedValue({ tag: 'dev', available: false, markdown: '', url: '', maturity: 'dev' })
    open({ target: { version: 'dev', commit: 'none', buildDate: 'unknown' } })
    expect(await screen.findByText('update.notesUnavailable')).toBeTruthy()
    expect(screen.getByText('update.dev')).toBeTruthy()
    expect(screen.queryByRole('link')).toBeNull()
  })
})

describe('ReleaseNotesModal under a required policy', () => {
  const required = (props: Partial<Parameters<typeof ReleaseNotesModal>[0]> = {}) =>
    open({ policy: 'required', ...props })

  it('keeps close controls alongside the refresh action', async () => {
    get.mockResolvedValue(notes())
    const onClose = vi.fn()
    required({ onClose })
    await screen.findByTestId('md')
    await userEvent.click(screen.getByRole('button', { name: 'common.close' }))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(document.querySelector('.ant-modal-close')).toBeTruthy()
    expect(screen.getByRole('button', { name: /update.refreshTo/ })).toBeTruthy()
    expect(screen.getByRole('link', { name: /update.viewOnGithub/ })).toBeTruthy()
  })

  it('lets Escape close the reminder', async () => {
    get.mockResolvedValue(notes())
    const onClose = vi.fn()
    required({ onClose })
    await screen.findByTestId('md')
    fireEvent.keyDown(document, { key: 'Escape', keyCode: 27 })
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  it('warns that unsaved work goes with the page', async () => {
    get.mockResolvedValue(notes())
    required()
    expect(await screen.findByText('update.requiredWarning')).toBeTruthy()
  })
})

// The wiring, rather than the parser (which has its own tests): a convention nothing consults is a
// convention nobody follows.
describe('ReleaseNotesModal with bilingual notes', () => {
  beforeEach(() => {
    get.mockReset()
    get.mockResolvedValue(notes({ markdown: bilingual }))
  })

  it('renders only the section for the reader’s language', async () => {
    open()
    await waitFor(() => expect(screen.getByTestId('md').textContent).toContain('English note'))
    expect(screen.getByTestId('md').textContent).not.toContain('中文说明')
  })
})
