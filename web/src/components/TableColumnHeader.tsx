import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

export type ColumnWidths = Record<string, number>
export type ResizeColumn = (key: string, width: number, commit?: boolean, snapshot?: ColumnWidths) => void

export default function TableColumnHeader({ id, name, children, onMeasure, onResize, onCancel }: {
  id: string; name: string; children: ReactNode
  onMeasure: (key: string, width: number) => void
  onResize: ResizeColumn
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const title = useRef<HTMLDivElement>(null)
  const drag = useRef<{ pointer: number; x: number; width: number; next: number; snapshot: ColumnWidths } | null>(null)
  const [width, setWidth] = useState(96)
  useLayoutEffect(() => {
    const cell = title.current?.closest('th')
    if (!cell) return
    const measure = () => {
      const measured = Math.round(cell.getBoundingClientRect().width)
      if (measured > 0) { setWidth(measured); onMeasure(id, measured) }
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(cell)
    return () => observer.disconnect()
  }, [id, onMeasure])
  const snapshot = () => {
    const widths: ColumnWidths = {}
    title.current?.closest('tr')?.querySelectorAll<HTMLElement>('[data-resize-column]').forEach((element) => {
      widths[element.dataset.resizeColumn!] = Math.round(element.closest('th')!.getBoundingClientRect().width)
    })
    return widths
  }
  return <div className="rp-table-column-title" ref={title} data-resize-column={id}>
    <span>{children}</span>
    <span role="separator" tabIndex={0} aria-orientation="vertical" aria-valuemin={64} aria-valuemax={1200}
      aria-valuenow={width} aria-label={t('table.resizeColumn', { name })} title={t('table.resizeColumn', { name })}
      className="rp-table-resize-handle"
      onClick={(event) => event.stopPropagation()}
      onPointerDown={(event) => {
        if (event.button !== 0) return
        event.preventDefault(); event.stopPropagation(); event.currentTarget.focus()
        const widths = snapshot()
        drag.current = { pointer: event.pointerId, x: event.clientX, width: widths[id] || width, next: widths[id] || width, snapshot: widths }
        event.currentTarget.setPointerCapture?.(event.pointerId)
      }}
      onPointerMove={(event) => {
        const active = drag.current
        if (!active || active.pointer !== event.pointerId) return
        active.next = Math.max(64, Math.min(1200, active.width + event.clientX - active.x))
        onResize(id, active.next, false, active.snapshot)
      }}
      onPointerUp={(event) => {
        const active = drag.current
        if (!active || active.pointer !== event.pointerId) return
        drag.current = null
        if (active.next === active.width) { onCancel(); return }
        onResize(id, active.next, true, active.snapshot)
      }}
      onPointerCancel={() => { drag.current = null; onCancel() }}
      onLostPointerCapture={() => { if (drag.current) { drag.current = null; onCancel() } }}
      onKeyDown={(event) => {
        if (event.key === 'Escape') { drag.current = null; onCancel(); return }
        if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
        event.preventDefault(); event.stopPropagation()
        onResize(id, width + (event.key === 'ArrowRight' ? 1 : -1) * (event.shiftKey ? 50 : 10), true, snapshot())
      }} />
  </div>
}
