import { useEffect, useState } from 'react'
import { App, Alert, Input, Modal, Radio, Select, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../../api/client'
import type { ExportFilter, ExportFormat, ExportJob, ExportSource } from '../../../api/types'
import { buildExportRequest } from '../../../lib/exportFilter'
import { exportBase, exportBlock, type ExportMode } from '../../../lib/exportMode'
import type { ExportPolicy } from '../../../api/types'
import type { ExportLabels } from './labels'

export const NAME_MAX = 80
type Layout = 'flat' | 'date' | 'symbol'

// The Export dialog: name (with counter), format, time zone, and for a report Markdown archive the
// per-file front matter, the index.csv manifest and the folder layout. It states how many rows it
// will export and submits a background task; the file is collected from the task tray.
//
// For a reader (ADR 0046) the format is fixed, columns are not chosen, and the submit button is
// disabled with the reason when the count is over either limit, so the server's 422/429 stay a
// backstop rather than the normal way to find out.
export default function ExportDialog({
  open,
  source,
  filter,
  columns,
  total,
  labels,
  defaultName,
  mode = 'admin',
  policy,
  onCancel,
  onCreated,
}: {
  open: boolean
  source: ExportSource
  filter: ExportFilter
  columns: string[]
  total: number
  labels: ExportLabels
  defaultName: string
  mode?: ExportMode
  /** The reader's policy and usage; absent for an administrator. */
  policy?: ExportPolicy | null
  onCancel: () => void
  onCreated: (job: ExportJob) => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [name, setName] = useState(defaultName)
  const [format, setFormat] = useState<ExportFormat>(source.formats[0])
  const [tz, setTz] = useState<'panel' | 'utc'>('panel')
  const [frontMatter, setFrontMatter] = useState(true)
  const [indexCsv, setIndexCsv] = useState(true)
  const [layout, setLayout] = useState<Layout>('flat')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return
    setName(defaultName)
    setFormat(source.formats[0])
    setError('')
  }, [open, defaultName, source])

  const reportArchive = source.key === 'reports' && format === 'md_zip'
  const block = mode === 'reader' ? exportBlock(total, policy) : null

  const submit = async () => {
    setBusy(true)
    setError('')
    try {
      const job = await api.post<ExportJob>(
        exportBase(mode),
        buildExportRequest({
          source: source.key,
          format,
          filter,
          columns: mode === 'reader' ? undefined : columns,
          name,
          tz,
          options: reportArchive ? { front_matter: frontMatter, index_csv: indexCsv, layout } : undefined,
        }),
      )
      onCreated(job)
    } catch (e) {
      const text = errText(e, t)
      setError(text)
      message.error(text)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('export.dialog.title')}
      onCancel={onCancel}
      destroyOnHidden
      okText={t('export.dialog.submit')}
      cancelText={t('export.filter.cancel')}
      okButtonProps={{ disabled: total === 0 || block !== null, loading: busy }}
      onOk={submit}
    >
      <Space orientation="vertical" size={16} style={{ width: '100%' }}>
        <div>
          <Typography.Text strong>{t('export.dialog.name')}</Typography.Text>
          <Input
            value={name}
            maxLength={NAME_MAX}
            showCount
            placeholder={t('export.dialog.namePlaceholder')}
            onChange={(e) => setName(e.target.value)}
            aria-label={t('export.dialog.name')}
            style={{ marginTop: 6 }}
          />
        </div>
        {source.formats.length > 1 && <div>
          <Typography.Text strong>{t('export.dialog.format')}</Typography.Text>
          <div style={{ marginTop: 6 }}>
            <Radio.Group value={format} onChange={(e) => setFormat(e.target.value)} aria-label={t('export.dialog.format')}>
              {source.formats.map((f) => (
                <Radio key={f} value={f}>
                  {labels.format(f)}
                </Radio>
              ))}
            </Radio.Group>
          </div>
        </div>}
        <div>
          <Typography.Text strong>{t('export.dialog.tz')}</Typography.Text>
          <div style={{ marginTop: 6 }}>
            <Radio.Group value={tz} onChange={(e) => setTz(e.target.value)} aria-label={t('export.dialog.tz')}>
              <Radio value="panel">{t('export.dialog.tzPanel')}</Radio>
              <Radio value="utc">{t('export.dialog.tzUtc')}</Radio>
            </Radio.Group>
          </div>
        </div>
        {reportArchive && (
          <Space orientation="vertical" size={10} style={{ width: '100%' }}>
            <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              <Switch checked={frontMatter} onChange={setFrontMatter} aria-label={t('export.dialog.frontMatter')} />
              <span>{t('export.dialog.frontMatter')}</span>
            </label>
            <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              <Switch checked={indexCsv} onChange={setIndexCsv} aria-label={t('export.dialog.indexCsv')} />
              <span>{t('export.dialog.indexCsv')}</span>
            </label>
            <div>
              <Typography.Text strong>{t('export.dialog.layout')}</Typography.Text>
              <Select<Layout>
                style={{ width: '100%', marginTop: 6 }}
                value={layout}
                aria-label={t('export.dialog.layout')}
                onChange={setLayout}
                options={[
                  { value: 'flat', label: t('export.dialog.layoutFlat') },
                  { value: 'date', label: t('export.dialog.layoutDate') },
                  { value: 'symbol', label: t('export.dialog.layoutSymbol') },
                ]}
              />
            </div>
          </Space>
        )}
        <Alert type="info" showIcon title={t('export.dialog.rows', { n: total })} description={t('export.dialog.note')} />
        {block && (
          <Alert
            type="warning"
            showIcon
            role="alert"
            title={block.kind === 'tooLarge' ? t('export.reader.blockTooLarge', { n: block.n, max: block.max }) : t('export.reader.blockQuota', { n: block.n, remaining: block.remaining })}
            description={t('export.reader.blockHint')}
          />
        )}
        {error && <Alert type="error" showIcon title={error} />}
      </Space>
    </Modal>
  )
}
