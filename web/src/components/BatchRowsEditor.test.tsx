import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'

import BatchRowsEditor from './BatchRowsEditor'
import type { PluginInput } from '../api/types'
import { blankBatchRow, type BatchDraftRow } from '../lib/batchRows'
import { findFailure } from '../test/notifications'

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

// The default jsdom viewport answers no media query, which antd reads as a phone: the grid then shows
// cards and keeps tap-to-edit. These blocks switch to a desktop-wide viewport for the keyboard model.
function useDesktopViewport() {
  const real = window.matchMedia
  beforeEach(() => {
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: (query: string) => ({
        matches: /min-width|hover: hover/.test(query),
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }),
    })
  })
  afterEach(() => {
    Object.defineProperty(window, 'matchMedia', { configurable: true, value: real })
  })
}

const cellEl = (row: number, col: number) =>
  document.querySelector(`[data-grid-cell="${row}:${col}"] textarea, [data-grid-cell="${row}:${col}"] input`) as HTMLTextAreaElement
const cellBox = (row: number, col: number) => document.querySelector(`[data-grid-cell="${row}:${col}"]`) as HTMLElement
const filled = (symbol: string, query = '', context = ''): BatchDraftRow => ({ symbol, query, context })
const twoRows = () => [filled('a', 'q1'), filled('b', 'q2')]
const inGridCell = () => !!document.activeElement?.closest('[data-grid-cell]')

