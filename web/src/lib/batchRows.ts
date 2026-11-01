import type { PluginInput } from '../api/types'
import { parseCSV } from './csv'

export type BatchDraftRow = Record<string, string>

export interface MissingBatchCell {
  rowIndex: number
  key: string
}

export function blankBatchRow(inputs: PluginInput[]): BatchDraftRow {
  return Object.fromEntries(inputs.map((input) => [input.key, '']))
}

export function batchRowHasValues(row: BatchDraftRow): boolean {
  return Object.values(row).some((value) => String(value ?? '').trim() !== '')
}

/**
 * The rows a run or a task sends: those with data, each value trimmed at its edges. A cell pasted
 * from a spreadsheet often carries a trailing space, and the CSV view has always trimmed; trimming
 * here makes the grid send what the CSV view would.
 */
export function submittedBatchRows(rows: BatchDraftRow[]): BatchDraftRow[] {
  return activeBatchRows(rows).map((row) => Object.fromEntries(Object.entries(row).map(([key, value]) => [key, String(value ?? '').trim()])))
}

export function activeBatchRows(rows: BatchDraftRow[]): BatchDraftRow[] {
  return rows.filter(batchRowHasValues)
}

/**
 * The grid always ends with exactly one blank row, spreadsheet-style: typing into it makes the next
 * blank row appear. When several blank rows already trail the data the earliest one stays, because
 * that is the row someone has just cleared and is still focused; swapping it for a later row would
 * pull the field out from under the cursor. Blank rows between filled ones are left alone.
 */
export function ensureTrailingBlank(rows: BatchDraftRow[], inputs: PluginInput[]): BatchDraftRow[] {
  let firstTrailingBlank = rows.length
  while (firstTrailingBlank > 0 && !batchRowHasValues(rows[firstTrailingBlank - 1])) firstTrailingBlank--
  if (firstTrailingBlank === rows.length) return [...rows, blankBatchRow(inputs)]
  return rows.slice(0, firstTrailingBlank + 1)
}

/**
 * Rows to keep when the workflow changes: values whose keys exist on the next workflow stay, which
 * makes correcting an accidental pick cheap without ever assigning a value to a different column by
 * position. Nothing filled in yields one blank row of the next workflow.
 */
export function carryRowsToTarget(rows: BatchDraftRow[], nextInputs: PluginInput[]): BatchDraftRow[] {
  const filled = activeBatchRows(rows)
  if (filled.length === 0) return [blankBatchRow(nextInputs)]
  return filled.map((row) => Object.fromEntries(nextInputs.map((input) => [input.key, row[input.key] ?? ''])))
}

export function missingRequiredCells(rows: BatchDraftRow[], inputs: PluginInput[]): MissingBatchCell[] {
  const required = inputs.filter((input) => input.required)
  const missing: MissingBatchCell[] = []
  rows.forEach((row, rowIndex) => {
    if (!batchRowHasValues(row)) return
    required.forEach((input) => {
      if (String(row[input.key] ?? '').trim() === '') missing.push({ rowIndex, key: input.key })
    })
  })
  return missing
}

/**
 * Parses the text/plain flavour that Excel and Google Sheets put on the clipboard: TSV in which a cell
 * holding a tab, a line break or a quote is wrapped in double quotes (a quote inside doubles up).
 * It is deliberately not CSV: a copied single column whose cells contain commas ("Apple, Inc.") must
 * stay one column, which a comma split would break.
 *
 * Quoting follows Excel's and Sheets' exactly, including the odd case: hand-typed text such as
 * `"Hello\tA\nB\t5"` + newline is one quoted cell (its closing quote ends the row), which is also what
 * those applications make of it.
 *
 * Rows end at CRLF, LF or CR. Excel always appends one terminator after the last row and Sheets does
 * not, so one trailing terminator never makes a row; trailing blank rows are dropped, but blank rows
 * in the middle stay because they keep the rows after them aligned with the source.
 */
export function parseClipboardGrid(text: string): string[][] {
  const rows: string[][] = []
  let row: string[] = []
  let cell = ''
  // True once anything of the current row was consumed, so a final row without a terminator is kept
  // while a terminator at the very end does not add an empty one.
  let pending = false
  let atCellStart = true
  let i = 0
  while (i < text.length) {
    const ch = text[i]
    if (atCellStart && ch === '"') {
      const quoted = readQuotedCell(text, i)
      if (quoted) {
        cell += quoted.value
        i = quoted.next
        pending = true
        atCellStart = false
        continue
      }
      // No closing quote: the quote is ordinary text.
    }
    atCellStart = false
    if (ch === '\t') {
      row.push(cell)
      cell = ''
      pending = true
      atCellStart = true
      i++
    } else if (ch === '\r' || ch === '\n') {
      row.push(cell)
      rows.push(row)
      row = []
      cell = ''
      pending = false
      atCellStart = true
      i += ch === '\r' && text[i + 1] === '\n' ? 2 : 1
    } else {
      cell += ch
      pending = true
      i++
    }
  }
  if (pending) {
    row.push(cell)
    rows.push(row)
  }
  while (rows.length > 0 && rows[rows.length - 1].every((value) => value === '')) rows.pop()
  return rows
}

