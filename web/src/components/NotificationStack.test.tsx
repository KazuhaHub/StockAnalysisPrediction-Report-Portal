import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import NotificationStack from './NotificationStack'
import { NOTIFICATION_DURATIONS, getNotifications, notifyInput, notify, resetNotifications, type NotifyKind } from '../lib/notify'
import { QUEUE_EVENT } from '../lib/shortcuts'

vi.mock('react-i18next', () => {
  const t = (k: string, o?: unknown) => (typeof o === 'string' || o === undefined ? k : `${k}:${JSON.stringify(o)}`)
  const hook = { t, i18n: { language: 'en-US' } }
  return { useTranslation: () => hook }
})

let visibility: 'visible' | 'hidden' = 'visible'
const setVisibility = (v: 'visible' | 'hidden') => {
  visibility = v
  document.dispatchEvent(new Event('visibilitychange'))
}
const advance = (ms: number) => act(() => { vi.advanceTimersByTime(ms) })
// jsdom's isTrusted cannot be forged, so the notifier's seam reads a flag the helper sets instead.
const fire = (type: string, trusted: boolean) =>
  act(() => {
    const e = new Event(type) as Event & { fromPerson?: boolean }
    e.fromPerson = trusted
    window.dispatchEvent(e)
  })
const interact = () => fire('pointermove', true)
const titles = () => Array.from(document.querySelectorAll('.rp-notify-title')).map((e) => e.textContent)

const realIsUserEvent = notifyInput.isUserEvent
beforeEach(() => {
  notifyInput.isUserEvent = (e) => (e as Event & { fromPerson?: boolean }).fromPerson === true
  vi.useFakeTimers()
  visibility = 'visible'
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility })
})
afterEach(() => {
  notifyInput.isUserEvent = realIsUserEvent
  act(() => resetNotifications())
  vi.useRealTimers()
})

const mount = () => render(<NotificationStack />)
const raise = (kind: NotifyKind, title = kind as string, extra = {}) => act(() => { notify({ kind, title, ...extra }) })

describe('notification countdown', () => {
  it('does not count down before the person interacts', () => {
    mount()
    raise('success', 'done')
    advance(60_000)
    expect(titles()).toEqual(['done'])
  })

  it('counts down once the person has interacted, then slides out', () => {
    mount()
    raise('success', 'done')
    interact()
    advance(NOTIFICATION_DURATIONS.success! - 200)
    expect(titles()).toEqual(['done'])
    advance(400)
    advance(400) // exit animation
    expect(titles()).toEqual([])
  })

  it('uses a different duration per kind and never closes a security notice', () => {
    mount()
    raise('security', 'sec')
    raise('failure', 'fail')
    raise('partial', 'part')
    raise('success', 'ok')
    interact()
    advance(8_500)
    expect(titles()).not.toContain('ok')
    expect(titles()).toContain('part')
    advance(7_000)
    expect(titles()).not.toContain('part')
    expect(titles()).toContain('fail')
    advance(5_500)
    expect(titles()).not.toContain('fail')
    advance(10 * 60_000)
    expect(titles()).toEqual(['sec'])
  })

  it('waits for a visible page, and only counts visible time', () => {
    mount()
    setVisibility('hidden')
    raise('success', 'done')
    advance(60_000)
    interact()
    advance(60_000)
    expect(titles()).toEqual(['done'])
    setVisibility('visible') // becoming visible counts as the first interaction
    advance(5_000)
    setVisibility('hidden')
    advance(60_000)
    expect(titles()).toEqual(['done'])
    setVisibility('visible')
    advance(4_000)
    expect(titles()).toEqual([])
  })

  it('pauses while hovered or focused', () => {
    mount()
    raise('success', 'done')
    interact()
    const card = document.querySelector('.rp-notify-card')!
    fireEvent.mouseEnter(card)
    advance(60_000)
    expect(titles()).toEqual(['done'])
    fireEvent.mouseLeave(card)
    fireEvent.focus(card)
    advance(60_000)
    expect(titles()).toEqual(['done'])
    fireEvent.blur(card)
    advance(NOTIFICATION_DURATIONS.success! + 400)
    expect(titles()).toEqual([])
  })
})

