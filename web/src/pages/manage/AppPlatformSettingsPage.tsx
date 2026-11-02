import { useEffect, useState } from 'react'
import { Alert, Button, Card, Descriptions, Empty, Modal, Result, Space, Tag, Typography, theme } from 'antd'
import { AppstoreOutlined, ArrowLeftOutlined, DeleteOutlined, KeyOutlined, LinkOutlined, ReloadOutlined, SettingOutlined } from '@ant-design/icons'
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
  recovery: { count: number; state?: string; pending: boolean }
  configuration_gaps: string[]
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

  useEffect(() => {
    setSettings(null)
    setBindings([])
    setBindingError('')
    setError('')
    setMissing(false)
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
    return t(`appPlatform.settings.gap.${gap}`, { defaultValue: t('appPlatform.settings.gap.unknown') })
  }
  const bytes = (value: string) => t('appPlatform.settings.bytes', {
    value: new Intl.NumberFormat(lang).format(BigInt(value)),
  })
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

    <Card title={t('appPlatform.settings.configuration')}>
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

    <Card title={t('appPlatform.settings.recovery')}>
      {settings.recovery.count
        ? <Descriptions column={1} size="small" items={[
          { key: 'state', label: t('appPlatform.settings.recoveryState'), children: settings.recovery.state || '—' },
          { key: 'pending', label: t('appPlatform.settings.recoveryPending'), children: settings.recovery.pending ? t('appPlatform.settings.yes') : t('appPlatform.settings.no') },
        ]} />
        : <Typography.Text type="secondary">{t('appPlatform.settings.noRecovery')}</Typography.Text>}
    </Card>

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
      {deletePlan && <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Alert type="error" showIcon title={t('appPlatform.settings.deleteDataWarning')} />
        {deleteError && <Alert type="error" showIcon title={deleteError} />}
        <Descriptions column={1} size="small" items={[
          { key: 'namespace', label: t('appPlatform.settings.dataNamespace'), children: <Typography.Text code>{deletePlan.review.data_namespace_id}</Typography.Text> },
          { key: 'generation', label: t('appPlatform.settings.generation'), children: `${deletePlan.review.source_generation} → ${deletePlan.review.target_generation}` },
          { key: 'format', label: t('appPlatform.settings.dataFormat'), children: deletePlan.review.data_format },
          { key: 'scope', label: t('appPlatform.settings.deleteDataScope'), children: deletePlan.review.scope },
        ]} />
      </Space>}
    </Modal>
  </Space>
}
