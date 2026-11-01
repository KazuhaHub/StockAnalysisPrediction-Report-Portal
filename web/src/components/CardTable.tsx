import { useRef } from 'react'
import { Button, Empty, Grid, Skeleton, Table, type TableProps } from 'antd'
import { useTranslation } from 'react-i18next'
import type { ColumnType } from 'antd/es/table'

/** A column may name itself for the phone card when its title is not plain text. */
export type CardColumn<T> = ColumnType<T> & { phoneLabel?: string }

// antd's Table, which on a phone lays each row out as a card of label and value instead of a row of
// columns: a phone cannot show six columns, and a sideways scroll hides most of them. Everything else
// is antd's own (sorting, selection, expansion, pagination); this only labels the cells and drops the
// horizontal scroll where the cards make it unnecessary (.rp-card-table--cards in index.css).
export default function CardTable<T>({
  filtered,
  onClearFilters,
  ...props
}: TableProps<T> & {
  /** Filters or a search are narrowing the rows; an empty result then says so and offers to clear them. */
  filtered?: boolean
  onClearFilters?: () => void
}) {
  const { t } = useTranslation()
  const phone = !Grid.useBreakpoint().sm
  // The first column names the row (the item); on a card it is the title rather than a labelled field.
  const columns = props.columns?.map((column, i) => {
    const c = column as CardColumn<T>
    const label = c.phoneLabel ?? (typeof c.title === 'string' ? c.title : '')
    const card = i === 0 ? { 'data-label': label, 'data-primary': '' } : { 'data-label': label }
    return {
      ...c,
      onCell: (record: T, index?: number) => ({ ...(c.onCell?.(record, index) ?? {}), ...card }) as ReturnType<NonNullable<ColumnType<T>['onCell']>>,
    }
  })
  const className = [props.className, 'rp-card-table', phone && 'rp-card-table--cards'].filter(Boolean).join(' ')
  // A vertical scroll still bounds a long list; only the sideways one goes.
  const scroll = phone ? (props.scroll?.y ? { y: props.scroll.y } : undefined) : props.scroll
  // Nothing matching is not nothing there [Carbon search / empty states].
  // A first load has no rows to dim under a spinner, so it shows skeleton rows in the shape of the
  // list, from the first frame and fading in (a delay left a blank table); a refresh keeps its rows
  // under antd's own spinner, which waits 300 ms because the rows are already there to read.
  const loadingFlag = typeof props.loading === 'object' ? props.loading.spinning !== false : !!props.loading
  // Only before the first answer: a reload of a list that is genuinely empty (or filtered to
  // nothing) keeps its empty state under the spinner. A different list (another scope) remounts.
  const answered = useRef(false)
  if (!loadingFlag) answered.current = true
  const firstLoad = loadingFlag && !props.dataSource?.length && !answered.current
  const loading = firstLoad
    ? false
    : typeof props.loading === 'object'
      ? { delay: 300, ...props.loading }
      : props.loading && { spinning: true, delay: 300 }
  const locale = firstLoad
    ? {
        ...props.locale,
        emptyText: (
          <div aria-busy>
            <span role="status" className="rp-sr-only">{t('common.loading')}</span>
            <Skeleton active title={false} paragraph={{ rows: 5 }} className="rp-fade-in" />
          </div>
        ),
      }
    : filtered
    ? {
        ...props.locale,
        emptyText: <NoMatches onClear={onClearFilters} />,
      }
    : props.locale
  return <Table<T> {...props} loading={loading} locale={locale} columns={columns} className={className} scroll={scroll} />
}

/**
 * Nothing matching is not nothing there: the empty state for a list narrowed by a search or filters,
 * with the way back [Carbon search / empty states]. CardTable shows it itself when `filtered`; a view
 * that hides whole sections on a filter shows it where the sections would be.
 */
export function NoMatches({ onClear }: { onClear?: () => void }) {
  const { t } = useTranslation()
  return (
    <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('table.noMatches')}>
      {onClear && <Button size="small" onClick={onClear}>{t('table.clearFilters')}</Button>}
    </Empty>
  )
}
