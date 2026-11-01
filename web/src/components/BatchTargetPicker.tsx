import { useId } from 'react'
import { Select, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import type { BatchTarget } from '../api/types'

// The workflow picker row of the batch composer. The batch console and the recurring-task page both
// compose a run from the same parts, so the label, placeholder and option text live here once.
//
// `missing` keeps a slot for a target that no longer exists (a recurring task whose workflow was
// deleted), labelled, so the picker does not show a bare id; `warning` says what to do about it.
export default function BatchTargetPicker({
  targets,
  value,
  onChange,
  missing,
  warning,
}: {
  targets: BatchTarget[]
  value: number | undefined
  onChange: (id: number | undefined) => void
  missing?: { id: number; label: string }
  warning?: string
}) {
  const { t } = useTranslation()
  // The label names the picker for assistive technology too, not only on screen.
  const id = useId()
  return (
    <>
      <div className="rp-batch-field-row">
        <label htmlFor={id}>{t('batch.target')}</label>
        <Select
          id={id}
          showSearch
          optionFilterProp="label"
          placeholder={t('batch.selectTarget')}
          value={value}
          onChange={onChange}
          options={[
            ...targets.map((tg) => ({
              value: tg.id,
              label: tg.plugin_name ? `${tg.name}（${tg.plugin_name}）` : tg.name,
            })),
            ...(missing ? [{ value: missing.id, label: missing.label }] : []),
          ]}
        />
      </div>
      {warning && <Typography.Text type="warning" className="rp-batch-field-warning">{warning}</Typography.Text>}
    </>
  )
}
