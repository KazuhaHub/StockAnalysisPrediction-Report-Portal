import { afterEach, expect, it, vi } from 'vitest'
import { animateDashboardChange, animateDashboardModeChange } from './dashboardMotion'

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })
it('applies edits even when animations are unavailable', () => {
  const update = vi.fn()
  animateDashboardChange(update)
  expect(update).toHaveBeenCalledOnce()
})
it('does not create viewport snapshots even when view transitions are available', () => {
  const transition = vi.fn()
  Object.defineProperty(document, 'startViewTransition', { value: transition, configurable: true })
  try {
    const update = vi.fn()
    animateDashboardChange(update)
    expect(update).toHaveBeenCalledOnce()
    expect(transition).not.toHaveBeenCalled()
  } finally { delete (document as unknown as Record<string, unknown>).startViewTransition }
})
it('fades changed widgets in place without translating, scaling or adding ghost nodes', () => {
  const widget = document.createElement('div')
  widget.dataset.dashboardBlock = 'reports'
  document.body.append(widget)
  let y = 0
  vi.spyOn(widget, 'getBoundingClientRect').mockImplementation(() => ({ x: 0, y, width: 200, height: 100 }) as DOMRect)
  const animate = vi.fn()
  widget.animate = animate
  try {
    animateDashboardChange(() => { y = 300 })
    expect(animate).toHaveBeenCalledWith([{ opacity: 0.65 }, { opacity: 1 }], { duration: 180, easing: 'ease-out' })
    expect(document.querySelectorAll('[data-dashboard-block]')).toHaveLength(1)
  } finally { widget.remove() }
})
it('respects reduced motion and cancels an earlier animation before applying another edit', () => {
  vi.spyOn(window, 'matchMedia').mockReturnValue({ matches: true } as MediaQueryList)
  const widget = document.createElement('div')
  widget.dataset.dashboardBlock = 'reports'
  const cancel = vi.fn()
  widget.getAnimations = () => [{ cancel }] as unknown as Animation[]
  widget.animate = vi.fn()
  document.body.append(widget)
  try {
    const update = vi.fn()
    animateDashboardChange(update)
    expect(cancel).toHaveBeenCalledOnce()
    expect(update).toHaveBeenCalledOnce()
    expect(widget.animate).not.toHaveBeenCalled()
  } finally { widget.remove() }
})

it('keeps the content scroller in place when an edit resets scroll during layout', () => {
  const pane = document.createElement('div')
  pane.className = 'rp-admin-page-body'
  const grid = document.createElement('div')
  grid.className = 'rp-dashboard-grid'
  const widget = document.createElement('div')
  widget.dataset.dashboardBlock = 'reports'
  grid.append(widget); pane.append(grid); document.body.append(pane)
  pane.scrollTop = 420
  try {
    animateDashboardChange(() => { pane.scrollTop = 0 }, { preserveScroll: true, scope: grid, force: true })
    expect(pane.scrollTop).toBe(420)
  } finally { pane.remove() }
})

it('finishes fading outgoing edit controls before exiting and always animates the new mode', async () => {
  const grid = document.createElement('div')
  const widget = document.createElement('div')
  widget.dataset.dashboardBlock = 'reports'
  const tools = document.createElement('div')
  tools.className = 'rp-dashboard-widget-tools'
  widget.append(tools); grid.append(widget); document.body.append(grid)
  let finish!: () => void
  const finished = new Promise<void>(resolve => { finish = resolve })
  tools.animate = vi.fn().mockReturnValue({ finished })
  widget.animate = vi.fn()
  const update = vi.fn(() => tools.remove())
  try {
    const change = animateDashboardModeChange(update, grid)
    expect(update).not.toHaveBeenCalled()
    finish()
    await change
    expect(update).toHaveBeenCalledOnce()
    expect(widget.animate).toHaveBeenCalledWith([{opacity:0.65},{opacity:1}],{duration:180,easing:'ease-out'})
  } finally { grid.remove() }
})
