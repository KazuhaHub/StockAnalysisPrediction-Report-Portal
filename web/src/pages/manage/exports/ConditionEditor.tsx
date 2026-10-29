import { useState } from 'react'
import { Button, Select, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import type { ExportCond, ExportField, ExportOperator } from '../../../api/types'
import { coerceValues, defaultOperator, isCompleteCond } from '../../../lib/exportFilter'
import ValueInput from './ValueInput'
import type { ExportLabels } from './labels'

export function OperatorSelect({
  field,
  value,
  onChange,
  labels,
  ariaLabel,
  width = 140,
}: {
  field: ExportField
  value: ExportOperator
  onChange: (op: ExportOperator) => void
  labels: ExportLabels
  ariaLabel?: string
  width?: number | string
}) {
  return (
    <Select
      style={{ width }}
      aria-label={ariaLabel}
      value={value}
      options={field.operators.map((o) => ({ value: o, label: labels.op(o, field.type) }))}
      onChange={onChange}
    />
  )
}

// The popover body behind a filter chip, as in Workspace: field name as the title, an operator
// (Is / Is not / ...), a searchable value picker, then Apply.
export default function ConditionEditor({
  field,
  initial,
  labels,
  onApply,
  onCancel,
}: {
  field: ExportField
  initial?: ExportCond
  labels: ExportLabels
  onApply: (c: ExportCond) => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const [cond, setCond] = useState<ExportCond>(initial ?? { field: field.key, operator: defaultOperator(field), values: [] })
  const ok = isCompleteCond(cond)
  return (
    <div style={{ width: 320, maxWidth: '80vw' }}>
      <Typography.Text strong style={{ display: 'block', marginBottom: 12 }}>
        {labels.field(field.key)}
      </Typography.Text>
      <Space orientation="vertical" size={10} style={{ width: '100%' }}>
        <OperatorSelect
          field={field}
          value={cond.operator}
          width="100%"
          ariaLabel={t('export.builder.operator')}
          labels={labels}
          onChange={(operator) => setCond((c) => ({ ...c, operator, values: coerceValues(operator, c.values) }))}
        />
        <ValueInput
          field={field}
          operator={cond.operator}
          values={cond.values}
          labels={labels}
          ariaLabel={t('export.builder.value')}
          placeholder={t('export.filter.valuePlaceholder')}
          onChange={(values) => setCond((c) => ({ ...c, values }))}
        />
      </Space>
      <Space style={{ marginTop: 16, width: '100%', justifyContent: 'flex-end' }}>
        <Button onClick={onCancel}>{t('export.filter.cancel')}</Button>
        <Button type="primary" disabled={!ok} onClick={() => onApply(cond)}>
          {t('export.filter.apply')}
        </Button>
      </Space>
    </div>
  )
}