describe('notification card', () => {
  it('shows a labelled close button on hover or focus that closes the card', () => {
    const onClose = vi.fn()
    mount()
    raise('failure', 'bad', { onClose })
    const close = screen.getByRole('button', { name: 'notifications.close' })
    expect(close.getAttribute('data-visible')).toBe('false')
    const card = document.querySelector('.rp-notify-card')!
    fireEvent.mouseEnter(card)
    expect(close.getAttribute('data-visible')).toBe('true')
    fireEvent.mouseLeave(card)
    fireEvent.focus(close)
    expect(close.getAttribute('data-visible')).toBe('true')
    fireEvent.click(close)
    advance(400)
    expect(titles()).toEqual([])
    expect(onClose).toHaveBeenCalledWith('manual')
  })

  it('closes after an action, unless the action keeps it open', () => {
    const go = vi.fn()
    mount()
    raise('actionable', 'a', { actions: [{ label: 'Go', primary: true, onClick: go }] })
    raise('actionable', 'b', { actions: [{ label: 'Stay', keepOpen: true }] })
    fireEvent.click(screen.getByText('Go'))
    fireEvent.click(screen.getByText('Stay'))
    advance(400)
    expect(go).toHaveBeenCalled()
    expect(titles()).toEqual(['b'])
  })

  it('renders a link for an href action', () => {
    mount()
    raise('actionable', 'a', { actions: [{ label: 'Download', href: '/f' }] })
    expect(screen.getByRole('link', { name: /Download/ }).getAttribute('href')).toBe('/f')
  })

  it('replaces a notification with the same key instead of stacking', () => {
    mount()
    raise('success', 'one', { key: 'k' })
    raise('success', 'two', { key: 'k' })
    expect(titles()).toEqual(['two'])
  })

  it('draws an antd icon for every kind and no emoji or glyph text', () => {
    mount()
    const kinds: NotifyKind[] = ['security', 'failure', 'partial', 'actionable', 'success']
    for (const k of kinds) raise(k, k, { key: k })
    // Three are drawn; check them, then the rest after closing.
    const check = () => {
      for (const card of Array.from(document.querySelectorAll('.rp-notify-card'))) {
        expect(card.querySelector('.rp-notify-icon .anticon')).toBeTruthy()
        expect(card.querySelector('.rp-notify-close .anticon')).toBeTruthy()
        expect(card.querySelector('.rp-notify-icon')!.textContent).toBe('')
        expect(card.querySelector('.rp-notify-close')!.textContent).toBe('')
      }
    }
    check()
    act(() => resetNotifications())
    for (const k of kinds.slice(0, 2)) raise(k, k, { key: k })
    check()
  })

  it('announces failures and security assertively and the rest politely', () => {
    mount()
    raise('security', 's')
    raise('failure', 'f')
    raise('success', 'ok')
    const roles = Array.from(document.querySelectorAll('.rp-notify-card')).map((c) => [c.getAttribute('role'), c.getAttribute('aria-live')])
    expect(roles).toEqual([['status', 'polite'], ['alert', 'assertive'], ['alert', 'assertive']])
    expect(screen.getByRole('region', { name: 'notifications.region' })).toBeTruthy()
  })
})

describe('notification stack limit', () => {
  it('draws three, newest first, and folds the rest into a pill that opens the Tasks drawer', () => {
    const opened = vi.fn()
    window.addEventListener(QUEUE_EVENT, opened)
    mount()
    for (const n of ['1', '2', '3', '4', '5']) raise('success', n)
    expect(titles()).toEqual(['5', '4', '3'])
    const pill = screen.getByText('notifications.more:{"count":2}')
    fireEvent.click(pill)
    expect(opened).toHaveBeenCalledTimes(1)
    window.removeEventListener(QUEUE_EVENT, opened)
  })

  it('keeps hidden notifications paused until they are drawn', () => {
    mount()
    raise('success', 'old')
    for (const n of ['2', '3', '4']) raise('failure', n)
    interact()
    advance(NOTIFICATION_DURATIONS.success! + 1_000) // old is hidden: no time passes for it
    expect(titles()).toEqual(['4', '3', '2'])
    advance(NOTIFICATION_DURATIONS.failure! - 9_000 + 100) // the three expire
    advance(400)
    expect(titles()).toEqual(['old'])
    interact() // it was folded away when the person last interacted; it needs a fresh look
    advance(NOTIFICATION_DURATIONS.success! - 1_000)
    expect(titles()).toEqual(['old'])
    advance(1_500)
    advance(400)
    expect(titles()).toEqual([])
  })
})

