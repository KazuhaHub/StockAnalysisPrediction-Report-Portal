import { describe, expect, it } from 'vitest'

import {
  activeBatchRows,
  blankBatchRow,
  carryRowsToTarget,
  ensureTrailingBlank,
  missingRequiredCells,
  missingCSVHeaders,
  pasteBatchGrid,
  submittedBatchRows,
} from './batchRows'
import type { PluginInput } from '../api/types'

const inputs: PluginInput[] = [
  { key: 'symbol', required: true },
  { key: 'query', required: true },
  { key: 'context' },
]

describe('batch row editing', () => {
  it('creates a blank row with every declared column', () => {
    expect(blankBatchRow(inputs)).toEqual({ symbol: '', query: '', context: '' })
  })

  it('pastes a spreadsheet block into the selected cell and grows rows as needed', () => {
    expect(pasteBatchGrid([blankBatchRow(inputs)], inputs, 0, 0, '000001\talpha\n000002\tbeta')).toEqual([
      { symbol: '000001', query: 'alpha', context: '' },
      { symbol: '000002', query: 'beta', context: '' },
    ])
  })

  it('skips a pasted header row when it matches the destination columns', () => {
    expect(pasteBatchGrid([blankBatchRow(inputs)], inputs, 0, 0, 'symbol\tquery\n000001\talpha')).toEqual([
      { symbol: '000001', query: 'alpha', context: '' },
    ])
  })

  it('ignores placeholder rows and identifies missing required cells', () => {
    const rows = [blankBatchRow(inputs), { symbol: '000001', query: '', context: '' }]
    expect(activeBatchRows(rows)).toEqual([{ symbol: '000001', query: '', context: '' }])
    expect(missingRequiredCells(rows, inputs)).toEqual([{ rowIndex: 1, key: 'query' }])
  })

  it('reports columns missing from an imported CSV header', () => {
    expect(missingCSVHeaders('symbol,query\n000001,alpha', inputs)).toEqual(['context'])
  })
})

describe('trailing blank row', () => {
  const filled = (symbol: string) => ({ ...blankBatchRow(inputs), symbol })

  it('adds one blank row to an empty grid and after a filled last row', () => {
    expect(ensureTrailingBlank([], inputs)).toEqual([blankBatchRow(inputs)])
    expect(ensureTrailingBlank([filled('a')], inputs)).toEqual([filled('a'), blankBatchRow(inputs)])
  })

  it('keeps exactly one blank row when several already trail the data', () => {
    const rows = [filled('a'), blankBatchRow(inputs), blankBatchRow(inputs), blankBatchRow(inputs)]
    expect(ensureTrailingBlank(rows, inputs)).toEqual([filled('a'), blankBatchRow(inputs)])
  })

  it('keeps the earliest of the trailing blank rows so a row being cleared is not swapped under the cursor', () => {
    const edited = { ...blankBatchRow(inputs), symbol: ' ' } // whitespace is blank, but it is what the user typed
    expect(ensureTrailingBlank([filled('a'), edited, blankBatchRow(inputs)], inputs)[1]).toBe(edited)
  })

  it('leaves a blank row in the middle of the data alone', () => {
    const rows = [filled('a'), blankBatchRow(inputs), filled('b')]
    expect(ensureTrailingBlank(rows, inputs)).toEqual([...rows, blankBatchRow(inputs)])
  })

  it('is stable once applied', () => {
    const once = ensureTrailingBlank([filled('a')], inputs)
    expect(ensureTrailingBlank(once, inputs)).toEqual(once)
  })
})

describe('carrying rows to another workflow', () => {
  const next: PluginInput[] = [{ key: 'query' }, { key: 'extra' }]

  it('keeps values whose keys exist on the next workflow and drops the rest', () => {
    const rows = [{ symbol: '1', query: 'q1', context: 'c' }, blankBatchRow(inputs), { symbol: '2', query: '', context: '' }]
    expect(carryRowsToTarget(rows, next)).toEqual([
      { query: 'q1', extra: '' },
      { query: '', extra: '' },
    ])
  })

  it('falls back to one blank row of the next workflow when nothing was filled in', () => {
    expect(carryRowsToTarget([blankBatchRow(inputs)], next)).toEqual([{ query: '', extra: '' }])
    expect(carryRowsToTarget([], next)).toEqual([{ query: '', extra: '' }])
  })
})


// What is sent: rows with data, each value trimmed at its edges, the same whether it was typed into
// the grid, pasted from a spreadsheet or read from the CSV view (which always trimmed).
describe('submittedBatchRows', () => {
  it('drops blank rows and trims every value', () => {
    expect(submittedBatchRows([
      { symbol: ' 600519 ', note: 'a  b' },
      { symbol: '  ', note: '' },
    ])).toEqual([{ symbol: '600519', note: 'a  b' }])
  })
})