describe('BatchRowsEditor keyboard model', () => {
  useDesktopViewport()

  it('gives the table grid semantics and marks the active cell selected', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    expect(screen.getByRole('grid')).toBeTruthy()
    await user.click(cellEl(1, 1))
    const selected = screen.getAllByRole('gridcell').filter((c) => c.getAttribute('aria-selected') === 'true')
    expect(selected).toHaveLength(1)
    expect(selected[0].contains(cellEl(1, 1))).toBe(true)
    expect(cellEl(1, 1).getAttribute('aria-description')).toBe('batch.editor.cellModeSelect')
  })

  it('puts only the active cell (or the first one) in the tab order', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    const stops = () => [...document.querySelectorAll('[data-grid-cell]')].filter((c) => c.querySelector('[tabindex="0"]'))
    expect(stops()).toHaveLength(1)
    expect(stops()[0]).toBe(cellBox(0, 0))
    await user.click(cellEl(1, 2))
    expect(stops()).toEqual([cellBox(1, 2)])
  })

  it('selects on click, without a caret, and edits on double-click with the caret at the end', async () => {
    const user = userEvent.setup()
    render(<Harness initial={[filled('abc')]} />)
    await user.click(cellEl(0, 0))
    expect(document.activeElement).toBe(cellEl(0, 0))
    expect([cellEl(0, 0).selectionStart, cellEl(0, 0).selectionEnd]).toEqual([0, 3])
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--selecting')
    await user.dblClick(cellEl(0, 0))
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--editing')
    expect([cellEl(0, 0).selectionStart, cellEl(0, 0).selectionEnd]).toEqual([3, 3])
  })

  it('moves the active cell with the arrow keys and stops at the edges and the trailing blank row', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{ArrowUp}{ArrowLeft}')
    expect(document.activeElement).toBe(cellEl(0, 0))
    await user.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(cellEl(0, 1))
    await user.keyboard('{ArrowDown}{ArrowDown}')
    // Row index 2 is the trailing blank row.
    expect(document.activeElement).toBe(cellEl(2, 1))
    await user.keyboard('{ArrowDown}{ArrowRight}{ArrowRight}')
    expect(document.activeElement).toBe(cellEl(2, 2))
    await user.keyboard('{ArrowUp}{ArrowLeft}')
    expect(document.activeElement).toBe(cellEl(1, 1))
  })

  it('replaces the content when typing in select mode and enters edit mode', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('xy')
    expect(cellEl(0, 0).value).toBe('xy')
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--editing')
    expect(cellEl(0, 0).getAttribute('aria-description')).toBe('batch.editor.cellModeEdit')
  })

  it('commits on Enter and moves down', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('xy{Enter}')
    expect(cellEl(0, 0).value).toBe('xy')
    expect(document.activeElement).toBe(cellEl(1, 0))
    expect(cellBox(1, 0).className).toContain('rp-grid-cell--selecting')
  })

  it('reverts to the value from before the edit on Escape and stays on the cell in select mode', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('zz{Escape}')
    expect(cellEl(0, 0).value).toBe('a')
    expect(document.activeElement).toBe(cellEl(0, 0))
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--selecting')
    // Escape in select mode does nothing.
    await user.keyboard('{Escape}')
    expect(cellEl(0, 0).value).toBe('a')
  })

  it('opens an explicit edit on F2 or Enter, where the arrow keys move the caret and not the cell', async () => {
    const user = userEvent.setup()
    render(<Harness initial={[filled('abc')]} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{F2}{ArrowLeft}{ArrowLeft}')
    expect(document.activeElement).toBe(cellEl(0, 0))
    expect(cellEl(0, 0).selectionStart).toBe(1)
    await user.keyboard('X')
    expect(cellEl(0, 0).value).toBe('aXbc')
    // Escape undid the edit; Enter opens a new one with the caret at the end of the restored text.
    await user.keyboard('{Escape}{Enter}')
    expect(cellEl(0, 0).value).toBe('abc')
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--editing')
    expect(cellEl(0, 0).selectionStart).toBe(3)
  })

  it('commits and moves on an arrow key after a typed edit, as Sheets does', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('x{ArrowRight}')
    expect(cellEl(0, 0).value).toBe('x')
    expect(document.activeElement).toBe(cellEl(0, 1))
    await user.keyboard('y{ArrowDown}')
    expect(cellEl(0, 1).value).toBe('y')
    expect(document.activeElement).toBe(cellEl(1, 1))
  })

  it('wraps Tab to the next row and Shift+Tab to the previous one', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 2))
    await user.tab()
    expect(document.activeElement).toBe(cellEl(1, 0))
    await user.tab({ shift: true })
    expect(document.activeElement).toBe(cellEl(0, 2))
    await user.tab({ shift: true })
    expect(document.activeElement).toBe(cellEl(0, 1))
  })

  it('commits an edit when Tab moves on', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('x')
    await user.tab()
    expect(cellEl(0, 0).value).toBe('x')
    expect(document.activeElement).toBe(cellEl(0, 1))
    expect(cellBox(0, 0).className).not.toContain('rp-grid-cell--editing')
  })

  it('lets Tab leave the grid at the last cell and Shift+Tab at the first, landing outside the body', async () => {
    const user = userEvent.setup()
    render(
      <>
        <Harness initial={twoRows()} />
        <button type="button">after the grid</button>
      </>,
    )
    await user.click(cellEl(0, 0))
    await user.tab({ shift: true })
    // The element before the body is the header's last control, never a handle or a button in a row.
    expect(document.activeElement?.closest('.rp-table-column-title')).not.toBeNull()
    expect(document.activeElement?.closest('tbody')).toBeNull()
    // The trailing blank row's last column is the end of the grid; the next stop is the control after it.
    await user.click(cellEl(2, 2))
    await user.tab()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'after the grid' }))
  })

  it('enters the grid once from outside and lands on a cell', async () => {
    render(<Harness initial={twoRows()} />)
    act(() => cellEl(0, 0).focus())
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--selecting')
    expect(document.activeElement).toBe(cellEl(0, 0))
  })

  it('clears the cell on Delete and Backspace', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{Delete}')
    expect(cellEl(0, 0).value).toBe('')
    await user.click(cellEl(1, 1))
    await user.keyboard('{Backspace}')
    expect(cellEl(1, 1).value).toBe('')
  })

  it('pastes a block at the active cell and a single value over the cell', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(1, 1))
    fireEvent.paste(cellEl(1, 1), { clipboardData: { getData: () => 'x\ty\r\nz\tw\r\n' } })
    expect(cellEl(1, 1).value).toBe('x')
    expect(cellEl(1, 2).value).toBe('y')
    expect(cellEl(2, 1).value).toBe('z')
    expect(cellEl(2, 2).value).toBe('w')
    await user.click(cellEl(0, 0))
    fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => 'single' } })
    expect(cellEl(0, 0).value).toBe('single')
  })

  it('copies the selected cell value', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 1))
    const data: Record<string, string> = {}
    fireEvent.copy(cellEl(0, 1), { clipboardData: { setData: (type: string, value: string) => { data[type] = value } } })
    expect(data['text/plain']).toBe('q1')
  })

  it('keeps an input method composition in the cell and leaves its keys alone', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    const box = cellEl(0, 0)
    fireEvent.compositionStart(box)
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--editing')
    fireEvent.change(box, { target: { value: 'ni' } })
    // A candidate's Enter and arrows belong to the input method.
    expect(fireEvent.keyDown(box, { key: 'Enter', isComposing: true })).toBe(true)
    expect(fireEvent.keyDown(box, { key: 'ArrowDown', isComposing: true })).toBe(true)
    fireEvent.change(box, { target: { value: '你' } })
    fireEvent.compositionEnd(box)
    expect(cellEl(0, 0).value).toBe('你')
    expect(document.activeElement).toBe(cellEl(0, 0))
  })

  it('inserts a newline with Alt+Enter in a paragraph cell only', async () => {
    const user = userEvent.setup()
    const fields: PluginInput[] = [{ key: 'note', label: 'Note' }, { key: 'body', label: 'Body', type: 'paragraph' }]
    render(<Harness fields={fields} initial={[{ note: 'one', body: 'two' }]} />)
    await user.click(cellEl(0, 1))
    await user.keyboard('{F2}{Alt>}{Enter}{/Alt}')
    expect(cellEl(0, 1).value).toBe('two\n')
    await user.keyboard('{Escape}')
    await user.click(cellEl(0, 0))
    await user.keyboard('{F2}{Alt>}{Enter}{/Alt}{Control>}{Enter}{/Control}')
    expect(cellEl(0, 0).value).toBe('one')
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--editing')
  })

  it('starts a number cell only on a digit, minus or dot', async () => {
    const user = userEvent.setup()
    const fields: PluginInput[] = [{ key: 'amount', label: 'Amount', type: 'number' }]
    render(<Harness fields={fields} initial={[{ amount: '5' }]} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('x')
    expect(cellEl(0, 0).value).toBe('5')
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--selecting')
    await user.keyboard('7')
    expect(cellEl(0, 0).value).toBe('7')
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--editing')
  })

  it('opens a select cell for editing on Enter and closes it on Escape', async () => {
    const user = userEvent.setup()
    const fields: PluginInput[] = [{ key: 'mode', label: 'Mode', type: 'select', options: ['a', 'b'] }]
    render(<Harness fields={fields} initial={[{ mode: 'a' }]} />)
    await user.click(cellEl(0, 0))
    expect(cellEl(0, 0).getAttribute('aria-expanded')).toBe('false')
    await user.keyboard('{Enter}')
    expect(cellEl(0, 0).getAttribute('aria-expanded')).toBe('true')
    await user.keyboard('{Escape}')
    expect(cellEl(0, 0).getAttribute('aria-expanded')).toBe('false')
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--selecting')
  })

  it('follows the active cell onto the next page', async () => {
    const user = userEvent.setup()
    const many = Array.from({ length: 25 }, (_, i) => filled(`s${i}`, `q${i}`))
    render(<Harness initial={many} />)
    expect(cellEl(19, 0)).toBeTruthy()
    expect(cellEl(20, 0)).toBeNull()
    await user.click(cellEl(19, 0))
    await user.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(cellEl(20, 0))
    expect(cellEl(20, 0).value).toBe('s20')
    await user.keyboard('{ArrowUp}')
    expect(document.activeElement).toBe(cellEl(19, 0))
  })

  it('keeps the active cell when a delete button is pressed', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(1, 0))
    fireEvent.click(screen.getByRole('button', { name: /deleteRowN.*"row":1/ }))
    expect(cellBox(0, 0)).toBeTruthy()
    expect(cellEl(0, 0).value).toBe('b')
  })
})

