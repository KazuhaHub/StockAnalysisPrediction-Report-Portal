import type { ReactNode } from 'react'

// The one place a pop-up notification is raised. Every transient message in the app goes through
// notify(); NotificationStack (mounted once in the signed-in shell) draws what this store holds.
//
// A notification must not vanish while its reader is away, so its countdown is gated on the
// notification having been SEEN: the page is visible and the person has interacted since it
// appeared. Time only counts while the page is visible, and hovering or focusing the card holds it.

export type NotifyKind = 'security' | 'failure' | 'partial' | 'actionable' | 'success'

/** Milliseconds a notification stays after it has been seen; null never closes by itself. */
export const NOTIFICATION_DURATIONS: Record<NotifyKind, number | null> = {
  security: null,
  failure: 20_000,
  partial: 15_000,
  actionable: 15_000,
  success: 8_000,
}

/** How many notifications are drawn at once; the rest collapse into a "N more" pill. */
export const MAX_VISIBLE_NOTIFICATIONS = 3

/** Kinds announced assertively; the rest are polite. */
export const ASSERTIVE_KINDS: ReadonlySet<NotifyKind> = new Set<NotifyKind>(['failure', 'security'])

const EXIT_MS = 250
const TICK_MS = 100

export type NotifyCloseReason = 'manual' | 'action' | 'timeout'

export interface NotifyAction {
  label: ReactNode
  /** The main action of the card, drawn as the primary button. */
  primary?: boolean
  /** Renders a link instead of a button. */
  href?: string
  onClick?: () => void
  /** Keep the notification open after the action runs (default: the action closes it). */
  keepOpen?: boolean
}

export interface NotifyOptions {
  kind: NotifyKind
  title: ReactNode
  description?: ReactNode
  actions?: NotifyAction[]
  /** A notification with the same key replaces the earlier one instead of stacking. */
  key?: string
  onClose?: (reason: NotifyCloseReason) => void
}

export interface NotificationItem {
  /** Unique per raise: a replacement under the same key gets a new id. */
  id: number
  key: string
  kind: NotifyKind
  title: ReactNode
  description?: ReactNode
  actions: NotifyAction[]
  createdAt: number
  leaving: boolean
  onClose?: (reason: NotifyCloseReason) => void
}

interface Timer {
  remaining: number | null
  seen: boolean
  hover: boolean
  focus: boolean
}

let seq = 0
let items: NotificationItem[] = [] // newest first
let snapshot: readonly NotificationItem[] = items
const timers = new Map<string, Timer>()
const exitTimeouts = new Map<string, ReturnType<typeof setTimeout>>()
const listeners = new Set<() => void>()
const announced = new Set<number>()
let mounted = 0
let tickHandle: ReturnType<typeof setInterval> | null = null
let lastTick = 0

// Scroll is deliberately absent: pages scroll themselves (chat auto-scroll), which is not a person
// looking at the screen. Only trusted (user-originated) events count.
const INTERACTION_EVENTS = ['pointermove', 'pointerdown', 'mousemove', 'mousedown', 'keydown', 'touchstart', 'wheel']

function reducedMotion(): boolean {
  try {
    return window.matchMedia('(prefers-reduced-motion: reduce)').matches
  } catch {
    return false
  }
}

function pageVisible(): boolean {
  return typeof document === 'undefined' || document.visibilityState !== 'hidden'
}

function publish() {
  snapshot = items.slice()
  listeners.forEach((l) => l())
  syncRuntime()
}

/** The notifications that are drawn: the newest few that are not on their way out, plus leaving ones. */
export function visibleKeys(list: readonly NotificationItem[] = snapshot): Set<string> {
  return new Set(list.filter((i) => !i.leaving).slice(0, MAX_VISIBLE_NOTIFICATIONS).map((i) => i.key))
}

