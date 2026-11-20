// The trusted confirmation for a network.http destination change. Everything shown is the server's
// own diff of the plan about to be consumed, plus fixed translations. Nothing an app asked for is
// ever rendered as text: the app's request only chose which plan the server drew up.
import { useEffect, useState } from 'react'
import { Alert, Descriptions, Modal, Space, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import type { AddedTarget, CapabilityPlan, PolicyBounds, PolicyTarget } from '../../lib/appNetworkTargetsV2'

function limits(t: TFunction, item: Pick<PolicyTarget, 'request_bytes' | 'response_bytes' | 'deadline_ms'>) {
  const parts: string[] = []
  if (item.request_bytes) parts.push(t('appPlatform.network.limit.requestBytes', { value: item.request_bytes }))
  if (item.response_bytes) parts.push(t('appPlatform.network.limit.responseBytes', { value: item.response_bytes }))
  if (item.deadline_ms) parts.push(t('appPlatform.network.limit.deadlineMs', { value: item.deadline_ms }))
  return parts.length ? parts.join(' · ') : t('appPlatform.network.limit.shared')
}

function boundsRows(t: TFunction, before: PolicyBounds, after: PolicyBounds, prefix: string) {
  const rows: { key: string; label: string; children: string }[] = []
  const entries: [keyof PolicyBounds, string][] = [
    ['request_bytes', 'appPlatform.network.limit.requestBytesName'],
    ['response_bytes', 'appPlatform.network.limit.responseBytesName'],
    ['deadline_ms', 'appPlatform.network.limit.deadlineMsName'],
    ['concurrency', 'appPlatform.network.limit.concurrencyName'],
  ]
  for (const [key, label] of entries) if (before[key] !== after[key]) rows.push({ key: `${prefix}${key}`, label: t(label), children: `${before[key]} → ${after[key]}` })
  return rows
}

function TargetBlock({ item, tone, fromApp }: { item: AddedTarget | PolicyTarget; tone: 'add' | 'remove'; fromApp: boolean }) {
  const { t } = useTranslation()
  const added = tone === 'add' ? (item as AddedTarget) : null
  const privateTarget = item.network === 'private'
  return <div data-testid={`network-${tone}-${item.id}`} style={{ border: '1px solid var(--ant-color-border-secondary, #f0f0f0)', borderRadius: 8, padding: 12 }}>
    <Space size={[6, 6]} wrap style={{ marginBottom: 8 }}>
      <Tag color={tone === 'add' ? 'success' : 'default'}>{t(tone === 'add' ? 'appPlatform.network.review.adding' : 'appPlatform.network.review.removing')}</Tag>
      {fromApp && tone === 'add'
        ? <Typography.Text strong>{t('appPlatform.network.review.appChosenId', { id: item.id })}</Typography.Text>
        : <Typography.Text strong>{item.id}</Typography.Text>}
      <Tag color={privateTarget ? 'error' : 'default'} data-testid={privateTarget ? 'network-private-tag' : undefined}>{t(`appPlatform.network.class.${item.network}`, { defaultValue: item.network })}</Tag>
      {added?.plain_http && <Tag color="error" data-testid="network-plain-http-tag">{t('appPlatform.network.review.plainHTTP')}</Tag>}
    </Space>
    <Descriptions column={1} size="small" items={[
      { key: 'origin', label: t('appPlatform.network.origin'), children: <Typography.Text code>{item.origin}</Typography.Text> },
      { key: 'paths', label: t('appPlatform.network.paths'), children: item.paths.join(', ') },
      { key: 'methods', label: t('appPlatform.network.methods'), children: item.methods.join(', ') },
      { key: 'limits', label: t('appPlatform.network.limits'), children: limits(t, item) },
      ...(added?.allowlist ? [{ key: 'allow', label: t('appPlatform.network.review.allowlistEntry'), children: t('appPlatform.network.review.allowlistValue', { prefix: added.allowlist.prefix, ports: added.allowlist.ports.join(', ') }) }] : []),
    ]} />
  </div>
}

// How long confirm stays disabled after a plan appears, so a click or key press meant for the page
// underneath cannot approve it.
export const REVIEW_ARM_MS = 1000

export default function NetworkChangeReview({ plan, appName, busy, error, onConfirm, onCancel }: {
  plan: CapabilityPlan | null
  // Present when the request came from an application: the dialog then says so.
  appName?: string
  busy: boolean
  error: string
  onConfirm: () => void
  // The dialog never closes by itself: cancel is the one way out, and the caller decides what it means.
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const diff = plan?.diff
  const [armed, setArmed] = useState(false)
  const planId = plan?.plan_id
  useEffect(() => {
    setArmed(false)
    if (!planId) return
    const timer = setTimeout(() => setArmed(true), REVIEW_ARM_MS)
    return () => clearTimeout(timer)
  }, [planId])
  const risky = !!diff?.added.some(item => item.network === 'private' || item.plain_http)
  const boundsChanged = diff ? boundsRows(t, diff.bounds.old, diff.bounds.new, 'shared-') : []
  return <Modal
    className="rp-platform-review-modal"
    open={!!plan}
    title={t('appPlatform.network.review.title')}
    okText={t('appPlatform.network.review.confirm')}
    cancelText={t('common.cancel')}
    okButtonProps={{ danger: risky, disabled: !!error || !armed }}
    confirmLoading={busy}
    closable={!busy}
    mask={{ closable: false }}
    onOk={() => { if (armed) onConfirm() }}
    onCancel={() => { if (!busy) onCancel() }}
  >
    {diff && <div role="document" tabIndex={0} className="rp-platform-review-scroll" data-testid="network-review">
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        {appName !== undefined && <Alert type="info" showIcon data-testid="network-from-app" title={t('appPlatform.network.review.fromApp', { app: appName })} />}
        <Alert type={risky ? 'error' : 'warning'} showIcon title={t(risky ? 'appPlatform.network.review.riskyWarning' : 'appPlatform.network.review.warning')} />
        {error && <Alert type="error" showIcon title={error} />}
        {diff.added.map(item => <TargetBlock key={`add-${item.id}`} item={item} tone="add" fromApp={appName !== undefined} />)}
        {diff.removed.map(item => <TargetBlock key={`remove-${item.id}`} item={item} tone="remove" fromApp={appName !== undefined} />)}
        {boundsChanged.length > 0 && <div data-testid="network-bounds-change">
          <Typography.Text strong>{t('appPlatform.network.review.boundsChange')}</Typography.Text>
          <Descriptions column={1} size="small" items={boundsChanged} />
        </div>}
        {diff.narrowing.map(item => <div key={`narrow-${item.id}`} data-testid={`network-narrow-${item.id}`}>
          <Typography.Text strong>{t('appPlatform.network.review.targetLimits', { id: item.id })}</Typography.Text>
          <Descriptions column={1} size="small" items={boundsRows(t, item.old, item.new, `${item.id}-`)} />
        </div>)}
        {diff.allowlist.old_sha256 !== diff.allowlist.new_sha256 && <Descriptions column={1} size="small" items={[
          { key: 'allowlist', label: t('appPlatform.network.review.allowlistDigest'), children: <Typography.Text code>{`${diff.allowlist.old_sha256 || '—'} → ${diff.allowlist.new_sha256 || '—'}`}</Typography.Text> },
        ]} />}
        {diff.paused_bindings.length > 0 && <Alert type="warning" showIcon data-testid="network-paused-bindings" title={t('appPlatform.network.review.paused')}
          description={<ul style={{ margin: 0, paddingInlineStart: 20 }}>{diff.paused_bindings.map(name => <li key={name}>{name}</li>)}</ul>} />}
      </Space>
    </div>}
  </Modal>
}
