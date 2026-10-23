import { Alert } from 'antd'
import { useTranslation } from 'react-i18next'
import type { ForecastPurgedMonth } from '../../api/types'
import { civilDate } from '../../lib/forecast'

// The months a custom range lost to the retention policy. The server removes a purged month's sets
// from an exploratory range but keeps the month's period, so a month with nothing left would
// otherwise read as a cohort under the publication gate. This is the one place that says it is gone,
// and when; the matrix draws such a month as removed, and the export carries the same list.
//
// The same list is shown over the matrix and over a cell's member list, each with its own sentence
// (the matrix's figures exclude them; the drawer's rows do), so the sentence is the caller's.

interface Props {
  months: ForecastPurgedMonth[] | undefined
  /** The translation key of the sentence; it receives the number of months as {{n}}. */
  titleKey: 'forecast.custom.excluded_title' | 'forecast.drawer.excluded_title'
}

export default function ForecastRetentionNote({ months, titleKey }: Props) {
  const { t } = useTranslation()
  if (!months || months.length === 0) return null
  return (
    <Alert
      type="info"
      showIcon
      role="note"
      // The list is part of the title, not a description: a description switches antd to its large
      // icon and heading size, which this note (one of several on the page) does not need.
      title={
        <>
          {t(titleKey, { n: months.length })}
          <ul role="list" data-part="excluded" style={{ margin: '2px 0 0', padding: 0, listStyle: 'none', display: 'flex', flexWrap: 'wrap', gap: '2px 16px', fontSize: 12 }}>
            {months.map((m) => (
              <li key={m.cohort}>{t('forecast.custom.excluded_item', { cohort: m.cohort, date: civilDate(m.purged_at) || m.purged_at })}</li>
            ))}
          </ul>
        </>
      }
    />
  )
}
