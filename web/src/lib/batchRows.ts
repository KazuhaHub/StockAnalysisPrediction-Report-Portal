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

function parseSpreadsheetGrid(text: string): string[][] {
  if (!text.includes('\t')) return parseCSV(text)
  return text
    .replace(/\r\n?/g, '\n')
    .split('\n')
    .map((line) => line.split('\t'))
    .filter((row) => row.some((cell) => cell !== ''))
}

function matchesDestinationHeader(row: string[], inputs: PluginInput[], startColumn: number): boolean {
  if (row.length === 0) return false
  return row.every((cell, offset) => cell.trim() === inputs[startColumn + offset]?.key)
}

export function pasteBatchGrid(
  rows: BatchDraftRow[],
  inputs: PluginInput[],
  startRow: number,
  startColumn: number,
  text: string,
): BatchDraftRow[] {
  let grid = parseSpreadsheetGrid(text)
  if (matchesDestinationHeader(grid[0] || [], inputs, startColumn)) grid = grid.slice(1)
  if (grid.length === 0) return rows

  const next = (rows.length > 0 ? rows : [blankBatchRow(inputs)]).map((row) => ({ ...blankBatchRow(inputs), ...row }))
  grid.forEach((cells, rowOffset) => {
    const rowIndex = startRow + rowOffset
    while (next.length <= rowIndex) next.push(blankBatchRow(inputs))
    cells.forEach((cell, columnOffset) => {
      const input = inputs[startColumn + columnOffset]
      if (input) next[rowIndex][input.key] = cell
    })
  })
  return next
}

export function missingCSVHeaders(text: string, inputs: PluginInput[]): string[] {
  const header = parseCSV(text)[0]?.map((cell) => cell.trim()) || []
  return inputs.map((input) => input.key).filter((key) => !header.includes(key))
}
