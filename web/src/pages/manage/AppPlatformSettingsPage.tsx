import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Descriptions, Empty, Input, Modal, Result, Space, Tag, Typography, theme } from 'antd'
import { AppstoreOutlined, ArrowLeftOutlined, DeleteOutlined, ImportOutlined, KeyOutlined, LinkOutlined, ReloadOutlined, SettingOutlined, ToolOutlined, VerticalAlignTopOutlined } from '@ant-design/icons'
import { Link, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, ApiError, errText } from '../../api/client'
import type { AppDisplay } from '../../lib/descriptor-contract.generated'
import { AppDisplayIcon, appDisplayText } from '../../lib/appDisplayV2'
import { usePrefs } from '../../prefs'
import { LoadingPlaceholder } from '../../components/LoadGate'
import StatusTag, { type StatusTone } from '../../components/StatusTag'
import { confirmAction } from '../../components/Confirm'
import AppNetworkProxyPanel from './AppNetworkProxyPanel'
import AppIngressPanel from './AppIngressPanel'
import AppPostgresPolicyPanel from './AppPostgresPolicyPanel'
import AppPublicProjectionPanel from './AppPublicProjectionPanel'
import AppMachineReadPanel from './AppMachineReadPanel'
import AppNetworkTargetsPanel from './AppNetworkTargetsPanel'
import type { NetworkPolicyView } from '../../lib/appNetworkTargetsV2'

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
  // The reviewed network.http destinations, present while that grant is on.
  networkPolicy?: NetworkPolicyView
  backend: { required: boolean; registered: boolean; enabled: boolean }
  data?: { id: string; state: string; format: number; quota_bytes: string; quota_inodes: string; observed_bytes: string; revision: string }
  recovery: { count: number; state?: string; pending: boolean; points: RecoveryPoint[] }
  configuration_gaps: string[]
  repair_eligible: boolean
  quota_raise_eligible?: boolean
  import_eligible?: boolean
  control_paused: boolean
  operation_pending?: boolean
  migration_recovery?: MigrationRecovery | null
  // Present while a release this installation needs has image blobs that are gone or unreadable.
  image_delivery?: ImageDelivery | null
}
type ImageDelivery = { state: 'reupload_required'; release_sha256: string; reason: string }
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
type QuotaReview = {
  action: 'set_data_quota'
  installation_id: string
  app_id: string
  publisher_id: string
  current_data_quota_bytes: string
  current_data_quota_inodes: string
  data_quota_bytes: string
  data_quota_inodes: string
  observed_bytes: string
  capacity: 'fits' | 'short'
  capacity_dimension?: string
  capacity_requested?: string
  capacity_remaining?: string
}
type QuotaPlan = { plan_id: string; plan_sha256: string; idempotency_key: string; review: QuotaReview }
type ImportReview = {
  action: 'import_data'
  installation_id: string
  app_id: string
  publisher_id: string
  archive_sha256: string
  archive_bytes: string
  archive_entries: string
  content_bytes: string
  archive_data_format: string
  release_data_format: string
  data_quota_bytes: string
  data_quota_inodes: string
  replaces_contents: true
  keeps_checkpoint: true
  requires_disabled: true
}
type ImportPlan = { plan_id: string; plan_sha256: string; idempotency_key: string; review: ImportReview }
// Platform bounds for a raised quota: the runner accepts any positive size up to 16 GiB.
const maxQuotaBytes = 17179869184n
const minQuotaInodes = 65536n
const maxQuotaInodes = 1000000n
const decimal = /^[1-9][0-9]*$/
const digits = /^[0-9]+$/
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
  const { modal } = App.useApp()
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
  const [raiseOpen, setRaiseOpen] = useState(false)
  const [raiseBytes, setRaiseBytes] = useState('')
  const [raiseInodes, setRaiseInodes] = useState('')
  const [raisePlan, setRaisePlan] = useState<QuotaPlan | null>(null)
  const [raiseError, setRaiseError] = useState('')
  const [raising, setRaising] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [importDigest, setImportDigest] = useState('')
  const [importPlan, setImportPlan] = useState<ImportPlan | null>(null)
  const [importError, setImportError] = useState('')
  const [importing, setImporting] = useState(false)
  const [restorePlan, setRestorePlan] = useState<RestorePlan | null>(null)
  const [restoreError, setRestoreError] = useState('')
  const [restoring, setRestoring] = useState(false)
  const [restorePointID, setRestorePointID] = useState('')
  const [migrationBusy, setMigrationBusy] = useState('')
  const [principalsOpen, setPrincipalsOpen] = useState(false)
  const [principalsBusy, setPrincipalsBusy] = useState(false)
  const [principalsError, setPrincipalsError] = useState('')

  useEffect(() => {
    setSettings(null)
    setBindings([])
    setBindingError('')
    setError('')
    setMissing(false)
    setRepairPlan(null)
    setRepairError('')
    setRepairing(false)
    setRaiseOpen(false); setRaisePlan(null); setRaiseError(''); setRaising(false)
    setImportOpen(false); setImportPlan(null); setImportError(''); setImporting(false); setImportDigest('')
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
  const usagePercent = settings?.data && BigInt(settings.data.quota_bytes) > 0n
    ? Number((BigInt(settings.data.observed_bytes) * 100n) / BigInt(settings.data.quota_bytes))
    : 0
  const usageTone = (percent: number): StatusTone => (percent >= 90 ? 'error' : percent >= 70 ? 'warning' : 'success')
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
  // principals.read is optional and off after install (doc 181 section 4). An administrator enables
  // it after reading the disclosure; withdrawal needs no second step because it only removes access.
  const principals = settings.capabilities.find((item) => item.capability === 'principals.read' && !item.required)
  const publicProjection = settings.capabilities.find((item) => item.capability === 'public.projection' && !item.required)
  const networkProxy = settings.capabilities.find((item) => item.capability === 'network.proxy' && !item.required)
  const machineRead = settings.capabilities.find((item) => item.capability === 'machine.read' && !item.required)
  const postgresProbe = settings.capabilities.find((item) => item.capability === 'network.postgres')
  const ingress = settings.capabilities.find((item) => item.capability === 'ingress.webhook' && !item.required)
  const principalsReady = settings.desired_state === 'enabled' && settings.phase === 'ready' && !settings.operation_pending
  const changePrincipals = async (enabled: boolean) => {
    if (principalsBusy) return
    setPrincipalsBusy(true); setPrincipalsError('')
    try {
      await api.post(`${root}/installations/${installation}/principals-read`, { enabled, policyRevision: settings.policy_revision })
      setPrincipalsOpen(false)
      setRefresh((value) => value + 1)
    } catch (failure) {
      setPrincipalsError(errText(failure, t))
    } finally {
      setPrincipalsBusy(false)
    }
  }
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
  const openRaise = () => {
    if (!settings.data) return
    setRaiseBytes(settings.data.quota_bytes); setRaiseInodes(settings.data.quota_inodes)
    setRaisePlan(null); setRaiseError(''); setRaiseOpen(true)
  }
  const closeRaise = () => { if (!raising) { setRaiseOpen(false); setRaisePlan(null); setRaiseError('') } }
  const prepareRaise = async () => {
    if (!settings.data || !settings.quota_raise_eligible || raising) return
    setRaiseError('')
    const bytesValue = raiseBytes.trim()
    const inodesValue = raiseInodes.trim()
    if (!decimal.test(bytesValue) || !decimal.test(inodesValue) ||
      BigInt(bytesValue) > maxQuotaBytes || BigInt(inodesValue) < minQuotaInodes || BigInt(inodesValue) > maxQuotaInodes) {
      setRaiseError(t('err.data_quota_invalid'))
      return
    }
    const nextBytes = BigInt(bytesValue)
    const nextInodes = BigInt(inodesValue)
    const currentBytes = BigInt(settings.data.quota_bytes)
    const currentInodes = BigInt(settings.data.quota_inodes)
    if (nextBytes < currentBytes || nextInodes < currentInodes || (nextBytes === currentBytes && nextInodes === currentInodes)) {
      setRaiseError(t('err.data_quota_lower_refused'))
      return
    }
    setRaising(true)
    try {
      const result = await api.post<{ plan_id: string; plan_sha256: string; expires_ms: string; review: QuotaReview }>(
        `${root}/plans`,
        { action: 'set_data_quota', installation_id: installation, data_quota_bytes: bytesValue, data_quota_inodes: inodesValue },
      )
      const review = result.review
      if (review.action !== 'set_data_quota' ||
        review.installation_id !== installation ||
        review.data_quota_bytes !== bytesValue || review.data_quota_inodes !== inodesValue ||
        !digits.test(review.current_data_quota_bytes) || !digits.test(review.current_data_quota_inodes) ||
        !digits.test(review.observed_bytes) ||
        (review.capacity !== 'fits' && review.capacity !== 'short') ||
        (review.capacity_requested != null && !digits.test(review.capacity_requested)) ||
        (review.capacity_remaining != null && !digits.test(review.capacity_remaining)) ||
        !uuid.test(result.plan_id) || !/^[0-9a-f]{64}$/.test(result.plan_sha256)) {
        throw new Error(t('appPlatform.invalidOperation'))
      }
      setRaisePlan({ ...result, idempotency_key: crypto.randomUUID() })
    } catch (failure) {
      setRaiseError(errText(failure, t))
    } finally {
      setRaising(false)
    }
  }
  const confirmRaise = async () => {
    if (!raisePlan || raisePlan.review.capacity !== 'fits' || raising) return
    setRaising(true); setRaiseError('')
    try {
      const result = await api.post<{ operation_id: string }>(`${root}/operations`, {
        plan_id: raisePlan.plan_id,
        plan_sha256: raisePlan.plan_sha256,
        idempotency_key: raisePlan.idempotency_key,
      })
      if (!uuid.test(result.operation_id)) throw new Error(t('appPlatform.invalidOperation'))
      setRaiseOpen(false); setRaisePlan(null)
      navigate(`/manage/apps?operation=${result.operation_id}`)
    } catch (failure) {
      setRaiseError(errText(failure, t))
    } finally {
      setRaising(false)
    }
  }
  const openImport = () => { setImportDigest(''); setImportPlan(null); setImportError(''); setImportOpen(true) }
  const closeImport = () => { if (!importing) { setImportOpen(false); setImportPlan(null); setImportError('') } }
  const prepareImport = async () => {
    if (!settings.import_eligible || importing) return
    setImportError('')
    const digest = importDigest.trim()
    if (!/^[0-9a-f]{64}$/.test(digest)) {
      setImportError(t('appPlatform.settings.importDigestInvalid'))
      return
    }
    setImporting(true)
    try {
      const result = await api.post<{ plan_id: string; plan_sha256: string; expires_ms: string; review: ImportReview }>(
        `${root}/plans`,
        { action: 'import_data', installation_id: installation, archive_sha256: digest },
      )
      const review = result.review
      if (review.action !== 'import_data' ||
        review.installation_id !== installation ||
        review.archive_sha256 !== digest ||
        !digits.test(review.archive_bytes) || !digits.test(review.archive_entries) || !digits.test(review.content_bytes) ||
        !digits.test(review.data_quota_bytes) || !digits.test(review.data_quota_inodes) ||
        review.replaces_contents !== true || review.keeps_checkpoint !== true || review.requires_disabled !== true ||
        !uuid.test(result.plan_id) || !/^[0-9a-f]{64}$/.test(result.plan_sha256)) {
        throw new Error(t('appPlatform.invalidOperation'))
      }
      setImportPlan({ ...result, idempotency_key: crypto.randomUUID() })
    } catch (failure) {
      setImportError(errText(failure, t))
    } finally {
      setImporting(false)
    }
  }
  // The import replaces the namespace's contents, so the review's confirmation asks once more, with the shared rules.
  const confirmImport = () => {
    if (!importPlan || importing) return
    confirmAction(modal, {
      title: t('appPlatform.settings.importConfirmTitle'),
      description: t('appPlatform.settings.importConfirmBody'),
      okText: t('appPlatform.settings.importAction'),
      onOk: () => commitImport(),
    }, t)
  }
  const commitImport = async () => {
    if (!importPlan || importing) return
    setImporting(true); setImportError('')
    try {
      const result = await api.post<{ operation_id: string }>(`${root}/operations`, {
        plan_id: importPlan.plan_id,
        plan_sha256: importPlan.plan_sha256,
        idempotency_key: importPlan.idempotency_key,
      })
      if (!uuid.test(result.operation_id)) throw new Error(t('appPlatform.invalidOperation'))
      setImportOpen(false); setImportPlan(null)
      navigate(`/manage/apps?operation=${result.operation_id}`)
    } catch (failure) {
      setImportError(errText(failure, t))
    } finally {
      setImporting(false)
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
      {principals && <Space orientation="vertical" size={8} style={{ width: '100%', marginTop: 12 }}>
        <Typography.Text type="secondary">{t('appPlatform.capability.principals.read')}</Typography.Text>
        {principalsError && !principalsOpen && <Alert type="error" showIcon title={principalsError} />}
        {principals.granted
          ? <Button loading={principalsBusy} disabled={!principalsReady} onClick={() => void changePrincipals(false)}>{t('appPlatform.settings.principals.withdraw')}</Button>
          : <Button disabled={!principalsReady} onClick={() => { setPrincipalsError(''); setPrincipalsOpen(true) }}>{t('appPlatform.settings.principals.enable')}</Button>}
        {!principalsReady && <Typography.Text type="secondary">{t('appPlatform.settings.principals.notReady')}</Typography.Text>}
      </Space>}
      {publicProjection && <AppPublicProjectionPanel root={root} installation={installation} granted={publicProjection.granted} ready={principalsReady} onChanged={() => setRefresh((value) => value + 1)} />}
      {networkProxy && <AppNetworkProxyPanel root={root} installation={installation} granted={networkProxy.granted} ready={principalsReady} onChanged={() => setRefresh((value) => value + 1)} />}
      {machineRead && <AppMachineReadPanel root={root} installation={installation} granted={machineRead.granted} ready={principalsReady} onChanged={() => setRefresh((value) => value + 1)} />}
      {postgresProbe && <AppPostgresPolicyPanel root={root} installation={installation} ready={principalsReady} onChanged={() => setRefresh((value) => value + 1)} />}
      {ingress && <AppIngressPanel root={root} installation={installation} granted={ingress.granted} ready={principalsReady} onChanged={() => setRefresh((value) => value + 1)} />}
    </Card>
    {settings.networkPolicy && <Card title={t('appPlatform.network.title')}>
      <AppNetworkTargetsPanel key={settings.networkPolicy.policy_sha256} installation={installation} policyRevision={settings.policy_revision} policy={settings.networkPolicy}
        ready={principalsReady} onChanged={() => setRefresh((value) => value + 1)} />
    </Card>}
    <Modal
      className="rp-platform-review-modal"
      open={principalsOpen}
      title={t('appPlatform.settings.principals.enableTitle')}
      okText={t('appPlatform.settings.principals.confirmEnable')}
      cancelText={t('common.cancel')}
      confirmLoading={principalsBusy}
      closable={!principalsBusy}
      mask={{ closable: false }}
      onOk={() => void changePrincipals(true)}
      onCancel={() => { if (!principalsBusy) { setPrincipalsOpen(false); setPrincipalsError('') } }}
    >
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Alert type="info" showIcon title={t('appPlatform.capability.principals.read')} />
        {principalsError && <Alert type="error" showIcon title={principalsError} />}
        <Typography.Text type="secondary">{t('appPlatform.settings.principals.hint')}</Typography.Text>
      </Space>
    </Modal>

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

    <Card title={t('appPlatform.settings.data')} extra={(canDeleteData || settings.quota_raise_eligible || settings.data)
      ? <Space wrap>
        {settings.quota_raise_eligible && <Button icon={<VerticalAlignTopOutlined aria-hidden="true" />} onClick={openRaise}>{t('appPlatform.settings.raiseQuota')}</Button>}
        {settings.data && <Button disabled={!settings.import_eligible} icon={<ImportOutlined aria-hidden="true" />} onClick={openImport}>{t('appPlatform.settings.importData')}</Button>}
        {canDeleteData && <Button danger loading={deleting} icon={<DeleteOutlined aria-hidden="true" />} onClick={() => void prepareDelete()}>{t('appPlatform.settings.deleteData')}</Button>}
      </Space>
      : undefined}>
      {settings.data && !settings.import_eligible && <Typography.Paragraph type="secondary" data-testid="import-disabled-hint">{t('appPlatform.settings.importDisabledHint')}</Typography.Paragraph>}
      {deleteError && !deletePlan && <Alert type="error" showIcon title={deleteError} />}
      {settings.data ? <Descriptions column={1} size="small" items={[
        { key: 'namespace', label: t('appPlatform.settings.dataNamespace'), children: <Typography.Text code>{settings.data.id}</Typography.Text> },
        { key: 'state', label: t('appPlatform.settings.dataState'), children: settings.data.state },
        { key: 'format', label: t('appPlatform.settings.dataFormat'), children: settings.data.format },
        { key: 'quota', label: t('appPlatform.settings.dataQuota'), children: bytes(settings.data.quota_bytes) },
        { key: 'inodes', label: t('appPlatform.settings.dataQuotaInodes'), children: new Intl.NumberFormat(lang).format(BigInt(settings.data.quota_inodes)) },
        { key: 'observed', label: t('appPlatform.settings.dataObserved'), children: bytes(settings.data.observed_bytes) },
        { key: 'usage', label: t('appPlatform.settings.dataUsage'), children: <StatusTag tone={usageTone(usagePercent)}>{t('appPlatform.settings.dataUsagePercent', { percent: usagePercent })}</StatusTag> },
      ]} /> : <Typography.Text type="secondary">{t('appPlatform.settings.noData')}</Typography.Text>}
    </Card>

    <Card title={t('appPlatform.settings.recovery')} extra={settings.repair_eligible
      ? <Button loading={repairing} icon={<ToolOutlined aria-hidden="true" />} onClick={() => void prepareRepair()}>{t('appPlatform.settings.repair')}</Button>
      : undefined}>
      {settings.control_paused && <Alert type="warning" showIcon title={t('err.app_platform_control_paused')} description={t('appPlatform.settings.restorePaused')} />}
      {repairError && !repairPlan && <Alert type="error" showIcon title={repairError} />}
      {restoreError && !restorePlan && <Alert type="error" showIcon title={restoreError} />}
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

    {settings.image_delivery && <Alert type="warning" showIcon data-testid="image-delivery-reupload"
      title={t('appPlatform.settings.imageDelivery.reuploadRequired')}
      description={t('appPlatform.settings.imageDelivery.reuploadDetail', { release: settings.image_delivery.release_sha256.slice(0, 12) })} />}

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
      {bindingError && <Alert type="warning" showIcon title={bindingError} />}
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
      open={raiseOpen}
      title={t('appPlatform.settings.raiseQuotaTitle')}
      okText={raisePlan ? t('appPlatform.settings.raiseQuotaAction') : t('appPlatform.settings.reviewPlan')}
      cancelText={t('common.cancel')}
      okButtonProps={{ disabled: raisePlan ? raisePlan.review.capacity !== 'fits' : false }}
      confirmLoading={raising}
      closable={!raising}
      mask={{ closable: false }}
      onOk={() => void (raisePlan ? confirmRaise() : prepareRaise())}
      onCancel={closeRaise}
    >
      <div role="document" tabIndex={0} className="rp-platform-review-scroll">
        <Space orientation="vertical" size={12} style={{ width: '100%' }}>
          {raiseError && <Alert type="error" showIcon title={raiseError} />}
          {!raisePlan && settings.data && <>
            <Typography.Text type="secondary">{t('appPlatform.settings.raiseQuotaHint', { bytes: bytes(settings.data.quota_bytes), inodes: new Intl.NumberFormat(lang).format(BigInt(settings.data.quota_inodes)) })}</Typography.Text>
            <label>{t('appPlatform.settings.raiseQuotaBytes')}
              <Input inputMode="numeric" value={raiseBytes} onChange={(event) => setRaiseBytes(event.target.value)} aria-label={t('appPlatform.settings.raiseQuotaBytes')} />
            </label>
            <label>{t('appPlatform.settings.raiseQuotaInodes')}
              <Input inputMode="numeric" value={raiseInodes} onChange={(event) => setRaiseInodes(event.target.value)} aria-label={t('appPlatform.settings.raiseQuotaInodes')} />
            </label>
          </>}
          {raisePlan && <>
            <Alert type="info" showIcon title={t('appPlatform.settings.raiseQuotaWarning')} />
            <Descriptions column={1} size="small" items={[
              { key: 'bytes', label: t('appPlatform.settings.dataQuota'), children: `${bytes(raisePlan.review.current_data_quota_bytes)} → ${bytes(raisePlan.review.data_quota_bytes)}` },
              { key: 'inodes', label: t('appPlatform.settings.dataQuotaInodes'), children: `${new Intl.NumberFormat(lang).format(BigInt(raisePlan.review.current_data_quota_inodes))} → ${new Intl.NumberFormat(lang).format(BigInt(raisePlan.review.data_quota_inodes))}` },
              { key: 'observed', label: t('appPlatform.settings.dataObserved'), children: bytes(raisePlan.review.observed_bytes) },
              { key: 'capacity', label: t('appPlatform.settings.raiseQuotaCapacity'), children: <StatusTag tone={raisePlan.review.capacity === 'fits' ? 'success' : 'error'}>{t(`appPlatform.settings.raiseQuota${raisePlan.review.capacity === 'fits' ? 'Fits' : 'NotFit'}`)}</StatusTag> },
            ]} />
            {raisePlan.review.capacity === 'short' && <Alert type="error" showIcon data-testid="raise-capacity-short"
              title={t('appPlatform.settings.raiseQuotaShort', {
                dimension: raisePlan.review.capacity_dimension ?? '—',
                requested: raisePlan.review.capacity_requested ?? '—',
                remaining: raisePlan.review.capacity_remaining ?? '—',
              })} />}
          </>}
        </Space>
      </div>
    </Modal>
    <Modal
      className="rp-platform-review-modal"
      open={importOpen}
      title={t('appPlatform.settings.importTitle')}
      okText={importPlan ? t('appPlatform.settings.importAction') : t('appPlatform.settings.reviewPlan')}
      cancelText={t('common.cancel')}
      okButtonProps={{ danger: !!importPlan }}
      confirmLoading={importing}
      closable={!importing}
      mask={{ closable: false }}
      onOk={() => void (importPlan ? confirmImport() : prepareImport())}
      onCancel={closeImport}
    >
      <div role="document" tabIndex={0} className="rp-platform-review-scroll">
        <Space orientation="vertical" size={12} style={{ width: '100%' }}>
          {importError && <Alert type="error" showIcon title={importError} />}
          {!importPlan && <>
            <Typography.Text type="secondary">{t('appPlatform.settings.importHint')}</Typography.Text>
            <Input value={importDigest} onChange={(event) => setImportDigest(event.target.value)} spellCheck={false} placeholder="sha256" aria-label={t('appPlatform.settings.importDigest')} />
          </>}
          {importPlan && <>
            <Alert type="error" showIcon title={t('appPlatform.settings.importReplaces')} />
            <Alert type="info" showIcon title={t('appPlatform.settings.importCheckpoint')} />
            <Alert type="warning" showIcon title={t('appPlatform.settings.importDisabled')} />
            <Descriptions column={1} size="small" items={[
              { key: 'digest', label: t('appPlatform.settings.importDigest'), children: <Typography.Text code copyable>{importPlan.review.archive_sha256}</Typography.Text> },
              { key: 'size', label: t('appPlatform.settings.importSize'), children: t('appPlatform.settings.importOfQuota', { value: bytes(importPlan.review.content_bytes), quota: bytes(importPlan.review.data_quota_bytes) }) },
              { key: 'archive', label: t('appPlatform.settings.importArchiveBytes'), children: bytes(importPlan.review.archive_bytes) },
              { key: 'entries', label: t('appPlatform.settings.importEntries'), children: t('appPlatform.settings.importOfQuota', { value: new Intl.NumberFormat(lang).format(BigInt(importPlan.review.archive_entries)), quota: new Intl.NumberFormat(lang).format(BigInt(importPlan.review.data_quota_inodes)) }) },
              { key: 'format', label: t('appPlatform.settings.dataFormat'), children: t('appPlatform.settings.importFormats', { archive: importPlan.review.archive_data_format, release: importPlan.review.release_data_format }) },
            ]} />
          </>}
        </Space>
      </div>
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