describe('BatchRowsEditor on a phone', () => {
  it('keeps tap-to-edit: no grid semantics, no key navigation, no row handles', async () => {
    render(<Harness initial={twoRows()} />)
    expect(screen.queryByRole('grid')).toBeNull()
    expect(screen.queryAllByRole('separator', { name: /batch\.editor\.resizeRow/ })).toHaveLength(0)
    const first = cellEl(0, 0)
    first.focus()
    expect(fireEvent.keyDown(first, { key: 'ArrowDown' })).toBe(true)
    expect(document.activeElement).toBe(first)
    expect(cellBox(0, 0).className).not.toContain('rp-grid-cell--selecting')
    // Enter still cannot leave a newline in a single-line value.
    expect(fireEvent.keyDown(first, { key: 'Enter' })).toBe(false)
  })
})

describe('BatchRowsEditor row heights', () => {
  useDesktopViewport()
  const handle = (row: number) => screen.getByRole('separator', { name: new RegExp(`resizeRow.*"row":${row}\\b`) })

  it('resizes a row by dragging the handle on its number cell, within one to ten lines', () => {
    render(<Harness initial={twoRows()} />)
    const h = handle(1)
    expect(h.getAttribute('aria-valuenow')).toBe('32')
    fireEvent.pointerDown(h, { pointerId: 1, clientY: 100, button: 0 })
    fireEvent.pointerMove(h, { pointerId: 1, clientY: 144 })
    expect(h.getAttribute('aria-valuenow')).toBe('76')
    expect(cellEl(0, 0).style.height).toBe('76px')
    // The neighbouring row is untouched.
    expect(cellEl(1, 0).style.height).not.toBe('76px')
    fireEvent.pointerMove(h, { pointerId: 1, clientY: 5000 })
    expect(h.getAttribute('aria-valuenow')).toBe('230')
    fireEvent.pointerMove(h, { pointerId: 1, clientY: -5000 })
    expect(h.getAttribute('aria-valuenow')).toBe('32')
    fireEvent.pointerUp(h, { pointerId: 1 })
  })

  it('cancels a drag with Escape', () => {
    render(<Harness initial={twoRows()} />)
    const h = handle(1)
    fireEvent.pointerDown(h, { pointerId: 1, clientY: 100, button: 0 })
    fireEvent.pointerMove(h, { pointerId: 1, clientY: 150 })
    fireEvent.keyDown(h, { key: 'Escape' })
    expect(cellBox(0, 0).dataset.clamp).toBe('auto')
  })

  it('resizes from the keyboard with Up and Down, five lines with Shift', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    handle(2).focus()
    await user.keyboard('{ArrowDown}')
    expect(handle(2).getAttribute('aria-valuenow')).toBe('54')
    await user.keyboard('{Shift>}{ArrowDown}{/Shift}')
    expect(handle(2).getAttribute('aria-valuenow')).toBe('164')
    await user.keyboard('{ArrowUp}')
    expect(handle(2).getAttribute('aria-valuenow')).toBe('142')
    // The handle's keys are its own: the grid's active cell does not move.
    expect(inGridCell()).toBe(false)
  })

  it('lifts the three-line clamp for a resized row only', () => {
    render(<Harness initial={twoRows()} />)
    expect(cellBox(0, 0).dataset.clamp).toBe('auto')
    fireEvent.keyDown(handle(1), { key: 'ArrowDown' })
    expect(cellBox(0, 0).dataset.clamp).toBe('row')
    expect(cellBox(0, 1).dataset.clamp).toBe('row')
    expect(cellBox(1, 0).dataset.clamp).toBe('auto')
  })

  it('expands the editor of a cell being edited without changing the stored row height', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    fireEvent.keyDown(handle(1), { key: 'ArrowDown' })
    await user.dblClick(cellEl(0, 0))
    expect(cellBox(0, 0).dataset.clamp).toBe('expanded')
    expect(cellBox(0, 0).style.getPropertyValue('--rp-row-h')).toBe('54px')
    await user.keyboard('{Escape}')
    expect(cellBox(0, 0).dataset.clamp).toBe('row')
    expect(handle(1).getAttribute('aria-valuenow')).toBe('54')
    // An automatic row expands too, and falls back.
    await user.dblClick(cellEl(1, 0))
    expect(cellBox(1, 0).dataset.clamp).toBe('expanded')
    await user.keyboard('{Escape}')
    expect(cellBox(1, 0).dataset.clamp).toBe('auto')
  })

  it('drops a deleted row height and shifts the ones below it up', () => {
    render(<Harness initial={[filled('a'), filled('b'), filled('c')]} />)
    fireEvent.keyDown(handle(2), { key: 'ArrowDown' })
    fireEvent.click(screen.getByRole('button', { name: /deleteRowN.*"row":1/ }))
    expect(cellEl(0, 0).value).toBe('b')
    expect(cellBox(0, 0).dataset.clamp).toBe('row')
    expect(cellBox(1, 0).dataset.clamp).toBe('auto')
  })

  it('keeps heights through a paste, which only overwrites or appends rows', () => {
    render(<Harness initial={twoRows()} />)
    fireEvent.keyDown(handle(2), { key: 'ArrowDown' })
    fireEvent.paste(cellEl(2, 0), { clipboardData: { getData: () => 'x\ty\r\nz\tw\r\n' } })
    expect(cellBox(1, 0).dataset.clamp).toBe('row')
    expect(cellBox(0, 0).dataset.clamp).toBe('auto')
  })
})


