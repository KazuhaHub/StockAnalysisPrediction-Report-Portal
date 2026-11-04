import { useEffect, useState } from 'react'
import { Alert, Button, Descriptions, Modal, Space, Spin, Tag, Typography } from 'antd'
import { InfoCircleOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { AppDirectoryState } from '../../lib/appDirectoryV2'

type AppStatus = {
  state: AppDirectoryState
  openable: boolean
  action?: string
  stage?: string
  error_code?: string
  control_paused: boolean
}

const states = new Set(['installed', 'pending', 'disabled', 'attention'])
const actions = new Set(['install', 'update', 'disable', 'enable', 'repair', 'uninstall', 'delete_data', 'restore'])
const stages = new Set([
  'queued', 'validate', 'download', 'stage', 'prepare_data', 'quiesce',
  'snapshot', 'migrate', 'start', 'probe', 'activate', 'revoke', 'stop',
  'remove_code', 'remove_data', 'restore_data', 'complete', 'failed',
  'recovery_required',
])
const errors = new Set([
  'app_platform_operation_failed',
  'install_lease_recovery_required',
  'install_authority_review_required',
  'install_execution_review_required',
])

export function parseAppStatus(value: unknown): AppStatus | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const raw = value as Record<string, unknown>
  if (
    typeof raw.state !== 'string' ||
    !states.has(raw.state) ||
    typeof raw.openable !== 'boolean' ||
    (raw.openable && raw.state !== 'installed') ||
    typeof raw.control_paused !== 'boolean' ||
    (raw.action != null && (typeof raw.action !== 'string' || !actions.has(raw.action))) ||
    (raw.stage != null && (typeof raw.stage !== 'string' || !stages.has(raw.stage))) ||
    (raw.error_code != null && (typeof raw.error_code !== 'string' || !errors.has(raw.error_code)))
  ) return null
  return {
    state: raw.state as AppDirectoryState,
    openable: raw.openable,
    control_paused: raw.control_paused,
    ...(typeof raw.action === 'string' ? { action: raw.action } : {}),
    ...(typeof raw.stage === 'string' ? { stage: raw.stage } : {}),
    ...(typeof raw.error_code === 'string' ? { error_code: raw.error_code } : {}),
  }
}

export default function AppInstallationStatus({
  installationId,
  name,
}: {
  installationId: string
  name: string
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [status, setStatus] = useState<AppStatus | null>(null)
  const [loading, setLoading] = useState(false)
  const [failed, setFailed] = useState(false)
  const [refresh, setRefresh] = useState(0)

  useEffect(() => {
    if (!open) return
    const abort = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    setLoading(true)
    setFailed(false)
    const poll = async () => {
      try {
        const value = await api.get<unknown>(
          `/api/app-platform/installations/${installationId}/status`,
          { signal: abort.signal },
        )
        if (abort.signal.aborted) return
        const parsed = parseAppStatus(value)
        if (!parsed) throw new Error('invalid')
        setStatus(parsed)
        setLoading(false)
        const terminal = !parsed.stage || ['complete', 'failed', 'recovery_required'].includes(parsed.stage)
        if (!terminal && !parsed.control_paused) timer = setTimeout(poll, 2000)
      } catch {
        if (!abort.signal.aborted) {
          setFailed(true)
          setLoading(false)
        }
      }
    }
    void poll()
    return () => {
      abort.abort()
      clearTimeout(timer)
    }
  }, [installationId, open, refresh])

  return <>
    <Button
      size="small"
      type="link"
      icon={<InfoCircleOutlined aria-hidden="true" />}
      aria-label={t('apps.status.openFor', { name })}
      onClick={() => {
        setStatus(null)
        setFailed(false)
        setOpen(true)
      }}
    >
      {t('apps.status.open')}
    </Button>
    <Modal
      open={open}
      title={t('apps.status.title', { name })}
      destroyOnHidden
      footer={[
        <Button key="refresh" disabled={loading} onClick={() => setRefresh(value => value + 1)}>
          {t('apps.status.refresh')}
        </Button>,
        <Button key="close" type="primary" onClick={() => setOpen(false)}>
          {t('apps.status.close')}
        </Button>,
      ]}
      onCancel={() => setOpen(false)}
    >
      <div aria-live="polite">
        {loading && !status && <div role="status"><Spin /><span>{t('apps.status.loading')}</span></div>}
        {failed && <Alert type="error" showIcon title={t('apps.status.unavailable')} />}
        {!failed && status && <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
            <Descriptions column={1} size="small" items={[
              {
                key: 'state',
                label: t('apps.status.state'),
                children: <Tag color={status.state === 'attention' ? 'red' : status.state === 'installed' ? 'blue' : 'default'}>
                  {t(`apps.v2State.${status.state}`)}
                </Tag>,
              },
              ...(status.action ? [{
                key: 'action',
                label: t('apps.status.action'),
                children: t(`appPlatform.${status.action}`, { defaultValue: status.action }),
              }] : []),
              ...(status.stage ? [{
                key: 'stage',
                label: t('apps.status.stage'),
                children: <Typography.Text>{t(`appPlatform.stage.${status.stage}`, { defaultValue: status.stage })}</Typography.Text>,
              }] : []),
            ]} />
            {status.control_paused
              ? <Alert type="warning" showIcon title={t('apps.status.paused')} />
              : status.error_code
              ? <Alert type="error" showIcon title={t(`err.${status.error_code}`)} />
              : <Alert type="info" showIcon title={t(`apps.v2Hint.${status.state}`)} />}
          </Space>}
      </div>
    </Modal>
  </>
}
