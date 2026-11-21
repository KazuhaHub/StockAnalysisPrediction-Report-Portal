import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Descriptions, Flex, Space, Spin, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import Confirm from '../../components/Confirm'
import Expand from '../../components/Expand'
import LongId from '../../components/LongId'
import StatusTag, { type StatusTone } from '../../components/StatusTag'
import { downloadText } from '../../lib/download'
import { notifyError } from '../../lib/notify'

const root = '/api/admin/app-platform'
const fileName = 'portal-runner-enrollment.json'
// The commands are the handbook's (section 4.0); they are shell text, not copy.
const setupCommand = `docker compose run --rm app-runner host-setup\ndocker compose run --rm app-runner host-setup --apply --enrollment /path/to/${fileName}`

type RuntimeState = 'uninitialized' | 'paused' | 'none' | 'pending' | 'confirmed' | 'conflict'
type RuntimeBinding = { runtime_id: string; deployment_id: string; controller_epoch: string; verification_key_fingerprint: string; enabled: boolean }
type RuntimeStatus = { state: RuntimeState; endpoint_configured: boolean; runtime: RuntimeBinding | null }
type Observed = { deployment_id: string; controller_epoch: string; control_revision: string; verification_key_fingerprint: string }
type ControlResult = RuntimeBinding & { enrollment?: string; observed?: Observed }

const tone: Record<RuntimeState, StatusTone> = {
  uninitialized: 'waiting', paused: 'warning', none: 'neutral', pending: 'processing', confirmed: 'success', conflict: 'error',
}

/**
 * The first runtime enrollment of a host (handbook section 4.7): prepare the binding, hand the
 * public enrollment document to host-setup, then confirm the runtime once the runner serves. A
 * restored Portal enrolls its runtime through the recovery procedure instead, so the paused and
 * conflict states offer no action here.
 */
export default function AppPlatformRuntimePanel({ reload = 0 }: { reload?: number }) {
  const { t } = useTranslation()
  const [status, setStatus] = useState<RuntimeStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const [enrollment, setEnrollment] = useState('')
  const [observed, setObserved] = useState<Observed | null>(null)
  const translate = useRef(t)
  translate.current = t

  useEffect(() => {
    const abort = new AbortController()
    setLoading(true)
    api.get<RuntimeStatus>(`${root}/runtime`, { signal: abort.signal }).then(value => {
      if (abort.signal.aborted) return
      setStatus(value); setError('')
      if (value.state !== 'pending') setEnrollment('')
    }).catch(e => { if (!abort.signal.aborted) { setStatus(null); setError(errText(e, translate.current)) } })
      .finally(() => { if (!abort.signal.aborted) setLoading(false) })
    return () => abort.abort()
  }, [reload, refresh])

  const act = async (body: Record<string, string>) => {
    setBusy(true)
    try {
      const result = await api.post<ControlResult>(`${root}/control/runtime`, body)
      if (result.enrollment) setEnrollment(result.enrollment)
      if (result.observed) { setObserved(result.observed); setEnrollment('') }
    } catch (e) {
      notifyError(errText(e, translate.current))
    } finally {
      setBusy(false)
      setRefresh(v => v + 1)
    }
  }

  const state = status?.state
  const runtime = status?.runtime
  const configured = !!status?.endpoint_configured
  return <Card title={t('appRuntime.title')} extra={<Button disabled={busy} onClick={() => setRefresh(v => v + 1)}>{t('appRuntime.refresh')}</Button>} style={{ marginBottom: 16 }}>
    <Spin spinning={loading}>
      {error && <Alert type="error" showIcon title={t('appRuntime.loadFailed')} description={error} action={<Button size="small" onClick={() => setRefresh(v => v + 1)}>{t('appRuntime.retry')}</Button>} />}
      {status && state && <Flex vertical gap="small">
        <Space wrap>
          <StatusTag tone={tone[state]}>{t(`appRuntime.state.${state}`)}</StatusTag>
          {(state === 'pending' || state === 'confirmed') && <StatusTag tone={configured ? 'success' : 'waiting'}>{t(configured ? 'appRuntime.endpoint.configured' : 'appRuntime.endpoint.missing')}</StatusTag>}
        </Space>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>{t(`appRuntime.hint.${state}`)}</Typography.Paragraph>
        {runtime && <Descriptions size="small" column={1} items={[
          { key: 'deployment', label: t('appRuntime.deployment'), children: <LongId value={runtime.deployment_id} /> },
          { key: 'fingerprint', label: t('appRuntime.fingerprint'), children: <LongId value={runtime.verification_key_fingerprint} /> },
        ]} />}
        {state === 'pending' && !configured && <Alert type="warning" showIcon title={t('appRuntime.hint.endpointMissing')} />}
        {state === 'none' && <Button type="primary" style={{ alignSelf: 'flex-start' }} loading={busy} onClick={() => void act({ action: 'prepare' })}>{t('appRuntime.prepare')}</Button>}
        {state === 'pending' && runtime && <Space wrap>
          <Button disabled={busy} onClick={() => void act({ action: 'prepare' })}>{t('appRuntime.showDocument')}</Button>
          <Confirm title={t('appRuntime.confirmTitle')} description={t('appRuntime.confirmDescription')} okText={t('appRuntime.confirmAction')} danger={false} disabled={!configured || busy} onConfirm={() => act({ action: 'confirm', deployment_id: runtime.deployment_id })}>
            <Button type="primary" disabled={!configured || busy} loading={busy}>{t('appRuntime.confirm')}</Button>
          </Confirm>
        </Space>}
        <Expand open={!!enrollment && state === 'pending'}>
          <Flex vertical gap="small">
            <Typography.Text strong>{t('appRuntime.documentTitle')}</Typography.Text>
            <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>{t('appRuntime.documentHint')}</Typography.Paragraph>
            <pre className="rp-code rp-code--nowrap">{enrollment}</pre>
            <Space wrap>
              <Button onClick={() => downloadText(fileName, enrollment, 'application/json')}>{t('appRuntime.download')}</Button>
              <Typography.Text copyable={{ text: enrollment }}>{t('appRuntime.copy')}</Typography.Text>
            </Space>
            <Typography.Text strong>{t('appRuntime.setupTitle')}</Typography.Text>
            <pre className="rp-code rp-code--nowrap">{setupCommand}</pre>
            <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>{t('appRuntime.setupHint')}</Typography.Paragraph>
          </Flex>
        </Expand>
        {observed && state === 'confirmed' && <Alert type="success" showIcon title={t('appRuntime.observed')} description={<Descriptions size="small" column={1} items={[
          { key: 'deployment', label: t('appRuntime.deployment'), children: <LongId value={observed.deployment_id} /> },
          { key: 'epoch', label: t('appRuntime.epoch'), children: <LongId value={observed.controller_epoch} /> },
          { key: 'revision', label: t('appRuntime.revision'), children: observed.control_revision },
          { key: 'fingerprint', label: t('appRuntime.fingerprint'), children: <LongId value={observed.verification_key_fingerprint} /> },
        ]} />} />}
      </Flex>}
    </Spin>
  </Card>
}
