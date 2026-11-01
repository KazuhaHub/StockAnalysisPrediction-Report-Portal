import { useCallback, useState } from 'react'
import type { ColumnWidths, ResizeColumn } from '../components/TableColumnHeader'

// The width half of useTableView, for tables that are editable grids and so must not offer column
// hiding or reordering: widths a user dragged, kept in localStorage under the same `rp.table.` prefix
// and the same `{ widths }` shape so both kinds of table read and write one format.

const STORED_MIN = 64
const STORED_MAX = 10000
const DRAG_MAX = 1200

/** A dragged width, held between the column's own minimum and the shared maximum. */
export const clampColumnWidth = (width: number, min: number) => Math.round(Math.max(min, Math.min(DRAG_MAX, width)))

/** The usable widths out of whatever was parsed from storage; anything malformed is dropped. */
export function storedColumnWidths(stored: unknown): ColumnWidths {
  const raw = (stored as { widths?: unknown } | null)?.widths
  if (!raw || typeof raw !== 'object') return {}
  return Object.fromEntries(
    Object.entries(raw).filter(
      (entry): entry is [string, number] =>
        typeof entry[1] === 'number' && Number.isFinite(entry[1]) && entry[1] >= STORED_MIN && entry[1] <= STORED_MAX,
    ),
  )
}

/**
 * The table's `scroll.x` for a grid whose columns fill the available width: each column counts for
 * its minimum, or for the width the user gave it, and `fixed` stands for the pinned columns. The
 * table is at least this wide, so a container narrower than it scrolls inside the grid; a wider one
 * is filled.
 */
export function fillScrollWidth(keys: string[], widths: ColumnWidths, min: number, fixed: number) {
  return fixed + keys.reduce((sum, key) => sum + Math.max(min, widths[key] ?? min), 0)
}

const ignoreMeasure = () => {}

/** Widths for a resizable grid. `name` is the storage name; without one nothing is stored. */
export function useColumnWidths(name: string | undefined, min: number) {
  const storageKey = name ? `rp.table.${name}` : undefined
  const [stored, setStored] = useState<ColumnWidths>(() => {
    if (!storageKey) return {}
    try {
      return storedColumnWidths(JSON.parse(localStorage.getItem(storageKey) || 'null'))
    } catch {
      return {}
    }
  })
  const [draft, setDraft] = useState<ColumnWidths | null>(null)
  const save = useCallback(
    (next: ColumnWidths) => {
      setStored(next)
      if (!storageKey) return
      try {
        localStorage.setItem(storageKey, JSON.stringify({ widths: next }))
      } catch {
        /* Display preferences are optional. */
      }
    },
    [storageKey],
  )
  const resize: ResizeColumn = (key, width, commit = true) => {
    if (!Number.isFinite(width)) return
    const next = { ...stored, [key]: clampColumnWidth(width, min) }
    if (commit) {
      setDraft(null)
      save(next)
    } else {
      setDraft(next)
    }
  }
  return {
    widths: draft ?? stored,
    resize,
    cancel: () => setDraft(null),
    reset: () => {
      setDraft(null)
      save({})
    },
    // The header reports its measured width for tables that need it to trade width with a neighbour;
    // a fill grid lets the neighbours reflow on their own.
    measure: ignoreMeasure,
  }
}