export function subscribeNotifications(l: () => void): () => void {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

export function getNotifications(): readonly NotificationItem[] {
  return snapshot
}

/** Raise a notification; returns its key. */
export function notify(opts: NotifyOptions): string {
  const key = opts.key ?? `notify-${seq + 1}`
  const prev = items.findIndex((i) => i.key === key)
  if (prev >= 0) items.splice(prev, 1)
  const held = timers.get(key)
  const pending = exitTimeouts.get(key)
  if (pending) {
    clearTimeout(pending)
    exitTimeouts.delete(key)
  }
  items.unshift({
    id: ++seq,
    key,
    kind: opts.kind,
    title: opts.title,
    description: opts.description,
    actions: opts.actions ?? [],
    createdAt: Date.now(),
    leaving: false,
    onClose: opts.onClose,
  })
  // A replacement keeps the holds of the card it replaces: the pointer is still over it.
  timers.set(key, { remaining: NOTIFICATION_DURATIONS[opts.kind], seen: false, hover: held?.hover ?? false, focus: held?.focus ?? false })
  publish()
  return key
}

/** Close a notification by key (an exit animation, then removal). */
export function dismissNotification(key: string, reason: NotifyCloseReason = 'manual'): void {
  const item = items.find((i) => i.key === key)
  if (!item || item.leaving) return
  timers.delete(key)
  const remove = () => {
    exitTimeouts.delete(key)
    items = items.filter((i) => i.key !== key)
    publish()
  }
  if (reducedMotion()) {
    items = items.map((i) => (i.key === key ? { ...i, leaving: true } : i))
    remove()
  } else {
    items = items.map((i) => (i.key === key ? { ...i, leaving: true } : i))
    exitTimeouts.set(key, setTimeout(remove, EXIT_MS))
    publish()
  }
  item.onClose?.(reason)
}

/** Hold or release a notification's countdown (hover or keyboard focus). */
export function holdNotification(key: string, source: 'hover' | 'focus', on: boolean): void {
  const timer = timers.get(key)
  if (timer) timer[source] = on
}

/** Mark the cards that are on screen as seen. A card folded into the "N more" pill has not been. */
function markSeen() {
  if (!pageVisible()) return
  visibleKeys().forEach((key) => {
    const t = timers.get(key)
    if (t) t.seen = true
  })
}

/** How an input event is judged to come from a person. A seam so tests, whose events are never trusted, can stand in. */
export const notifyInput = { isUserEvent: (e: Event): boolean => e.isTrusted }

function onInteract(e: Event) {
  if (notifyInput.isUserEvent(e)) markSeen()
}

export function isAnnounced(id: number): boolean {
  return announced.has(id)
}

export function markAnnounced(id: number): void {
  announced.add(id)
}

function tick() {
  const now = Date.now()
  const dt = Math.min(Math.max(now - lastTick, 0), 1000)
  lastTick = now
  if (mounted === 0 || !pageVisible()) return
  const shown = visibleKeys()
  const expired: string[] = []
  shown.forEach((key) => {
    const t = timers.get(key)
    if (!t || t.remaining === null || !t.seen || t.hover || t.focus) return
    t.remaining -= dt
    if (t.remaining <= 0) expired.push(key)
  })
  expired.forEach((key) => dismissNotification(key, 'timeout'))
}

function onVisibility() {
  markSeen()
}

function syncRuntime() {
  const wanted = items.length > 0 && mounted > 0
  if (wanted && tickHandle === null) {
    lastTick = Date.now()
    tickHandle = setInterval(tick, TICK_MS)
    INTERACTION_EVENTS.forEach((e) => window.addEventListener(e, onInteract, { passive: true, capture: true }))
    document.addEventListener('visibilitychange', onVisibility)
  } else if (!wanted && tickHandle !== null) {
    clearInterval(tickHandle)
    tickHandle = null
    INTERACTION_EVENTS.forEach((e) => window.removeEventListener(e, onInteract, { capture: true }))
    document.removeEventListener('visibilitychange', onVisibility)
  }
}

/** Called by the stack on mount: countdowns only run while something is drawing the notifications. */
export function attachNotificationStack(): () => void {
  mounted++
  syncRuntime()
  return () => {
    mounted--
    syncRuntime()
  }
}

/** Drop everything; for tests and sign-out. */
export function resetNotifications(): void {
  exitTimeouts.forEach((h) => clearTimeout(h))
  exitTimeouts.clear()
  timers.clear()
  announced.clear()
  items = []
  publish()
}
