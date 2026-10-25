import { flushSync } from 'react-dom'

interface DashboardMotionOptions {
  preserveScroll?: boolean
  force?: boolean
  scope?: HTMLElement | null
}

/** Animate edits in place: viewport-level snapshots escape the dashboard's scroll clip. */
export function animateDashboardChange(update: () => void, options: DashboardMotionOptions = {}) {
  const scope = options.scope ?? document.querySelector<HTMLElement>('.rp-dashboard-grid')
  const widgets = () => Array.from((scope ?? document).querySelectorAll<HTMLElement>('[data-dashboard-block]'))
  const pane = options.preserveScroll ? scope?.closest<HTMLElement>('.rp-admin-page-body') : null
  const scrollTop = pane?.scrollTop ?? 0
  const scrollLeft = pane?.scrollLeft ?? 0
  const oldAnchor = pane?.style.overflowAnchor ?? ''
  const paneRect = pane?.getBoundingClientRect()
  const contentRect = (element: HTMLElement) => (element.querySelector('.rp-dashboard-widget-content') ?? element).getBoundingClientRect()
  const anchor = scrollTop > 0 && paneRect ? widgets().find(element => {
    const rect = contentRect(element)
    return rect.bottom > paneRect.top && rect.top < paneRect.bottom
  }) : undefined
  const anchorID = anchor?.dataset.dashboardBlock
  const anchorTop = anchor ? contentRect(anchor).top : undefined
  const focused = document.activeElement
  if (pane) pane.style.overflowAnchor = 'none'
  const before = new Map(widgets().map(element => {
    element.getAnimations?.().forEach(animation => animation.cancel())
    return [element.dataset.dashboardBlock!, element.getBoundingClientRect()]
  }))
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  flushSync(update)
  if (focused && focused !== document.body && !focused.isConnected) {
    scope?.closest('.rp-admin-content')?.querySelector<HTMLElement>('[data-dashboard-mode-toggle]')?.focus({ preventScroll: true })
  }
  const restoreScroll = () => {
    if (!pane) return
    const nextAnchor = anchorID ? widgets().find(element => element.dataset.dashboardBlock === anchorID) : null
    const delta = nextAnchor && anchorTop !== undefined ? contentRect(nextAnchor).top - anchorTop : null
    pane.scrollTop = delta === null ? scrollTop : pane.scrollTop + delta
    pane.scrollLeft = scrollLeft
  }
  restoreScroll()
  if (pane) requestAnimationFrame(() => {
    // Sortable activation and focus effects run after commit; neither may move the viewport.
    restoreScroll()
    pane.style.overflowAnchor = oldAnchor
  })
  if (reduced) return
  for (const element of widgets()) {
    const old = before.get(element.dataset.dashboardBlock!)
    const next = element.getBoundingClientRect()
    if (options.force || !old || old.x !== next.x || old.y !== next.y || old.width !== next.width || old.height !== next.height) {
      element.animate?.([{ opacity: 0.65 }, { opacity: 1 }], { duration: 180, easing: 'ease-out' })
    }
  }
}

/** Give outgoing edit controls an exit, then restore the reader's position after the layout change. */
export async function animateDashboardModeChange(update: () => void, scope: HTMLElement | null) {
  if (!window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
    const exits = Array.from(scope?.querySelectorAll<HTMLElement>('.rp-dashboard-widget-tools') ?? [])
      .map(element => element.animate?.([{ opacity: 1 }, { opacity: 0 }], { duration: 120, easing: 'ease-in', fill: 'forwards' }))
      .filter((animation): animation is Animation => !!animation)
    if (exits.length) await Promise.all(exits.map(animation => animation.finished.catch(() => {})))
  }
  animateDashboardChange(update, { scope, preserveScroll: true, force: true })
}
