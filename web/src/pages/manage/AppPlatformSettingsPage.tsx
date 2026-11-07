import { useEffect, useState } from 'react'
import { Alert, Button, Card, Descriptions, Empty, Modal, Result, Space, Tag, Typography, theme } from 'antd'
import { AppstoreOutlined, ArrowLeftOutlined, DeleteOutlined, KeyOutlined, LinkOutlined, ReloadOutlined, SettingOutlined, ToolOutlined } from '@ant-design/icons'
import { Link, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, ApiError, errText } from '../../api/client'
import type { AppDisplay } from '../../lib/descriptor-contract.generated'
import { AppDisplayIcon, appDisplayText } from '../../lib/appDisplayV2'
import { usePrefs } from '../../prefs'
import { LoadingPlaceholder } from '../../components/LoadGate'
import StatusTag, { type StatusTone } from '../../components/StatusTag'

type Audience = { type: 'user' | 'ou'; id: string; name: string }
type Capability = { capability: string; granted: boolean; required: boolean }
type RecoveryPoint = {
  id: string
  state: string
  checkpoint_id: string
  release_sha256: string
  format: number
  bytes: string
  verified_ms?: string
  restore_eligible: boolean
}
type Settings = {
  installation_id: string
  app_id: string
  publisher_id: string
  desired_state: string
  phase: string
  generation: string
  revision: string
  policy_revision: string
  current_release_sha256?: string
  version?: string
  display?: AppDisplay
  audience: Audience[]
  capabilities: Capability[]
  backend: { required: boolean; registered: boolean; enabled: boolean }
  data?: { id: string; state: string; format: number; quota_bytes: string; observed_bytes: string; revision: string }
  recovery: { count: number; state?: string; pending: boolean; points: RecoveryPoint[] }
  configuration_gaps: string[]
  repair_eligible: boolean
  control_paused: boolean
  migration_recovery?: MigrationRecovery | null
}
type MigrationChoice =
  | { kind: 'rollback'; recovery_set_id: string }
  | { kind: 'retry'; recovers_operation_id: string; release_sha256: string; trust_revision: string }
type MigrationRecovery = {
  state: 'settling' | 'settled' | 'blocked'
  operation_id: string
  // Why a blocked settlement is stuck, when the runner said so. An open value: only known ones have their own sentence.
  reason?: string
  choices: MigrationChoice[]
}
type Binding = {
  id: string
  name: string
  credential_label: string
  operation: string
  state: string
  revision: number
}
type DeleteReview = {
  action: 'delete_data'
  installation_id: string
  app_id: string
  publisher_id: string
  data_namespace_id: string
  release_sha256: string
  source_generation: string
  target_generation: string
  data_format: string
  writer_fence: string
  scope: string
}
type DeletePlan = { plan_id: string; plan_sha256: string; idempotency_key: string; review: DeleteReview }
type RepairReview = {
  action: 'repair'
  installation_id: string
  app_id: string
  publisher_id: string
  release_sha256: string
  source_generation: string
  target_generation: string
  policy_revision: string
  grants_sha256: string
  data_effect: 'checkpoint_and_retain'
}
type RepairPlan = { plan_id: string; plan_sha256: string; idempotency_key: string; review: RepairReview }
type RestoreReview = {
  action: 'restore'
  installation_id: string
  recovery_set_id: string
  checkpoint_operation_id: string
  checkpoint_effect_id: string
  checkpoint_id: string
  release_sha256: string
  manifest_sha256: string
  archive_bytes: string
  source_generation: string
  target_generation: string
  data_effect: 'restore_checkpoint'
}
type UpdateReview = {
  action: 'update'
  installation_id: string
  release_sha256?: string
  source_generation?: string
  target_generation?: string
}
type RestorePlan = { plan_id: string; plan_sha256: string; idempotency_key: string; review: RestoreReview | UpdateReview }
const knownMigrationReasons = ['foreign_container']
const migrationTone = { settling: 'processing', settled: 'success', blocked: 'error' } as const
const root = '/api/admin/app-platform'
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

function phaseTone(settings: Settings): StatusTone {
  if (settings.phase === 'ready' && settings.desired_state === 'enabled') return 'success'
  if (settings.phase === 'disabled') return 'neutral'
  if (settings.phase === 'pending' || settings.phase === 'setup_required') return 'processing'
  if (settings.phase === 'review_required' || settings.phase === 'recovery_required' || settings.phase === 'quarantined') return 'error'
  return 'neutral'
}

