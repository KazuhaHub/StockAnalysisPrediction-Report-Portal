import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ClipboardEvent, type KeyboardEvent } from 'react'
import {
  Alert,
  Button,
  Input,
  InputNumber,
  Popover,
  Segmented,
  Select,
  Space,
  Tag,
  Tooltip,
  Typography,
  Upload,
} from 'antd'
import {
  DeleteOutlined,
  DownloadOutlined,
  QuestionCircleOutlined,
  UndoOutlined,
  UploadOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import Confirm from './Confirm'

import type { PluginInput } from '../api/types'
import {
  activeBatchRows,
  batchRowHasValues,
  blankBatchRow,
  ensureTrailingBlank,
  isGridClipboard,
  missingCSVHeaders,
  missingRequiredCells,
  pastedRowCount,
  pasteBatchGridReport,
  toClipboardText,
  type BatchDraftRow,
} from '../lib/batchRows'
import { notifyError } from '../lib/notify'
import { csvToRows, downloadCSV, toCSV } from '../lib/csv'
import { inputDisplayMeta } from '../lib/difyInputs'
import { fillScrollWidth, useColumnWidths } from '../lib/columnWidths'
import CardTable, { type CardColumn } from './CardTable'
import { useFinePointer } from '../lib/useFinePointer'
import { useNarrow } from '../lib/useNarrow'
import RowResizeHandle, { measureRowHeight } from './RowResizeHandle'
import TableColumnHeader from './TableColumnHeader'

type EditorMode = 'grid' | 'csv'
type TableRow = { values: BatchDraftRow; rowIndex: number }
type Cell = { row: number; col: number }
// "explicit" edits (double-click, Enter, F2) keep the caret and let the arrow keys move it; a "typed"
// edit began by typing over a selected cell, where the arrow keys commit and move, as in Google Sheets.
// `start` is what the cell held when the edit began, which Escape restores.
type Edit = { kind: 'explicit' | 'typed'; start: string }
type CellKind = 'text' | 'paragraph' | 'number' | 'select'
// What undo restores: the rows, the active cell and the row heights as they were before an operation.
type Snapshot = { rows: BatchDraftRow[]; active: Cell | null; heights: Record<number, number> }
const HISTORY_LIMIT = 50
const TABBABLE = 'a[href], button, input, select, textarea, [tabindex]'
const rowsEqual = (a: BatchDraftRow[], b: BatchDraftRow[]) =>
  a.length === b.length && a.every((row, i) => Object.keys(row).length === Object.keys(b[i]).length && Object.entries(row).every(([k, v]) => b[i][k] === v))

// Input columns share the available width but never get narrower than this; when their minimums do
// not fit, the grid scrolls inside itself (docs/frontend-guidelines.md, "Editable grids").
const MIN_COLUMN_WIDTH = 160
const ROW_NUMBER_WIDTH = 54
const DELETE_WIDTH = 62
const PAGE_SIZE = 20
// Row heights are the height of a text editor in px: one line of an antd middle-size control, then one
// line of text at a time up to ten lines (also the height an editor expands to while it is edited).
const LINE_PX = 22
const CONTROL_MIN = 32
const EDIT_MAX_LINES = 10
const CONTROL_MAX = CONTROL_MIN + (EDIT_MAX_LINES - 1) * LINE_PX
const clampHeight = (height: number) => Math.max(CONTROL_MIN, Math.min(CONTROL_MAX, Math.round(height)))
const NAV_KEYS: Record<string, [number, number]> = { ArrowUp: [-1, 0], ArrowDown: [1, 0], ArrowLeft: [0, -1], ArrowRight: [0, 1] }

function selectWholeText(element: Element | null | undefined) {
  if (!(element instanceof HTMLTextAreaElement || element instanceof HTMLInputElement)) return
  if (element.selectionStart === 0 && element.selectionEnd === element.value.length) return
  try {
    element.select()
  } catch {
    // Some input types refuse a selection; the cell is still the active one.
  }
}
const lastPage = (count: number, size: number) => Math.max(1, Math.ceil(count / size))

export default function BatchRowsEditor({
  inputs,
  rows,
  onChange,
  onValidityChange,
  widthsKey,
}: {
  inputs: PluginInput[]
  rows: BatchDraftRow[]
  onChange: (rows: BatchDraftRow[]) => void
  onValidityChange?: (valid: boolean) => void
  /** Names where dragged column widths are kept (one per workflow); without it they last until unmount. */
  widthsKey?: string
}) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<EditorMode>('grid')
  const [rawCSV, setRawCSV] = useState(() => toCSV(inputs.map((input) => input.key), []))

  const keys = useMemo(() => inputs.map((input) => input.key), [inputs])
  // The grid always ends on one blank row, spreadsheet-style: typing into it makes the next one
  // appear. The blank rows are a view concern: they are never counted, never submitted (activeBatchRows
  // drops them) and never reach the CSV.
  const displayedRows = useMemo(() => ensureTrailingBlank(rows, inputs), [rows, inputs])
  const { widths, resize, cancel, reset: resetWidths, measure } = useColumnWidths(widthsKey, MIN_COLUMN_WIDTH)
  const [page, setPage] = useState({ current: 1, size: PAGE_SIZE })
  const previousCount = useRef(displayedRows.length)

  // Keyboard model (docs/frontend-guidelines.md, "Editable grids"). One cell is active; the grid is a
  // roving-tabindex widget, so the active cell is its only tab stop. In select mode the active cell's
  // own editor keeps focus with its whole text selected and its caret hidden: native typing and input
  // method composition then replace the selection, with no hidden proxy input to keep in step. A phone
  // shows cards instead, where a tap edits as before, so none of this applies there. The model is for
  // a fine pointer on a wide viewport: a touch screen of any width (a tablet in a table layout) and a
  // narrow window keep plain tap-to-edit inputs.
  const narrow = useNarrow()
  const finePointer = useFinePointer()
  const grid = finePointer && !narrow
  const gridRef = useRef<HTMLDivElement>(null)
  const [active, setActive] = useState<Cell | null>(null)
  const [edit, setEdit] = useState<Edit | null>(null)
  const pendingFocus = useRef(false)
  const pendingCaret = useRef<number | 'end' | null>(null)
  const composing = useRef(false)
  const history = useRef<{ past: Snapshot[]; future: Snapshot[] }>({ past: [], future: [] })
  const editSnapshot = useRef<Snapshot | null>(null)
  // Row heights live in component state by row index and are not persisted: rows are transient data.
  // Deleting a row shifts the heights below it up; a paste only overwrites or appends rows, so it
  // moves nothing. Anything that replaces the rows wholesale (Clear, a CSV round trip, a different
  // workflow) drops them all rather than guess which row is which.
  const [rowHeights, setRowHeights] = useState<Record<number, number>>({})
  const activeRef = useRef<Cell | null>(null)
  const rowsRef = useRef(displayedRows)
  rowsRef.current = displayedRows
  activeRef.current = active
  // ensureTrailingBlank can collapse several trailing blank rows into the earliest one; a height kept
  // for an index that no longer exists would land on the next blank row that appears there.
  useEffect(() => {
    setRowHeights((heights) =>
      Object.keys(heights).every((index) => Number(index) < displayedRows.length)
        ? heights
        : Object.fromEntries(Object.entries(heights).filter(([index]) => Number(index) < displayedRows.length)),
    )
  }, [displayedRows.length])
  useEffect(() => {
    const before = previousCount.current
    previousCount.current = displayedRows.length
    // A paste on the last page follows the rows it added onto the next one. Typing grows the grid by
    // exactly one row (the new blank one), and the cell being typed in must stay on screen; a paste
    // into the middle of a long grid does not yank anyone to the end either.
    setPage((p) => {
      // A pending focus wins: whatever moved the active cell (a paste, an undo) must find it on screen.
      const wanted = pendingFocus.current && activeRef.current ? Math.floor(activeRef.current.row / p.size) + 1 : null
      if (wanted != null) return wanted === p.current ? p : { ...p, current: Math.min(wanted, lastPage(displayedRows.length, p.size)) }
      const last = lastPage(displayedRows.length, p.size)
      if (displayedRows.length > before + 1 && p.current >= lastPage(before, p.size)) return { ...p, current: last }
      return p.current > last ? { ...p, current: last } : p
    })
  }, [displayedRows.length])
  const keySignature = inputs.map((input) => input.key).join('\u0000')
  useEffect(() => {
    setRowHeights({})
    setActive(null)
    setEdit(null)
    history.current = { past: [], future: [] }
  }, [keySignature])
  const readyRows = useMemo(() => activeBatchRows(rows), [rows])
  const missingCells = useMemo(() => missingRequiredCells(rows, inputs), [rows, inputs])
  const missingCellSet = useMemo(
    () => new Set(missingCells.map((cell) => `${cell.rowIndex}:${cell.key}`)),
    [missingCells],
  )
  const headerMissing = useMemo(
    () => (mode === 'csv' ? missingCSVHeaders(rawCSV, inputs) : []),
    [mode, rawCSV, inputs],
  )
  const valid = readyRows.length > 0 && missingCells.length === 0 && headerMissing.length === 0

  useEffect(() => {
    onValidityChange?.(valid)
  }, [onValidityChange, valid])

  const setModeAndSync = (next: EditorMode) => {
    if (next === mode) return
    if (next === 'csv') {
      setRawCSV(toCSV(keys, readyRows.map((row) => keys.map((key) => row[key] ?? ''))))
    } else {
      const parsed = csvToRows(rawCSV, keys)
      onChange(ensureTrailingBlank(parsed, inputs))
      setRowHeights({})
      history.current = { past: [], future: [] }
    }
    setMode(next)
  }

  const updateCell = (rowIndex: number, key: string, value: string) => {
    const next = displayedRows.map((row) => ({ ...row }))
    next[rowIndex][key] = value
    onChange(ensureTrailingBlank(next, inputs))
  }

  const activeCell =
    grid && active && active.row < displayedRows.length && active.col < inputs.length ? active : null
  const isActive = (row: number, col: number) => activeCell?.row === row && activeCell.col === col
  const isEditing = (row: number, col: number) => isActive(row, col) && edit != null
  const cellValue = (row: number, col: number) => displayedRows[row]?.[inputs[col]?.key] ?? ''
  const controlOf = (row: number, col: number) =>
    gridRef.current?.querySelector<HTMLElement>(
      `[data-grid-cell="${row}:${col}"] textarea, [data-grid-cell="${row}:${col}"] input`,
    ) ?? null

  const snapshot = (): Snapshot => ({ rows: displayedRows, active: activeCell, heights: rowHeights })
  const pushHistory = (snap: Snapshot = snapshot()) => {
    const { past } = history.current
    past.push(snap)
    if (past.length > HISTORY_LIMIT) past.shift()
    history.current.future = []
  }

  // Makes (row, col) the active cell, follows it onto its page and queues a focus for the render that
  // mounts it, so focus never falls back to the page body when a paste, an undo or a move changes page.
  const reveal = (row: number, col: number) => {
    setActive({ row, col })
    setEdit(null)
    pendingFocus.current = true
    const target = Math.floor(row / page.size) + 1
    if (target !== page.current) setPage({ ...page, current: target })
  }

  const restore = (snap: Snapshot) => {
    editSnapshot.current = null
    onChange(ensureTrailingBlank(snap.rows, inputs))
    setRowHeights(snap.heights)
    if (snap.active) reveal(snap.active.row, snap.active.col)
    else setEdit(null)
  }
  const undo = () => {
    const snap = history.current.past.pop()
    if (!snap) return
    history.current.future.push(snapshot())
    restore(snap)
  }
  const redo = () => {
    const snap = history.current.future.pop()
    if (!snap) return
    history.current.past.push(snapshot())
    restore(snap)
  }

  // Ends an edit. The text is already in the rows, so committing is leaving edit mode; what is left to
  // do is remember the state from before the edit, once, if the edit changed the cell.
  const finishEdit = () => {
    const snap = editSnapshot.current
    editSnapshot.current = null
    if (edit && snap && activeCell && cellValue(activeCell.row, activeCell.col) !== edit.start) pushHistory(snap)
    setEdit(null)
  }

  // Paste. While a cell is being edited the browser's own paste inserts at the caret (as in Sheets);
  // the one thing done by hand is a single-line cell, which must never receive a line break, so its
  // newlines become spaces. In select mode a paste is a block from the active cell: every cell is
  // checked against its column (lib/batchRows), and a paste that changes nothing is dropped.
  const handlePaste = (event: ClipboardEvent<HTMLElement>, rowIndex: number, columnIndex: number, kind: CellKind) => {
    const text = event.clipboardData.getData('text')
    if (!grid) {
      if (isGridClipboard(text)) {
        event.preventDefault()
        const result = pasteBatchGridReport(displayedRows, inputs, rowIndex, columnIndex, text)
        if (result.skipped > 0) notifyError(t('batch.editor.pasteSkipped', { n: result.skipped }))
        onChange(ensureTrailingBlank(result.rows, inputs))
      }
      return
    }
    if (isEditing(rowIndex, columnIndex)) {
      if (kind !== 'text' && kind !== 'paragraph') return
      // Excel copies one cell as "abc\r\n": exactly one trailing terminator is the row's end, not
      // part of the value. A single-line cell also gets its inner line breaks as spaces.
      const body = text.replace(/(?:\r\n|\n|\r)$/, '')
      const inserted = kind === 'text' ? body.replace(/\r\n|\r|\n/g, ' ') : body
      if (inserted === text) return
      event.preventDefault()
      const element = event.target as HTMLTextAreaElement
      const from = element.selectionStart ?? element.value.length
      const to = element.selectionEnd ?? from
      updateCell(rowIndex, inputs[columnIndex].key, element.value.slice(0, from) + inserted + element.value.slice(to))
      pendingCaret.current = from + inserted.length
      return
    }
    event.preventDefault()
    const { rows: next, skipped } = pasteBatchGridReport(displayedRows, inputs, rowIndex, columnIndex, text)
    // One notice per paste, also when nothing at all was accepted.
    if (skipped > 0) notifyError(t('batch.editor.pasteSkipped', { n: skipped }))
    if (rowsEqual(next, displayedRows)) return
    pushHistory()
    onChange(ensureTrailingBlank(next, inputs))
    // The block's last row keeps the focus, which is also where the grid scrolls to.
    reveal(rowIndex + Math.max(1, pastedRowCount(inputs, columnIndex, text)) - 1, columnIndex)
  }

  const removeRow = (rowIndex: number) => {
    pushHistory()
    const next = ensureTrailingBlank(displayedRows.filter((_row, index) => index !== rowIndex), inputs)
    onChange(next)
    setRowHeights((heights) => {
      const shifted: Record<number, number> = {}
      for (const [index, height] of Object.entries(heights)) {
        const at = Number(index)
        if (at < rowIndex) shifted[at] = height
        else if (at > rowIndex) shifted[at - 1] = height
      }
      return shifted
    })
    // The active cell keeps pointing at the same data: a row removed above it shifts it up, and when
    // its own row goes the cell stays at that index, on whatever row moved into it.
    if (activeCell) {
      const row = rowIndex < activeCell.row ? activeCell.row - 1 : Math.min(activeCell.row, next.length - 1)
      setActive({ row, col: activeCell.col })
    }
  }

  // Makes (row, col) the active cell in select mode and moves focus to it, following it onto another
  // page of a paginated grid. The focus itself happens after the render that mounts the cell.
  const moveTo = (row: number, col: number) => {
    const target = Math.floor(row / page.size) + 1
    const samePlace = activeCell?.row === row && activeCell.col === col && target === page.current
    if (!samePlace) {
      finishEdit()
      reveal(row, col)
      return
    }
    finishEdit()
    setActive({ row, col })
    const element = controlOf(row, col)
    element?.focus({ preventScroll: true })
    selectWholeText(element)
  }

  // Tab order inside the grid: along the row, then to the first column of the next row (and back).
  // Past the ends there is no target, so the browser's own Tab leaves the grid.
  const tabTarget = (row: number, col: number, direction: 1 | -1): Cell | null => {
    if (direction === 1) {
      if (col + 1 < inputs.length) return { row, col: col + 1 }
      return row + 1 < displayedRows.length ? { row: row + 1, col: 0 } : null
    }
    if (col > 0) return { row, col: col - 1 }
    return row > 0 ? { row: row - 1, col: inputs.length - 1 } : null
  }

  const beginEdit = (row: number, col: number, kind: Edit['kind']) => {
    if (!edit) editSnapshot.current = snapshot()
    setActive({ row, col })
    setEdit({ kind, start: cellValue(row, col) })
    if (kind === 'explicit') pendingCaret.current = 'end'
  }

  // The first tab stop outside the grid in the given direction. The grid wrapper holds the header's
  // controls, which the browser would visit first when Tab starts from the wrapper itself.
  const focusOutside = (direction: 1 | -1) => {
    const wrapper = gridRef.current
    if (!wrapper) return false
    const candidates = [...document.querySelectorAll<HTMLElement>(TABBABLE)].filter(
      (element) =>
        element.tabIndex >= 0 &&
        !element.hasAttribute('disabled') &&
        !wrapper.contains(element) &&
        !element.contains(wrapper) &&
        !element.closest('[inert], [aria-hidden="true"]'),
    )
    const wanted =
      direction === 1
        ? candidates.find((element) => wrapper.compareDocumentPosition(element) & Node.DOCUMENT_POSITION_FOLLOWING)
        : candidates.reverse().find((element) => wrapper.compareDocumentPosition(element) & Node.DOCUMENT_POSITION_PRECEDING)
    wanted?.focus()
    return wanted != null
  }

  const onCellKeyDown = (event: KeyboardEvent<HTMLElement>, row: number, col: number, kind: CellKind) => {
    if (!grid) return
    // Keys that belong to an input method (a candidate's Enter, arrows, Tab) are never ours. Safari
    // reports the confirming key after compositionend, with keyCode 229 and isComposing false.
    if (event.nativeEvent.isComposing || composing.current || event.keyCode === 229) return
    const key = event.key
    const field = inputs[col].key
    const editing = isEditing(row, col)
    // Windows reports AltGr as Ctrl+Alt, so a character typed with AltGr (a German backslash, a French
    // bracket) must not be taken for a shortcut: it counts as no modifier at all.
    const altGraph = event.getModifierState?.('AltGraph') ?? false
    const modifier = (event.ctrlKey || event.metaKey) && !altGraph
    const swallow = () => {
      event.preventDefault()
      event.stopPropagation()
    }
    if (!isActive(row, col)) setActive({ row, col })

    if (key === 'Tab') {
      // The edit is already in the data, so committing is just leaving edit mode.
      finishEdit()
      const target = tabTarget(row, col, event.shiftKey ? -1 : 1)
      if (target) {
        swallow()
        moveTo(target.row, target.col)
      }
      return
    }
    if (key === 'Escape') {
      if (!editing || !edit) {
        // Select mode: hand focus to the grid itself, from where Tab and Shift+Tab leave it in either
        // direction and Enter or ArrowDown come back to the active cell.
        gridRef.current?.focus()
        return
      }
      swallow()
      editSnapshot.current = null
      if (cellValue(row, col) !== edit.start) updateCell(row, field, edit.start)
      setEdit(null)
      return
    }
    if (key === 'Enter') {
      if (!editing) {
        swallow()
        beginEdit(row, col, 'explicit')
        return
      }
      // A select handles its own Enter (it picks the highlighted option).
      if (kind === 'select') return
      if (event.altKey || modifier) {
        swallow()
        if (kind === 'paragraph') {
          const element = event.target as HTMLTextAreaElement
          const from = element.selectionStart ?? element.value.length
          const to = element.selectionEnd ?? from
          updateCell(row, field, `${element.value.slice(0, from)}\n${element.value.slice(to)}`)
          pendingCaret.current = from + 1
        }
        return
      }
      if (event.shiftKey && kind === 'paragraph') return
      swallow()
      if (row + 1 < displayedRows.length) moveTo(row + 1, col)
      else finishEdit()
      return
    }
    if (key === 'F2' && !editing) {
      swallow()
      beginEdit(row, col, 'explicit')
      return
    }
    // Alt+Shift+Up and Down resize the active row: the keyboard route to the row-number handle,
    // which is not a tab stop of its own.
    if (!editing && event.altKey && event.shiftKey && !modifier && (key === 'ArrowUp' || key === 'ArrowDown')) {
      swallow()
      const from = rowHeights[row] ?? clampHeight(measureRowHeight(event.target as HTMLElement))
      setRowHeights((heights) => ({ ...heights, [row]: clampHeight(from + (key === 'ArrowDown' ? LINE_PX : -LINE_PX)) }))
      return
    }
    const step = NAV_KEYS[key]
    if (step) {
      if (editing && (edit?.kind === 'explicit' || kind === 'select')) return
      swallow()
      if (event.shiftKey || event.altKey || modifier) return
      moveTo(
        Math.max(0, Math.min(displayedRows.length - 1, row + step[0])),
        Math.max(0, Math.min(inputs.length - 1, col + step[1])),
      )
      return
    }
    if (editing) return
    // Select mode from here on: the keys below act on the whole cell.
    const letter = key.toLowerCase()
    if (modifier && event.altKey && !altGraph && (key === '-' || event.code === 'Minus')) {
      // Google Sheets' delete-row shortcut; the trailing blank row is the place to type, not a row.
      swallow()
      if (row === displayedRows.length - 1 && !batchRowHasValues(displayedRows[row])) return
      removeRow(row)
      return
    }
    if (modifier && !event.altKey) {
      if (letter === 'z' || letter === 'y') {
        swallow()
        if (letter === 'y' || event.shiftKey) redo()
        else undo()
        return
      }
      if (letter === 'c' || letter === 'x') {
        // Written by hand where the Clipboard API exists: Safari does not fire a copy for a cell that
        // has no text selected, and a select cell has none. Without the API the native copy event is
        // handled by onCopy/onCut.
        if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) return
        swallow()
        const copied = cellValue(row, col)
        const before = snapshot()
        let written: Promise<unknown>
        try {
          written = Promise.resolve(navigator.clipboard.writeText(toClipboardText(copied)))
        } catch (error) {
          written = Promise.reject(error)
        }
        // A cut empties the cell only once the clipboard has the text, and only if the cell still holds
        // what was copied; a refusal leaves everything as it was and says so.
        void written.then(
          () => {
            if (letter !== 'x' || copied === '') return
            const latest = rowsRef.current
            if ((latest[row]?.[field] ?? '') !== copied) return
            pushHistory(before)
            onChange(ensureTrailingBlank(latest.map((r, i) => (i === row ? { ...r, [field]: '' } : r)), inputs))
          },
          () => notifyError(t('batch.editor.copyFailed')),
        )
        return
      }
    }
    if (key === 'Delete' || key === 'Backspace') {
      swallow()
      if (cellValue(row, col) !== '') {
        pushHistory()
        updateCell(row, field, '')
      }
      return
    }
    if (key === 'Home' || key === 'End') {
      swallow()
      moveTo(row, key === 'Home' ? 0 : inputs.length - 1)
      return
    }
    if (key === 'PageUp' || key === 'PageDown') {
      swallow()
      return
    }
    if (!modifier && [...key].length === 1) {
      // A number cell only starts editing on a digit, minus or dot; any other character is dropped
      // rather than typed into a field that would refuse it. A select cell opens its list and the
      // character filters it (there is no text to replace); a text cell is overwritten natively
      // because its whole text is selected.
      if (kind === 'number' && !/[0-9.-]/.test(key)) {
        swallow()
        return
      }
      beginEdit(row, col, 'typed')
      if (kind !== 'select') selectWholeText(event.target as HTMLElement)
    }
  }

  // Keys on the grid itself, which holds focus after Escape: Enter or ArrowDown go back to the
  // active cell; Tab and Shift+Tab skip the header controls inside the grid and leave it.
  const onGridKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    if (!grid || event.target !== event.currentTarget) return
    if (event.key === 'Enter' || event.key === 'ArrowDown') {
      event.preventDefault()
      const target = activeCell ?? { row: Math.min((page.current - 1) * page.size, displayedRows.length - 1), col: 0 }
      moveTo(target.row, target.col)
    } else if (event.key === 'Tab') {
      // With nothing tabbable beyond the grid, stay on it rather than let the browser tab into the
      // header controls it contains.
      focusOutside(event.shiftKey ? -1 : 1)
      event.preventDefault()
    }
  }

  // antd gives no hook for the role of the table it renders, and with a fixed-height body it renders
  // two (header and body). The one that holds the rows is the grid, because that is where the cells
  // and their keyboard focus are; the header table keeps its own column-header semantics. Since a
  // reader cannot see the header from a body cell, every editor's accessible name carries the row
  // number and the column title (batch.editor.cellLabel). Attributes React does not manage are not touched by its renders.
  useLayoutEffect(() => {
    const root = gridRef.current
    if (!root) return
    // antd keeps an aria-hidden measuring row at the top of the body table that repeats the column
    // titles, resize handles and help buttons. Hidden from assistive technology, those copies must not
    // be reachable by Tab either, or Shift+Tab from the first cell would land on one inside the body.
    root.querySelectorAll<HTMLElement>('tr[aria-hidden="true"]').forEach((measureRow) => {
      measureRow.setAttribute('inert', '')
      measureRow.querySelectorAll<HTMLElement>(TABBABLE).forEach((element) => {
        if (element.tabIndex >= 0) element.tabIndex = -1
      })
    })
    root.querySelectorAll('table').forEach((table) => {
      if (!grid || !table.querySelector(':scope > tbody > tr.ant-table-row')) {
        table.removeAttribute('role')
        table.removeAttribute('aria-label')
        table.removeAttribute('aria-rowcount')
        table.removeAttribute('aria-colcount')
        table.removeAttribute('aria-description')
        return
      }
      table.setAttribute('role', 'grid')
      table.setAttribute('aria-label', t('batch.editor.gridLabel'))
      // Counts include the header row, which lives in the other table; each body row says its own
      // absolute position (onRow), so a paginated grid still reads as rows N of M.
      table.setAttribute('aria-rowcount', String(displayedRows.length + 1))
      table.setAttribute('aria-colcount', String(inputs.length + 2))
      table.setAttribute('aria-description', t('batch.editor.keyHint'))
    })
  })

  // The caret, focus and selection a render asked for. Runs after every render because the cell it
  // refers to may only have been mounted by that render (a new page, the trailing blank row).
  useLayoutEffect(() => {
    if (!grid || !activeCell) return
    const element = controlOf(activeCell.row, activeCell.col)
    if (!element) return
    if (pendingFocus.current) {
      pendingFocus.current = false
      element.focus({ preventScroll: true })
      element.closest('[data-grid-cell]')?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
    }
    if (document.activeElement !== element) return
    if (edit == null) {
      if (!composing.current) selectWholeText(element)
    } else if (pendingCaret.current != null && (element instanceof HTMLTextAreaElement || element instanceof HTMLInputElement)) {
      const at = pendingCaret.current === 'end' ? element.value.length : pendingCaret.current
      try {
        element.setSelectionRange(at, at)
      } catch {
        // Not every input type has a caret to place.
      }
    }
    pendingCaret.current = null
  })

  const importCSV = (file: File) => {
    const reader = new FileReader()
    reader.onload = () => {
      const text = String(reader.result || '')
      setRawCSV(text)
      onChange(csvToRows(text, keys))
      setRowHeights({})
      history.current = { past: [], future: [] }
      setMode('csv')
    }
    reader.readAsText(file)
    return false
  }

  const currentCSV = () =>
    mode === 'csv' ? rawCSV : toCSV(keys, readyRows.map((row) => keys.map((key) => row[key] ?? '')))

  const pageStart = (page.current - 1) * page.size
  // Roving tabindex: the active cell is the grid's only tab stop, or the first cell of the page while
  // nothing is active (or the active cell is on another page).
  const tabStop: Cell = activeCell && activeCell.row >= pageStart && activeCell.row < pageStart + page.size
    ? activeCell
    : { row: pageStart, col: 0 }

  const fieldControl = (input: PluginInput, rowIndex: number, columnIndex: number) => {
    const { title } = inputDisplayMeta(input)
    const value = displayedRows[rowIndex]?.[input.key] ?? ''
    const status = missingCellSet.has(`${rowIndex}:${input.key}`) ? 'error' : undefined
    const label = t('batch.editor.cellLabel', { row: rowIndex + 1, field: title })
    const here = isActive(rowIndex, columnIndex)
    const editingHere = isEditing(rowIndex, columnIndex)
    const kind: CellKind =
      input.type === 'select' && input.options?.length ? 'select' : input.type === 'number' ? 'number' : input.type === 'paragraph' ? 'paragraph' : 'text'
    // Typing into a cell that is only selected (a character, or an input method's composition that
    // reached the editor before a keydown could say so) starts an edit that Escape can undo.
    const typedInto = () => {
      if (grid && !editingHere) beginEdit(rowIndex, columnIndex, 'typed')
    }
    const common = {
      value,
      status: status as 'error' | undefined,
      'aria-label': label,
      ...(grid
        ? {
            tabIndex: tabStop.row === rowIndex && tabStop.col === columnIndex ? 0 : -1,
            'aria-description': here ? t(editingHere ? 'batch.editor.cellModeEdit' : 'batch.editor.cellModeSelect') : undefined,
          }
        : {}),
    }
    const rowHeight = grid ? rowHeights[rowIndex] : undefined
    // A resized row shows its text up to the height it was given, with no three-line clamp. While a
    // cell is being edited its editor grows to ten lines (never below the row's own height) without
    // touching the stored height, so the row falls back when the edit ends.
    const clamp = editingHere ? 'expanded' : rowHeight ? 'row' : 'auto'

    let control
    if (kind === 'select') {
      control = (
        <Select
          {...common}
          allowClear
          showSearch
          // In the grid the list opens only while the cell is being edited, so a click selects the
          // cell instead of dropping the list. A typed character filters the list (the one place
          // typing does not replace the cell's text, because a select has none to replace).
          open={grid ? editingHere : undefined}
          onOpenChange={(open) => {
            if (!open && editingHere) setEdit(null)
          }}
          options={(input.options ?? []).map((option) => ({ value: option, label: option }))}
          onChange={(next) => {
            pushHistory(editSnapshot.current ?? undefined)
            editSnapshot.current = null
            updateCell(rowIndex, input.key, String(next ?? ''))
            if (editingHere) setEdit(null)
          }}
        />
      )
    } else if (kind === 'number') {
      control = (
        <InputNumber
          {...common}
          stringMode
          controls={false}
          // The arrow keys move between cells here; they must not also step the value.
          keyboard={!grid}
          onChange={(next) => {
            typedInto()
            updateCell(rowIndex, input.key, String(next ?? ''))
          }}
        />
      )
    } else {
      // Long text wraps inside the cell: the editor grows up to three lines and then scrolls, instead of
      // widening the column. Every text input uses this editor, but only a paragraph input may hold a
      // line break, so Enter in the others is swallowed rather than leaving a newline inside a value.
      const singleLine = kind !== 'paragraph'
      control = (
        <Input.TextArea
          {...common}
          autoSize={clamp === 'expanded' ? { minRows: 1, maxRows: EDIT_MAX_LINES } : clamp === 'row' ? false : { minRows: 1, maxRows: 3 }}
          style={clamp === 'row' ? { height: rowHeight, resize: 'none' } : undefined}
          onChange={(event) => {
            typedInto()
            updateCell(rowIndex, input.key, event.target.value)
          }}
          onKeyDown={
            singleLine && !grid
              ? (event: KeyboardEvent<HTMLTextAreaElement>) => {
                  // An input method confirms its candidate with Enter; that keystroke is not ours.
                  if (event.key === 'Enter' && !event.nativeEvent.isComposing) event.preventDefault()
                }
              : undefined
          }
        />
      )
    }
    return (
      <div
        className={[
          'rp-grid-cell',
          here && 'rp-grid-cell--active',
          here && (editingHere ? 'rp-grid-cell--editing' : 'rp-grid-cell--selecting'),
        ]
          .filter(Boolean)
          .join(' ')}
        data-grid-cell={`${rowIndex}:${columnIndex}`}
        data-clamp={clamp}
        style={rowHeight ? ({ '--rp-row-h': `${rowHeight}px` } as React.CSSProperties) : undefined}
        onPaste={(event) => handlePaste(event, rowIndex, columnIndex, kind)}
        onKeyDownCapture={(event) => onCellKeyDown(event, rowIndex, columnIndex, kind)}
        onFocus={(event) => {
          if (!grid) return
          if (!here) {
            setActive({ row: rowIndex, col: columnIndex })
            setEdit(null)
          }
          if (!editingHere) selectWholeText(event.target)
        }}
        onBlur={(event) => {
          composing.current = false
          if (grid && editingHere && !event.currentTarget.contains(event.relatedTarget as Node | null)) finishEdit()
        }}
        onMouseDown={(event) => {
          // A click selects the cell without placing a caret; only an edit in progress keeps the
          // browser's own caret and text selection (and a select's list its own clicks).
          if (!grid || editingHere || event.button !== 0) return
          event.preventDefault()
          setActive({ row: rowIndex, col: columnIndex })
          finishEdit()
          const element = controlOf(rowIndex, columnIndex)
          element?.focus({ preventScroll: true })
          selectWholeText(element)
        }}
        onDoubleClick={() => {
          if (grid && !editingHere) beginEdit(rowIndex, columnIndex, 'explicit')
        }}
        onCompositionStart={() => {
          composing.current = true
          typedInto()
        }}
        onCompositionEnd={() => {
          composing.current = false
        }}
        onCopy={(event) => {
          if (!grid || editingHere) return
          event.clipboardData.setData('text/plain', toClipboardText(value))
          event.preventDefault()
        }}
        onCut={(event) => {
          if (!grid || editingHere) return
          event.clipboardData.setData('text/plain', toClipboardText(value))
          event.preventDefault()
          if (value !== '') pushHistory()
          updateCell(rowIndex, input.key, '')
        }}
      >
        {control}
      </div>
    )
  }

  const columns: CardColumn<TableRow>[] = [
    {
      title: '#',
      dataIndex: 'rowIndex',
      key: 'rowIndex',
      width: ROW_NUMBER_WIDTH,
      fixed: 'left',
      align: 'center',
      onCell: () => ({ className: 'rp-grid-rownum', ...(grid ? { 'aria-colindex': 1 } : {}) }),
      render: (rowIndex: number) => (
        <>
          <Typography.Text type="secondary">{rowIndex + 1}</Typography.Text>
          {grid && (
            <RowResizeHandle
              row={rowIndex}
              value={rowHeights[rowIndex]}
              min={CONTROL_MIN}
              max={CONTROL_MAX}
              step={LINE_PX}
              onMeasure={measureRowHeight}
              onChange={(height) =>
                setRowHeights((heights) => {
                  const next = { ...heights }
                  if (height == null) delete next[rowIndex]
                  else next[rowIndex] = height
                  return next
                })
              }
            />
          )}
        </>
      ),
    },
    ...inputs.map((input, columnIndex) => {
      const { title, detail } = inputDisplayMeta(input)
      return {
        title: (
          <TableColumnHeader id={input.key} name={title} onMeasure={measure} onResize={resize} onCancel={cancel}>
          <div className="rp-batch-grid-heading">
            <div className="rp-batch-grid-heading__title">
              <Tooltip title={title}>
                <span className="rp-batch-grid-heading__name">{title}</span>
              </Tooltip>
              {input.required ? <span className="rp-batch-grid-heading__required">*</span> : <span className="rp-batch-grid-heading__optional">{t('run.optionalMark')}</span>}
              {detail && (
                <Popover title={title} content={<div className="rp-run-input-help__content">{detail}</div>} trigger="click">
                  <Button
                    type="text"
                    size="small"
                    shape="circle"
                    className="rp-batch-grid-heading__help"
                    icon={<QuestionCircleOutlined />}
                    aria-label={t('run.inputHelp', { field: title })}
                  />
                </Popover>
              )}
            </div>
            {title !== input.key && <Typography.Text code>{input.key}</Typography.Text>}
          </div>
          </TableColumnHeader>
        ),
        phoneLabel: title,
        key: input.key,
        // No width until the user drags one: the column then shares what is left with its siblings.
        width: widths[input.key],
        onCell: (record: TableRow) =>
          grid ? { role: 'gridcell', 'aria-colindex': columnIndex + 2, 'aria-selected': isActive(record.rowIndex, columnIndex) } : {},
        render: (_value: string, record: TableRow) => fieldControl(input, record.rowIndex, columnIndex),
      }
    }),
    // Once every input column has a width the user gave it, the spare space goes to an empty column
    // here rather than being spread back over theirs, so a dragged width is the width shown.
    ...(inputs.length > 0 && inputs.every((input) => widths[input.key] != null)
      ? [{ key: 'rp-grid-filler', onHeaderCell: () => ({ className: 'rp-grid-filler' }), onCell: () => ({ className: 'rp-grid-filler' }) } as CardColumn<TableRow>]
      : []),
    {
      key: 'actions',
      onCell: () => (grid ? { 'aria-colindex': inputs.length + 2 } : {}),
      width: DELETE_WIDTH,
      fixed: 'right',
      align: 'center',
      // The trailing blank row is the place to type the next row, not a row to delete.
      render: (_value, record) =>
        record.rowIndex === displayedRows.length - 1 && !batchRowHasValues(record.values) ? null : (
          <Tooltip title={t('batch.editor.deleteRow')}>
            <Button
              type="text"
              danger
              icon={<DeleteOutlined />}
              tabIndex={grid ? -1 : undefined}
              aria-label={t('batch.editor.deleteRowN', { row: record.rowIndex + 1 })}
              onClick={() => removeRow(record.rowIndex)}
            />
          </Tooltip>
        ),
    },
  ]

  const tableRows: TableRow[] = displayedRows.map((values, rowIndex) => ({ values, rowIndex }))
  const missingRowCount = new Set(missingCells.map((cell) => cell.rowIndex)).size

  return (
    <div className="rp-batch-rows-editor">
      <div className="rp-batch-editor-toolbar">
        <Space wrap size={8}>
          <Segmented
            value={mode}
            onChange={(value) => setModeAndSync(value as EditorMode)}
            options={[
              { value: 'grid', label: t('batch.editor.gridMode') },
              { value: 'csv', label: t('batch.editor.csvMode') },
            ]}
          />
          <Tag color={readyRows.length > 0 ? 'blue' : undefined}>{t('batch.parsedRows', { n: readyRows.length })}</Tag>
        </Space>
        <Space wrap size={4}>
          {mode === 'grid' && Object.keys(widths).length > 0 && (
            <Button size="small" type="text" icon={<UndoOutlined />} onClick={resetWidths}>
              {t('batch.editor.resetWidths')}
            </Button>
          )}
          <Upload accept=".csv,.txt" showUploadList={false} beforeUpload={importCSV}>
            <Button size="small" icon={<UploadOutlined />}>{t('batch.uploadCsv')}</Button>
          </Upload>
          <Button size="small" icon={<DownloadOutlined />} onClick={() => downloadCSV('batch-rows.csv', currentCSV())}>
            {t('batch.editor.downloadRows')}
          </Button>
          <Confirm
            title={t('batch.editor.clearConfirm')}
            onConfirm={() => {
              setRawCSV(toCSV(keys, []))
              setRowHeights({})
              setActive(null)
              history.current = { past: [], future: [] }
              onChange([blankBatchRow(inputs)])
            }}
            okText={t('batch.editor.clearAction')}
          >
            <Button size="small" danger>{t('batch.editor.clear')}</Button>
          </Confirm>
        </Space>
      </div>

      {mode === 'grid' ? (
        <>
          <Typography.Paragraph type="secondary" className="rp-batch-editor-hint">
            {t('batch.editor.pasteHint')}
            {grid && ` ${t('batch.editor.keyHint')}`}
          </Typography.Paragraph>
          <div
            className="rp-batch-grid"
            ref={gridRef}
            {...(grid
              ? { tabIndex: -1, role: 'group', 'aria-label': t('batch.editor.gridLabel'), onKeyDown: onGridKeyDown }
              : {})}
          >
            <CardTable<TableRow>
              size="small"
              className="rp-adaptive-table"
              rowKey={(record) => String(record.rowIndex)}
              onRow={(record) => (grid ? { 'aria-rowindex': record.rowIndex + 2 } : {})}
              columns={columns}
              dataSource={tableRows}
              pagination={
                tableRows.length > page.size
                  ? {
                      current: page.current,
                      pageSize: page.size,
                      showSizeChanger: true,
                      onChange: (current, size) => setPage({ current, size }),
                    }
                  : false
              }
              scroll={{
                x: fillScrollWidth(keys, widths, MIN_COLUMN_WIDTH, ROW_NUMBER_WIDTH + DELETE_WIDTH),
                y: 420,
              }}
            />
          </div>
        </>
      ) : (
        <Input.TextArea
          className="rp-batch-csv-editor"
          rows={12}
          value={rawCSV}
          onChange={(event) => {
            const text = event.target.value
            setRawCSV(text)
            onChange(csvToRows(text, keys))
          }}
          placeholder={t('batch.csvPlaceholder', { keys: keys.join(',') })}
        />
      )}

      {headerMissing.length > 0 ? (
        <Alert type="error" showIcon title={t('batch.editor.missingHeaders', { fields: headerMissing.join(', ') })} />
      ) : missingCells.length > 0 ? (
        <Alert
          type="error"
          showIcon
          title={t('batch.editor.missingRequired', { cells: missingCells.length, rows: missingRowCount })}
        />
      ) : readyRows.length > 0 ? (
        <Alert type="success" showIcon title={t('batch.editor.ready', { n: readyRows.length })} />
      ) : (
        <Alert type="info" showIcon title={t('batch.editor.empty')} />
      )}
    </div>
  )
}
