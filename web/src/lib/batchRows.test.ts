import { describe, expect, it } from 'vitest'

import {
  activeBatchRows,
  blankBatchRow,
  carryRowsToTarget,
  ensureTrailingBlank,
  missingRequiredCells,
  missingCSVHeaders,
  isGridClipboard,
  parseClipboardGrid,
  toClipboardText,
  pasteBatchGrid,
  pasteBatchGridReport,
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


// Payloads exactly as each application puts them on the clipboard as text/plain.
describe('parseClipboardGrid', () => {
  it('reads an Excel block of two columns and two rows (CRLF rows, trailing terminator)', () => {
    expect(parseClipboardGrid('600519\tq1\r\n000001\tq2\r\n')).toEqual([['600519', 'q1'], ['000001', 'q2']])
  })

  it('keeps commas inside a single Excel column instead of splitting on them', () => {
    expect(parseClipboardGrid('Apple, Inc.\r\nFoo, Bar\r\n')).toEqual([['Apple, Inc.'], ['Foo, Bar']])
  })

  it('reads a quoted cell holding a line break', () => {
    expect(parseClipboardGrid('"line1\nline2"\tB\r\n')).toEqual([['line1\nline2', 'B']])
  })

  it('reads a quoted cell holding a tab', () => {
    expect(parseClipboardGrid('"a\tb"\tC\r\n')).toEqual([['a\tb', 'C']])
  })

  it('unescapes doubled quotes inside a quoted cell', () => {
    expect(parseClipboardGrid('"say ""hi"""\tB\r\n')).toEqual([['say "hi"', 'B']])
  })

  it('treats a quote that does not start the cell as a literal character', () => {
    expect(parseClipboardGrid('5" pipe\tB')).toEqual([['5" pipe', 'B']])
  })

  it('reads Google Sheets output: LF rows and no trailing terminator', () => {
    expect(parseClipboardGrid('600519\tq1\n000001\tq2')).toEqual([['600519', 'q1'], ['000001', 'q2']])
  })

  it('accepts a bare CR as a row terminator', () => {
    expect(parseClipboardGrid('a\tb\rc\td')).toEqual([['a', 'b'], ['c', 'd']])
  })

  it('keeps a blank row in the middle so the rows after it stay aligned', () => {
    expect(parseClipboardGrid('a\tb\r\n\t\r\nc\td\r\n')).toEqual([['a', 'b'], ['', ''], ['c', 'd']])
  })

  it('drops only trailing blank rows', () => {
    expect(parseClipboardGrid('a\tb\r\n\t\r\n\r\n')).toEqual([['a', 'b']])
    expect(parseClipboardGrid('\r\n')).toEqual([])
    expect(parseClipboardGrid('')).toEqual([])
  })

  it('reads one copied cell as a one-by-one grid', () => {
    expect(parseClipboardGrid('600519\r\n')).toEqual([['600519']])
  })

  it('keeps an empty leading cell', () => {
    expect(parseClipboardGrid('\tb\r\n')).toEqual([['', 'b']])
  })

  it('treats an unterminated opening quote as literal text and never swallows what follows', () => {
    expect(parseClipboardGrid('"abc\tB')).toEqual([['"abc', 'B']])
    expect(parseClipboardGrid('"abc\r\nd\te')).toEqual([['"abc'], ['d', 'e']])
  })

  // Excel's rule: a quote opens a quoted cell only when its matching closing quote is followed by a
  // tab, a line break or the end of the text; otherwise the cell is literal, quotes kept.
  it('keeps a cell literal when the closing quote is followed by more text', () => {
    expect(parseClipboardGrid('"Big" deal')).toEqual([['"Big" deal']])
    expect(parseClipboardGrid('"5" monitor\t10')).toEqual([['"5" monitor', '10']])
  })

  it('does not merge rows around a quote that cannot close a cell', () => {
    expect(parseClipboardGrid('"abc\tx\n"def"\tq')).toEqual([['"abc', 'x'], ['def', 'q']])
  })

  it('closes a quoted cell at the end of the text or before a line break', () => {
    expect(parseClipboardGrid('"a b"')).toEqual([['a b']])
    expect(parseClipboardGrid('"a\nb"\n"c"\r\n')).toEqual([['a\nb'], ['c']])
  })
})

describe('isGridClipboard', () => {
  it('is a grid paste when the text has a tab or a line break', () => {
    expect(isGridClipboard('a\tb')).toBe(true)
    expect(isGridClipboard('600519\r\n')).toBe(true)
    expect(isGridClipboard('a\nb')).toBe(true)
  })

  it('does not treat a lone quoted value with no tab or line break as a grid', () => {
    expect(isGridClipboard('"say ""hi"""')).toBe(false)
    expect(isGridClipboard('"Big" deal')).toBe(false)
  })

  it('leaves a plain single value to the native paste', () => {
    expect(isGridClipboard('600519')).toBe(false)
    expect(isGridClipboard('Apple, Inc.')).toBe(false)
    expect(isGridClipboard('5" pipe')).toBe(false)
  })
})

describe('pasteBatchGrid with clipboard text', () => {
  it('puts a single column of comma-bearing cells in one column', () => {
    expect(pasteBatchGrid([blankBatchRow(inputs)], inputs, 0, 1, 'Apple, Inc.\r\nFoo, Bar\r\n')).toEqual([
      { symbol: '', query: 'Apple, Inc.', context: '' },
      { symbol: '', query: 'Foo, Bar', context: '' },
    ])
  })

  it('keeps the blank middle row as an empty row of cells', () => {
    expect(pasteBatchGrid([blankBatchRow(inputs)], inputs, 0, 0, 'a\tb\r\n\t\r\nc\td\r\n')).toEqual([
      { symbol: 'a', query: 'b', context: '' },
      { symbol: '', query: '', context: '' },
      { symbol: 'c', query: 'd', context: '' },
    ])
  })
})


describe('toClipboardText', () => {
  it('writes a plain value as it is', () => {
    expect(toClipboardText('600519')).toBe('600519')
    expect(toClipboardText('Apple, Inc.')).toBe('Apple, Inc.')
  })

  it('quotes a value holding a tab, a line break or a quote, doubling the quotes', () => {
    expect(toClipboardText('a\nb')).toBe('"a\nb"')
    expect(toClipboardText('a\tb')).toBe('"a\tb"')
    expect(toClipboardText('say "hi"')).toBe('"say ""hi"""')
  })

  it('round-trips through the parser', () => {
    for (const value of ['a\nb', 'a\tb', 'say "hi"', '5"', '"', 'plain']) {
      expect(parseClipboardGrid(toClipboardText(value))).toEqual([[value]])
    }
  })
})

describe('pasting into typed columns', () => {
  const typed: PluginInput[] = [
    { key: 'name' },
    { key: 'amount', type: 'number' },
    { key: 'mode', type: 'select', options: ['fast', 'slow'] },
    { key: 'body', type: 'paste-paragraph' },
  ]
  const blank = () => [blankBatchRow(typed)]

  it('puts a number into a number column and leaves the cell alone for anything else', () => {
    expect(pasteBatchGrid(blank(), typed, 0, 1, '12.5')[0].amount).toBe('12.5')
    expect(pasteBatchGrid(blank(), typed, 0, 1, '-3')[0].amount).toBe('-3')
    expect(pasteBatchGrid([{ ...blankBatchRow(typed), amount: '7' }], typed, 0, 1, 'abc')[0].amount).toBe('7')
    expect(pasteBatchGrid([{ ...blankBatchRow(typed), amount: '7' }], typed, 0, 1, '1,5')[0].amount).toBe('7')
  })

  it('puts an option into a select column and leaves the cell alone for anything else', () => {
    expect(pasteBatchGrid(blank(), typed, 0, 2, 'slow')[0].mode).toBe('slow')
    expect(pasteBatchGrid([{ ...blankBatchRow(typed), mode: 'fast' }], typed, 0, 2, 'medium')[0].mode).toBe('fast')
  })

  it('applies the same checks to every cell of a block, keeping the valid ones', () => {
    const out = pasteBatchGrid(blank(), typed, 0, 0, 'x\tnope\tslow\r\ny\t5\tbad\r\n')
    expect(out).toEqual([
      { name: 'x', amount: '', mode: 'slow', body: '' },
      { name: 'y', amount: '5', mode: '', body: '' },
    ])
  })

  it('keeps a single-line text value on one line but lets a paragraph keep its line breaks', () => {
    const fields: PluginInput[] = [{ key: 'one' }, { key: 'many', type: 'paragraph' }]
    expect(pasteBatchGrid([blankBatchRow(fields)], fields, 0, 0, '"a\nb"\t"c\nd"')[0]).toEqual({ one: 'a b', many: 'c\nd' })
  })

  it('does not take a lone value equal to a column key for a header row', () => {
    expect(pasteBatchGrid([blankBatchRow(inputs)], inputs, 0, 0, 'symbol')[0].symbol).toBe('symbol')
  })
})


describe('number cells from real-world spreadsheets', () => {
  const num: PluginInput[] = [{ key: 'amount', type: 'number' }]
  const paste = (text: string, before = '7') => pasteBatchGrid([{ amount: before }], num, 0, 0, text)[0].amount

  it('normalises full-width digits, signs and dots to ASCII', () => {
    expect(paste('１２３')).toBe('123')
    expect(paste('－４．５')).toBe('-4.5')
    expect(paste('＋８')).toBe('8')
  })

  it('accepts a leading plus and surrounding spaces', () => {
    expect(paste('+5')).toBe('5')
    expect(paste('  12.5  ')).toBe('12.5')
    expect(paste('\u3000９\u3000')).toBe('9')
  })

  it('accepts thousands separators in valid groups only', () => {
    expect(paste('1,234')).toBe('1234')
    expect(paste('1,234.50')).toBe('1234.50')
    expect(paste('-12,345,678')).toBe('-12345678')
    expect(paste('1,23')).toBe('7')
    expect(paste('12,34,567')).toBe('7')
  })

  it('still refuses text that is not a plain number', () => {
    expect(paste('5%')).toBe('7')
    expect(paste('abc')).toBe('7')
    expect(paste('1e5')).toBe('7')
    expect(paste('$5')).toBe('7')
  })
})

describe('pasteBatchGridReport', () => {
  const typed: PluginInput[] = [{ key: 'amount', type: 'number' }, { key: 'mode', type: 'select', options: ['a'] }, { key: 'note' }]

  it('counts the cells that did not fit their column and left the cell as it was', () => {
    const out = pasteBatchGridReport([blankBatchRow(typed)], typed, 0, 0, 'x\tb\tfree\r\n5\ta\tok\r\n')
    expect(out.skipped).toBe(2)
    expect(out.rows).toEqual([
      { amount: '', mode: '', note: 'free' },
      { amount: '5', mode: 'a', note: 'ok' },
    ])
  })

  it('reports a paste in which every cell was refused', () => {
    const out = pasteBatchGridReport([blankBatchRow(typed)], typed, 0, 0, 'nope')
    expect(out.skipped).toBe(1)
    expect(out.rows[0]).toEqual(blankBatchRow(typed))
  })

  it('does not count empty cells or cells past the last column', () => {
    expect(pasteBatchGridReport([blankBatchRow(typed)], typed, 0, 0, '\t\t\tover\tflow').skipped).toBe(0)
  })
})
