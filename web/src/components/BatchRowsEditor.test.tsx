import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'

import BatchRowsEditor from './BatchRowsEditor'
import type { PluginInput } from '../api/types'
import { blankBatchRow, type BatchDraftRow } from '../lib/batchRows'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, values?: Record<string, unknown>) => (values ? `${key}:${JSON.stringify(values)}` : key) }),
}))

const inputs: PluginInput[] = [
  { key: 'symbol', label: 'Stock code', required: true },
  { key: 'query', label: 'Research query', required: true },
  { key: 'context', label: 'Context', description: 'JSON object passed to the workflow' },
]

function Harness({
  onValidityChange = () => {},
  fields = inputs,
  widthsKey,
  initial,
  onRows,
}: {
  onValidityChange?: (valid: boolean) => void
  fields?: PluginInput[]
  widthsKey?: string
  initial?: BatchDraftRow[]
  onRows?: (rows: BatchDraftRow[]) => void
}) {
  const [rows, setRows] = useState<BatchDraftRow[]>(initial ?? [blankBatchRow(fields)])
  return (
    <App>
      <BatchRowsEditor
        inputs={fields}
        rows={rows}
        onChange={(next) => {
          setRows(next)
          onRows?.(next)
        }}
        onValidityChange={onValidityChange}
        widthsKey={widthsKey}
      />
    </App>
  )
}

const stockBoxes = () => screen.getAllByRole('textbox', { name: /Stock code/ }) as HTMLTextAreaElement[]
const parsedCount = () => screen.getByText(/batch\.parsedRows/).textContent

describe('BatchRowsEditor', () => {
  it('renders a bounded spreadsheet with compact field headings', () => {
    render(<Harness />)
    expect(document.body.querySelector('.rp-batch-grid')).toBeTruthy()
    expect(screen.getAllByText('Stock code').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Research query').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Context').length).toBeGreaterThan(0)
    expect(screen.getAllByText('run.optionalMark').length).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: /run\.inputHelp.*Context/ })).toBeTruthy()
  })

  it('accepts a multi-row spreadsheet paste starting at the focused cell', () => {
    render(<Harness />)
    const first = screen.getByRole('textbox', { name: /batch\.editor\.cellLabel.*Stock code/ })
    fireEvent.paste(first, { clipboardData: { getData: () => '000001\talpha\n000002\tbeta' } })

    expect(screen.getByDisplayValue('000001')).toBeTruthy()
    expect(screen.getByDisplayValue('alpha')).toBeTruthy()
    expect(screen.getByDisplayValue('000002')).toBeTruthy()
    expect(screen.getByDisplayValue('beta')).toBeTruthy()
  })

  it('keeps a synchronized CSV mode for bulk text editing', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(screen.getByText('batch.editor.csvMode'))

    const editor = screen.getByPlaceholderText(/batch\.csvPlaceholder/) as HTMLTextAreaElement
    expect(editor.value).toBe('symbol,query,context')
    await user.clear(editor)
    await user.type(editor, 'symbol,query,context{enter}000001,alpha,')
    expect(screen.getByText(/batch\.editor\.ready/)).toBeTruthy()
  })

  it('marks an incomplete required cell and only becomes valid after it is filled', async () => {
    const validity = vi.fn()
    const user = userEvent.setup()
    render(<Harness onValidityChange={validity} />)
    await user.type(stockBoxes()[0], '000001')

    expect(screen.getByText(/batch\.editor\.missingRequired/)).toBeTruthy()
    expect(validity).toHaveBeenLastCalledWith(false)

    // The first row is the one being filled in; the blank row that appeared below it is not data.
    await user.type(screen.getAllByRole('textbox', { name: /Research query/ })[0], 'alpha')
    expect(validity).toHaveBeenLastCalledWith(true)
  })
})

