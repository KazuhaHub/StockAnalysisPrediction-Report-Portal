import { useEffect, useRef, useState, useSyncExternalStore, type CSSProperties, type ReactNode } from 'react'
import { Button, theme } from 'antd'
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  CloseOutlined,
  DownloadOutlined,
  ExclamationCircleOutlined,
  SafetyCertificateOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import {
  ASSERTIVE_KINDS,
  MAX_VISIBLE_NOTIFICATIONS,
  attachNotificationStack,
  dismissNotification,
  getNotifications,
  isAnnounced,
  markAnnounced,
  holdNotification,
  subscribeNotifications,
  visibleKeys,
  type NotificationItem,
  type NotifyKind,
} from '../lib/notify'
import { QUEUE_EVENT } from '../lib/shortcuts'
import { useHoverNone } from '../lib/useHoverNone'
import { formatTaskTime } from '../lib/taskUi'

const ICONS: Record<NotifyKind, ReactNode> = {
  security: <SafetyCertificateOutlined />,
  failure: <CloseCircleOutlined />,
  partial: <ExclamationCircleOutlined />,
  actionable: <DownloadOutlined />,
  success: <CheckCircleOutlined />,
}

// Whether the last input was the keyboard: only then does closing a card hand focus on, so a
// mouse click never makes the next card's close button appear.
let keyboardInput = false
// Where focus was before it entered the stack, to give it back when the last card closes.
let focusOrigin: Element | null = null

/** Focus left on a card that is closing goes to the next card, or back where the person came from. */
function handOffFocus(card: HTMLElement | null) {
  if (!card || !keyboardInput || !card.contains(document.activeElement)) return
  let next = card.nextElementSibling
  while (next && !next.matches('.rp-notify-card:not(.is-leaving)')) next = next.nextElementSibling
  const origin = focusOrigin instanceof HTMLElement && focusOrigin.isConnected ? focusOrigin : null
  // Focus that entered from nowhere (a script, or the page itself) goes to the header, not to body.
  const landmark = document.querySelector<HTMLElement>('#rp-app-header button, #rp-app-header a[href]')
  const target = next?.querySelector<HTMLElement>('.rp-notify-close') ?? origin ?? landmark
  target?.focus()
}

// A horizontal drag is claimed once it has moved this far and is mostly sideways.
const SWIPE_CLAIM_PX = 8
// Dismissed at this share of the card's width, or by a flick at least this fast (px per ms) over a few pixels.
const SWIPE_DISMISS_RATIO = 0.35
const SWIPE_FLICK_SPEED = 0.5
const SWIPE_FLICK_MIN_PX = 24

interface Drag {
  id: number
  x: number
  y: number
  t: number
  width: number
  claimed: boolean
}

function reducedMotionNow(): boolean {
  try {
    return window.matchMedia('(prefers-reduced-motion: reduce)').matches
  } catch {
    return false
  }
}

