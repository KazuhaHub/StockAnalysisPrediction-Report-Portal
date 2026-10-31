import { useLayoutEffect, type RefObject } from 'react'

// A row that scrolls sideways without a scrollbar marks the sides that have more content
// (data-start / data-end), and .rp-scroll-row in index.css fades those edges: the settings tab row,
// the stock timeline, anything else laid out as one swipeable line. Kept current on scroll, on a
// resize of the row and when its children change size.
// `deps` are what can change the row's content (its items); the observers are rebuilt only then, not
// on every render of the page around it.
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
    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(mark)
    ro?.observe(row)
    for (const child of row.children) ro?.observe(child)
    return () => {
      row.removeEventListener('scroll', mark)
      ro?.disconnect()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)
}