// Excel's rule: a quote opens a quoted cell only when the matching closing quote (a doubled quote
// inside is a literal one) is followed by a tab, a line break or the end of the text. Anything else
// ("Big" deal, 5" pipe, an opening quote that never closes) is literal text, quotes kept, so a stray
// quote can never swallow the cells and rows after it.
function readQuotedCell(text: string, start: number): { value: string; next: number } | null {
  let value = ''
  let i = start + 1
  while (i < text.length) {
    if (text[i] === '"') {
      if (text[i + 1] === '"') {
        value += '"'
        i += 2
        continue
      }
      const after = text[i + 1]
      if (after === undefined || after === '\t' || after === '\r' || after === '\n') return { value, next: i + 1 }
      return null
    }
    value += text[i]
    i++
  }
  return null
}

/**
 * Whether clipboard text should be pasted as a block of cells: it holds a tab or a line break. A
 * lone quoted value without either is just a value.
 */
export function isGridClipboard(text: string): boolean {
  return /[\t\r\n]/.test(text)
}

/** A cell as Excel puts it on the clipboard: quoted, with quotes doubled, when it holds a tab, line break or quote. */
export function toClipboardText(value: string): string {
  return /[\t\r\n"]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value
}

/**
 * A number as spreadsheets and East Asian input methods write it, in the plain ASCII form the field
 * stores: full-width digits, signs and dots are narrowed, a leading plus and surrounding spaces are
 * dropped, and commas are accepted only as thousands separators in groups of three ("1,234.50").
 * Anything else ("5%", "$5", "1e5", "1,23") is not a number here.
 */
function normaliseNumber(cell: string): string | undefined {
  const ascii = cell
    .trim()
    .replace(/[\uff10-\uff19]/g, (digit) => String.fromCharCode(digit.charCodeAt(0) - 0xfee0))
    .replace(/[\uff0d\u2212]/g, '-')
    .replace(/\uff0b/g, '+')
    .replace(/\uff0e/g, '.')
    .replace(/\uff0c/g, ',')
  const match = /^([+-]?)(\d{1,3}(?:,\d{3})+|\d+)(\.\d*)?$|^([+-]?)(\.\d+)$/.exec(ascii)
  if (!match) return undefined
  const sign = (match[1] ?? match[4]) === '-' ? '-' : ''
  return sign + (match[2] ?? '').replace(/,/g, '') + (match[3] ?? match[5] ?? '')
}

/**
 * What a pasted cell becomes in its column, or undefined to leave the cell as it was. A number
 * column takes a number, a select column one of its options; a single-line text column never holds a
 * line break, so one is read as a space. An empty cell clears whatever the column is.
 */
function coercePastedCell(input: PluginInput, cell: string): string | undefined {
  if (cell === '') return ''
  if (input.type === 'number') return normaliseNumber(cell)
  if (input.type === 'select' && input.options?.length) {
    const trimmed = cell.trim()
    return input.options.includes(trimmed) ? trimmed : undefined
  }
  return input.type === 'paragraph' ? cell : cell.replace(/\r\n|\r|\n/g, ' ')
}

function matchesDestinationHeader(row: string[], inputs: PluginInput[], startColumn: number): boolean {
  if (row.length === 0) return false
  return row.every((cell, offset) => cell.trim() === inputs[startColumn + offset]?.key)
}

function pastedGrid(inputs: PluginInput[], startColumn: number, text: string): string[][] {
  const grid = parseClipboardGrid(text)
  // A header row is only recognised in a block of cells: one pasted value that happens to equal a column key is data.
  if ((grid.length > 1 || grid[0]?.length > 1) && matchesDestinationHeader(grid[0], inputs, startColumn)) return grid.slice(1)
  return grid
}

/** How many rows a paste of this text puts into the grid (a skipped header row does not count). */
export function pastedRowCount(inputs: PluginInput[], startColumn: number, text: string): number {
  return pastedGrid(inputs, startColumn, text).length
}

/** `skipped` counts the pasted cells a typed column refused; each of those cells was left as it was. */
export function pasteBatchGridReport(
  rows: BatchDraftRow[],
  inputs: PluginInput[],
  startRow: number,
  startColumn: number,
  text: string,
): { rows: BatchDraftRow[]; skipped: number } {
  const grid = pastedGrid(inputs, startColumn, text)
  if (grid.length === 0) return { rows, skipped: 0 }

  let skipped = 0
  const next = (rows.length > 0 ? rows : [blankBatchRow(inputs)]).map((row) => ({ ...blankBatchRow(inputs), ...row }))
  grid.forEach((cells, rowOffset) => {
    const rowIndex = startRow + rowOffset
    while (next.length <= rowIndex) next.push(blankBatchRow(inputs))
    cells.forEach((cell, columnOffset) => {
      const input = inputs[startColumn + columnOffset]
      if (!input) return
      const value = coercePastedCell(input, cell)
      if (value === undefined) skipped++
      else next[rowIndex][input.key] = value
    })
  })
  return { rows: next, skipped }
}

export function pasteBatchGrid(
  rows: BatchDraftRow[],
  inputs: PluginInput[],
  startRow: number,
  startColumn: number,
  text: string,
): BatchDraftRow[] {
  return pasteBatchGridReport(rows, inputs, startRow, startColumn, text).rows
}

export function missingCSVHeaders(text: string, inputs: PluginInput[]): string[] {
  const header = parseCSV(text)[0]?.map((cell) => cell.trim()) || []
  return inputs.map((input) => input.key).filter((key) => !header.includes(key))
}
