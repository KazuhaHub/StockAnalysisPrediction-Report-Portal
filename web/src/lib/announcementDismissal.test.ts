import { describe, it, expect, beforeEach } from 'vitest'
import {
  announcementSig,
  dismissBanner,
  dismissPopup,
  forgetAcknowledgements,
  loadDismissals,
  markSeen,
  migrateLegacyDismissal,
} from './announcementDismissal'
import type { Announcement } from '../api/types'

const MAP_KEY = 'report-portal.announce.dismissed.v1.alice'
const LEGACY_KEY = 'report-portal.site-announcement.popup.dismissed'
const DAY = 86400000

function announcement(over: Partial<Announcement> = {}): Announcement {
  return {
    id: 1,
    level: 'warning',
    title: '维护通知',
    content: '今晚 22:00 开始维护。',
    display: 'both',
    stripDismissible: false,
    popupDismissible: true,
    scope: 'home',
    endsAt: '',
    ...over,
  }
}

describe('announcementSig', () => {
  // The legacy localStorage value was produced by exactly this hash over exactly this shape. That
  // is what makes the one-time migration a match rather than a guess, and it is why the signature
  // is computed in the browser instead of being handed down by the server.
  it('is stable, and covers only what the announcement says', () => {
    const a = announcement()
    expect(announcementSig(a)).toBe(announcementSig(announcement()))
    expect(announcementSig(announcement({ id: 99, display: 'strip', scope: 'app', endsAt: 'x' })))
      .toBe(announcementSig(a))
    expect(announcementSig(announcement({ content: '推迟到明晚。' }))).not.toBe(announcementSig(a))
    expect(announcementSig(announcement({ level: 'error' }))).not.toBe(announcementSig(a))
  })
})

