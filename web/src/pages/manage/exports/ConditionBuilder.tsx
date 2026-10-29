import { Button, Select, Space, Tooltip, Typography, theme } from 'antd'
import { CloseOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import type { ExportCond, ExportField, ExportFilter, ExportGroup, ExportSource } from '../../../api/types'
import {
  MAX_CONDITIONS,
  MAX_DEPTH,
  addConditionAt,
  canNestAt,
  coerceValues,
  countConditions,
  defaultOperator,
  isGroup,
  nestAt,
  removeAt,
  setOpAt,
  updateAt,
  type Path,
} from '../../../lib/exportFilter'
import { OperatorSelect } from './ConditionEditor'
import ValueInput from './ValueInput'
import type { ExportLabels } from './labels'

// Advanced tab: AND / OR groups of [field][operator][value] rows, with Add condition and Nest
// (wrap a row in a group), bounded to 20 conditions and 3 levels, and the limits are shown.
export default function ConditionBuilder({
  source,
  filter,
  onChange,
  labels,
}: {
  source: ExportSource
  filter: ExportFilter
  onChange: (f: ExportFilter) => void
  labels: ExportLabels
}) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const count = countConditions(filter)
  const atLimit = count >= MAX_CONDITIONS
  const fieldDef = (key: string): ExportField | undefined => source.fields.find((f) => f.key === key)

  const blank = (): ExportCond => {
    const f = source.fields[0]
    return { field: f?.key ?? '', operator: f ? defaultOperator(f) : 'is', values: [] }
  }

  const row = (c: ExportCond, path: Path) => {
    const def = fieldDef(c.field)
    const nestable = canNestAt(filter, path)
    return (
      <div key={path.join('.')} data-testid="builder-row" style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
        <Select
          style={{ width: 170 }}
          showSearch
          aria-label={t('export.builder.field')}
          value={c.field}
          options={source.fields.map((f) => ({ value: f.key, label: labels.field(f.key) }))}
          filterOption={(input, o) => String(o?.label ?? '').toLowerCase().includes(input.toLowerCase())}
          onChange={(key) => {
            const f = fieldDef(key)
            if (f) onChange(updateAt(filter, path, { field: key, operator: defaultOperator(f), values: [] }))
          }}
        />
        {def && (
          <>
            <OperatorSelect
              field={def}
              value={c.operator}
              labels={labels}
              ariaLabel={t('export.builder.operator')}
              onChange={(operator) => onChange(updateAt(filter, path, { ...c, operator, values: coerceValues(operator, c.values) }))}
            />
            <div style={{ flex: '1 1 220px', minWidth: 200 }}>
              <ValueInput
                field={def}
                operator={c.operator}
                values={c.values}
                labels={labels}
                ariaLabel={t('export.builder.value')}
                placeholder={t('export.filter.valuePlaceholder')}
                onChange={(values) => onChange(updateAt(filter, path, { ...c, values }))}
              />
            </div>
          </>
        )}
        <Tooltip title={nestable ? t('export.builder.nest') : t('export.builder.depthReached', { max: MAX_DEPTH })}>
          <Button
            type="text"
            disabled={!nestable}
            aria-label={t('export.builder.nest')}
            onClick={() => {
              const next = nestAt(filter, path)
              if (next) onChange(next)
            }}
          >
            {'<>'}
          </Button>
        </Tooltip>
        <Button
          type="text"
          icon={<CloseOutlined />}
          aria-label={t('export.builder.remove')}
          onClick={() => onChange(removeAt(filter, path))}
        />
      </div>
    )
  }

  const group = (g: ExportGroup, path: Path) => (
    <div
      key={path.join('.') || 'root'}
      data-testid="builder-group"
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: 10,
        padding: path.length ? 12 : 0,
        border: path.length ? `1px solid ${token.colorBorderSecondary}` : undefined,
        borderRadius: token.borderRadiusLG,
        background: path.length ? token.colorFillQuaternary : undefined,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <Select
          size="small"
          style={{ width: 116 }}
          popupMatchSelectWidth={false}
          aria-label={t('export.builder.groupOp')}
          value={g.op}
          options={[
            { value: 'and', label: t('export.builder.and') },
            { value: 'or', label: t('export.builder.or') },
          ]}
          onChange={(op) => onChange(setOpAt(filter, path, op))}
        />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {g.op === 'and' ? t('export.builder.andHint') : t('export.builder.orHint')}
        </Typography.Text>
        {path.length > 0 && (
          <Button
            type="text"
            size="small"
            style={{ marginInlineStart: 'auto' }}
            icon={<CloseOutlined />}
            aria-label={t('export.builder.removeGroup')}
            onClick={() => onChange(removeAt(filter, path))}
          />
        )}
      </div>
      {g.items.map((n, i) => (isGroup(n) ? group(n, [...path, i]) : row(n, [...path, i])))}
      <div>
        <Button
          icon={<PlusOutlined />}
          disabled={atLimit}
          onClick={() => {
            const next = addConditionAt(filter, path, blank())
            if (next) onChange(next)
          }}
        >
          {t('export.builder.addCondition')}
        </Button>
      </div>
    </div>
  )

  return (
    <Space orientation="vertical" size={10} style={{ width: '100%' }}>
      {group(filter, [])}
      <Typography.Text type={atLimit ? 'warning' : 'secondary'} style={{ fontSize: 12 }} role="status">
        {atLimit
          ? t('export.builder.limitReached', { max: MAX_CONDITIONS })
          : t('export.builder.limit', { n: count, max: MAX_CONDITIONS, depth: MAX_DEPTH })}
      </Typography.Text>
    </Space>
  )
}
