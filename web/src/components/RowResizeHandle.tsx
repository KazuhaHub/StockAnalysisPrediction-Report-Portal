import { useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

/**
 * The bottom-edge handle of a grid row's number cell, the row counterpart of the column handle in
 * `TableColumnHeader` (same pointer capture, same Escape-to-cancel, same arrow-key steps) so a mouse
 * user drags it as in Google Sheets and a keyboard user adjusts it with Up and Down.
 *
 * `value` is the height the user gave the row, or undefined while the row sizes itself; `onChange`
 * receives undefined when a drag is cancelled back to that state.
 */
/** The height of a row's tallest cell editor, in px, never below one line. */
export function measureRowHeight(from: Element): number {
  const heights = [...(from.closest('tr')?.querySelectorAll<HTMLElement>('.rp-grid-cell') ?? [])].map((element) => element.offsetHeight)
  return Math.max(0, ...heights)
}

export default function RowResizeHandle({ row, value, min, max, step, onMeasure, onChange }: {
  row: number
  value: number | undefined
  min: number
  max: number
  /** One keyboard step; Shift takes five. */
  step: number
  /** The height the row has now, read when a drag or key press starts from an automatic height. */
  onMeasure: (handle: HTMLElement) => number
  onChange: (height: number | undefined) => void
}) {
  const { t } = useTranslation()
  const handle = useRef<HTMLSpanElement>(null)
  const latestMeasure = useRef(onMeasure)
  latestMeasure.current = onMeasure
  // While the row sizes itself the handle reports the height it measures, kept current as the row's
  // text grows or wraps, so a reader hears the real height and the first arrow key starts from it.
  const [measured, setMeasured] = useState(min)
  useLayoutEffect(() => {
    const element = handle.current
    if (!element) return
    const update = () => setMeasured(Math.max(min, Math.min(max, Math.round(latestMeasure.current(element)))))
    update()
    const row = element.closest('tr')
    if (!row || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(update)
    observer.observe(row)
    return () => observer.disconnect()
  }, [value, min, max])
  const drag = useRef<{ pointer: number; y: number; start: number | undefined; from: number } | null>(null)
  const clamp = (height: number) => Math.max(min, Math.min(max, Math.round(height)))
  const label = t('batch.editor.resizeRow', { row: row + 1 })
  return (
    <span
      role="separator"
      ref={handle}
      // Not a tab stop: the grid is one (roving tabindex). Alt+Shift+Up and Down on a cell are the keyboard route.
      tabIndex={-1}
      aria-orientation="horizontal"
      aria-valuemin={min}
      aria-valuemax={max}
      aria-valuenow={value ?? measured}
      aria-label={label}
      title={label}
      className="rp-table-resize-handle rp-table-resize-handle--row"
      onClick={(event) => event.stopPropagation()}
      onPointerDown={(event) => {
        if (event.button !== 0) return
        event.preventDefault()
        event.stopPropagation()
        event.currentTarget.focus()
        drag.current = { pointer: event.pointerId, y: event.clientY, start: value, from: value ?? measured }
        event.currentTarget.setPointerCapture?.(event.pointerId)
      }}
      onPointerMove={(event) => {
        const active = drag.current
        if (!active || active.pointer !== event.pointerId) return
        onChange(clamp(active.from + event.clientY - active.y))
      }}
      onPointerUp={(event) => {
        if (drag.current?.pointer === event.pointerId) drag.current = null
      }}
      onPointerCancel={() => {
        const active = drag.current
        drag.current = null
        if (active) onChange(active.start)
      }}
      onKeyDown={(event) => {
        // Keys on the handle resize the row; none of them reaches the grid's own keyboard model.
        event.stopPropagation()
        if (event.key === 'Escape' && drag.current) {
          onChange(drag.current.start)
          drag.current = null
          return
        }
        if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return
        event.preventDefault()
        const from = value ?? measured
        onChange(clamp(from + (event.key === 'ArrowDown' ? 1 : -1) * step * (event.shiftKey ? 5 : 1)))
      }}
    />
  )
}