describe('BatchRowsEditor trailing blank row', () => {
  it('starts with a single blank row and no add-row button', () => {
    render(<Harness />)
    expect(stockBoxes()).toHaveLength(1)
    expect(screen.queryByText('batch.editor.addRow')).toBeNull()
    expect(parsedCount()).toContain('"n":0')
  })

  it('grows a new blank row as soon as the last row is typed into', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.type(stockBoxes()[0], '0')
    expect(stockBoxes()).toHaveLength(2)
    expect(stockBoxes()[1].value).toBe('')
    expect(parsedCount()).toContain('"n":1')
    // Typing more into the same row, or into a column of it, does not add another row.
    await user.type(stockBoxes()[0], '1')
    await user.type(screen.getAllByRole('textbox', { name: /Research query/ })[0], 'alpha')
    expect(stockBoxes()).toHaveLength(2)
  })

  it('keeps the focus in the cell being typed in while the row below appears', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.type(stockBoxes()[0], '000')
    expect(document.activeElement).toBe(stockBoxes()[0])
    expect(stockBoxes()[0].value).toBe('000')
  })

  it('never counts or submits the blank rows', async () => {
    const user = userEvent.setup()
    const seen: BatchDraftRow[][] = []
    render(<Harness onRows={(rows) => seen.push(rows)} />)
    await user.type(stockBoxes()[0], '000001')
    expect(parsedCount()).toContain('"n":1')
    const last = seen[seen.length - 1]
    expect(last).toHaveLength(2)
    expect(activeCount(last)).toBe(1)
  })

  it('does not treat a row of spaces as data, and does not grow for it', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.type(stockBoxes()[0], '  ')
    expect(stockBoxes()).toHaveLength(1)
    expect(parsedCount()).toContain('"n":0')
  })

  it('expands for a multi-row paste and still ends on one blank row', () => {
    render(<Harness />)
    fireEvent.paste(stockBoxes()[0], { clipboardData: { getData: () => '000001\talpha\n000002\tbeta' } })
    expect(stockBoxes()).toHaveLength(3)
    expect(parsedCount()).toContain('"n":2')
  })

  it('pastes into the trailing blank row and grows another below it', () => {
    render(<Harness />)
    fireEvent.paste(stockBoxes()[0], { clipboardData: { getData: () => '000001\talpha\n000002\tbeta' } })
    fireEvent.paste(stockBoxes()[2], { clipboardData: { getData: () => '000003\tgamma\n000004\tdelta' } })
    expect(stockBoxes()).toHaveLength(5)
    expect(parsedCount()).toContain('"n":4')
  })

  it('deleting a row leaves one trailing blank row, and the blank row itself has no delete action', () => {
    render(<Harness />)
    fireEvent.paste(stockBoxes()[0], { clipboardData: { getData: () => '000001\talpha\n000002\tbeta' } })
    const deletes = () => screen.queryAllByRole('button', { name: /batch\.editor\.deleteRowN/ })
    expect(deletes()).toHaveLength(2)
    fireEvent.click(screen.getByRole('button', { name: /deleteRowN.*"row":1/ }))
    expect(stockBoxes()).toHaveLength(2)
    expect(stockBoxes()[0].value).toBe('000002')
    expect(deletes()).toHaveLength(1)
    fireEvent.click(deletes()[0])
    expect(stockBoxes()).toHaveLength(1)
    expect(deletes()).toHaveLength(0)
  })

  it('exports and imports CSV without the trailing blank row', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    fireEvent.paste(stockBoxes()[0], { clipboardData: { getData: () => '000001\talpha\n000002\tbeta' } })
    await user.click(screen.getByText('batch.editor.csvMode'))
    const editor = screen.getByPlaceholderText(/batch\.csvPlaceholder/) as HTMLTextAreaElement
    expect(editor.value).toBe('symbol,query,context\n000001,alpha,\n000002,beta,')
    await user.click(screen.getByText('batch.editor.gridMode'))
    expect(stockBoxes()).toHaveLength(3)
    expect(stockBoxes()[2].value).toBe('')
  })

  it('follows the blank row onto the last page of a long grid', () => {
    render(<Harness />)
    const block = Array.from({ length: 25 }, (_row, index) => `c${index + 1}\tq${index + 1}`).join('\n')
    fireEvent.paste(stockBoxes()[0], { clipboardData: { getData: () => block } })
    // 25 rows and the blank one make 26: the second page of 20 holds the last six, blank row included.
    expect(stockBoxes()).toHaveLength(6)
    expect(stockBoxes()[5].value).toBe('')
    expect(parsedCount()).toContain('"n":25')
  })

  it('returns to one blank row after Clear is confirmed', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    fireEvent.paste(stockBoxes()[0], { clipboardData: { getData: () => '000001\talpha\n000002\tbeta' } })
    await user.click(screen.getByRole('button', { name: 'batch.editor.clear' }))
    await user.click(await screen.findByRole('button', { name: 'batch.editor.clearAction' }))
    expect(stockBoxes()).toHaveLength(1)
    expect(parsedCount()).toContain('"n":0')
  })
})

const activeCount = (rows: BatchDraftRow[]) => rows.filter((row) => Object.values(row).some((value) => value.trim() !== '')).length

