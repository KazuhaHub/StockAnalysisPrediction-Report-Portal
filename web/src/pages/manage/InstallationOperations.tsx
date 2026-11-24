import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import CardTable from '../../components/CardTable'
import Expand from '../../components/Expand'
import StatusTag from '../../components/StatusTag'
import type { PlatformOperationRow } from '../../api/appOperations'
import { usePrefs } from '../../prefs'
import { actionLabel, failureReason } from './InstallationOperationLine'
import OperationSteps from './OperationSteps'
import { operationStages } from './operationSteps'
import { operationDurationMs, operationOutcome, operationVersions } from './operationSummary'
import type { OperationFeed } from './useOperationFeed'

/** "1 min 5 s": how long an operation took, to the second. */
export function formatDuration(t: TFunction, ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000))
  const s = total % 60, m = Math.floor(total / 60) % 60, h = Math.floor(total / 3600)
  if (h > 0) return t('appPlatform.ops.durationHours', { h, m })
  if (m > 0) return t('appPlatform.ops.durationMinutes', { m, s })
  return t('appPlatform.ops.durationSeconds', { s })
}

/**
 * The recent operations of one installation (the feed the Overview already reads): what ran, between which versions, when it started, how long it took and
 * how it ended (with the reason when it failed). Each row opens its steps, drawn from the same stage list the
 * progress dialog uses, with a failure on the step it stopped at.
 */
export default function InstallationOperations({ feed }: { feed: OperationFeed }) {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  // A row's steps unfold and fold (Expand) instead of appearing: the table row is kept while it plays, and mounted
  // closed first so opening animates too.
  const [rendered, setRendered] = useState<string[]>([])
  const [open, setOpen] = useState<string[]>([])
  const timers = useRef<ReturnType<typeof setTimeout>[]>([])
  useEffect(() => () => timers.current.forEach(clearTimeout), [])
  const toggle = (expanded: boolean, row: PlatformOperationRow) => {
    const key = row.operation_id
    if (expanded) {
      setRendered(v => v.includes(key) ? v : [...v, key])
      timers.current.push(setTimeout(() => setOpen(v => v.includes(key) ? v : [...v, key]), 30))
    } else {
      setOpen(v => v.filter(k => k !== key))
      timers.current.push(setTimeout(() => setRendered(v => v.filter(k => k !== key)), 450))
    }
  }
  const started = (value: string) => new Intl.DateTimeFormat(lang, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(Number(value)))
  const columns: ColumnsType<PlatformOperationRow> = [
    { title: t('appPlatform.ops.col.action'), key: 'action', render: (_: unknown, row) => <Typography.Text strong>{actionLabel(t, row.action)}</Typography.Text> },
    {
      title: t('appPlatform.ops.col.versions'), key: 'versions', render: (_: unknown, row) => {
        const { from, to } = operationVersions(row)
        return from && to ? `${from} → ${to}` : to ?? '—'
      },
    },
    { title: t('appPlatform.ops.col.started'), key: 'started', render: (_: unknown, row) => started(row.created_ms) },
    { title: t('appPlatform.ops.col.duration'), key: 'duration', render: (_: unknown, row) => { const ms = operationDurationMs(row); return ms === undefined ? '—' : formatDuration(t, ms) } },
    {
      title: t('appPlatform.ops.col.result'), key: 'result', render: (_: unknown, row) => {
        const outcome = operationOutcome(row)
        if (outcome === 'running') return <StatusTag tone="processing">{t('appPlatform.ops.running')}</StatusTag>
        if (outcome === 'succeeded') return <StatusTag tone="success">{t('appPlatform.ops.succeededShort')}</StatusTag>
        return <div>
          <StatusTag tone="error">{t('appPlatform.ops.failedShort')}</StatusTag>
          <div><Typography.Text type="danger">{failureReason(t, row.error_code)}</Typography.Text></div>
        </div>
      },
    },
  ]
  return <Card title={t('appPlatform.ops.recent')} className="rp-installation-operations">
    {feed.error && <Alert type="error" showIcon title={feed.error} action={<Button size="small" onClick={feed.reload}>{t('common.retry')}</Button>} />}
    {!feed.error || feed.rows
      ? <CardTable<PlatformOperationRow> rowKey="operation_id" size="small" pagination={false} columns={columns} dataSource={feed.rows ?? []} loading={feed.rows === null}
        locale={{ emptyText: t('appPlatform.ops.empty') }}
        expandable={{
          rowExpandable: row => operationStages(row.action, row.managed).length > 0,
          expandedRowKeys: rendered,
          onExpand: toggle,
          expandedRowRender: row => <Expand open={open.includes(row.operation_id)}><OperationSteps progress={row} /></Expand>,
        }} />
      : null}
  </Card>
}