describe('notification countdown, edge cases', () => {
  it('releases a hover hold when the card is folded into the pill while hovered', () => {
    mount()
    raise('success', 'old')
    interact()
    fireEvent.mouseEnter(document.querySelector('.rp-notify-card')!)
    for (const n of ['a', 'b', 'c']) raise('success', n) // pushes "old" out; no mouseleave ever fires
    interact()
    advance(NOTIFICATION_DURATIONS.success! + 500)
    advance(400)
    expect(titles()).toEqual(['old'])
    interact() // it came back from the pill: a fresh look starts its countdown
    advance(NOTIFICATION_DURATIONS.success! + 500)
    advance(400)
    expect(titles()).toEqual([])
  })

  it('does not mark a card seen while it is folded into the pill', () => {
    mount()
    raise('success', 'old')
    for (const n of ['a', 'b', 'c']) raise('success', n)
    interact()
    advance(NOTIFICATION_DURATIONS.success! + 500)
    advance(400)
    expect(titles()).toEqual(['old']) // moved up, but nobody has looked at it yet
    advance(10 * 60_000)
    expect(titles()).toEqual(['old'])
    interact()
    advance(NOTIFICATION_DURATIONS.success! + 500)
    advance(400)
    expect(titles()).toEqual([])
  })

  it('ignores scroll and untrusted events', () => {
    mount()
    raise('success', 'done')
    fire('scroll', true)
    fire('pointermove', false)
    fire('keydown', false)
    advance(60_000)
    expect(titles()).toEqual(['done'])
    fire('keydown', true)
    advance(NOTIFICATION_DURATIONS.success! + 500)
    advance(400)
    expect(titles()).toEqual([])
  })

  it('keeps the hover hold across a replacement under the same key', () => {
    mount()
    raise('success', 'one', { key: 'k' })
    interact()
    fireEvent.mouseEnter(document.querySelector('.rp-notify-card')!)
    raise('success', 'two', { key: 'k' })
    interact()
    advance(60_000)
    expect(titles()).toEqual(['two'])
  })

  it('runs no timer or listener while no stack is mounted', () => {
    raise('success', 'orphan')
    expect(vi.getTimerCount()).toBe(0)
    const view = mount()
    expect(vi.getTimerCount()).toBeGreaterThan(0)
    view.unmount()
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('notification announcements and focus', () => {
  it('hides the relative time from assistive technology', () => {
    mount()
    raise('success', 'done')
    expect(document.querySelector('.rp-notify-time')!.getAttribute('aria-hidden')).toBe('true')
  })

  it('announces a card once: not again when it returns from the pill, but again when replaced', () => {
    mount()
    raise('failure', 'old', { key: 'old' })
    for (const n of ['a', 'b', 'c']) raise('success', n)
    interact()
    advance(NOTIFICATION_DURATIONS.success! + 500)
    advance(400)
    const card = document.querySelector('.rp-notify-card')!
    expect(card.textContent).toContain('old')
    expect(card.getAttribute('aria-live')).toBeNull()
    expect(card.getAttribute('role')).not.toBe('alert')
    raise('failure', 'old again', { key: 'old' })
    expect(document.querySelector('.rp-notify-card')!.getAttribute('aria-live')).toBe('assertive')
  })

  it('moves keyboard focus to the next card when the focused one closes', () => {
    mount()
    raise('success', 'lower')
    raise('success', 'upper')
    act(() => { fire('keydown', true) })
    const closes = screen.getAllByRole('button', { name: 'notifications.close' })
    closes[0].focus()
    fireEvent.click(closes[0])
    expect(document.activeElement!.closest('.rp-notify-card')!.textContent).toContain('lower')
  })

  it('gives focus back to where it came from when the last card closes', () => {
    render(<button>before</button>)
    mount()
    raise('success', 'only')
    const before = screen.getByText('before')
    before.focus()
    fire('keydown', true)
    const close = screen.getByRole('button', { name: 'notifications.close' })
    close.focus()
    fireEvent.click(close)
    expect(document.activeElement).toBe(before)
  })
})

describe('notification re-review cases', () => {
  it('restarts a seen card that was folded into the pill, and needs a fresh look', () => {
    mount()
    raise('success', 'old')
    interact()
    advance(7_500) // almost expired
    for (const n of ['a', 'b', 'c']) raise('success', n) // old is folded into the pill
    interact()
    advance(NOTIFICATION_DURATIONS.success! + 500) // a, b, c expire
    advance(400)
    expect(titles()).toEqual(['old'])
    advance(60_000) // no interaction since it came back: still there
    expect(titles()).toEqual(['old'])
    interact()
    advance(7_500) // a full duration again, not the 0.5 s it had left
    expect(titles()).toEqual(['old'])
    advance(1_000)
    advance(400)
    expect(titles()).toEqual([])
  })

  it('does not hand focus to another card when an action closes the card', () => {
    mount()
    raise('success', 'lower')
    raise('actionable', 'upper', { actions: [{ label: 'Go', primary: true }] })
    fire('keydown', true)
    const go = screen.getByRole('button', { name: 'Go' })
    go.focus()
    fireEvent.click(go)
    expect(document.activeElement!.closest('.rp-notify-card')?.textContent ?? '').not.toContain('lower')
  })

  it('falls back to the header when focus entered the stack from nowhere', () => {
    render(<header id="rp-app-header"><button>Tasks</button></header>)
    mount()
    raise('success', 'only')
    fire('keydown', true)
    const close = screen.getByRole('button', { name: 'notifications.close' })
    close.focus()
    fireEvent.click(close)
    expect(document.activeElement).toBe(screen.getByText('Tasks'))
  })
})

describe('notifications across sign-in identities', () => {
  it('resetNotifications drops every card', () => {
    raise('security', 'alice only')
    act(() => resetNotifications())
    expect(getNotifications()).toHaveLength(0)
  })
})

describe('notifications on a touch device', () => {
  const CARD_WIDTH = 300
  beforeEach(() => {
    vi.stubGlobal('matchMedia', (query: string): MediaQueryList => ({
      matches: query.includes('any-pointer: coarse'),
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }))
  })
  afterEach(() => vi.unstubAllGlobals())

  const card = () => {
    const el = document.querySelector<HTMLElement>('.rp-notify-card')!
    el.getBoundingClientRect = () => ({ width: CARD_WIDTH, height: 60, top: 0, left: 0, right: CARD_WIDTH, bottom: 60, x: 0, y: 0, toJSON: () => ({}) })
    return el
  }
  const touch = (el: Element, type: 'pointerDown' | 'pointerMove' | 'pointerUp', x: number, y = 0) =>
    act(() => {
      fireEvent[type](el, { pointerId: 1, pointerType: 'touch', clientX: x, clientY: y, isPrimary: true })
    })

  it('keeps the close button visible', () => {
    mount()
    raise('success', 'done')
    expect(screen.getByRole('button', { name: 'notifications.close' }).getAttribute('data-visible')).toBe('true')
  })

  it('dismisses a card dragged to the right past the threshold', () => {
    const onClose = vi.fn()
    mount()
    raise('success', 'done', { onClose })
    const el = card()
    touch(el, 'pointerDown', 10)
    touch(el, 'pointerMove', 60)
    advance(600)
    touch(el, 'pointerMove', 10 + CARD_WIDTH * 0.4)
    touch(el, 'pointerUp', 10 + CARD_WIDTH * 0.4)
    expect(onClose).toHaveBeenCalledWith('swipe')
    expect(el.classList.contains('is-leaving')).toBe(true)
  })

  it('dismisses on a fast flick that is shorter than the threshold', () => {
    const onClose = vi.fn()
    mount()
    raise('success', 'done', { onClose })
    const el = card()
    touch(el, 'pointerDown', 10)
    advance(40)
    touch(el, 'pointerMove', 70)
    touch(el, 'pointerUp', 70)
    expect(onClose).toHaveBeenCalledWith('swipe')
  })

  it('snaps a short, slow drag back', () => {
    const onClose = vi.fn()
    mount()
    raise('success', 'done', { onClose })
    const el = card()
    touch(el, 'pointerDown', 10)
    touch(el, 'pointerMove', 40)
    advance(600)
    touch(el, 'pointerMove', 50)
    touch(el, 'pointerUp', 50)
    expect(onClose).not.toHaveBeenCalled()
    expect(titles()).toEqual(['done'])
    expect(el.style.transform).toBe('')
  })

  it('does not claim a vertical gesture', () => {
    const onClose = vi.fn()
    mount()
    raise('success', 'done', { onClose })
    const el = card()
    touch(el, 'pointerDown', 10, 10)
    touch(el, 'pointerMove', 20, 80)
    touch(el, 'pointerMove', 10 + CARD_WIDTH, 90)
    touch(el, 'pointerUp', 10 + CARD_WIDTH, 90)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('pauses the countdown while a card is pressed and resumes on release', () => {
    mount()
    raise('success', 'done')
    interact()
    const el = card()
    touch(el, 'pointerDown', 10)
    advance(NOTIFICATION_DURATIONS.success! + 2000)
    expect(titles()).toEqual(['done'])
    touch(el, 'pointerUp', 10)
    advance(NOTIFICATION_DURATIONS.success! - 200)
    expect(titles()).toEqual(['done'])
    advance(400)
    advance(400)
    expect(titles()).toEqual([])
  })
})
