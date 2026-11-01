import { useCallback, useMemo, useRef, useState, type HTMLAttributes } from 'react'
import { Grid } from 'antd'
import type { ColumnType } from 'antd/es/table'
import { useTranslation } from 'react-i18next'
import TableColumnHeader, { type ColumnWidths, type ResizeColumn } from './TableColumnHeader'
import { ColumnMenuButton, makeColumnMenuComponents, type ColumnMenuState } from './TableColumnMenu'

type Preference = { hidden: string[]; fit: boolean; widths: ColumnWidths; order: string[] }
const DEFAULT: Preference = { hidden: [], fit: true, widths: {}, order: [] }
const columnKey = <T,>(column: ColumnType<T>) => String(column.key ?? column.dataIndex ?? '')
const clampWidth = (width: number) => Math.round(Math.max(64, Math.min(1200, width)))

/** Flat data tables share local display preferences; neither filters nor row data are stored. */
export function useTableView<T>(name: string, source: ColumnType<T>[], required: string[] = [], labels: Record<string, string> = {}) {
  const { t } = useTranslation()
  const screens = Grid.useBreakpoint()
  const storageKey = `rp.table.${name}`
  const [preference, setPreference] = useState<Preference>(() => {
    try {
      const stored = JSON.parse(localStorage.getItem(storageKey) || 'null')
      const widths = Object.fromEntries(Object.entries(stored?.widths ?? {}).filter((entry): entry is [string, number] =>
        typeof entry[1] === 'number' && Number.isFinite(entry[1]) && entry[1] >= 64 && entry[1] <= 10000))
      return { hidden: Array.isArray(stored?.hidden) ? stored.hidden.filter((key: unknown) => typeof key === 'string') : [],
        fit: typeof stored?.fit === 'boolean' ? stored.fit : true, widths,
        order: Array.isArray(stored?.order) ? stored.order.filter((key: unknown) => typeof key === 'string') : [] }
    } catch { return DEFAULT }
  })
  const [menuOpen, setMenuOpen] = useState(false)
  const [draftWidths, setDraftWidths] = useState<ColumnWidths | null>(null)
  const [measured, setMeasured] = useState<ColumnWidths>({})
  const measure = useCallback((key: string, width: number) => {
    setMeasured((previous) => previous[key] === width ? previous : { ...previous, [key]: width })
  }, [])
  const save = (next: Preference) => {
    setPreference(next)
    try { localStorage.setItem(storageKey, JSON.stringify(next)) } catch { /* Display preferences are optional. */ }
  }
  const setHidden = (hidden: string[]) => save({ ...preference, hidden: hidden.filter((key) => !required.includes(key)) })
  const reset = () => { setDraftWidths(null); save(DEFAULT) }
  const byKey = new Map(source.map((column) => [columnKey(column), column]))
  // A column the saved order has never seen (a new feature, a column only some roles get) goes where
  // the source puts it, before the next column it precedes, not after a pinned actions column.
  const order = preference.order.filter((key) => byKey.has(key))
  const sourceKeys = [...byKey.keys()]
  sourceKeys.forEach((key, index) => {
    if (order.includes(key)) return
    const before = sourceKeys.slice(index + 1).find((later) => order.includes(later))
    order.splice(before === undefined ? order.length : order.indexOf(before), 0, key)
  })
  const ordered = order.map((key) => byKey.get(key)!)
  const available = ordered.filter((column) => !column.responsive || column.responsive.some((breakpoint) => screens[breakpoint]))
  // A phone shows each row as a card with no header to reach the menu from, so it shows every field
  // rather than keeping columns hidden that it offers no way to bring back.
  const phone = !screens.sm
  const shown = available.filter((column) => phone || required.includes(columnKey(column)) || !preference.hidden.includes(columnKey(column)))
  const weight = (column: ColumnType<T>) => typeof column.width === 'number' ? column.width : 240
  const defaults = Object.fromEntries(source.map((column) => [columnKey(column), weight(column)]))
  const moveColumn = (from: string, to: string) => {
    const next = [...order], fromIndex = next.indexOf(from), toIndex = next.indexOf(to)
    if (fromIndex < 0 || toIndex < 0 || fromIndex === toIndex) return
    next.splice(fromIndex, 1); next.splice(toIndex, 0, from)
    save({ ...preference, order: next })
  }
  const resizeColumn: ResizeColumn = (key, width, commit = true, snapshot = measured) => {
    if (!byKey.has(key) || !Number.isFinite(width)) return
    const widths = { ...defaults, ...preference.widths, ...snapshot }
    const next = clampWidth(width), previous = widths[key]
    const index = shown.findIndex((column) => columnKey(column) === key)
    const neighbor = index < 0 ? undefined : shown[index + 1] ?? shown[index - 1]
    // Exchange width with the adjacent column where possible, avoiding a jump in the overall
    // table width when a fitted layout first switches to explicit pixel widths.
    if (neighbor) {
      const other = columnKey(neighbor)
      widths[other] = Math.max(64, widths[other] - (next - previous))
    }
    widths[key] = next
    if (commit) { setDraftWidths(null); save({ ...preference, fit: false, widths }) }
    else setDraftWidths(widths)
  }
  const fit = preference.fit && draftWidths === null
  const explicit = draftWidths ?? preference.widths
  const minimum = shown.reduce((sum, column) => sum + (column.minWidth ?? 96), 0)
  const total = shown.reduce((sum, column) => sum + (explicit[columnKey(column)] ?? weight(column)), 0)
  const labelOf = (column: ColumnType<T>) => labels[columnKey(column)] ?? (typeof column.title === 'function' ? columnKey(column) : column.title || t('table.actions'))
  const nameOf = (column: ColumnType<T>) => { const label = labelOf(column); return typeof label === 'string' || typeof label === 'number' ? String(label) : columnKey(column) }
  const fixedOf = (column: ColumnType<T>, index: number) =>
    preference.order.length && ((column.fixed === 'right' && index !== shown.length - 1) || (column.fixed === 'left' && index !== 0)) ? undefined : column.fixed
  const menu: ColumnMenuState = {
    options: available.map((column) => ({ key: columnKey(column), label: labelOf(column),
      required: required.includes(columnKey(column)), visible: required.includes(columnKey(column)) || !preference.hidden.includes(columnKey(column)) })),
    fit,
    sortable: shown.filter((column, index) => !fixedOf(column, index)).map(columnKey),
    onVisible: (key, visible) => setHidden(visible ? preference.hidden.filter((item) => item !== key) : [...preference.hidden, key]),
    onFit: (nextFit) => { setDraftWidths(null); save({ ...preference, fit: nextFit }) },
    onReset: reset,
    onReorder: moveColumn,
  }
  const live = useRef(menu)
  live.current = menu
  const components = useMemo(() => makeColumnMenuComponents(live), [])
  // phoneLabel: the header is a component here, so CardTable is told the column's name for its cards.
  const columns: (ColumnType<T> & { phoneLabel?: string })[] = shown.map((column, index) => ({ ...column, responsive: undefined, phoneLabel: nameOf(column),
    width: fit ? `${(column.minWidth ?? 96) / minimum * 100}%` : explicit[columnKey(column)] ?? weight(column),
    fixed: fixedOf(column, index),
    onHeaderCell: (data) => ({ ...column.onHeaderCell?.(data), 'data-column-key': columnKey(column) } as HTMLAttributes<HTMLElement>),
    title: (props) => <TableColumnHeader id={columnKey(column)} name={nameOf(column)} onMeasure={measure}
      onResize={resizeColumn} onCancel={() => setDraftWidths(null)}
      extra={index === shown.length - 1 ? <ColumnMenuButton state={menu} open={menuOpen} onOpenChange={setMenuOpen} /> : undefined}>
      {typeof column.title === 'function' ? column.title(props) : column.title}
    </TableColumnHeader>,
  }))
  return { columns, components, setHidden, reset, moveColumn, resizeColumn, scroll: { x: fit ? minimum : total }, className: 'rp-adaptive-table' }
}
