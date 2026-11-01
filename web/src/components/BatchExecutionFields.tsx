import type { ReactNode } from 'react'
import { InputNumber, Typography } from 'antd'
import { useTranslation } from 'react-i18next'

// The run-settings fields every batch composer shares, in one order: how often a failed row is
// retried, how many rows run at once, then whatever priority control the page offers (`children`).
// The batch console and the recurring-task page both render this, so the two cannot drift apart in
// order, limits or look.
export const MAX_RETRIES_LIMIT = 5
export const DEFAULT_CONCURRENCY_LIMIT = 20

export default function BatchExecutionFields({
  maxRetries,
  onMaxRetries,
  rowConcurrency,
  onRowConcurrency,
  maxConcurrency = DEFAULT_CONCURRENCY_LIMIT,
  children,
}: {
  maxRetries: number
  onMaxRetries: (value: number) => void
  rowConcurrency: number
  onRowConcurrency: (value: number) => void
  /** The most rows at once the page may offer; a recurring task takes the server's own ceiling. */
  maxConcurrency?: number
  children?: ReactNode
}) {
  const { t } = useTranslation()
  return (
    <div className="rp-batch-setting-fields">
      <label>
        <Typography.Text>{t('batch.maxRetries')}</Typography.Text>
        <InputNumber min={0} max={MAX_RETRIES_LIMIT} value={maxRetries} onChange={(v) => onMaxRetries(v ?? 0)} />
      </label>
      <label>
        <Typography.Text>{t('batch.rowConcurrency')}</Typography.Text>
        <InputNumber min={1} max={maxConcurrency} value={rowConcurrency} onChange={(v) => onRowConcurrency(v ?? 1)} />
      </label>
      {children}
    </div>
  )
}