describe('BatchRowsEditor cell editors', () => {
  const mixed: PluginInput[] = [
    { key: 'note', label: 'Note' },
    { key: 'body', label: 'Body', type: 'paragraph' },
    { key: 'amount', label: 'Amount', type: 'number' },
    { key: 'mode', label: 'Mode', type: 'select', options: ['a', 'b'] },
  ]

  it('wraps long text in a growing editor instead of widening the column', () => {
    render(<Harness fields={mixed} />)
    const note = screen.getByRole('textbox', { name: /Note/ })
    const body = screen.getByRole('textbox', { name: /Body/ })
    expect(note.tagName).toBe('TEXTAREA')
    expect(body.tagName).toBe('TEXTAREA')
    // Keeps number and select inputs as they were.
    expect(screen.getByRole('spinbutton', { name: /Amount/ }).tagName).toBe('INPUT')
    expect(screen.getByRole('combobox', { name: /Mode/ })).toBeTruthy()
  })

  it('keeps a single-line cell on one logical line: Enter does not insert a newline, but a paragraph cell keeps it', () => {
    render(<Harness fields={mixed} />)
    expect(fireEvent.keyDown(screen.getByRole('textbox', { name: /Note/ }), { key: 'Enter' })).toBe(false)
    expect(fireEvent.keyDown(screen.getByRole('textbox', { name: /Body/ }), { key: 'Enter' })).toBe(true)
  })

  it('does not swallow Enter while an input method is composing', () => {
    render(<Harness fields={mixed} />)
    expect(fireEvent.keyDown(screen.getByRole('textbox', { name: /Note/ }), { key: 'Enter', isComposing: true })).toBe(true)
  })
})

describe('BatchRowsEditor column widths', () => {
  const widthOf = 300
  const original = Element.prototype.getBoundingClientRect
  beforeEach(() => {
    localStorage.clear()
    Element.prototype.getBoundingClientRect = function () {
      return { width: widthOf, height: 30, top: 0, left: 0, right: widthOf, bottom: 30, x: 0, y: 0, toJSON: () => ({}) }
    }
  })
  afterEach(() => {
    Element.prototype.getBoundingClientRect = original
  })

  it('gives every input column the shared resize handle', () => {
    render(<Harness widthsKey="rows-1" />)
    const handles = screen.getAllByRole('separator')
    expect(handles).toHaveLength(inputs.length)
  })

  it('persists a dragged width per workflow and offers to reset it', () => {
    const { unmount } = render(<Harness widthsKey="rows-1" />)
    expect(screen.queryByRole('button', { name: /batch\.editor\.resetWidths/ })).toBeNull()
    fireEvent.keyDown(screen.getAllByRole('separator')[0], { key: 'ArrowRight' })
    expect(JSON.parse(localStorage.getItem('rp.table.rows-1') || 'null')).toEqual({ widths: { symbol: 310 } })
    expect(localStorage.getItem('rp.table.rows-2')).toBeNull()
    unmount()

    render(<Harness widthsKey="rows-1" />)
    fireEvent.click(screen.getByRole('button', { name: /batch\.editor\.resetWidths/ }))
    expect(JSON.parse(localStorage.getItem('rp.table.rows-1') || 'null')).toEqual({ widths: {} })
    expect(screen.queryByRole('button', { name: /batch\.editor\.resetWidths/ })).toBeNull()
  })

  it('works without a storage key, and survives unusable storage', () => {
    const set = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('quota')
    })
    render(<Harness />)
    expect(() => fireEvent.keyDown(screen.getAllByRole('separator')[0], { key: 'ArrowRight' })).not.toThrow()
    set.mockRestore()
  })

  it('does not put a resize handle on the row number or the delete column', () => {
    render(<Harness widthsKey="rows-1" />)
    const header = screen.getAllByRole('columnheader')
    expect(within(header[0]).queryByRole('separator')).toBeNull()
    expect(within(header[header.length - 1]).queryByRole('separator')).toBeNull()
  })
})

describe('BatchRowsEditor review fixes', () => {
  // Typing into the last row of a full page adds one blank row on the next page; the cell being
  // typed in must stay on screen.
  it('stays on the page being typed on when the new blank row lands on the next one', () => {
    const filled = Array.from({ length: 19 }, (_, i) => ({ symbol: String(600000 + i), query: 'q', context: '' }))
    render(<Harness initial={[...filled, blankBatchRow(inputs)]} />)
    const boxes = stockBoxes()
    const last = boxes[boxes.length - 1]
    fireEvent.change(last, { target: { value: '6' } })
    expect(stockBoxes().some((box) => box.value === '6')).toBe(true)
  })

  // With every column dragged, spare space goes to an empty column, not back over the dragged ones.
  it('adds an empty spare-space column once every input column has a dragged width', () => {
    localStorage.setItem('rp.table.rows-9', JSON.stringify({ widths: { symbol: 200, query: 200, context: 200 } }))
    const { container } = render(<Harness widthsKey="rows-9" />)
    expect(container.querySelector('th.rp-grid-filler')).not.toBeNull()
    localStorage.removeItem('rp.table.rows-9')
  })

  it('has no spare-space column while any column still fills', () => {
    const { container } = render(<Harness />)
    expect(container.querySelector('th.rp-grid-filler')).toBeNull()
  })
})
