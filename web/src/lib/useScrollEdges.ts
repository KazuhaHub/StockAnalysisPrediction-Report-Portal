import { useLayoutEffect, type RefObject } from 'react'

// A row that scrolls sideways without a scrollbar marks the sides that have more content
// (data-start / data-end), and .rp-scroll-row in index.css fades those edges: the settings tab row,
// the stock timeline, anything else laid out as one swipeable line. Kept current on scroll, on a
// resize of the row and when its children change size.
// A mouse cannot swipe, and its wheel only moves up and down: on such a row the mouse wheel and a
// press-and-drag scroll it sideways, the way a trackpad or a finger already does. The wheel is kept
// only while the row can still move that way, so at either end it scrolls the page again.
// `deps` are what can change the row's content (its items); the observers are rebuilt only then, not
// on every render of the page around it.
// A trackpad scrolls in pixels and its vertical swipe must keep scrolling the page; a mouse wheel
// reports lines (Firefox) or notches of 120 (Chromium and Safari's legacy wheelDeltaY).
function isMouseWheel(event: WheelEvent) {
  if (event.deltaMode === 1) return true
  const legacy = (event as WheelEvent & { wheelDeltaY?: number }).wheelDeltaY
  return !!legacy && legacy % 120 === 0
}

export function useScrollEdges(ref: RefObject<HTMLElement | null>, deps: readonly unknown[] = []) {
  useLayoutEffect(() => {
    const row = ref.current
    if (!row) return
    const mark = () => {
      const set = (side: 'start' | 'end', on: boolean) => {
        if (on) row.dataset[side] = 'true'
        else delete row.dataset[side]
      }
      set('start', row.scrollLeft > 1)
      set('end', row.scrollLeft + row.clientWidth < row.scrollWidth - 1)
    }
    mark()
    row.addEventListener('scroll', mark, { passive: true })
    const wheel = (event: WheelEvent) => {
      if (event.ctrlKey || !isMouseWheel(event) || Math.abs(event.deltaX) >= Math.abs(event.deltaY)) return
      const max = row.scrollWidth - row.clientWidth
      if (max <= 1) return
      const next = Math.min(max, Math.max(0, row.scrollLeft + event.deltaY * (event.deltaMode === 1 ? 16 : 1)))
      if (next === row.scrollLeft) return
      event.preventDefault()
      row.scrollLeft = next
    }
    row.addEventListener('wheel', wheel, { passive: false })
    // A drag starts after 5px, so a press that barely moves is still a click; one that did drag
    // swallows the click that follows it, so releasing over a tab does not select it.
    let drag: { x: number; left: number; moved: boolean } | null = null
    let swallow = false
    const down = (event: PointerEvent) => {
      swallow = false
      if (event.pointerType !== 'mouse' || event.button !== 0 || row.scrollWidth - row.clientWidth <= 1) return
      drag = { x: event.clientX, left: row.scrollLeft, moved: false }
    }
    const move = (event: PointerEvent) => {
      if (!drag) return
      // The button came up where we never heard about it (a menu or dialog took the release).
      if ((event.buttons & 1) === 0) {
        drag = null
        delete row.dataset.dragging
        return
      }
      const dx = event.clientX - drag.x
      if (!drag.moved && Math.abs(dx) < 5) return
      drag.moved = true
      row.dataset.dragging = 'true'
      row.scrollLeft = drag.left - dx
    }
    const up = () => {
      // The click a drag ends in is dispatched straight after its release, so the flag lives only
      // until then: a later click, or Space on a focused tab, is never taken for it.
      if (drag?.moved) {
        swallow = true
        setTimeout(() => { swallow = false }, 0)
      }
      drag = null
      delete row.dataset.dragging
    }
    const click = (event: MouseEvent) => {
      if (!swallow) return
      swallow = false
      event.preventDefault()
      event.stopPropagation()
    }
    row.addEventListener('pointerdown', down)
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
    window.addEventListener('pointercancel', up)
    row.addEventListener('click', click, true)
    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(mark)
    ro?.observe(row)
    for (const child of row.children) ro?.observe(child)
    return () => {
      row.removeEventListener('scroll', mark)
      row.removeEventListener('wheel', wheel)
      row.removeEventListener('pointerdown', down)
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      window.removeEventListener('pointercancel', up)
      row.removeEventListener('click', click, true)
      ro?.disconnect()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)
}
