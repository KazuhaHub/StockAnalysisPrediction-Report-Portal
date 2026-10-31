import { useState } from 'react'
import { Badge, Button, Input, Select, Space, Typography } from 'antd'
import type { SelectProps } from 'antd'
import { CloseOutlined, FilterOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'

import PopoverOrSheet from '../../components/PopoverOrSheet'

import { AUDIT_FIELDS, type AuditCondition, type AuditField } from '../../lib/auditQuery'

interface Props {
  conditions: AuditCondition[]
  onChange: (next: AuditCondition[]) => void
  actionOptions: SelectProps['options']
}

// The audit log's conditions, laid out like Cloudflare's log filters with Workspace's attribute,
// operator and value: one row per condition, "Add filter" for another attribute, Apply to search.
// Edits stay in the panel until applied, so a half-typed value never sends a request.
export default function AuditFilterPanel({ conditions, onChange, actionOptions }: Props) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<AuditCondition[]>([])

  const name: Record<AuditField, string> = {
    action: t('audit.action'),
    actor: t('audit.actor'),
    ip: t('audit.ipFilter'),
    target: t('audit.target'),
  }
  const unused = (rows: AuditCondition[]) => AUDIT_FIELDS.filter((f) => !rows.some((c) => c.field === f))

  const show = (next: boolean) => {
    // Opening starts from what is applied; with nothing applied, from one empty row to fill in.
    if (next) setDraft(conditions.length ? conditions.map((c) => ({ ...c })) : [{ field: 'action', op: 'is', value: '' }])
    setOpen(next)
  }
  const patch = (i: number, p: Partial<AuditCondition>) =>
    setDraft((rows) => rows.map((c, j) => (j === i ? { ...c, ...p, ...(p.field && p.field !== c.field ? { value: '' } : {}) } : c)))
  // Applying what is already applied, or clearing nothing, would only ask the server again.
  const apply = () => {
    const next = draft.filter((c) => c.value.trim()).map((c) => ({ ...c, value: c.value.trim() }))
    if (JSON.stringify(next) !== JSON.stringify(conditions)) onChange(next)
    setOpen(false)
  }
  const clearAll = () => {
    if (conditions.length) onChange([])
    setOpen(false)
  }

  const valueControl = (c: AuditCondition, i: number) =>
    c.field === 'action' ? (
      <Select
        showSearch
        optionFilterProp="label"
        aria-label={name.action}
        placeholder={name.action}
        value={c.value || undefined}
        onChange={(v) => patch(i, { value: v ?? '' })}
        options={actionOptions}
        classNames={{ popup: { root: 'rp-audit-action-popup' } }}
        // The labels are sentences; a popup no wider than this cell would truncate every one.
        popupMatchSelectWidth={false}
        autoFocus={i === 0}
        style={{ width: '100%' }}
      />
    ) : (
      <Input
        aria-label={name[c.field]}
        placeholder={name[c.field]}
        value={c.value}
        onChange={(e) => patch(i, { value: e.target.value })}
        onPressEnter={apply}
        autoFocus={i === 0}
      />
    )

  const panel = (
    <div className="rp-audit-filter-panel">
      <div className="rp-audit-filter-panel__head">
        <Typography.Text strong>{t('audit.filters')}</Typography.Text>
        <Button type="link" size="small" onClick={clearAll} disabled={conditions.length === 0}>
          {t('audit.clearAll')}
        </Button>
      </div>
      {draft.map((c, i) => (
        <div className="rp-audit-filter-row" key={i} role="group" aria-label={name[c.field]}>
          <Select
            className="rp-audit-filter-row__field"
            aria-label={t('audit.f.field')}
            value={c.field}
            onChange={(field) => patch(i, { field })}
            // A field already used by another row is not offered twice: one condition per field.
            options={AUDIT_FIELDS.filter((f) => f === c.field || !draft.some((d) => d.field === f)).map((f) => ({ value: f, label: name[f] }))}
          />
          <Select
            className="rp-audit-filter-row__op"
            aria-label={t('audit.f.op')}
            value={c.op}
            onChange={(op) => patch(i, { op })}
            options={[
              { value: 'is', label: t('audit.op.is') },
              { value: 'is_not', label: t('audit.op.isNot') },
            ]}
          />
          <div className="rp-audit-filter-row__value">{valueControl(c, i)}</div>
          <Button
            className="rp-audit-filter-row__remove"
            type="text"
            size="small"
            icon={<CloseOutlined />}
            aria-label={t('audit.removeFilter', { name: name[c.field] })}
            onClick={() => setDraft((rows) => rows.filter((_, j) => j !== i))}
          />
        </div>
      ))}
      <div className="rp-audit-filter-panel__foot">
        <Button
          type="link"
          size="small"
          icon={<PlusOutlined />}
          disabled={unused(draft).length === 0}
          onClick={() => setDraft((rows) => [...rows, { field: unused(rows)[0], op: 'is', value: '' }])}
        >
          {t('audit.addFilter')}
        </Button>
        <Button type="primary" size="small" onClick={apply}>
          {t('audit.apply')}
        </Button>
      </div>
    </div>
  )

  return (
    <Space.Compact>
      <PopoverOrSheet open={open} onOpenChange={show} title={t('audit.filters')} content={panel}>
        <Button icon={<FilterOutlined />} aria-haspopup="dialog" aria-expanded={open}>
          {t('audit.filters')}
          {conditions.length > 0 && <Badge count={conditions.length} size="small" style={{ backgroundColor: 'var(--ant-color-primary)' }} />}
        </Button>
      </PopoverOrSheet>
      {conditions.length > 0 && <Button icon={<CloseOutlined />} aria-label={t('audit.clearAll')} onClick={clearAll} />}
    </Space.Compact>
  )
}
