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
