import { useCallback, useState } from 'react'
import { Button, Grid, Popover, Switch, Typography } from 'antd'
import { SettingOutlined } from '@ant-design/icons'
import type { ColumnType } from 'antd/es/table'
import { useTranslation } from 'react-i18next'
import TableColumnHeader, { type ColumnWidths, type ResizeColumn } from './TableColumnHeader'
import TableColumnOptions from './TableColumnOptions'

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
  const [open, setOpen] = useState(false)
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
  const order = [...new Set([...preference.order.filter((key) => byKey.has(key)), ...byKey.keys()])]
  const ordered = order.map((key) => byKey.get(key)!)
  const available = ordered.filter((column) => !column.responsive || column.responsive.some((breakpoint) => screens[breakpoint]))
  const shown = available.filter((column) => required.includes(columnKey(column)) || !preference.hidden.includes(columnKey(column)))
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
  const columns: ColumnType<T>[] = shown.map((column, index) => ({ ...column, responsive: undefined,
    width: fit ? `${(column.minWidth ?? 96) / minimum * 100}%` : explicit[columnKey(column)] ?? weight(column),
    fixed: preference.order.length && ((column.fixed === 'right' && index !== shown.length - 1) || (column.fixed === 'left' && index !== 0)) ? undefined : column.fixed,
    title: (props) => <TableColumnHeader id={columnKey(column)} name={nameOf(column)} onMeasure={measure}
      onResize={resizeColumn} onCancel={() => setDraftWidths(null)}>
      {typeof column.title === 'function' ? column.title(props) : column.title}
    </TableColumnHeader>,
  }))
  const controls = <Popover open={open} onOpenChange={setOpen} trigger="click" placement="bottomRight" title={t('table.display')} content={
    <div className="rp-table-view-options">
      <label className="rp-table-view-fit"><span>{t('table.fit')}</span><Switch size="small" checked={fit}
        onChange={(nextFit) => { setDraftWidths(null); save({ ...preference, fit: nextFit }) }} /></label>
      <Typography.Text type="secondary">{t('table.columnsHint')}</Typography.Text>
      <Typography.Text type="secondary">{t('table.adjustHint')}</Typography.Text>
      <TableColumnOptions options={available.map((column) => ({ key: columnKey(column), label: labelOf(column), name: nameOf(column),
        required: required.includes(columnKey(column)), visible: required.includes(columnKey(column)) || !preference.hidden.includes(columnKey(column)),
        width: measured[columnKey(column)] ?? explicit[columnKey(column)] ?? weight(column) }))}
        onVisible={(key, visible) => setHidden(visible ? preference.hidden.filter((item) => item !== key) : [...preference.hidden, key])}
        onWidth={resizeColumn} onReorder={moveColumn} />
      <Button onClick={reset}>{t('table.reset')}</Button>
    </div>
  }><Button icon={<SettingOutlined />}>{t('table.display')}</Button></Popover>
  return { columns, controls, setHidden, reset, moveColumn, resizeColumn, scroll: { x: fit ? minimum : total }, className: 'rp-adaptive-table' }
}
