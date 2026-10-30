import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import NotificationStack from './NotificationStack'
import { NOTIFICATION_DURATIONS, notify, resetNotifications, type NotifyKind } from '../lib/notify'
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
const interact = () => act(() => { window.dispatchEvent(new Event('pointermove')) })
const titles = () => Array.from(document.querySelectorAll('.rp-notify-title')).map((e) => e.textContent)

beforeEach(() => {
  vi.useFakeTimers()
  visibility = 'visible'
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility })
})
afterEach(() => {
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
    advance(NOTIFICATION_DURATIONS.failure! - 9_000 + 100) // the three expire; old has counted ~0.1s
    advance(400)
    expect(titles()).toEqual(['old'])
    advance(NOTIFICATION_DURATIONS.success! - 1_000)
    expect(titles()).toEqual(['old'])
    advance(1_500)
    advance(400)
    expect(titles()).toEqual([])
  })
})
