import { useMemo, useState } from 'react'
import { Button, DatePicker, Input, Popover, Segmented, Space, Typography, theme } from 'antd'
import { CloseOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'
import type { ExportCond, ExportFilter, ExportSource } from '../../../api/types'
import {
  MAX_CONDITIONS,
  activePreset,
  applyBelowEntryPreset,
  clearBelowEntryPreset,
  isBelowEntryActive,
  chipsOf,
  countConditions,
  presetCond,
  removeChip,
  upsertChip,
  type TimePreset,
} from '../../../lib/exportFilter'
import ConditionEditor from './ConditionEditor'
import type { ExportLabels } from './labels'

// Filter tab: one bar of chips. "Add a filter" opens a searchable field list, a field opens the
// operator/value popover, and chips combine with AND. A time preset row acts on the source's time field.
export default function FilterBar({
  source,
  filter,
  onChange,
  labels,
  today,
}: {
  source: ExportSource
  filter: ExportFilter
  onChange: (f: ExportFilter) => void
  labels: ExportLabels
  today: string
}) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const chips = chipsOf(filter)
  const used = new Set(chips.map((c) => c.field))
  const [editing, setEditing] = useState<string | null>(null)
  const [addOpen, setAddOpen] = useState(false)
  const [picked, setPicked] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [customTime, setCustomTime] = useState(false)
  const atLimit = countConditions(filter) >= MAX_CONDITIONS
  const fieldDef = (key: string) => source.fields.find((f) => f.key === key)
  // The entry-price screen is offered only where the server exposes it: with quotes off the source
  // has no below_entry field and the preset disappears with it.
  const hasBelowEntry = !!fieldDef('below_entry') && !!fieldDef('latest_per_symbol')
  const belowEntryOn = isBelowEntryActive(filter)

  const closeAdd = () => {
    setAddOpen(false)
    setPicked(null)
    setSearch('')
  }

  const available = useMemo(
    () =>
      source.fields.filter(
        (f) => !used.has(f.key) && labels.field(f.key).toLowerCase().includes(search.trim().toLowerCase()),
      ),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [source.fields, filter, search, labels],
  )

  const chipText = (c: ExportCond) => {
    const vals = c.values.map((v) => labels.value(c.field, v))
    const shown = vals.length > 2 ? `${vals.slice(0, 2).join(', ')} +${vals.length - 2}` : vals.join(c.operator === 'between' ? ' – ' : ', ')
    return `${labels.field(c.field)} ${labels.op(c.operator, source.fields.find((f) => f.key === c.field)?.type)} ${shown}`
  }

  const chipStyle = {
    display: 'inline-flex',
    alignItems: 'center',
    border: `1px solid ${token.colorBorder}`,
    borderRadius: 999,
    background: token.colorFillQuaternary,
    paddingInlineStart: 4,
    maxWidth: '100%',
  } as const

  const timeField = source.time_field ? fieldDef(source.time_field) : undefined
  const preset: TimePreset = timeField ? activePreset(filter, source.time_field, today) : 'all'
  const presetValue: TimePreset = customTime ? 'custom' : preset
  const timeCond = chips.find((c) => c.field === source.time_field)

  const onPreset = (p: TimePreset) => {
    if (p === 'custom') {
      setCustomTime(true)
      return
    }
    setCustomTime(false)
    if (p === 'all') onChange(removeChip(filter, source.time_field))
    else onChange(upsertChip(filter, presetCond(source.time_field, p, today)))
  }

  return (
    <Space orientation="vertical" size={12} style={{ width: '100%' }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
        {chips.map((c) => {
          const def = fieldDef(c.field)
          return (
            <span key={c.field} style={chipStyle} data-testid={`chip-${c.field}`}>
              <Popover
                trigger="click"
                placement="bottomLeft"
                destroyOnHidden
                open={editing === c.field}
                onOpenChange={(o) => setEditing(o ? c.field : null)}
                content={
                  def && (
                    <ConditionEditor
                      field={def}
                      initial={c}
                      labels={labels}
                      onCancel={() => setEditing(null)}
                      onApply={(next) => {
                        onChange(upsertChip(filter, next))
                        setEditing(null)
                      }}
                    />
                  )
                }
              >
                <Button
                  type="text"
                  size="small"
                  title={t('export.filter.edit')}
                  style={{ maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis' }}
                >
                  {chipText(c)}
                </Button>
              </Popover>
              <Button
                type="text"
                size="small"
                shape="circle"
                icon={<CloseOutlined />}
                aria-label={t('export.filter.remove', { field: labels.field(c.field) })}
                onClick={() => onChange(removeChip(filter, c.field))}
              />
            </span>
          )
        })}
        <Popover
          trigger="click"
          placement="bottomLeft"
          destroyOnHidden
          open={addOpen}
          onOpenChange={(o) => (o ? setAddOpen(true) : closeAdd())}
          content={
            picked && fieldDef(picked) ? (
              <ConditionEditor
                field={fieldDef(picked)!}
                labels={labels}
                onCancel={closeAdd}
                onApply={(c) => {
                  onChange(upsertChip(filter, c))
                  closeAdd()
                }}
              />
            ) : (
              <div style={{ width: 260, maxWidth: '80vw' }}>
                <Input
                  autoFocus
                  allowClear
                  prefix={<SearchOutlined />}
                  placeholder={t('export.filter.search')}
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                />
                <div role="listbox" aria-label={t('export.filter.add')} style={{ marginTop: 8, maxHeight: 260, overflowY: 'auto' }}>
                  {available.length === 0 && (
                    <Typography.Text type="secondary">{t('export.filter.noFields')}</Typography.Text>
                  )}
                  {available.map((f) => (
                    <Button key={f.key} type="text" block style={{ textAlign: 'start', justifyContent: 'flex-start' }} onClick={() => setPicked(f.key)}>
                      {labels.field(f.key)}
                    </Button>
                  ))}
                </div>
              </div>
            )
          }
        >
          <Button icon={<PlusOutlined />} shape="round" disabled={atLimit} title={atLimit ? t('export.builder.limitReached', { max: MAX_CONDITIONS }) : undefined}>
            {t('export.filter.add')}
          </Button>
        </Popover>
        {hasBelowEntry && (
          <Button
            shape="round"
            type={belowEntryOn ? 'primary' : 'default'}
            aria-pressed={belowEntryOn}
            title={t('export.preset.belowEntryHint')}
            data-testid="preset-below-entry"
            onClick={() => onChange(belowEntryOn ? clearBelowEntryPreset(filter) : applyBelowEntryPreset(filter))}
          >
            {t('export.preset.belowEntry')}
          </Button>
        )}
        {chips.length > 0 && (
          <Button type="link" onClick={() => { setCustomTime(false); onChange({ op: 'and', items: [] }) }}>
            {t('export.filter.clear')}
          </Button>
        )}
      </div>

      {timeField && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
          <Typography.Text type="secondary">{labels.field(source.time_field)}</Typography.Text>
          <Segmented<TimePreset>
            size="small"
            value={presetValue}
            onChange={onPreset}
            options={[
              { value: 'all', label: t('export.time.all') },
              { value: 'today', label: t('export.time.today') },
              { value: '7d', label: t('export.time.7d') },
              { value: '30d', label: t('export.time.30d') },
              { value: 'custom', label: t('export.time.custom') },
            ]}
          />
          {presetValue === 'custom' && (
            <DatePicker.RangePicker
              size="small"
              aria-label={t('export.time.custom')}
              value={timeCond?.operator === 'between' && timeCond.values.length === 2 ? [dayjs(timeCond.values[0]), dayjs(timeCond.values[1])] : null}
              onChange={(r) => {
                if (r?.[0] && r[1]) onChange(upsertChip(filter, { field: source.time_field, operator: 'between', values: [r[0].format('YYYY-MM-DD'), r[1].format('YYYY-MM-DD')] }))
                else onChange(removeChip(filter, source.time_field))
              }}
            />
          )}
        </div>
      )}
    </Space>
  )
}
