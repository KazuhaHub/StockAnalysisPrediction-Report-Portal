import { fireEvent, render, screen, within } from '@testing-library/react'
import { Table } from 'antd'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useTableView } from './TableView'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

function Preview() {
  const view = useTableView('test', [
    { key: 'name', title: 'Name', width: 300, minWidth: 140 },
    { key: 'owner', title: 'Owner', width: 200, minWidth: 100 },
    { key: 'actions', title: 'Actions', width: 100, minWidth: 80, fixed: 'right' },
  ], ['name', 'actions'])
  return <><Table rowKey="name" dataSource={[]} columns={view.columns} components={view.components} />
    <div data-testid="columns">{view.columns.map((column) => column.key).join(',')}</div>
    <div data-testid="width">{view.scroll.x}</div>
    <div data-testid="fixed">{view.columns.find((column) => column.key === 'actions')?.fixed}</div>
    <button onClick={() => view.setHidden(['owner', 'actions'])}>Hide owner</button>
    <button onClick={view.reset}>Reset</button></>
}
// A desktop width: every min-width breakpoint matches. (The suite default matches none, i.e. a phone.)
const phoneMatchMedia = window.matchMedia
const desktop = (query: string) => ({ matches: query.includes('min-width'), media: query, onchange: null,
  addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false })
beforeEach(() => {
  localStorage.clear()
  Object.defineProperty(window, 'matchMedia', { configurable: true, value: desktop })
})
afterEach(() => { Object.defineProperty(window, 'matchMedia', { configurable: true, value: phoneMatchMedia }) })
it('fits selected columns, preserves required actions and restores a persisted selection', () => {
  const { unmount } = render(<Preview />)
  expect(screen.getByTestId('width').textContent).toBe('320')
  expect(screen.getByTestId('fixed').textContent).toBe('right')
  fireEvent.click(screen.getByText('Hide owner'))
  expect(screen.getByTestId('columns').textContent).toBe('name,actions')
  expect(screen.getByTestId('width').textContent).toBe('220')
  unmount()
  render(<Preview />)
  expect(screen.getByTestId('columns').textContent).toBe('name,actions')
  fireEvent.click(screen.getByText('Reset'))
  expect(screen.getByTestId('columns').textContent).toBe('name,owner,actions')
})
it('recovers from corrupt display preferences', () => {
  localStorage.setItem('rp.table.test', '{invalid')
  render(<Preview />)
  expect(screen.getByTestId('columns').textContent).toBe('name,owner,actions')
})
it('opens the column menu from the trailing ⋮, without letting essential columns disappear', async () => {
  render(<Preview />)
  fireEvent.click(screen.getByRole('button', { name: 'table.columnMenu' }))
  const owner = await screen.findByRole('checkbox', { name: 'Owner' })
  expect((screen.getByRole('checkbox', { name: 'Actions' }) as HTMLInputElement).disabled).toBe(true)
  fireEvent.click(owner)
  expect(screen.getByTestId('columns').textContent).toBe('name,actions')
  fireEvent.click(screen.getByRole('switch'))
  expect(screen.getByTestId('width').textContent).toBe('400')
})
it('opens the same menu on a right-click anywhere in the header', async () => {
  render(<Preview />)
  fireEvent.contextMenu(screen.getAllByRole('columnheader')[0])
  const menu = await screen.findByRole('group', { name: 'table.display' })
  expect(within(menu).getAllByRole('checkbox').map((box) => box.closest('label')?.textContent)).toEqual(['Name', 'Owner', 'Actions'])
})
it('lets headers be dragged to reorder, except a pinned column, and keeps them column headers', () => {
  render(<Preview />)
  const headers = screen.getAllByRole('columnheader')
  expect(headers.map((th) => th.getAttribute('aria-roledescription'))).toEqual(['table.movableColumn', 'table.movableColumn', null])
  expect(headers.every((th) => !th.hasAttribute('role') || th.getAttribute('role') === 'columnheader')).toBe(true)
  // The ⋮ sits only on the last header.
  expect(within(headers[2]).getByRole('button', { name: 'table.columnMenu' })).toBeTruthy()
  expect(within(headers[0]).queryByRole('button', { name: 'table.columnMenu' })).toBeNull()
})