function WithNeighbours() {
  return (
    <>
      <button type="button">before the grid</button>
      <Harness initial={twoRows()} />
      <button type="button">after the grid</button>
    </>
  )
}

function mockClipboard() {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
  return writeText
}

describe('BatchRowsEditor clipboard behaviour', () => {
  useDesktopViewport()

  it('pastes natively at the caret in edit mode, turning newlines into spaces in a single-line cell', async () => {
    const user = userEvent.setup()
    render(<Harness initial={[filled('abc', 'q1')]} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{F2}')
    expect(fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => 'x\ny' } })).toBe(false)
    expect(cellEl(0, 0).value).toBe('abcx y')
    // It did not spread over the other cells as a block.
    expect(cellEl(0, 1).value).toBe('q1')
  })

  it('leaves a plain paste in a paragraph cell to the browser while editing', async () => {
    const user = userEvent.setup()
    const fields: PluginInput[] = [{ key: 'body', type: 'paragraph' }]
    render(<Harness fields={fields} initial={[{ body: 'abc' }]} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{F2}')
    expect(fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => 'x\ny' } })).toBe(true)
    expect(cellEl(0, 0).value).toBe('abc')
  })

  it('ignores an empty clipboard and a lone line break in select mode', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => '' } })
    fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => '\n' } })
    expect(cellEl(0, 0).value).toBe('a')
    expect(document.querySelectorAll('[data-grid-cell$=":0"]')).toHaveLength(3)
  })

  it('copies in the Excel format so a paragraph cell round-trips through a paste', async () => {
    const user = userEvent.setup()
    const fields: PluginInput[] = [{ key: 'one', type: 'paragraph' }, { key: 'two', type: 'paragraph' }]
    render(<Harness fields={fields} initial={[{ one: 'a\nb', two: '' }]} />)
    await user.click(cellEl(0, 0))
    const data: Record<string, string> = {}
    fireEvent.copy(cellEl(0, 0), { clipboardData: { setData: (type: string, value: string) => { data[type] = value } } })
    expect(data['text/plain']).toBe('"a\nb"')
    await user.click(cellEl(0, 1))
    fireEvent.paste(cellEl(0, 1), { clipboardData: { getData: () => data['text/plain'] } })
    expect(cellEl(0, 1).value).toBe('a\nb')
  })

  it('writes the cell to the clipboard on Ctrl+C and Ctrl+X so Safari works without a selection', async () => {
    const user = userEvent.setup()
    const writeText = mockClipboard()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 1))
    await user.keyboard('{Control>}c{/Control}')
    expect(writeText).toHaveBeenLastCalledWith('q1')
    expect(cellEl(0, 1).value).toBe('q1')
    await user.keyboard('{Meta>}x{/Meta}')
    expect(writeText).toHaveBeenLastCalledWith('q1')
    expect(cellEl(0, 1).value).toBe('')
  })

  it('only puts a number in a number cell and an option in a select cell', async () => {
    const user = userEvent.setup()
    const fields: PluginInput[] = [{ key: 'amount', type: 'number' }, { key: 'mode', type: 'select', options: ['a', 'b'] }]
    render(<Harness fields={fields} initial={[{ amount: '5', mode: 'a' }]} />)
    await user.click(cellEl(0, 0))
    fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => 'abc' } })
    expect(cellEl(0, 0).value).toBe('5')
    fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => '42' } })
    expect(cellEl(0, 0).value).toBe('42')
    await user.click(cellEl(0, 1))
    fireEvent.paste(cellEl(0, 1), { clipboardData: { getData: () => 'zzz' } })
    fireEvent.paste(cellEl(0, 1), { clipboardData: { getData: () => 'x\ty' } })
    expect(document.querySelector('[data-grid-cell="0:1"] .ant-select-content')?.textContent).toBe('a')
  })
})

