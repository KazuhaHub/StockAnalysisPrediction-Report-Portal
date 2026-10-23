import { describe, it, expect, vi, beforeEach } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { AnnouncementPopup, AnnouncementStrip } from './SiteAnnouncement'
import { announcementSig, forgetAcknowledgements, migrateLegacyDismissal } from '../lib/announcementDismissal'
import type { Announcement } from '../api/types'

const feed = vi.hoisted(() => ({
  items: [] as Announcement[],
  user: 'alice' as string | null,
  collapse: false,
}))

vi.mock('../announcements', async () => {
  const actual = await vi.importActual<typeof import('../announcements')>('../announcements')
  return {
    ...actual,
    useAnnouncements: () => ({ items: feed.items, collapse: feed.collapse, refresh: async () => {} }),
  }
})

vi.mock('../auth', () => ({
  useAuth: () => ({ user: feed.user }),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o?.count ? `${k}:${o.count}` : k) }),
}))

const LEGACY_KEY = 'report-portal.site-announcement.popup.dismissed'
const MAP_KEY = 'report-portal.announce.dismissed.v1.alice'

function announcement(over: Partial<Announcement> = {}): Announcement {
  return {
    id: 1,
    level: 'warning',
    title: '维护通知',
    content: '今晚 22:00 开始维护。',
    display: 'strip',
    stripDismissible: false,
    popupDismissible: true,
    scope: 'home',
    endsAt: '',
    ...over,
  }
}

// The two surfaces AppLayout mounts together: the strip (every in-scope row) and the single popup
// over it. One list now, whatever the scope, so there is no home-only band to mount beside it.
function tree(path: string) {
  return (
    <MemoryRouter initialEntries={[path]}>
      <AnnouncementStrip />
      <AnnouncementPopup />
    </MemoryRouter>
  )
}

function renderAt(path = '/') {
  return render(tree(path))
}