export default function AppPlatformSettingsPage() {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  const { token } = theme.useToken()
  const navigate = useNavigate()
  const installation = useParams().installation ?? ''
  const [settings, setSettings] = useState<Settings | null>(null)
  const [bindings, setBindings] = useState<Binding[]>([])
  const [bindingError, setBindingError] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [missing, setMissing] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const [deletePlan, setDeletePlan] = useState<DeletePlan | null>(null)
  const [deleteError, setDeleteError] = useState('')
  const [deleting, setDeleting] = useState(false)
  const [repairPlan, setRepairPlan] = useState<RepairPlan | null>(null)
  const [repairError, setRepairError] = useState('')
  const [repairing, setRepairing] = useState(false)
  const [restorePlan, setRestorePlan] = useState<RestorePlan | null>(null)
  const [restoreError, setRestoreError] = useState('')
  const [restoring, setRestoring] = useState(false)
  const [restorePointID, setRestorePointID] = useState('')
  const [migrationBusy, setMigrationBusy] = useState('')

  useEffect(() => {
    setSettings(null)
    setBindings([])
    setBindingError('')
    setError('')
    setMissing(false)
    setRepairPlan(null)
    setRepairError('')
    setRepairing(false)
    setRestorePlan(null)
    setRestoreError('')
    setRestoring(false)
    setRestorePointID('')
    if (!uuid.test(installation)) {
      setLoading(false)
      setMissing(true)
      setError(t('appPlatform.settings.notFound'))
      return
    }
    const abort = new AbortController()
    setLoading(true)
    Promise.all([
      api.get<Settings>(`${root}/installations/${installation}/settings`, { signal: abort.signal }),
      api.get<{ bindings: Binding[] }>(`${root}/installations/${installation}/secret-bindings`, { signal: abort.signal })
        .catch((failure) => {
          if (!abort.signal.aborted) setBindingError(errText(failure, t))
          return { bindings: [] }
        }),
    ]).then(([next, credentialResult]) => {
      if (abort.signal.aborted) return
      setSettings(next)
      setBindings(credentialResult.bindings ?? [])
    }).catch((failure) => {
      if (!abort.signal.aborted) {
        setMissing(failure instanceof ApiError && failure.status === 404)
        setError(errText(failure, t))
      }
    }).finally(() => {
      if (!abort.signal.aborted) setLoading(false)
    })
    return () => abort.abort()
  }, [installation, refresh, t])

  if (loading) return <LoadingPlaceholder minHeight={320} />
  if (!settings) {
    return <Result
      status={missing ? '404' : 'error'}
      title={t(missing ? 'appPlatform.settings.notFound' : 'common.loadFailed')}
      subTitle={error || undefined}
      extra={<Space>
        {!missing && <Button icon={<ReloadOutlined aria-hidden="true" />} onClick={() => setRefresh((value) => value + 1)}>{t('common.retry')}</Button>}
        <Link to="/manage/apps">{t('appPlatform.settings.back')}</Link>
      </Space>}
    />
  }

  const copy = appDisplayText(settings.app_id, settings.display, lang)
  const capabilityLabel = (capability: string) => t(`appPlatform.capability.${capability}`, { defaultValue: capability })
  const gapLabel = (gap: string) => {
    if (gap.startsWith('capability:')) return t('appPlatform.settings.missingCapability', { capability: capabilityLabel(gap.slice('capability:'.length)) })
    if (gap.startsWith('secret_binding:')) return t('appPlatform.settings.missingSecretBinding', { binding: gap.slice('secret_binding:'.length) })
    return t(`appPlatform.settings.gap.${gap}`, { defaultValue: t('appPlatform.settings.gap.unknown') })
  }
  const setupRequired = settings.configuration_gaps.some((gap) => gap === 'setup_required' || gap.startsWith('secret_binding:'))
  const bytes = (value: string) => t('appPlatform.settings.bytes', {
    value: new Intl.NumberFormat(lang).format(BigInt(value)),
  })
  const timestamp = (value?: string) => {
    const milliseconds = Number(value)
    return value && Number.isSafeInteger(milliseconds)
      ? new Intl.DateTimeFormat(lang, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(milliseconds))
      : '—'
  }
  const canDeleteData = !!settings.data && settings.backend.required && settings.backend.registered &&
    (settings.desired_state === 'disabled' || settings.desired_state === 'removed')
  const prepareDelete = async () => {
    if (!settings.data || deleting) return
    setDeleting(true); setDeleteError('')
    try {
      const result = await api.post<{ plan_id: string; plan_sha256: string; expires_ms: string; review: DeleteReview }>(
        `${root}/plans`,
        { action: 'delete_data', installation_id: installation, data_namespace_id: settings.data.id },
      )
      if (result.review.action !== 'delete_data' ||
        result.review.installation_id !== installation ||
        result.review.data_namespace_id !== settings.data.id ||
        !uuid.test(result.plan_id) || !/^[0-9a-f]{64}$/.test(result.plan_sha256)) {
        throw new Error(t('appPlatform.invalidOperation'))
      }
      setDeletePlan({ ...result, idempotency_key: crypto.randomUUID() })
    } catch (failure) {
      setDeleteError(errText(failure, t))
    } finally {
      setDeleting(false)
    }
  }
  const confirmDelete = async () => {
    if (!deletePlan || deleting) return
    setDeleting(true); setDeleteError('')
    try {
      const result = await api.post<{ operation_id: string }>(`${root}/operations`, {
        plan_id: deletePlan.plan_id,
        plan_sha256: deletePlan.plan_sha256,
        idempotency_key: deletePlan.idempotency_key,
      })
      if (!uuid.test(result.operation_id)) throw new Error(t('appPlatform.invalidOperation'))
      setDeletePlan(null)
      navigate(`/manage/apps?operation=${result.operation_id}`)
    } catch (failure) {
      setDeleteError(errText(failure, t))
    } finally {
      setDeleting(false)
    }
  }
  const prepareRepair = async () => {
    if (!settings.repair_eligible || repairing) return
    setRepairing(true); setRepairError('')
    try {
      const result = await api.post<{ plan_id: string; plan_sha256: string; expires_ms: string; review: RepairReview }>(
        `${root}/plans`,
        { action: 'repair', installation_id: installation },
      )
      if (result.review.action !== 'repair' ||
        result.review.installation_id !== installation ||
        result.review.release_sha256 !== settings.current_release_sha256 ||
        result.review.data_effect !== 'checkpoint_and_retain' ||
        !uuid.test(result.plan_id) || !/^[0-9a-f]{64}$/.test(result.plan_sha256)) {
        throw new Error(t('appPlatform.invalidOperation'))
      }
      setRepairPlan({ ...result, idempotency_key: crypto.randomUUID() })
    } catch (failure) {
      setRepairError(errText(failure, t))
    } finally {
      setRepairing(false)
    }
  }
  const confirmRepair = async () => {
    if (!repairPlan || repairing) return
    setRepairing(true); setRepairError('')
    try {
      const result = await api.post<{ operation_id: string }>(`${root}/operations`, {
        plan_id: repairPlan.plan_id,
        plan_sha256: repairPlan.plan_sha256,
        idempotency_key: repairPlan.idempotency_key,
      })
      if (!uuid.test(result.operation_id)) throw new Error(t('appPlatform.invalidOperation'))
      setRepairPlan(null)
      navigate(`/manage/apps?operation=${result.operation_id}`)
    } catch (failure) {
      setRepairError(errText(failure, t))
    } finally {
      setRepairing(false)
    }
  }
  const prepareRestore = async (point: RecoveryPoint) => {
    if (!point.restore_eligible) return
    await requestRestorePlan(point.id, point.id, point)
  }
  // Shared review step for restore-shaped plans: posts the plan request, validates the
  // echoed review against what was asked, and opens the common review modal.
  const requestReviewPlan = async (busyKey: string, body: Record<string, string>, accept: (review: RestoreReview | UpdateReview) => boolean) => {
    if (settings.control_paused || restoring) return
    setRestoring(true); setRestoreError(''); setRestorePointID(busyKey)
    try {
      const result = await api.post<{ plan_id: string; plan_sha256: string; expires_ms: string; review: RestoreReview | UpdateReview }>(
        `${root}/plans`,
        { ...body, installation_id: installation },
      )
      if (!accept(result.review) ||
        result.review.installation_id !== installation ||
        !uuid.test(result.plan_id) || !/^[0-9a-f]{64}$/.test(result.plan_sha256)) {
        throw new Error(t('appPlatform.invalidOperation'))
      }
      setRestorePlan({ ...result, idempotency_key: crypto.randomUUID() })
    } catch (failure) {
      setRestoreError(errText(failure, t))
    } finally {
      setRestoring(false)
      setRestorePointID('')
    }
  }
  const requestRestorePlan = (recoverySetID: string, busyKey: string, point?: RecoveryPoint) =>
    requestReviewPlan(busyKey, { action: 'restore', recovery_set_id: recoverySetID }, (review) =>
      review.action === 'restore' &&
      review.recovery_set_id === recoverySetID &&
      (!point || (review.checkpoint_id === point.checkpoint_id && review.release_sha256 === point.release_sha256)) &&
      review.data_effect === 'restore_checkpoint')
  const rollbackMigration = (choice: Extract<MigrationChoice, { kind: 'rollback' }>) => {
    setMigrationBusy('rollback')
    const point = settings.recovery.points.find((candidate) => candidate.id === choice.recovery_set_id)
    return requestRestorePlan(choice.recovery_set_id, 'migration-rollback', point).finally(() => setMigrationBusy(''))
  }
  const retryMigration = (choice: Extract<MigrationChoice, { kind: 'retry' }>) => {
    setMigrationBusy('retry')
    return requestReviewPlan('migration-retry', {
      action: 'update',
      release_sha256: choice.release_sha256,
      trust_revision: choice.trust_revision,
      recovers_operation_id: choice.recovers_operation_id,
    }, (review) => review.action === 'update').finally(() => setMigrationBusy(''))
  }
  const confirmRestore = async () => {
    if (!restorePlan || restoring) return
    setRestoring(true); setRestoreError('')
    try {
      const result = await api.post<{ operation_id: string }>(`${root}/operations`, {
        plan_id: restorePlan.plan_id,
        plan_sha256: restorePlan.plan_sha256,
        idempotency_key: restorePlan.idempotency_key,
      })
      if (!uuid.test(result.operation_id)) throw new Error(t('appPlatform.invalidOperation'))
      setRestorePlan(null)
      navigate(`/manage/apps?operation=${result.operation_id}`)
    } catch (failure) {
      setRestoreError(errText(failure, t))
    } finally {
      setRestoring(false)
    }
  }
  return <Space className="rp-platform-settings" orientation="vertical" size={16} style={{ width: '100%' }}>
    <Link to="/manage/apps"><ArrowLeftOutlined aria-hidden="true" /> {t('appPlatform.settings.back')}</Link>
    <Typography.Title level={4} style={{ margin: 0 }}>{t('appPlatform.settings.title')}</Typography.Title>
    {error && <Alert type="error" showIcon title={error} action={<Button size="small" icon={<ReloadOutlined aria-hidden="true" />} onClick={() => setRefresh((value) => value + 1)}>{t('common.retry')}</Button>} />}
    <Card>
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 16, flexWrap: 'wrap' }}>
        {settings.display ? <AppDisplayIcon display={settings.display} /> : <AppstoreOutlined aria-hidden="true" style={{ fontSize: 32, color: token.colorPrimary }} />}
        <div style={{ minWidth: 0, flex: '1 1 240px' }}>
          <Typography.Title level={4} style={{ margin: 0 }}>{copy.name}</Typography.Title>
          <Typography.Text type="secondary">{settings.publisher_id} · {settings.app_id}</Typography.Text>
        </div>
        <StatusTag className={phaseTone(settings) === 'success' ? 'rp-platform-success-tag' : undefined} tone={phaseTone(settings)}>{t(`appPlatform.settings.phase.${settings.phase}`, { defaultValue: settings.phase })}</StatusTag>
      </div>
      {copy.description && <Typography.Paragraph type="secondary">{copy.description}</Typography.Paragraph>}
      <Descriptions column={1} size="small" items={[
        { key: 'state', label: t('appPlatform.settings.state'), children: t(`appPlatform.settings.desired.${settings.desired_state}`, { defaultValue: settings.desired_state }) },
        { key: 'generation', label: t('appPlatform.settings.generation'), children: settings.generation },
        { key: 'policy', label: t('appPlatform.settings.policyRevision'), children: settings.policy_revision },
        { key: 'release', label: t('appPlatform.settings.currentRelease'), children: settings.current_release_sha256 ? <Space orientation="vertical" size={0}><span>{settings.version || '—'}</span><Typography.Text code copyable>{settings.current_release_sha256}</Typography.Text></Space> : t('appPlatform.settings.noRelease') },
      ]} />
      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', marginTop: 16 }}>
        {(settings.desired_state === 'enabled' && settings.phase === 'ready') && <Link to={`/apps/v2/${installation}`}>{t('appPlatform.settings.openApplication')}</Link>}
        <Link to={`/manage/apps?installation=${installation}`}><SettingOutlined aria-hidden="true" /> {t('appPlatform.settings.lifecycleActions')}</Link>
      </div>
    </Card>

    <Card title={t('appPlatform.settings.configuration')} extra={setupRequired
      ? <Link to={`/manage/apps/${encodeURIComponent(settings.app_id)}/credentials`}><KeyOutlined aria-hidden="true" /> {t('appPlatform.settings.manageCredentials')}</Link>
      : undefined}>
      {settings.configuration_gaps.length
        ? <Alert type="warning" showIcon title={t('appPlatform.settings.configurationNeeded')} description={<ul style={{ margin: 0, paddingInlineStart: 20 }}>{settings.configuration_gaps.map((gap) => <li key={gap}>{gapLabel(gap)}</li>)}</ul>} />
        : <Alert type="success" showIcon title={t('appPlatform.settings.configurationReady')} />}
    </Card>

    <Card title={t('appPlatform.settings.audience')}>
      {settings.audience.length
        ? <Space size={[6, 6]} wrap>{settings.audience.map((item) => <Tag key={`${item.type}:${item.id}`} style={{ maxWidth: '100%', height: 'auto', whiteSpace: 'normal', overflowWrap: 'anywhere' }}>{t(`appPlatform.settings.principal.${item.type}`)}: {item.name} ({item.id})</Tag>)}</Space>
        : <Typography.Text type="secondary">{t('appPlatform.settings.noAudience')}</Typography.Text>}
    </Card>

    <Card title={t('appPlatform.settings.capabilities')}>
      {settings.capabilities.length
        ? <Space size={[6, 6]} wrap>{settings.capabilities.map((item) => <StatusTag key={item.capability} className={item.granted ? 'rp-platform-success-tag' : undefined} tone={item.granted ? 'success' : item.required ? 'error' : 'neutral'}>
          {capabilityLabel(item.capability)} · {item.granted ? t('appPlatform.settings.granted') : t('appPlatform.settings.declined')}{item.required ? ` · ${t('appPlatform.settings.required')}` : ''}
        </StatusTag>)}</Space>
        : <Typography.Text type="secondary">{t('appPlatform.settings.noCapabilities')}</Typography.Text>}
    </Card>

    <Card title={t('appPlatform.settings.backend')}>
      <Descriptions column={1} size="small" items={[
        { key: 'required', label: t('appPlatform.settings.backendRequired'), children: settings.backend.required ? t('appPlatform.settings.yes') : t('appPlatform.settings.staticOnly') },
        { key: 'state', label: t('appPlatform.settings.backendState'), children: settings.backend.required
          ? settings.backend.registered
            ? settings.backend.enabled ? t('appPlatform.settings.backendEnabled') : t('appPlatform.settings.backendDisabled')
            : t('appPlatform.settings.backendNotRegistered')
          : t('appPlatform.settings.notApplicable') },
      ]} />
    </Card>

    <Card title={t('appPlatform.settings.data')} extra={canDeleteData
      ? <Button danger loading={deleting} icon={<DeleteOutlined aria-hidden="true" />} onClick={() => void prepareDelete()}>{t('appPlatform.settings.deleteData')}</Button>
      : undefined}>
      {deleteError && !deletePlan && <Alert type="error" showIcon title={deleteError} style={{ marginBottom: 12 }} />}
      {settings.data ? <Descriptions column={1} size="small" items={[
        { key: 'namespace', label: t('appPlatform.settings.dataNamespace'), children: <Typography.Text code>{settings.data.id}</Typography.Text> },
        { key: 'state', label: t('appPlatform.settings.dataState'), children: settings.data.state },
        { key: 'format', label: t('appPlatform.settings.dataFormat'), children: settings.data.format },
        { key: 'quota', label: t('appPlatform.settings.dataQuota'), children: bytes(settings.data.quota_bytes) },
        { key: 'observed', label: t('appPlatform.settings.dataObserved'), children: bytes(settings.data.observed_bytes) },
      ]} /> : <Typography.Text type="secondary">{t('appPlatform.settings.noData')}</Typography.Text>}
    </Card>

    <Card title={t('appPlatform.settings.recovery')} extra={settings.repair_eligible
      ? <Button loading={repairing} icon={<ToolOutlined aria-hidden="true" />} onClick={() => void prepareRepair()}>{t('appPlatform.settings.repair')}</Button>
      : undefined}>
      {settings.control_paused && <Alert type="warning" showIcon title={t('err.app_platform_control_paused')} description={t('appPlatform.settings.restorePaused')} style={{ marginBottom: 12 }} />}
      {repairError && !repairPlan && <Alert type="error" showIcon title={repairError} style={{ marginBottom: 12 }} />}
      {restoreError && !restorePlan && <Alert type="error" showIcon title={restoreError} style={{ marginBottom: 12 }} />}
      {settings.recovery.points.length
        ? <Space orientation="vertical" size={12} style={{ width: '100%' }}>
          {settings.recovery.points.map((point) => <div key={point.id} style={{ borderBottom: `1px solid ${token.colorBorderSecondary}`, paddingBottom: 12 }}>
            <Descriptions column={1} size="small" items={[
              { key: 'id', label: t('appPlatform.settings.recoveryPoint'), children: <Typography.Text code>{point.id}</Typography.Text> },
              { key: 'state', label: t('appPlatform.settings.recoveryState'), children: point.state },
              { key: 'release', label: t('appPlatform.settings.currentRelease'), children: <Typography.Text code>{point.release_sha256}</Typography.Text> },
              { key: 'checkpoint', label: t('appPlatform.settings.recoveryCheckpoint'), children: <Typography.Text code>{point.checkpoint_id}</Typography.Text> },
              { key: 'format', label: t('appPlatform.settings.dataFormat'), children: point.format },
              { key: 'bytes', label: t('appPlatform.settings.recoveryBytes'), children: bytes(point.bytes) },
              { key: 'verified', label: t('appPlatform.settings.recoveryVerified'), children: timestamp(point.verified_ms) },
            ]} />
            <Space wrap>
              <Button disabled={!point.restore_eligible || settings.control_paused || restoring} loading={restoring && restorePointID === point.id} onClick={() => void prepareRestore(point)}>{t('appPlatform.settings.restore')}</Button>
              {!point.restore_eligible && <Typography.Text type="secondary">{t('appPlatform.settings.restoreUnavailable')}</Typography.Text>}
            </Space>
          </div>)}
        </Space>
        : settings.recovery.count
        ? <Descriptions column={1} size="small" items={[
          { key: 'state', label: t('appPlatform.settings.recoveryState'), children: settings.recovery.state || '—' },
          { key: 'pending', label: t('appPlatform.settings.recoveryPending'), children: settings.recovery.pending ? t('appPlatform.settings.yes') : t('appPlatform.settings.no') },
        ]} />
        : <Typography.Text type="secondary">{t('appPlatform.settings.noRecovery')}</Typography.Text>}
    </Card>

    {settings.migration_recovery && <Card title={t('appPlatform.settings.migrationRecovery.title')} extra={<Tag color={migrationTone[settings.migration_recovery.state]}>{t(`appPlatform.settings.migrationRecovery.state.${settings.migration_recovery.state}`)}</Tag>}>
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Typography.Text>{t(`appPlatform.settings.migrationRecovery.detail.${settings.migration_recovery.state}`)}</Typography.Text>
        {settings.migration_recovery.state === 'blocked' && settings.migration_recovery.reason && <Alert type="error" showIcon data-testid="migration-blocked-reason"
          title={knownMigrationReasons.includes(settings.migration_recovery.reason)
            ? t(`appPlatform.settings.migrationRecovery.reason.${settings.migration_recovery.reason}`)
            : t('appPlatform.settings.migrationRecovery.reason.unknown', { reason: settings.migration_recovery.reason })} />}
        {settings.migration_recovery.state === 'settled' && settings.migration_recovery.choices.length > 0 && <Space wrap>
          {settings.migration_recovery.choices.map((choice) => choice.kind === 'rollback'
            ? <Button key={`rollback-${choice.recovery_set_id}`} disabled={settings.control_paused || restoring} loading={migrationBusy === 'rollback'} onClick={() => void rollbackMigration(choice)}>{t('appPlatform.settings.migrationRecovery.rollback')}</Button>
            : <Button key={`retry-${choice.recovers_operation_id}`} type="primary" disabled={settings.control_paused || restoring} loading={migrationBusy === 'retry'} onClick={() => void retryMigration(choice)}>{t('appPlatform.settings.migrationRecovery.retry')}</Button>)}
        </Space>}
      </Space>
    </Card>}

    <Card title={t('appPlatform.settings.credentials')} extra={<Link to={`/manage/apps/${encodeURIComponent(settings.app_id)}/credentials`}><KeyOutlined aria-hidden="true" /> {t('appPlatform.settings.manageCredentials')}</Link>}>
      {bindingError && <Alert type="warning" showIcon title={bindingError} style={{ marginBottom: 12 }} />}
      {bindings.length ? <Space orientation="vertical" style={{ width: '100%' }}>
        {bindings.map((binding) => <div key={binding.id} style={{ display: 'flex', justifyContent: 'space-between', gap: 12, flexWrap: 'wrap', borderBottom: `1px solid ${token.colorBorderSecondary}`, paddingBlock: 8 }}>
          <Space size={8} wrap><Typography.Text strong>{binding.name}</Typography.Text><Tag>{binding.operation}</Tag><StatusTag className={binding.state === 'active' ? 'rp-platform-success-tag' : undefined} tone={binding.state === 'active' ? 'success' : binding.state === 'review_required' ? 'warning' : 'neutral'}>{binding.state}</StatusTag></Space>
          <Typography.Text type="secondary">{binding.credential_label} · {t('appPlatform.settings.bindingRevision', { revision: binding.revision })}</Typography.Text>
        </div>)}
      </Space> : (!bindingError && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('appPlatform.settings.noCredentials')} />)}
    </Card>
    <Modal
      className="rp-platform-review-modal"
      open={!!deletePlan}
      title={t('appPlatform.settings.deleteDataTitle')}
      okText={t('appPlatform.settings.deleteDataAction')}
      cancelText={t('common.cancel')}
      okButtonProps={{ danger: true }}
      confirmLoading={deleting}
      closable={!deleting}
      mask={{ closable: false }}
      onOk={() => void confirmDelete()}
      onCancel={() => { if (!deleting) { setDeletePlan(null); setDeleteError('') } }}
    >
      {deletePlan && <div role="document" tabIndex={0} className="rp-platform-review-scroll">
        <Space orientation="vertical" size={12} style={{ width: '100%' }}>
          <Alert type="error" showIcon title={t('appPlatform.settings.deleteDataWarning')} />
          {deleteError && <Alert type="error" showIcon title={deleteError} />}
          <Descriptions column={1} size="small" items={[
            { key: 'namespace', label: t('appPlatform.settings.dataNamespace'), children: <Typography.Text code>{deletePlan.review.data_namespace_id}</Typography.Text> },
            { key: 'generation', label: t('appPlatform.settings.generation'), children: `${deletePlan.review.source_generation} → ${deletePlan.review.target_generation}` },
            { key: 'format', label: t('appPlatform.settings.dataFormat'), children: deletePlan.review.data_format },
            { key: 'scope', label: t('appPlatform.settings.deleteDataScope'), children: deletePlan.review.scope },
          ]} />
        </Space>
      </div>}
    </Modal>
    <Modal
      className="rp-platform-review-modal"
      open={!!repairPlan}
      title={t('appPlatform.settings.repairTitle')}
      okText={t('appPlatform.settings.repairAction')}
      cancelText={t('common.cancel')}
      confirmLoading={repairing}
      closable={!repairing}
      mask={{ closable: false }}
      onOk={() => void confirmRepair()}
      onCancel={() => { if (!repairing) { setRepairPlan(null); setRepairError('') } }}
    >
      {repairPlan && <div role="document" tabIndex={0} className="rp-platform-review-scroll">
        <Space orientation="vertical" size={12} style={{ width: '100%' }}>
          <Alert type="info" showIcon title={t('appPlatform.settings.repairWarning')} />
          {repairError && <Alert type="error" showIcon title={repairError} />}
          <Descriptions column={1} size="small" items={[
            { key: 'release', label: t('appPlatform.settings.currentRelease'), children: <Typography.Text code copyable>{repairPlan.review.release_sha256}</Typography.Text> },
            { key: 'generation', label: t('appPlatform.settings.generation'), children: `${repairPlan.review.source_generation} → ${repairPlan.review.target_generation}` },
            { key: 'policy', label: t('appPlatform.settings.policyRevision'), children: repairPlan.review.policy_revision },
            { key: 'data', label: t('appPlatform.storage'), children: t('appPlatform.settings.repairDataRetained') },
            { key: 'grants', label: t('appPlatform.settings.repairGrants'), children: t('appPlatform.settings.repairGrantsUnchanged') },
          ]} />
        </Space>
      </div>}
    </Modal>
    <Modal
      className="rp-platform-review-modal"
      open={!!restorePlan}
      title={restorePlan?.review.action === 'update' ? t('appPlatform.settings.migrationRecovery.retryTitle') : t('appPlatform.settings.restoreTitle')}
      okText={restorePlan?.review.action === 'update' ? t('appPlatform.settings.migrationRecovery.retryAction') : t('appPlatform.settings.restoreAction')}
      cancelText={t('common.cancel')}
      okButtonProps={{ danger: restorePlan?.review.action !== 'update' }}
      confirmLoading={restoring}
      closable={!restoring}
      mask={{ closable: false }}
      onOk={() => void confirmRestore()}
      onCancel={() => { if (!restoring) { setRestorePlan(null); setRestoreError('') } }}
    >
      {restorePlan && <div role="document" tabIndex={0} className="rp-platform-review-scroll">
        <Space orientation="vertical" size={12} style={{ width: '100%' }}>
          <Alert type="warning" showIcon title={restorePlan.review.action === 'update' ? t('appPlatform.settings.migrationRecovery.retryWarning') : t('appPlatform.settings.restoreWarning')} />
          {restoreError && <Alert type="error" showIcon title={restoreError} />}
          {restorePlan.review.action === 'update' ? <Descriptions column={1} size="small" items={[
            ...(restorePlan.review.release_sha256 ? [{ key: 'release', label: t('appPlatform.settings.currentRelease'), children: <Typography.Text code>{restorePlan.review.release_sha256}</Typography.Text> }] : []),
            ...(restorePlan.review.source_generation && restorePlan.review.target_generation ? [{ key: 'generation', label: t('appPlatform.settings.generation'), children: `${restorePlan.review.source_generation} → ${restorePlan.review.target_generation}` }] : []),
          ]} /> : <Descriptions column={1} size="small" items={[
            { key: 'recovery', label: t('appPlatform.settings.recoveryPoint'), children: <Typography.Text code>{restorePlan.review.recovery_set_id}</Typography.Text> },
            { key: 'checkpoint', label: t('appPlatform.settings.recoveryCheckpoint'), children: <Typography.Text code>{restorePlan.review.checkpoint_id}</Typography.Text> },
            { key: 'release', label: t('appPlatform.settings.currentRelease'), children: <Typography.Text code>{restorePlan.review.release_sha256}</Typography.Text> },
            { key: 'manifest', label: t('appPlatform.settings.recoveryManifest'), children: <Typography.Text code>{restorePlan.review.manifest_sha256}</Typography.Text> },
            { key: 'generation', label: t('appPlatform.settings.generation'), children: `${restorePlan.review.source_generation} → ${restorePlan.review.target_generation}` },
            { key: 'bytes', label: t('appPlatform.settings.recoveryBytes'), children: bytes(restorePlan.review.archive_bytes) },
            { key: 'scope', label: t('appPlatform.settings.restoreScope'), children: t('appPlatform.settings.restoreScopeValue') },
          ]} />}
        </Space>
      </div>}
    </Modal>
  </Space>
}