describe('BatchRowsEditor undo and redo', () => {
  useDesktopViewport()
  const undo = '{Control>}z{/Control}'
  const redo = '{Control>}{Shift>}z{/Shift}{/Control}'

  it('undoes and redoes a clear in select mode and restores the active cell', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(1, 1))
    await user.keyboard('{Delete}')
    await user.click(cellEl(0, 0))
    await user.keyboard(undo)
    expect(cellEl(1, 1).value).toBe('q2')
    expect(cellBox(1, 1).className).toContain('rp-grid-cell--active')
    await user.keyboard(redo)
    expect(cellEl(1, 1).value).toBe('')
    await user.keyboard(undo + '{Control>}y{/Control}')
    expect(cellEl(1, 1).value).toBe('')
  })

  it('undoes a committed edit, a paste and a row delete', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('zz{Enter}')
    expect(cellEl(0, 0).value).toBe('zz')
    await user.keyboard(undo)
    expect(cellEl(0, 0).value).toBe('a')
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--active')
    fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => 'p\tq\r\nr\ts\r\n' } })
    expect(cellEl(1, 1).value).toBe('s')
    await user.keyboard(undo)
    expect(cellEl(0, 0).value).toBe('a')
    expect(cellEl(1, 1).value).toBe('q2')
    fireEvent.click(screen.getByRole('button', { name: /deleteRowN.*"row":1/ }))
    expect(cellEl(0, 0).value).toBe('b')
    await user.click(cellEl(0, 0))
    await user.keyboard(undo)
    expect(cellEl(0, 0).value).toBe('a')
  })

  it('leaves undo to the browser while a cell is being edited', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{Delete}{Enter}{ArrowUp}{F2}')
    expect(fireEvent.keyDown(cellEl(0, 0), { key: 'z', ctrlKey: true })).toBe(true)
    expect(cellEl(0, 0).value).toBe('')
  })

  it('keeps a bounded history', () => {
    render(<Harness initial={twoRows()} />)
    act(() => cellEl(0, 0).focus())
    for (let i = 1; i <= 55; i++) fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => `v${i}` } })
    for (let i = 0; i < 70; i++) fireEvent.keyDown(cellEl(0, 0), { key: 'z', ctrlKey: true })
    expect(cellEl(0, 0).value).toBe('v5')
  })
})

