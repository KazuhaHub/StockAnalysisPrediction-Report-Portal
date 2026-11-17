import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Space, Typography } from 'antd'
import CompactNumberInput from './CompactNumberInput'
import { useTranslation } from 'react-i18next'
import Confirm from './Confirm'
import { api, errText } from '../api/client'
import { useLoadFailure } from '../lib/useOnReconnect'
import { notifyError } from '../lib/notify'
import { formatReportDateTime } from '../lib/datetime'
import { LoadingPlaceholder } from './LoadGate'

// Retention of the application platform's control records (docs/app-platform/172-retention-plan.md): two
// adjustable values, the fixed policy shown read-only, and what the last sweep did, read from its audit row.
// The fixed numbers come from the server so this page can never state a limit the sweeper does not apply.

interface Policy {
  operation_days: number
  recovery_sets: number
  bounds: { operation_days_min: number; operation_days_max: number; recovery_sets_min: number; recovery_sets_max: number }
  fixed: {
    review_plan_minutes: number
    review_plan_grace_hours: number
    task_days: number
    task_rows_per_installation: number
    runner_log_mib: number
  }
  last_sweep: { at: string; counts: Record<string, number | string> } | null
  recovery_set_pruning: string
}

const num = (v: number | string | undefined) => (typeof v === 'number' ? v : 0)

const usable = (p: Partial<Policy> | undefined): p is Policy =>
  !!p && typeof p.operation_days === 'number' && typeof p.recovery_sets === 'number' && !!p.bounds && !!p.fixed

export default function AppPlatformRetentionCard() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [policy, setPolicy] = useState<Policy | null>(null)
  const [days, setDays] = useState<number | null>(null)
  const [sets, setSets] = useState<number | null>(null)
  const [failed, setFailed] = useState(false)
  const [saving, setSaving] = useState(false)
  const { failure, retryKey } = useLoadFailure()

  const load = () => {
    setFailed(false)
    return api.get<Partial<Policy>>('/api/admin/app-platform/retention')
      .then((r) => {
        if (!usable(r)) { setFailed(true); return }
        setPolicy(r); setDays(r.operation_days); setSets(r.recovery_sets)
      })
      .catch((e) => { if (failure(e) !== null) setFailed(true) })
  }
  useEffect(() => { void load() }, [retryKey])

  const b = policy?.bounds
  const inRange = (v: number | null, lo: number, hi: number) => v !== null && Number.isInteger(v) && v >= lo && v <= hi
  const valid = !!b && inRange(days, b.operation_days_min, b.operation_days_max) && inRange(sets, b.recovery_sets_min, b.recovery_sets_max)
  const changed = !!policy && (days !== policy.operation_days || sets !== policy.recovery_sets)

  const save = async () => {
    if (!policy || !valid) return
    // Only what changed: an untouched value is not re-asserted over a concurrent edit.
    const body: { operation_days?: number; recovery_sets?: number } = {}
    if (days !== policy.operation_days) body.operation_days = days as number
    if (sets !== policy.recovery_sets) body.recovery_sets = sets as number
    setSaving(true)
    try {
      await api.put('/api/admin/app-platform/retention', body)
      setPolicy({ ...policy, operation_days: days as number, recovery_sets: sets as number })
      message.success(t('common.saved'))
    } catch (e) { notifyError(errText(e, t)) }
    finally { setSaving(false) }
  }

  const row = (label: string, value: React.ReactNode) => (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '4px 16px' }}>
      <Typography.Text strong style={{ minWidth: 180 }}>{label}</Typography.Text>
      <Typography.Text type="secondary" style={{ flex: '1 1 260px' }}>{value}</Typography.Text>
    </div>
  )

  const sweep = policy?.last_sweep
  const counts = sweep?.counts ?? {}
  return (
    <Card title={t('storage.platform.title')}>
      {failed ? (
        <Alert type="error" title={t('storage.platform.loadFailed')} action={<Button onClick={() => void load()}>{t('common.retry')}</Button>} />
      ) : !policy || !b ? (
        <LoadingPlaceholder />
      ) : (
        <Space orientation="vertical" size={16} style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t('storage.platform.hint')}</Typography.Text>
          <Space wrap>
            <Typography.Text>{t('storage.platform.operationDays')}</Typography.Text>
            <CompactNumberInput aria-label={t('storage.platform.operationDays')} min={b.operation_days_min} max={b.operation_days_max}
              precision={0} value={days} onChange={setDays} disabled={saving} after={t('batch.admin.days')} />
          </Space>
          <Typography.Text type="secondary">
            {t('storage.platform.operationDaysHint', { min: b.operation_days_min, max: b.operation_days_max })}
          </Typography.Text>
          <Space wrap>
            <Typography.Text>{t('storage.platform.recoverySets')}</Typography.Text>
            <CompactNumberInput aria-label={t('storage.platform.recoverySets')} min={b.recovery_sets_min} max={b.recovery_sets_max}
              precision={0} value={sets} onChange={setSets} disabled={saving} after={t('storage.platform.setsUnit')} />
          </Space>
          <Typography.Text type="secondary">
            {t('storage.platform.recoverySetsHint', { min: b.recovery_sets_min, max: b.recovery_sets_max })}
          </Typography.Text>
          <Confirm title={t('storage.platform.confirm', { days, sets })} description={t('storage.platform.confirmHint')}
            okText={t('storage.platform.apply')} onConfirm={save} irreversible disabled={!valid}>
            <Button type="primary" loading={saving} disabled={!changed || !valid} style={{ width: 'fit-content' }}>{t('common.save')}</Button>
          </Confirm>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            <Typography.Text strong>{t('storage.platform.fixedTitle')}</Typography.Text>
            {row(t('storage.platform.fixed.reviewPlans'), t('storage.platform.fixed.reviewPlansValue', { minutes: policy.fixed.review_plan_minutes, hours: policy.fixed.review_plan_grace_hours }))}
            {row(t('storage.platform.fixed.tasks'), t('storage.platform.fixed.tasksValue', { days: policy.fixed.task_days, rows: policy.fixed.task_rows_per_installation }))}
            {row(t('storage.platform.fixed.runnerLogs'), t('storage.platform.fixed.runnerLogsValue', { mib: policy.fixed.runner_log_mib }))}
            {row(t('storage.platform.fixed.uninstalled'), t('storage.platform.fixed.uninstalledValue'))}
          </div>

          {typeof counts.error === 'string' && counts.error !== '' ? (
            <Alert type="error" showIcon title={t('storage.platform.sweepFailed', { error: counts.error })} />
          ) : null}
          <Typography.Text type="secondary" role="status">
            {sweep
              ? t('storage.platform.lastSweep', {
                  at: formatReportDateTime(sweep.at), operations: num(counts.operations), plans: num(counts.plans),
                  tombstones: num(counts.tombstones), tasks: num(counts.tasks_reduced) + num(counts.tasks_capped),
                })
              : t('storage.platform.neverSwept')}
          </Typography.Text>
          {policy.recovery_set_pruning === 'blocked' && (
            <Alert type="info" showIcon title={t('storage.platform.pruneBlocked', { count: num(counts.recovery_sets_beyond) })} />
          )}
        </Space>
      )}
    </Card>
  )
}
