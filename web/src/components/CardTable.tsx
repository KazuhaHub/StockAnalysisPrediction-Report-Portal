import { Grid, Table, type TableProps } from 'antd'
import type { ColumnType } from 'antd/es/table'

/** A column may name itself for the phone card when its title is not plain text. */
export type CardColumn<T> = ColumnType<T> & { phoneLabel?: string }

// antd's Table, which on a phone lays each row out as a card of label and value instead of a row of
// columns: a phone cannot show six columns, and a sideways scroll hides most of them. Everything else
// is antd's own (sorting, selection, expansion, pagination); this only labels the cells and drops the
// horizontal scroll where the cards make it unnecessary (.rp-card-table--cards in index.css).
export default function CardTable<T>(props: TableProps<T>) {
  const phone = !Grid.useBreakpoint().sm
  const columns = props.columns?.map((column) => {
    const c = column as CardColumn<T>
    const label = c.phoneLabel ?? (typeof c.title === 'string' ? c.title : '')
    return {
      ...c,
      onCell: (record: T, index?: number) => ({ ...(c.onCell?.(record, index) ?? {}), 'data-label': label }) as ReturnType<NonNullable<ColumnType<T>['onCell']>>,
    }
  })
  const className = [props.className, 'rp-card-table', phone && 'rp-card-table--cards'].filter(Boolean).join(' ')
  // A vertical scroll still bounds a long list; only the sideways one goes.
  const scroll = phone ? (props.scroll?.y ? { y: props.scroll.y } : undefined) : props.scroll
  return <Table<T> {...props} columns={columns} className={className} scroll={scroll} />
}
