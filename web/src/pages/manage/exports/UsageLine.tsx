import { Alert } from 'antd'
import { useTranslation } from 'react-i18next'
import type { ExportPolicy } from '../../../api/types'

// A reader's allowance, one line above the results (ADR 0046): what today has used, the daily quota
// and the per-export maximum. An administrator is not governed by the policy and sees nothing.
export default function UsageLine({ policy }: { policy: ExportPolicy }) {
  const { t } = useTranslation()
  if (policy.unlimited) return null
  const spent = policy.remaining <= 0
  return (
    <Alert
      type={spent ? 'warning' : 'info'}
      showIcon
      role="status"
      title={t('export.reader.usage', { used: policy.used_today.toLocaleString(), quota: policy.daily_quota.toLocaleString(), max: policy.max_reports.toLocaleString() })}
    />
  )
}