describe('BatchRowsEditor roving tabindex and leaving the grid', () => {
  useDesktopViewport()

  it('takes row buttons and resize handles out of the tab order', () => {
    render(<Harness initial={twoRows()} />)
    for (const button of screen.getAllByRole('button', { name: /batch\.editor\.deleteRowN/ })) expect(button.tabIndex).toBe(-1)
    for (const handle of screen.getAllByRole('separator', { name: /batch\.editor\.resizeRow/ })) expect(handle.tabIndex).toBe(-1)
  })

  it('deletes the active row with Ctrl+Alt+minus, but never the trailing blank row', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{Control>}{Alt>}-{/Alt}{/Control}')
    expect(cellEl(0, 0).value).toBe('b')
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--active')
    await user.click(cellEl(1, 0))
    await user.keyboard('{Control>}{Alt>}-{/Alt}{/Control}')
    expect(document.querySelectorAll('[data-grid-cell$=":0"]')).toHaveLength(2)
  })

  it('resizes the active row with Alt+Shift+Up and Down', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{Alt>}{Shift>}{ArrowDown}{/Shift}{/Alt}')
    expect(cellBox(0, 0).dataset.clamp).toBe('row')
    expect(screen.getByRole('separator', { name: /resizeRow.*"row":1\b/ }).getAttribute('aria-valuenow')).toBe('54')
    await user.keyboard('{Alt>}{Shift>}{ArrowUp}{/Shift}{/Alt}')
    expect(screen.getByRole('separator', { name: /resizeRow.*"row":1\b/ }).getAttribute('aria-valuenow')).toBe('32')
    // The active cell did not move.
    expect(document.activeElement).toBe(cellEl(0, 0))
  })

  it('spells the shortcuts out for everyone', () => {
    render(<Harness initial={twoRows()} />)
    expect(screen.getByText(/batch\.editor\.keyHint/)).toBeTruthy()
    expect(screen.getByRole('grid').getAttribute('aria-description')).toBe('batch.editor.keyHint')
  })

  it('releases focus to the grid on Escape, then Tab and Shift+Tab leave it either way', async () => {
    const user = userEvent.setup()
    render(<WithNeighbours />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{Escape}')
    const wrapper = document.querySelector('.rp-batch-grid') as HTMLElement
    expect(document.activeElement).toBe(wrapper)
    await user.tab()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'after the grid' }))
  })

  it('goes back before the grid on Shift+Tab from the released wrapper', async () => {
    const user = userEvent.setup()
    render(<WithNeighbours />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{Escape}')
    await user.tab({ shift: true })
    expect(document.querySelector('.rp-batch-grid')?.contains(document.activeElement)).toBe(false)
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'batch.editor.clear' }))
  })

  it('re-enters at the active cell on Enter or ArrowDown from the wrapper', async () => {
    const user = userEvent.setup()
    render(<WithNeighbours />)
    await user.click(cellEl(1, 1))
    await user.keyboard('{Escape}{Enter}')
    expect(document.activeElement).toBe(cellEl(1, 1))
    await user.keyboard('{Escape}{ArrowDown}')
    expect(document.activeElement).toBe(cellEl(1, 1))
  })
})