describe('dismissal storage', () => {
  beforeEach(() => {
    window.localStorage.clear()
    window.sessionStorage.clear()
    forgetAcknowledgements()
  })

  // The production report: an operator switched "readers can close it" off, and readers who had
  // closed the band while it was allowed never saw it again. The switch is read when the dismissal
  // is consulted, not only when it is made.
  it('honours a closed band only while the announcement may be closed', () => {
    const open = announcement({ stripDismissible: true })
    dismissBanner('alice', open)
    expect(loadDismissals('alice', [open]).bannerDismissed(open)).toBe(true)

    const locked = announcement({ stripDismissible: false })
    expect(loadDismissals('alice', [locked]).bannerDismissed(locked)).toBe(false)
    // Ignored, not erased — the same rule as garbage collection: what the feed says right now is
    // never a reason to delete what the reader chose.
    expect(loadDismissals('alice', [open]).bannerDismissed(open)).toBe(true)
  })

  it('never stores a band dismissal the operator did not allow', () => {
    dismissBanner('alice', announcement({ stripDismissible: false }))
    expect(window.localStorage.getItem(MAP_KEY)).toBeNull()
  })

  // The popup has its own switch, so the band's switch must not govern it and the other way round.
  it('honours a stored popup dismissal and "got it" only while the popup may be dismissed', () => {
    const open = announcement({ popupDismissible: true })
    dismissPopup('alice', open)
    markSeen('alice', open)
    forgetAcknowledgements() // a reload: only what the browser stored survives

    const locked = announcement({ popupDismissible: false })
    const state = loadDismissals('alice', [locked])
    expect(state.popupDismissed(locked)).toBe(false)
    expect(state.seenThisSession(locked)).toBe(false)

    // Ignored, not erased: switching it back on restores what the reader chose.
    const reopened = loadDismissals('alice', [open])
    expect(reopened.popupDismissed(open)).toBe(true)
    expect(reopened.seenThisSession(open)).toBe(true)
  })

  it('keeps the two switches independent of each other', () => {
    const bandOnly = announcement({ stripDismissible: true, popupDismissible: false })
    dismissBanner('alice', bandOnly)
    dismissPopup('alice', bandOnly)
    expect(loadDismissals('alice', [bandOnly]).bannerDismissed(bandOnly)).toBe(true)
    expect(loadDismissals('alice', [bandOnly]).popupDismissed(bandOnly)).toBe(false)

    const popupOnly = announcement({ id: 2, stripDismissible: false, popupDismissible: true })
    dismissBanner('alice', popupOnly)
    dismissPopup('alice', popupOnly)
    expect(loadDismissals('alice', [popupOnly]).bannerDismissed(popupOnly)).toBe(false)
    expect(loadDismissals('alice', [popupOnly]).popupDismissed(popupOnly)).toBe(true)
  })

  it('never stores a popup dismissal the operator did not allow', () => {
    const locked = announcement({ popupDismissible: false })
    dismissPopup('alice', locked)
    markSeen('alice', locked)
    expect(window.localStorage.getItem(MAP_KEY)).toBeNull()
    expect(window.sessionStorage.length).toBe(0)
  })

  // "Got it" on a popup readers may not dismiss still has to let them through — a dialog with no
  // exit locks them out of the portal — so it is remembered in memory, for this page load only.
  it('remembers "got it" on a locked popup for this page load and no longer', () => {
    const locked = announcement({ popupDismissible: false })
    markSeen('alice', locked)
    expect(loadDismissals('alice', [locked]).seenThisSession(locked)).toBe(true)
    expect(loadDismissals('bob', [locked]).seenThisSession(locked)).toBe(false)

    forgetAcknowledgements() // a reload
    expect(loadDismissals('alice', [locked]).seenThisSession(locked)).toBe(false)
  })

  it('keeps dismissals per announcement and per reader', () => {
    const a = announcement({ id: 1 })
    const b = announcement({ id: 2, title: '另一条' })
    dismissPopup('alice', a)

    const alice = loadDismissals('alice', [a, b])
    expect(alice.popupDismissed(a)).toBe(true)
    expect(alice.popupDismissed(b)).toBe(false)
    expect(loadDismissals('bob', [a, b]).popupDismissed(a)).toBe(false)
  })

  it('re-notifies when the wording changes but not when anything else does', () => {
    const a = announcement()
    dismissPopup('alice', a)
    expect(loadDismissals('alice', [a]).popupDismissed(a)).toBe(true)

    // Reordered, rescheduled, retargeted, switched — same message, still silenced.
    const untouched = announcement({ scope: 'app', endsAt: '2026-09-01T00:00:00Z', stripDismissible: true })
    expect(loadDismissals('alice', [untouched]).popupDismissed(untouched)).toBe(true)

    const edited = announcement({ content: '维护推迟到明晚。' })
    expect(loadDismissals('alice', [edited]).popupDismissed(edited)).toBe(false)
  })

  // The tempting garbage-collection rule — "drop anything not in the current feed" — is wrong. The
  // feed filters by enabled, by display window and by audience, so "absent" has four meanings and
  // only one is "deleted". Switching an announcement off for a day would erase everyone's
  // dismissal and re-interrupt them all the next morning.
  it('does not forget a dismissal just because the announcement is not on offer right now', () => {
    const a = announcement()
    dismissPopup('alice', a)
    loadDismissals('alice', []) // e.g. the operator switched it off overnight
    expect(loadDismissals('alice', [a]).popupDismissed(a)).toBe(true)
  })

  it('drops entries older than the retention window', () => {
    const a = announcement()
    dismissPopup('alice', a, Date.now() - 200 * DAY)
    // Any write re-runs the trim, so a second, current dismissal evicts the ancient one.
    dismissPopup('alice', announcement({ id: 2 }))
    const stored = JSON.parse(window.localStorage.getItem(MAP_KEY) as string)
    expect(Object.keys(stored)).toEqual(['2'])
  })

  // The three reader surfaces each see only the announcements they draw, so a migration run from
  // one of them could compare the legacy hash against a partial list — or, before the first fetch
  // lands, against nothing — and delete it unmatched. Refusing an empty list is what makes the
  // provider the only sensible caller.
  it('refuses to consume the pre-upgrade key when there is nothing to match it against', () => {
    const a = announcement()
    window.localStorage.setItem(LEGACY_KEY, announcementSig(a))

    migrateLegacyDismissal('alice', [])
    expect(window.localStorage.getItem(LEGACY_KEY)).toBe(announcementSig(a))

    migrateLegacyDismissal('alice', [a])
    expect(window.localStorage.getItem(LEGACY_KEY)).toBeNull()
    expect(loadDismissals('alice', [a]).popupDismissed(a)).toBe(true)
  })

  it('drops the pre-upgrade key when it matches nothing, so it cannot run forever', () => {
    window.localStorage.setItem(LEGACY_KEY, 'a-hash-of-something-since-deleted')
    const a = announcement()
    migrateLegacyDismissal('alice', [a])
    expect(window.localStorage.getItem(LEGACY_KEY)).toBeNull()
    expect(loadDismissals('alice', [a]).popupDismissed(a)).toBe(false)
  })

  it('survives a corrupt or hostile stored value instead of throwing in the app shell', () => {
    const a = announcement()
    for (const junk of ['not json', '[]', '{"1":42}', 'null']) {
      window.localStorage.setItem(MAP_KEY, junk)
      expect(() => loadDismissals('alice', [a])).not.toThrow()
      expect(loadDismissals('alice', [a]).popupDismissed(a)).toBe(false)
    }
  })
})