describe('SiteAnnouncement', () => {
  beforeEach(() => {
    window.localStorage.clear()
    window.sessionStorage.clear()
    forgetAcknowledgements()
    feed.items = []
    feed.user = 'alice'
    feed.collapse = false
  })

  it('renders nothing when the feed is empty', () => {
    const { container } = renderAt('/')
    expect(container.querySelector('.rp-announce-item')).toBeNull()
  })

  it('stacks every announcement in the operator’s order', async () => {
    feed.items = [
      announcement({ id: 1, title: '第一条' }),
      announcement({ id: 2, title: '第二条' }),
    ]
    renderAt('/')
    const banners = await screen.findAllByText(/第.条/)
    expect(banners.map((n) => n.textContent)).toEqual(['第一条', '第二条'])
  })

  // Folding is off by default: hiding something an operator decided was worth interrupting people
  // with, behind a click, is not a saving.
  it('shows every announcement by default', async () => {
    feed.items = [1, 2, 3, 4, 5].map((id) => announcement({ id, title: `第 ${id} 条` }))
    renderAt('/')

    expect(await screen.findByText('第 5 条')).toBeTruthy()
    expect(screen.queryByText(/announcement\.showMore/)).toBeNull()
  })

  it('folds the overflow behind a counter when the site asks for it', async () => {
    feed.collapse = true
    feed.items = [1, 2, 3, 4, 5].map((id) => announcement({ id, title: `第 ${id} 条` }))
    const user = userEvent.setup()
    renderAt('/')

    // The lead stays; the rest fold behind the counter.
    expect(await screen.findByText('第 1 条')).toBeTruthy()
    expect(screen.queryByText('第 2 条')).toBeNull()
    await user.click(screen.getByText('announcement.showMore:4'))
    expect(await screen.findByText('第 5 条')).toBeTruthy()
  })

  it('honours scope: a home announcement stays on Home, an app one follows the reader', async () => {
    feed.items = [
      announcement({ id: 1, title: '首页公告', scope: 'home' }),
      announcement({ id: 2, title: '全站公告', scope: 'app' }),
    ]
    const { container } = renderAt('/')
    expect(await screen.findByText('首页公告')).toBeTruthy()
    expect(screen.getByText('全站公告')).toBeTruthy()
    // One list holding both scopes, each row drawn once.
    expect(container.querySelectorAll('.rp-announce-item')).toHaveLength(2)
    expect(container.querySelector('.rp-announce-strip')).not.toBeNull()

    cleanup()
    renderAt('/queue')
    expect(await screen.findByText('全站公告')).toBeTruthy()
    expect(screen.queryByText('首页公告')).toBeNull()
    // Off the home page only the app-scoped row remains.
    expect(document.querySelectorAll('.rp-announce-item')).toHaveLength(1)
  })

  it('shows every site-wide announcement in full by default', async () => {
    feed.items = [
      announcement({ id: 1, title: '第一条', content: '正文一', scope: 'app' }),
      announcement({ id: 2, title: '第二条', content: '正文二', scope: 'app' }),
    ]
    renderAt('/queue')

    expect(await screen.findByText('第一条')).toBeTruthy()
    expect(screen.getByText('正文一')).toBeTruthy()
    expect(screen.getByText('第二条')).toBeTruthy()
    expect(screen.getByText('正文二')).toBeTruthy()
    expect(screen.queryByRole('button', { expanded: false })).toBeNull()
  })

  // The band used to be tinted by the loudest announcement while each row's icon kept its own
  // level's colour, so a blue notice under an amber warning looked like a misplaced icon. Each row
  // now carries its own background, and the band itself is neutral (styled in index.css).
  it('paints each strip row with its own level colour, not the loudest', async () => {
    feed.items = [
      announcement({ id: 1, level: 'notice', title: '注意', scope: 'app' }),
      announcement({ id: 2, level: 'warning', title: '警告', scope: 'app' }),
    ]
    const { container } = renderAt('/queue')
    await screen.findByText('警告')

    const band = container.querySelector<HTMLElement>('.rp-announce-strip')!
    expect(band.getAttribute('style') || '').not.toMatch(/background/)
    const rows = container.querySelectorAll<HTMLElement>('.rp-announce-item')
    expect(rows).toHaveLength(2)
    expect(rows[0].style.background).toBeTruthy()
    expect(rows[1].style.background).toBeTruthy()
    expect(rows[0].style.background).not.toBe(rows[1].style.background)
  })

  // The bug this replaced: the folded lead line stayed visible after expanding, so the first
  // announcement was drawn twice and the band looked broken.
  it('never draws the lead twice when the site-wide strip is folded and then expanded', async () => {
    feed.collapse = true
    feed.items = [
      announcement({ id: 1, title: '第一条', content: '正文一', scope: 'app' }),
      announcement({ id: 2, title: '第二条', content: '正文二', scope: 'app' }),
    ]
    const user = userEvent.setup()
    renderAt('/queue')

    expect(await screen.findByText('第一条')).toBeTruthy()
    expect(screen.queryByText('第二条')).toBeNull()
    // Folding hides the overflow, not the lead: the first announcement keeps its body too.
    expect(screen.getByText('正文一')).toBeTruthy()

    await user.click(screen.getByRole('button', { expanded: false }))
    await screen.findByText('第二条')
    expect(screen.getAllByText('第一条')).toHaveLength(1)
    expect(screen.getAllByText('正文一')).toHaveLength(1)
  })

  it('closes a dismissible site-wide strip', async () => {
    feed.items = [announcement({ id: 1, title: '可关闭', scope: 'app', stripDismissible: true })]
    const user = userEvent.setup()
    const { container } = renderAt('/queue')
    await screen.findByText('可关闭')

    await user.click(screen.getByRole('button', { name: 'announcement.close' }))
    await waitFor(() => expect(container.querySelector('.rp-announce-strip')).toBeNull())
  })

  // One dialog holds every eligible popup, titles in a sidebar, the picked one in full. Firing them
  // one after another would make a reader dismiss two modals in one sitting.
  it('offers every eligible popup in one dialog', async () => {
    feed.items = [
      announcement({ id: 1, title: '第一条', display: 'both' }),
      announcement({ id: 2, title: '第二条', display: 'both' }),
    ]
    renderAt('/')
    await screen.findByText('announcement.dontShowAgain')
    // Each appears in its strip row and in the dialog's stacked row.
    expect(screen.getAllByText('第一条')).toHaveLength(2)
    expect(screen.getAllByText('第二条')).toHaveLength(2)
  })

  // The footer answer is the whole dialog's, not one row's: "don't show again" silences every
  // announcement the dialog was showing, and none of them comes back on reload.
  it('"don\'t show again" silences every announcement in the dialog at once', async () => {
    feed.items = [
      announcement({ id: 1, title: '第一条', display: 'both' }),
      announcement({ id: 2, title: '第二条', display: 'both' }),
    ]
    const user = userEvent.setup()
    renderAt('/')
    await user.click(await screen.findByText('announcement.dontShowAgain'))

    cleanup()
    renderAt('/')
    // Neither re-fires: the dialog is gone, and only the strip rows remain (one title each).
    await waitFor(() => expect(screen.queryByText('announcement.dontShowAgain')).toBeNull())
    expect(screen.getAllByText('第一条')).toHaveLength(1)
    expect(screen.getAllByText('第二条')).toHaveLength(1)
  })

  // The reader keys dismissal on (id, wording). Reordering changes neither, so a drag in the admin
  // console must not re-interrupt everyone who already dismissed the popup.
  it('does not re-fire a dismissed popup when the announcements are reordered', async () => {
    const a = announcement({ id: 1, title: '第一条', display: 'both' })
    const b = announcement({ id: 2, title: '第二条' })
    feed.items = [a, b]
    const user = userEvent.setup()
    renderAt('/')
    await user.click(await screen.findByText('announcement.dontShowAgain'))

    cleanup()
    feed.items = [b, a] // dragged into a new order; same rows
    renderAt('/')
    await screen.findByText('第一条')
    expect(screen.queryByText('announcement.dontShowAgain')).toBeNull()
  })

  it('re-fires when the operator edits what the announcement says', async () => {
    feed.items = [announcement({ id: 1, display: 'both' })]
    const user = userEvent.setup()
    renderAt('/')
    await user.click(await screen.findByText('announcement.dontShowAgain'))

    cleanup()
    feed.items = [announcement({ id: 1, display: 'both', content: '维护推迟到明晚。' })]
    renderAt('/')
    expect(await screen.findByText('announcement.dontShowAgain')).toBeTruthy()
  })

  it('"got it" silences the popup for the session only, and never the banner', async () => {
    feed.items = [announcement({ id: 1, display: 'both' })]
    const user = userEvent.setup()
    renderAt('/')
    await user.click(await screen.findByText('announcement.gotIt'))

    cleanup()
    renderAt('/')
    // Banner still there; popup not offered again in this session.
    expect(await screen.findByText('维护通知')).toBeTruthy()
    expect(screen.queryByText('announcement.dontShowAgain')).toBeNull()
    expect(window.localStorage.getItem(MAP_KEY)).toBeNull()
  })

  it('closes only the banner it was asked to close', async () => {
    feed.items = [
      announcement({ id: 1, title: '可关闭', stripDismissible: true }),
      announcement({ id: 2, title: '不可关闭' }),
    ]
    const user = userEvent.setup()
    renderAt('/')
    await screen.findByText('可关闭')
    const close = screen.getByRole('button', { name: 'announcement.close' })
    expect(close).toBeTruthy()
    await user.click(close)
    await waitFor(() => expect(screen.queryByText('可关闭')).toBeNull())
    expect(screen.getByText('不可关闭')).toBeTruthy()
  })

  // The production report: an operator turned "readers can close it" off, yet readers could still
  // close the band and it stayed gone after a reload. Expected: no close control, any earlier
  // dismissal ignored, and the band back after a reload.
  it('brings back a band the reader closed once the operator stops allowing it', async () => {
    feed.items = [announcement({ id: 1, title: '维护通知', scope: 'app', stripDismissible: true })]
    const user = userEvent.setup()
    renderAt('/queue')
    await user.click(await screen.findByRole('button', { name: 'announcement.close' }))
    await waitFor(() => expect(screen.queryByText('维护通知')).toBeNull())

    cleanup()
    feed.items = [announcement({ id: 1, title: '维护通知', scope: 'app', stripDismissible: false })]
    renderAt('/queue') // a reload
    expect(await screen.findByText('维护通知')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'announcement.close' })).toBeNull()
  })

  // The same change arriving by the 60-second poll, in a tab where the reader closed the band from
  // the payload that still allowed it: the next payload puts it back without a reload.
  it('re-shows a closed band when the next payload no longer allows closing it', async () => {
    feed.items = [announcement({ id: 1, title: '维护通知', scope: 'app', stripDismissible: true })]
    const user = userEvent.setup()
    const { rerender } = renderAt('/queue')
    await user.click(await screen.findByRole('button', { name: 'announcement.close' }))
    await waitFor(() => expect(screen.queryByText('维护通知')).toBeNull())

    feed.items = [announcement({ id: 1, title: '维护通知', scope: 'app', stripDismissible: false })]
    rerender(tree('/queue'))
    expect(await screen.findByText('维护通知')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'announcement.close' })).toBeNull()
  })

  // ---------- popup "readers can close it" switched off ----------
  //
  // The popup has its own switch. "Don't show again" is a permanent dismissal like the band's close
  // button, and the operator said no. "Got it" stays — a dialog with no exit would lock the reader
  // out of the portal — but it lasts for this page load only.

  // A full page reload: the DOM goes, and so does everything held in memory for this page load.
  function reload(path = '/') {
    cleanup()
    forgetAcknowledgements()
    return renderAt(path)
  }

  it('offers a locked popup only "got it", and offers it again after a reload', async () => {
    feed.items = [announcement({ id: 1, title: '维护通知', display: 'both', popupDismissible: false })]
    const user = userEvent.setup()
    renderAt('/')
    // Asserted while the dialog is open: after it closes the absence would hold vacuously.
    const gotIt = await screen.findByText('announcement.gotIt')
    expect(screen.queryByText('announcement.dontShowAgain')).toBeNull()
    await user.click(gotIt)
    expect(window.sessionStorage.length).toBe(0)

    // Moving around the SPA remounts the popup without reloading the page.
    cleanup()
    renderAt('/queue')
    cleanup()
    renderAt('/')
    await screen.findByText('维护通知') // the strip is back; the popup must not be
    expect(screen.queryByRole('dialog')).toBeNull()

    reload('/')
    expect(await screen.findByText('announcement.gotIt')).toBeTruthy()
  })

  it('closes a locked popup from Escape too, and does not store it', async () => {
    feed.items = [announcement({ id: 1, display: 'both', popupDismissible: false })]
    const user = userEvent.setup()
    renderAt('/')
    await screen.findByText('announcement.gotIt')
    await user.keyboard('{Escape}')
    expect(window.sessionStorage.length).toBe(0)
    // antd keeps a closed dialog's nodes through its exit animation, which jsdom never finishes, so
    // the acknowledgement is observed by remounting instead of by watching the dialog leave.
    cleanup()
    renderAt('/')
    await screen.findByText('维护通知')
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it.each(['announcement.dontShowAgain', 'announcement.gotIt'])(
    'ignores a popup silenced with %s once it stops being dismissible',
    async (button) => {
      feed.items = [announcement({ id: 1, title: '维护通知', display: 'popup', popupDismissible: true })]
      const user = userEvent.setup()
      renderAt('/')
      await user.click(await screen.findByText(button))

      feed.items = [announcement({ id: 1, title: '维护通知', display: 'popup', popupDismissible: false })]
      reload('/')
      expect(await screen.findByText('announcement.gotIt')).toBeTruthy()
      expect(screen.queryByText('announcement.dontShowAgain')).toBeNull()
    },
  )

  // One dialog, one answer. With a locked row in it, "don't show again" would be a promise the
  // dialog cannot keep for that row, so it is not offered at all.
  it('offers "don\'t show again" only when every announcement in the dialog may be dismissed', async () => {
    feed.items = [
      announcement({ id: 1, title: '可关闭', display: 'popup', popupDismissible: true }),
      announcement({ id: 2, title: '不可关闭', display: 'popup', popupDismissible: false }),
    ]
    renderAt('/')
    expect(await screen.findByText('announcement.gotIt')).toBeTruthy()
    expect(screen.getByText('可关闭')).toBeTruthy()
    expect(screen.getByText('不可关闭')).toBeTruthy()
    expect(screen.queryByText('announcement.dontShowAgain')).toBeNull()
  })

  it('does not let the band switch decide the popup, nor the popup switch the band', async () => {
    feed.items = [
      announcement({ id: 1, title: '条带可关', display: 'both', stripDismissible: true, popupDismissible: false }),
    ]
    renderAt('/')
    expect(await screen.findByText('announcement.gotIt')).toBeTruthy()
    expect(screen.queryByText('announcement.dontShowAgain')).toBeNull()
    expect(screen.getAllByRole('button', { name: 'announcement.close' }).length).toBeGreaterThan(0)
  })

  it('keeps a dismissible popup exactly as it was: "got it" lasts the session, "don\'t show again" for good', async () => {
    feed.items = [announcement({ id: 1, display: 'popup', popupDismissible: true })]
    const user = userEvent.setup()
    renderAt('/')
    await user.click(await screen.findByText('announcement.gotIt'))
    expect(window.sessionStorage.length).toBe(1)
    expect(window.localStorage.getItem(MAP_KEY)).toBeNull()
  })

  // The old key held a bare hash with no id beside it. The imported row keeps the level, title and
  // body it was taken over, so matching it back is exact — which is why the signature is computed
  // in the browser rather than on the server. It runs from the provider, over the WHOLE feed: the
  // three surfaces below each see only their own slice, and one of them consuming the key against a
  // partial list is how every reader gets re-interrupted on upgrade day.
  it('carries the pre-upgrade dismissal across, once', async () => {
    const a = announcement({ id: 7, display: 'both' })
    window.localStorage.setItem(LEGACY_KEY, announcementSig(a))
    feed.items = [a]
    migrateLegacyDismissal('alice', feed.items)

    renderAt('/')
    await screen.findAllByText('维护通知')
    await waitFor(() => expect(screen.queryByText('announcement.dontShowAgain')).toBeNull())
    expect(window.localStorage.getItem(LEGACY_KEY)).toBeNull()
  })

  it('keeps one reader’s dismissals off another reader’s screen', async () => {
    feed.items = [announcement({ id: 1, display: 'both' })]
    const user = userEvent.setup()
    renderAt('/')
    await user.click(await screen.findByText('announcement.dontShowAgain'))

    cleanup()
    feed.user = 'bob' // same browser, different account
    renderAt('/')
    expect(await screen.findByText('announcement.dontShowAgain')).toBeTruthy()
  })
})