function AdjustablePreview() {
  const view = useTableView('adjustable', [
    { key: 'name', title: 'Name', width: 300 },
    { key: 'owner', title: 'Owner', width: 200 },
    { key: 'actions', title: 'Actions', width: 100, fixed: 'right' },
  ], ['name', 'actions'])
  return <><div data-testid="order">{view.columns.map((column) => column.key).join(',')}</div>
    <div data-testid="owner-width">{view.columns.find((column) => column.key === 'owner')?.width}</div>
    <button onClick={() => view.resizeColumn('owner', 180)}>Resize owner</button>
    <button onClick={() => view.moveColumn('actions', 'name')}>Move actions</button>
    <button onClick={view.reset}>Reset layout</button></>
}
it('remembers resized widths and reordered essential columns, with a complete reset', () => {
  const { unmount } = render(<AdjustablePreview />)
  fireEvent.click(screen.getByText('Resize owner'))
  expect(screen.getByTestId('owner-width').textContent).toBe('180')
  fireEvent.click(screen.getByText('Move actions'))
  expect(screen.getByTestId('order').textContent).toBe('actions,name,owner')
  unmount()
  render(<AdjustablePreview />)
  expect(screen.getByTestId('owner-width').textContent).toBe('180')
  expect(screen.getByTestId('order').textContent).toBe('actions,name,owner')
  fireEvent.click(screen.getByText('Reset layout'))
  expect(screen.getByTestId('order').textContent).toBe('name,owner,actions')
  expect(screen.getByTestId('owner-width').textContent).toContain('%')
})

it('keeps the menu open while unticking the column that holds the ⋮', async () => {
  function Unpinned() {
    const view = useTableView('unpinned', [{ key: 'name', title: 'Name', width: 300 }, { key: 'owner', title: 'Owner', width: 200 }], ['name'])
    return <Table rowKey="name" dataSource={[]} columns={view.columns} components={view.components} />
  }
  render(<Unpinned />)
  fireEvent.click(screen.getByRole('button', { name: 'table.columnMenu' }))
  fireEvent.click(await screen.findByRole('checkbox', { name: 'Owner' }))
  // The ⋮ moved to Name, and the menu came with it.
  expect(within(screen.getAllByRole('columnheader')[0]).getByRole('button', { name: 'table.columnMenu' })).toBeTruthy()
  expect(screen.getByRole('checkbox', { name: 'Owner' })).toBeTruthy()
})
it('keeps clicks and keys in the menu from reaching the header that opened it', async () => {
  const sorted = vi.fn()
  function Sortable() {
    const view = useTableView('sortable', [{ key: 'name', title: 'Name', width: 300, sorter: true, onHeaderCell: () => ({ onClick: sorted }) }], [])
    return <Table rowKey="name" dataSource={[]} columns={view.columns} components={view.components} />
  }
  render(<Sortable />)
  fireEvent.click(screen.getByRole('button', { name: 'table.columnMenu' }))
  const fit = await screen.findByRole('switch')
  fireEvent.click(fit)
  fireEvent.keyDown(fit, { key: ' ', code: 'Space' })
  expect(sorted).not.toHaveBeenCalled()
})
it('puts a column the saved order has never seen where the source has it, not after the pinned one', () => {
  localStorage.setItem('rp.table.test', JSON.stringify({ hidden: [], fit: true, widths: {}, order: ['name', 'actions'] }))
  render(<Preview />)
  expect(screen.getByTestId('columns').textContent).toBe('name,owner,actions')
  expect(screen.getByTestId('fixed').textContent).toBe('right')
})
it('shows every column on a phone, where the cards have no header to bring a hidden one back from', () => {
  localStorage.setItem('rp.table.test', JSON.stringify({ hidden: ['owner'], fit: true, widths: {}, order: [] }))
  Object.defineProperty(window, 'matchMedia', { configurable: true, value: phoneMatchMedia })
  render(<Preview />)
  expect(screen.getByTestId('columns').textContent).toBe('name,owner,actions')
})