function Card({ item, now }: { item: NotificationItem; now: number }) {
  const { t, i18n } = useTranslation()
  const { token } = theme.useToken()
  const [hover, setHover] = useState(false)
  const [focus, setFocus] = useState(false)
  const cardRef = useRef<HTMLDivElement>(null)
  const noHover = useHoverNone()
  const hot = hover || focus || noHover
  const drag = useRef<Drag | null>(null)
  const key = item.key
  // A card that unmounts (folded into the "N more" pill) takes its pointer and focus with it; the
  // enter/leave events that would release the hold never arrive.
  useEffect(
    () => () => {
      holdNotification(key, 'hover', false)
      holdNotification(key, 'focus', false)
      holdNotification(key, 'press', false)
    },
    [key],
  )
  // Announce a raise once. A card that comes back from the pill was announced when it first showed,
  // and the relative-time tick must not re-announce it, so only the first render of an id is live.
  const liveId = useRef<number | null>(null)
  if (liveId.current === null && !isAnnounced(item.id)) liveId.current = item.id
  if (liveId.current !== null && liveId.current !== item.id && !isAnnounced(item.id)) liveId.current = item.id
  const live = liveId.current === item.id
  useEffect(() => markAnnounced(item.id), [item.id])
  const close = (reason: 'manual' | 'action') => {
    // An action decides where focus goes itself (it may navigate, open a drawer, start a download).
    if (reason === 'manual') handOffFocus(cardRef.current)
    dismissNotification(key, reason)
  }
    const settle = (el: HTMLElement) => {
    el.style.transition = reducedMotionNow() ? '' : 'transform 0.2s ease, opacity 0.2s ease'
    el.style.transform = ''
    el.style.opacity = ''
    window.setTimeout(() => { el.style.transition = '' }, 220)
  }
  const release = () => {
    drag.current = null
    holdNotification(key, 'press', false)
  }
  // Judged per gesture by the pointer that makes it, so a hybrid device swipes with a finger and hovers with a mouse.
  const fingerDown = (e: React.PointerEvent) => e.pointerType === 'touch' || e.pointerType === 'pen'
  const gesture = {
        onPointerDown: (e: React.PointerEvent<HTMLDivElement>) => {
          if (!fingerDown(e) || !e.isPrimary) return
          drag.current = { id: e.pointerId, x: e.clientX, y: e.clientY, t: Date.now(), width: e.currentTarget.getBoundingClientRect().width, claimed: false }
          holdNotification(key, 'press', true)
        },
        onPointerMove: (e: React.PointerEvent<HTMLDivElement>) => {
          const d = drag.current
          if (!d || d.id !== e.pointerId) return
          const dx = e.clientX - d.x
          const dy = e.clientY - d.y
          if (!d.claimed) {
            if (Math.abs(dy) > SWIPE_CLAIM_PX && Math.abs(dy) >= Math.abs(dx)) {
              // The page is scrolling under the finger: not ours.
              release()
              return
            }
            if (Math.abs(dx) < SWIPE_CLAIM_PX || Math.abs(dx) <= Math.abs(dy)) return
            d.claimed = true
            try { e.currentTarget.setPointerCapture?.(e.pointerId) } catch { /* the pointer is already gone */ }
          }
          e.currentTarget.style.transform = `translateX(${Math.max(0, dx)}px)`
          e.currentTarget.style.opacity = String(Math.max(0.3, 1 - Math.max(0, dx) / (d.width * 1.2)))
        },
        onPointerUp: (e: React.PointerEvent<HTMLDivElement>) => {
          const d = drag.current
          if (!d || d.id !== e.pointerId) return
          const el = e.currentTarget
          const dx = e.clientX - d.x
          const elapsed = Math.max(Date.now() - d.t, 1)
          const far = d.width > 0 && dx >= d.width * SWIPE_DISMISS_RATIO
          const flick = dx >= SWIPE_FLICK_MIN_PX && dx / elapsed >= SWIPE_FLICK_SPEED
          release()
          if (!d.claimed) return
          if (far || flick) {
            // Hand the exit animation the card's current offset so it carries on from there.
            el.style.setProperty('--rp-swipe-x', `${Math.max(0, dx)}px`)
            el.style.setProperty('--rp-swipe-end', `${Math.max(d.width, 1) * 1.1}px`)
            el.style.transform = ''
            el.style.opacity = ''
            handOffFocus(el)
            dismissNotification(key, 'swipe')
          } else {
            settle(el)
          }
        },
        onPointerCancel: (e: React.PointerEvent<HTMLDivElement>) => {
          const d = drag.current
          release()
          if (d?.claimed) settle(e.currentTarget)
        },
      }
  const color: Record<NotifyKind, string> = {
    security: token.purple,
    failure: token.colorError,
    partial: token.colorWarning,
    actionable: token.colorPrimary,
    success: token.colorSuccess,
  }
  const assertive = ASSERTIVE_KINDS.has(item.kind)
  const time = formatTaskTime(item.createdAt, i18n?.language || 'en-US', now, t('tasks.justNow'))
  return (
    <div
      className={`rp-notify-card${item.leaving ? ' is-leaving' : ''}`}
      ref={cardRef}
      role={live ? (assertive ? 'alert' : 'status') : 'group'}
      aria-live={live ? (assertive ? 'assertive' : 'polite') : undefined}
      data-kind={item.kind}
      {...gesture}
      onMouseEnter={() => {
        setHover(true)
        holdNotification(item.key, 'hover', true)
      }}
      onMouseLeave={() => {
        setHover(false)
        holdNotification(item.key, 'hover', false)
      }}
      onFocus={() => {
        setFocus(true)
        holdNotification(item.key, 'focus', true)
      }}
      onBlur={(e) => {
        if (e.currentTarget.contains(e.relatedTarget as Node | null)) return
        setFocus(false)
        holdNotification(item.key, 'focus', false)
      }}
    >
      <button
        type="button"
        className="rp-notify-close"
        aria-label={t('notifications.close')}
        data-visible={hot ? 'true' : 'false'}
        onClick={() => close('manual')}
      >
        <CloseOutlined />
      </button>
      <span className="rp-notify-icon" style={{ background: color[item.kind] }} aria-hidden="true">
        {ICONS[item.kind]}
      </span>
      <div className="rp-notify-body">
        <div className="rp-notify-top">
          <span className="rp-notify-title">{item.title}</span>
          {time && <span className="rp-notify-time" aria-hidden="true">{time}</span>}
        </div>
        {item.description && <div className="rp-notify-desc">{item.description}</div>}
        {item.actions.length > 0 && (
          <div className="rp-notify-actions">
            {item.actions.map((a, i) => (
              <Button
                key={i}
                size="small"
                type={a.primary ? 'primary' : 'default'}
                href={a.href}
                onClick={() => {
                  a.onClick?.()
                  if (!a.keepOpen) close('action')
                }}
              >
                {a.label}
              </Button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

/** Draws the notifications raised through notify(); mount once in the signed-in shell. */
export default function NotificationStack() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const items = useSyncExternalStore(subscribeNotifications, getNotifications, getNotifications)
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => attachNotificationStack(), [])
  useEffect(() => {
    const key = () => { keyboardInput = true }
    const pointer = () => { keyboardInput = false }
    window.addEventListener('keydown', key, true)
    window.addEventListener('pointerdown', pointer, true)
    return () => {
      window.removeEventListener('keydown', key, true)
      window.removeEventListener('pointerdown', pointer, true)
    }
  }, [])
  useEffect(() => {
    if (items.length === 0) return
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(id)
  }, [items.length === 0]) // eslint-disable-line react-hooks/exhaustive-deps

  const shown = visibleKeys(items)
  const waiting = items.filter((i) => !i.leaving).length - MAX_VISIBLE_NOTIFICATIONS
  const vars = {
    '--rp-n-bg': `color-mix(in srgb, ${token.colorBgElevated} 78%, transparent)`,
    '--rp-n-solid': token.colorBgElevated,
    '--rp-n-border': token.colorBorderSecondary,
    '--rp-n-text': token.colorText,
    '--rp-n-muted': token.colorTextSecondary,
    '--rp-n-close-bg': token.colorBgElevated,
    '--rp-n-shadow': token.boxShadowSecondary,
  } as CSSProperties
  return (
    <div className="rp-notify-stack" role="region" aria-label={t('notifications.region')} style={vars}
      onFocus={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) focusOrigin = e.relatedTarget as Element | null
      }}
    >
      {items
        .filter((i) => shown.has(i.key) || i.leaving)
        .map((i) => (
          <Card key={i.key} item={i} now={now} />
        ))}
      {waiting > 0 && (
        <button type="button" className="rp-notify-more" onClick={() => window.dispatchEvent(new Event(QUEUE_EVENT))}>
          {t('notifications.more', { count: waiting })}
        </button>
      )}
    </div>
  )
}