describe('BatchRowsEditor focus and bookkeeping', () => {
  useDesktopViewport()

  it('keeps focus in the grid when a paste moves it to another page', () => {
    render(<Harness />)
    act(() => cellEl(0, 0).focus())
    const block = Array.from({ length: 25 }, (_row, index) => `c${index + 1}\tq${index + 1}`).join('\n')
    fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => block } })
    expect(document.activeElement).not.toBe(document.body)
    expect(document.activeElement).toBe(cellEl(24, 0))
    expect(cellEl(24, 0).value).toBe('c25')
  })

  it('moves the active cell up when a row above it is deleted, and keeps the index when it is the active row', async () => {
    const user = userEvent.setup()
    render(<Harness initial={[filled('a'), filled('b'), filled('c')]} />)
    await user.click(cellEl(2, 0))
    fireEvent.click(screen.getByRole('button', { name: /deleteRowN.*"row":2\b/ }))
    expect(cellBox(1, 0).className).toContain('rp-grid-cell--active')
    expect(cellEl(1, 0).value).toBe('c')
    fireEvent.click(screen.getByRole('button', { name: /deleteRowN.*"row":2\b/ }))
    expect(cellBox(1, 0).className).toContain('rp-grid-cell--active')
    expect(cellEl(1, 0).value).toBe('')
  })

  it('drops the height of a trailing blank row that collapses away', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    // Row 3 is the trailing blank row.
    fireEvent.keyDown(screen.getByRole('separator', { name: /resizeRow.*"row":3\b/ }), { key: 'ArrowDown' })
    expect(cellBox(2, 0).dataset.clamp).toBe('row')
    await user.click(cellEl(1, 0))
    await user.keyboard('{Delete}{Backspace}')
    await user.click(cellEl(1, 1))
    await user.keyboard('{Delete}')
    // Row 2 is now blank too, so the blank row below it collapsed into it.
    expect(document.querySelectorAll('[data-grid-cell$=":0"]')).toHaveLength(2)
    await user.click(cellEl(1, 0))
    await user.keyboard('n')
    expect(document.querySelectorAll('[data-grid-cell$=":0"]')).toHaveLength(3)
    expect(cellBox(2, 0).dataset.clamp).toBe('auto')
  })

  it('numbers rows and columns for assistive technology, header row included', () => {
    render(<Harness initial={twoRows()} />)
    const grid = screen.getByRole('grid')
    expect(grid.getAttribute('aria-rowcount')).toBe('4')
    expect(grid.getAttribute('aria-colcount')).toBe('5')
    const rows = [...grid.querySelectorAll('tbody > tr.ant-table-row')]
    expect(rows.map((row) => row.getAttribute('aria-rowindex'))).toEqual(['2', '3', '4'])
    expect(cellBox(0, 0).closest('td')?.getAttribute('aria-colindex')).toBe('2')
    expect(cellBox(1, 2).closest('td')?.getAttribute('aria-colindex')).toBe('4')
    // The editors carry the row number and the column title in their names.
    expect(cellEl(1, 1).getAttribute('aria-label')).toBe('batch.editor.cellLabel:{"row":2,"field":"Research query"}')
  })

  it('reports the measured height of an automatic row and grows from it', async () => {
    const user = userEvent.setup()
    const height = vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(66)
    render(<Harness initial={twoRows()} />)
    const handle = screen.getByRole('separator', { name: /resizeRow.*"row":1\b/ })
    expect(handle.getAttribute('aria-valuenow')).toBe('66')
    handle.focus()
    await user.keyboard('{ArrowDown}')
    expect(handle.getAttribute('aria-valuenow')).toBe('88')
    height.mockRestore()
  })
})

describe('BatchRowsEditor pointer and width rule', () => {
  const real = window.matchMedia
  const mock = (answers: (query: string) => boolean) => {
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: (query: string) => ({
        matches: answers(query), media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
      }),
    })
  }
  afterEach(() => {
    Object.defineProperty(window, 'matchMedia', { configurable: true, value: real })
  })

  it('uses the Sheets model with a fine pointer on a wide viewport', () => {
    mock((q) => /min-width|hover: hover/.test(q))
    render(<Harness initial={twoRows()} />)
    expect(screen.getByRole('grid')).toBeTruthy()
  })

  it('keeps tap-to-edit inputs on a touch tablet, still laid out as the table', () => {
    mock((q) => /min-width/.test(q))
    const { container } = render(<Harness initial={twoRows()} />)
    expect(screen.queryByRole('grid')).toBeNull()
    expect(container.querySelector('.rp-card-table--cards')).toBeNull()
    expect(screen.queryAllByRole('separator', { name: /resizeRow/ })).toHaveLength(0)
    expect(cellBox(0, 0).className).not.toContain('selecting')
  })

  it('keeps tap-to-edit inputs on a narrow viewport even with a mouse', () => {
    mock((q) => /min-width|hover: hover|767/.test(q))
    render(<Harness initial={twoRows()} />)
    expect(screen.queryByRole('grid')).toBeNull()
  })
})


