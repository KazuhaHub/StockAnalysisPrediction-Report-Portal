import { afterEach, describe, expect, it, vi } from 'vitest'
import { render } from '@testing-library/react'
import { Grid } from 'antd'

import CardTable from './CardTable'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

type Row = { id: number; name: string; size: number }
const rows: Row[] = [{ id: 1, name: 'alpha', size: 3 }]
const columns = [
  { title: 'Name', dataIndex: 'name', key: 'name' },
  { title: () => 'Size', dataIndex: 'size', key: 'size', phoneLabel: 'Size' },
]
const width = (phone: boolean) =>
  vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ sm: !phone, md: !phone, lg: !phone } as ReturnType<typeof Grid.useBreakpoint>)

describe('CardTable', () => {
  afterEach(() => vi.restoreAllMocks())

  it('is an ordinary table on a wide screen', () => {
    width(false)
    const { container } = render(<CardTable<Row> rowKey="id" columns={columns} dataSource={rows} scroll={{ x: 900 }} />)
    expect(container.querySelector('.ant-table-thead')).not.toBeNull()
    expect(container.querySelector('.rp-card-table--cards')).toBeNull()
  })

  // On a phone each row is a card of label and value, so nothing needs a sideways scroll.
  it('turns rows into labelled cards on a phone, without a horizontal scroll', () => {
    width(true)
    const { container } = render(<CardTable<Row> rowKey="id" className="mine" columns={columns} dataSource={rows} scroll={{ x: 900 }} />)
    const wrapper = container.querySelector('.ant-table-wrapper') as HTMLElement
    expect(wrapper.classList.contains('rp-card-table--cards')).toBe(true)
    expect(wrapper.classList.contains('mine')).toBe(true)
    const cells = [...container.querySelectorAll('.ant-table-tbody tr.ant-table-row td')]
    expect(cells.map((c) => c.getAttribute('data-label'))).toEqual(['Name', 'Size'])
    expect(container.querySelector('.ant-table-content')?.getAttribute('style') ?? '').not.toContain('overflow-x: auto')
  })

  it('keeps a column’s own cell props', () => {
    width(true)
    const own = [{ title: 'Name', dataIndex: 'name', key: 'name', onCell: () => ({ title: 'own' }) }]
    const { container } = render(<CardTable<Row> rowKey="id" columns={own} dataSource={rows} />)
    const cell = container.querySelector('.ant-table-tbody tr.ant-table-row td') as HTMLElement
    expect(cell.getAttribute('title')).toBe('own')
    expect(cell.getAttribute('data-label')).toBe('Name')
  })

  // The first column names the row (Shopify's primary column): on a card it is the title, full width
  // and unlabelled, not one more labelled field.
  it('makes the first column the card title', () => {
    width(true)
    const { container } = render(<CardTable<Row> rowKey="id" columns={columns} dataSource={rows} />)
    const cells = [...container.querySelectorAll('.ant-table-tbody tr.ant-table-row td')]
    expect(cells[0].hasAttribute('data-primary')).toBe(true)
    expect(cells[1].hasAttribute('data-primary')).toBe(false)
  })

  // Nothing matching the filters is not the same as nothing there: say it is the filters and offer
  // to clear them, instead of the plain empty state. [Carbon search / empty states]
  it('says when filters, not the data, leave the table empty, and clears them', async () => {
    width(false)
    const onClear = vi.fn()
    const { getByText, getByRole } = render(<CardTable<Row> rowKey="id" columns={columns} dataSource={[]} filtered onClearFilters={onClear} />)
    expect(getByText('table.noMatches')).toBeTruthy()
    getByRole('button', { name: 'table.clearFilters' }).click()
    expect(onClear).toHaveBeenCalled()
  })

  it('keeps the page’s own empty text when nothing is filtered', () => {
    width(false)
    const { getByText, queryByText } = render(
      <CardTable<Row> rowKey="id" columns={columns} dataSource={[]} locale={{ emptyText: 'No tokens yet' }} filtered={false} />,
    )
    expect(getByText('No tokens yet')).toBeTruthy()
    expect(queryByText('table.noMatches')).toBeNull()
  })
})