function FlooredPreview() {
  const view = useTableView('floored', [
    { key: 'name', title: 'Name', width: 300, minWidth: 140 },
    { key: 'owner', title: 'Owner', width: 200, minWidth: 120 },
    { key: 'actions', title: 'Actions', width: 100, minWidth: 80, fixed: 'right' },
  ], ['name', 'actions'])
  const width = (key: string) => view.columns.find((column) => column.key === key)?.width
  return <><Table rowKey="name" dataSource={[]} columns={view.columns} components={view.components} />
    <div data-testid="widths">{`${width('name')},${width('owner')}`}</div>
    <button onClick={() => view.resizeColumn('owner', 20)}>Shrink owner</button>
    <button onClick={() => view.resizeColumn('name', 900, true, { name: 300, owner: 200, actions: 100 })}>Widen name</button></>
}
it('holds a dragged column, and the neighbour that gives way, at their own minimum widths', () => {
  render(<FlooredPreview />)
  fireEvent.click(screen.getByText('Shrink owner'))
  expect(screen.getByTestId('widths').textContent?.split(',')[1]).toBe('120')
  fireEvent.click(screen.getByText('Widen name'))
  expect(screen.getByTestId('widths').textContent).toBe('900,120')
})
it('tells the resize handle each column’s own minimum', () => {
  render(<FlooredPreview />)
  const handles = screen.getAllByRole('separator')
  expect(handles.map((handle) => handle.getAttribute('aria-valuemin'))).toEqual(['140', '120', '80'])
})
it('resizes from the keyboard and still resizes when storage refuses to save', () => {
  const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota') })
  try {
    render(<FlooredPreview />)
    const [name] = screen.getAllByRole('separator')
    fireEvent.keyDown(name, { key: 'ArrowRight' })
    // jsdom measures nothing, so the handle steps from its 96px default, and the result is held at Name's 140 minimum.
    expect(screen.getByTestId('widths').textContent?.split(',')[0]).toBe('140')
  } finally { setItem.mockRestore() }
})

it('shows a width stored below a column’s minimum at the minimum, so shrinking never widens it', () => {
  localStorage.setItem('rp.table.floored', JSON.stringify({ hidden: [], fit: false, widths: { name: 300, owner: 70, actions: 100 }, order: [] }))
  render(<FlooredPreview />)
  expect(screen.getByTestId('widths').textContent).toBe('300,120')
  fireEvent.click(screen.getByText('Shrink owner'))
  expect(screen.getByTestId('widths').textContent).toBe('300,120')
})
it('lets a column narrower than the shared floor keep its own minimum', () => {
  function Narrow() {
    const view = useTableView('narrow', [
      { key: 'grip', title: 'Grip', width: 44, minWidth: 44 },
      { key: 'name', title: 'Name', width: 300, minWidth: 140 },
    ], ['grip', 'name'])
    return <><Table rowKey="name" dataSource={[]} columns={view.columns} components={view.components} />
      <div data-testid="grip">{view.columns[0].width}</div>
      <button onClick={() => view.resizeColumn('grip', 10)}>Shrink grip</button></>
  }
  render(<Narrow />)
  expect(screen.getAllByRole('separator')[0].getAttribute('aria-valuemin')).toBe('44')
  fireEvent.click(screen.getByText('Shrink grip'))
  expect(screen.getByTestId('grip').textContent).toBe('44')
})
