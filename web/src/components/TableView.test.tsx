import { fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { useTableView } from './TableView'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

function Preview() {
  const view = useTableView('test', [
    { key: 'name', title: 'Name', width: 300, minWidth: 140 },
    { key: 'owner', title: 'Owner', width: 200, minWidth: 100 },
    { key: 'actions', title: 'Actions', width: 100, minWidth: 80, fixed: 'right' },
  ], ['name', 'actions'])
  return <>{view.controls}<div data-testid="columns">{view.columns.map((column) => column.key).join(',')}</div>
    <div data-testid="width">{view.scroll.x}</div>
    <div data-testid="fixed">{view.columns.find((column) => column.key === 'actions')?.fixed}</div>
    <button onClick={() => view.setHidden(['owner', 'actions'])}>Hide owner</button>
    <button onClick={view.reset}>Reset</button></>
}
beforeEach(() => { localStorage.clear() })
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
it('offers column selection and fit controls without allowing essential columns to disappear', async () => {
  render(<Preview />)
  fireEvent.click(screen.getByRole('button', { name: /table.display/ }))
  const owner = await screen.findByRole('checkbox', { name: 'Owner' })
  expect((screen.getByRole('checkbox', { name: 'Actions' }) as HTMLInputElement).disabled).toBe(true)
  fireEvent.click(owner)
  expect(screen.getByTestId('columns').textContent).toBe('name,actions')
  fireEvent.click(screen.getByRole('switch'))
  expect(screen.getByTestId('width').textContent).toBe('400')
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
