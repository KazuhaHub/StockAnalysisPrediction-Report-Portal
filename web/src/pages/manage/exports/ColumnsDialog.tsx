import { useEffect, useState } from 'react'
import { Button, Modal, Select, Space, Typography, theme } from 'antd'
import { CloseOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import type { ExportSource } from '../../../api/types'
import { DragHandle, SortableItem, SortableWrapper } from '../dnd'
import type { ExportLabels } from './labels'

// "Manage columns": drag to reorder, X to remove, "Add new column" for the rest. The chosen set,
// in this order, is also the export's column set.
export default function ColumnsDialog({
  open,
  source,
  value,
  labels,
  onCancel,
  onSave,
}: {
  open: boolean
  source: ExportSource
  value: string[]
  labels: ExportLabels
  onCancel: () => void
  onSave: (keys: string[]) => void
}) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const [draft, setDraft] = useState(value)
  useEffect(() => {
    if (open) setDraft(value)
  }, [open, value])

  const remaining = source.columns.filter((c) => !draft.includes(c.key))
  const defaults = source.columns.filter((c) => c.default).map((c) => c.key)

  return (
    <Modal
      open={open}
      title={t('export.columns.title')}
      onCancel={onCancel}
      destroyOnHidden
      footer={
        <Space>
          <Button onClick={() => setDraft(defaults)}>{t('export.columns.reset')}</Button>
          <Button onClick={onCancel}>{t('export.filter.cancel')}</Button>
          <Button type="primary" disabled={draft.length === 0} onClick={() => onSave(draft)}>
            {t('export.columns.save')}
          </Button>
        </Space>
      }
    >
      <Typography.Paragraph type="secondary">{t('export.columns.hint')}</Typography.Paragraph>
      <SortableWrapper ids={draft} onReorder={setDraft} gap={6}>
        {draft.map((key) => (
          <SortableItem key={key} id={key}>
            <div
              data-testid="column-row"
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                padding: '4px 8px',
                border: `1px solid ${token.colorBorderSecondary}`,
                borderRadius: token.borderRadius,
                background: token.colorBgContainer,
              }}
            >
              <DragHandle label={t('export.columns.drag', { name: labels.column(key) })} />
              <span style={{ flex: 1 }}>{labels.column(key)}</span>
              <Button
                type="text"
                size="small"
                icon={<CloseOutlined />}
                disabled={draft.length === 1}
                aria-label={t('export.columns.remove', { name: labels.column(key) })}
                onClick={() => setDraft((d) => d.filter((k) => k !== key))}
              />
            </div>
          </SortableItem>
        ))}
      </SortableWrapper>
      <Select
        style={{ width: '100%', marginTop: 12 }}
        showSearch
        value={null}
        disabled={remaining.length === 0}
        placeholder={t('export.columns.add')}
        aria-label={t('export.columns.add')}
        options={remaining.map((c) => ({ value: c.key, label: labels.column(c.key) }))}
        filterOption={(input, o) => String(o?.label ?? '').toLowerCase().includes(input.toLowerCase())}
        onChange={(k: string | null) => k && setDraft((d) => [...d, k])}
      />
    </Modal>
  )
}