describe('BatchRowsEditor third-round fixes', () => {
  useDesktopViewport()

  it('does not delete a row on AltGr+key, which Windows reports as Ctrl+Alt, and still types the character', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    fireEvent.keyDown(cellEl(0, 0), { key: '-', code: 'Minus', ctrlKey: true, altKey: true, modifierAltGraph: true })
    expect(cellEl(0, 0).value).toBe('a')
    expect(cellEl(1, 0).value).toBe('b')
    fireEvent.keyDown(cellEl(0, 0), { key: '\\', ctrlKey: true, altKey: true, modifierAltGraph: true })
    expect(cellBox(0, 0).className).toContain('rp-grid-cell--editing')
  })

  it('still deletes the row on macOS Cmd+Option+minus', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    fireEvent.keyDown(cellEl(0, 0), { key: '\u2013', code: 'Minus', metaKey: true, altKey: true })
    expect(cellEl(0, 0).value).toBe('b')
  })

  it('strips one trailing line terminator from an edit-mode paste, in single-line and paragraph cells', async () => {
    const user = userEvent.setup()
    const fields: PluginInput[] = [{ key: 'one' }, { key: 'many', type: 'paragraph' }]
    render(<Harness fields={fields} initial={[{ one: 'x', many: 'p' }]} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{F2}')
    expect(fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => 'abc\r\n' } })).toBe(false)
    expect(cellEl(0, 0).value).toBe('xabc')
    await user.click(cellEl(0, 1))
    await user.keyboard('{F2}')
    expect(fireEvent.paste(cellEl(0, 1), { clipboardData: { getData: () => 'q\nr\n' } })).toBe(false)
    expect(cellEl(0, 1).value).toBe('pq\nr')
    // Only one terminator goes.
    fireEvent.paste(cellEl(0, 1), { clipboardData: { getData: () => 's\n\n' } })
    expect(cellEl(0, 1).value).toBe('pq\nrs\n')
  })

  it('clears a cut cell only after the clipboard accepted the text', async () => {
    const user = userEvent.setup()
    let resolve: () => void = () => {}
    const writeText = vi.fn(() => new Promise<void>((done) => { resolve = done }))
    render(<Harness initial={twoRows()} />)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    await user.click(cellEl(0, 1))
    await user.keyboard('{Control>}x{/Control}')
    expect(cellEl(0, 1).value).toBe('q1')
    await act(async () => resolve())
    expect(cellEl(0, 1).value).toBe('')
  })

  it('leaves the cell and says so when the clipboard refuses a cut or a copy', async () => {
    const user = userEvent.setup()
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    render(<Harness initial={twoRows()} />)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    await user.click(cellEl(0, 1))
    await user.keyboard('{Control>}x{/Control}')
    await findFailure('batch.editor.copyFailed')
    expect(cellEl(0, 1).value).toBe('q1')
    await user.keyboard('{Control>}c{/Control}')
    await findFailure('batch.editor.copyFailed')
    expect(cellEl(0, 1).value).toBe('q1')
  })

  it('tells the user once per paste how many cells did not fit their column', async () => {
    const user = userEvent.setup()
    const fields: PluginInput[] = [{ key: 'amount', type: 'number' }, { key: 'mode', type: 'select', options: ['a'] }]
    render(<Harness fields={fields} initial={[{ amount: '5', mode: 'a' }]} />)
    await user.click(cellEl(0, 0))
    fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => 'x\tzzz\r\n9\tbad\r\n' } })
    await findFailure('batch.editor.pasteSkipped:{"n":3}')
    expect(cellEl(0, 0).value).toBe('5')
    expect(cellEl(1, 0).value).toBe('9')
  })

  it('reports a paste in which every cell was refused, too', async () => {
    const user = userEvent.setup()
    const fields: PluginInput[] = [{ key: 'amount', type: 'number' }]
    render(<Harness fields={fields} initial={[{ amount: '5' }]} />)
    await user.click(cellEl(0, 0))
    fireEvent.paste(cellEl(0, 0), { clipboardData: { getData: () => 'abc' } })
    await findFailure('batch.editor.pasteSkipped:{"n":1}')
    expect(cellEl(0, 0).value).toBe('5')
  })

  it('keeps focus on the grid when Tab has nowhere to go after it', async () => {
    const user = userEvent.setup()
    render(<Harness initial={twoRows()} />)
    await user.click(cellEl(0, 0))
    await user.keyboard('{Escape}')
    const wrapper = document.querySelector('.rp-batch-grid')
    expect(document.activeElement).toBe(wrapper)
    await user.tab()
    expect(document.activeElement).toBe(wrapper)
  })
})
