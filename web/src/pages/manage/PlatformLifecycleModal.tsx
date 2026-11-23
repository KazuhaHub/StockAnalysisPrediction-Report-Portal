import { useEffect } from 'react'
import { Alert, Button, Descriptions, Modal, Select, Space, Spin, Steps, Typography } from 'antd'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { usePrefs } from '../../prefs'
import { appDisplayText } from '../../lib/appDisplayV2'
import { joinList } from '../../lib/joinList'
import StatusTag, { type StatusTone } from '../../components/StatusTag'
import PlatformReviewDetails, { reviewBlocked } from './PlatformReviewDetails'
import type { PlatformLifecycle } from './platformLifecycle'

const TITLE = { install: 'appPlatform.confirmTitle', update: 'appPlatform.confirmUpdateTitle', disable: 'appPlatform.confirmDisableTitle', uninstall: 'appPlatform.confirmUninstallTitle', enable: 'appPlatform.confirmEnableTitle', repair: 'appPlatform.confirmRepairTitle' } as const
const OK = { install: 'appPlatform.confirm', update: 'appPlatform.confirmUpdate', disable: 'appPlatform.confirmDisable', uninstall: 'appPlatform.confirmUninstall', enable: 'appPlatform.confirmEnable', repair: 'appPlatform.confirmRepair' } as const

/**
 * The one modal every lifecycle action runs in: Steps for who can use a new app, the review, and the progress of
 * the operation. A flow that is not an install has no first step. It shows no state of its own; everything comes
 * from the lifecycle hook, which keeps the safety rules.
 */
export default function PlatformLifecycleModal({ lifecycle, backTo }: { lifecycle: PlatformLifecycle; backTo?: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { lang } = usePrefs()
  const { target, step, review, intent, busy, expired, progress, progressError, actionError, groups } = lifecycle
  const action = lifecycle.action ?? 'install'
  const install = action === 'install'
  // The review fetched for a flow that was resumed after a reload has no release; the review is then the only record.
  const release = target?.release
  const copy = release ? appDisplayText(release.app_id, release.display, lang) : null
  const stepItems = [
    ...(install ? [{ title: t('appPlatform.step.audience') }] : []),
    { title: t('appPlatform.step.review') },
    { title: t('appPlatform.step.progress') },
  ]
  const stepIndex = step === 'audience' ? 0 : step === 'review' ? (install ? 1 : 0) : (install ? 2 : 1)

  // Keep keyboard focus inside the dialog, and on the review itself when it appears, so a keyboard user lands on
  // what they are confirming.
  useEffect(() => {
    if (!lifecycle.open) return
    const timer = setTimeout(() => {
      const review = document.querySelector<HTMLElement>('.rp-platform-review')
      if (review) review.focus()
      else {
        const dialog = document.querySelector<HTMLElement>('[role="dialog"].rp-platform-review-modal, .rp-platform-review-modal [role="dialog"]')
        if (dialog && !dialog.contains(document.activeElement)) dialog.focus()
      }
    }, 0)
    return () => clearTimeout(timer)
  }, [lifecycle.open, lifecycle.step, lifecycle.review, lifecycle.intent])

  const progressTone: StatusTone = progress?.control_paused ? 'warning'
    : progress?.terminal && ['failed', 'recovery_required'].includes(progress.stage) ? 'error'
    : progress?.stage === 'complete' ? 'success' : 'processing'
  const complete = progress?.stage === 'complete'
  const installationId = progress?.installation_id
  const uninstalled = action === 'uninstall' || progress?.action === 'uninstall'
  const go = (path: string) => { lifecycle.close(); navigate(path) }

  const footer = () => {
    if (step === 'audience') return [
      <Button key="cancel" disabled={busy} onClick={lifecycle.close}>{t('appPlatform.cancel')}</Button>,
      <Button key="next" type="primary" loading={busy} disabled={lifecycle.groupsLoading || !!lifecycle.groupsError} onClick={lifecycle.next}>{t('appPlatform.next')}</Button>,
    ]
    if (step === 'review') return [
      <Button key="cancel" disabled={busy} onClick={lifecycle.close}>{t('appPlatform.cancel')}</Button>,
      ...(install && !intent && review ? [<Button key="back" disabled={busy} onClick={lifecycle.back}>{t('appPlatform.back')}</Button>] : []),
      ...(review || intent ? [<Button key="ok" type="primary" danger={action === 'uninstall'} loading={busy} disabled={!intent && (expired || reviewBlocked(review))} onClick={() => void lifecycle.confirm()}>{intent ? t('appPlatform.retrySame') : t(OK[action])}</Button>] : []),
    ]
    return [
      <Button key="refresh" disabled={busy} onClick={lifecycle.refreshProgress}>{t('appPlatform.refresh')}</Button>,
      // Plain buttons, not links around buttons: antd spaces footer buttons with `.ant-btn + .ant-btn`, which a
      // wrapping link breaks. An uninstall leaves no settings to open.
      ...(complete && installationId && !uninstalled ? [<Button key="settings" onClick={() => go(`/manage/apps/${encodeURIComponent(`platform:${installationId}`)}`)}>{t('appPlatform.openSettings')}</Button>] : []),
      ...(complete && backTo ? [<Button key="back" onClick={() => go(backTo)}>{t('appStore.backToApps')}</Button>] : []),
      <Button key="done" type="primary" onClick={lifecycle.close}>{t('appPlatform.done')}</Button>,
    ]
  }

  return <Modal className="rp-platform-review-modal" open={lifecycle.open} title={t(TITLE[action])} footer={footer()} mask={{ closable: false }} closable={!busy} keyboard={!busy} onCancel={lifecycle.close} destroyOnHidden>
    <div className="rp-lifecycle-body">
      <Steps size="small" current={stepIndex} items={stepItems} />
      {copy ? <Typography.Text strong>{copy.name} · {release?.version}</Typography.Text> : intent?.app_id && <Typography.Text strong>{intent.app_id}</Typography.Text>}
      {actionError && <Alert type="error" title={actionError} />}
      {intent && <Alert type="warning" title={t('appPlatform.uncertain')} action={lifecycle.definitive ? <Button disabled={busy} onClick={lifecycle.clearRejected}>{t('appPlatform.reviewAgain')}</Button> : undefined} />}
      {step === 'audience' && <Space orientation="vertical" style={{ width: '100%' }}>
        {release && <Descriptions column={1} size="small" items={[
          { key: 'publisher', label: t('appPlatform.publisher'), children: release.publisher_id },
          { key: 'version', label: t('appPlatform.version'), children: release.version },
          { key: 'required', label: t('appPlatform.requiredPermissions'), children: release.required_capabilities?.length ? joinList(release.required_capabilities.map(c => t(`appPlatform.capability.${c}`, { defaultValue: c })), lang) : t('appPlatform.none') },
          { key: 'optional', label: t('appPlatform.optionalPermissions'), children: release.optional_capabilities?.length ? joinList(release.optional_capabilities.map(c => t(`appPlatform.capability.${c}`, { defaultValue: c })), lang) : t('appPlatform.none') },
          { key: 'resources', label: t('appPlatform.resources'), children: release.backend && release.resources ? t('appPlatform.resourceSummary', { platforms: release.platforms?.join(', ') ?? t('appPlatform.none'), memory: release.resources.memory_mib, cpu: release.resources.cpu_millis, pids: release.resources.pids }) : t('appPlatform.frontendOnly') },
        ]} />}
        <Typography.Text strong>{t('appPlatform.audience')}</Typography.Text>
        <Select mode="multiple" aria-label={t('appPlatform.audience')} placeholder={t('appPlatform.chooseAudience')} value={lifecycle.selected} options={groups.map(g => ({ value: g.id, label: `${g.name} (${g.id})` }))} disabled={busy || lifecycle.groupsLoading || !!lifecycle.groupsError} loading={lifecycle.groupsLoading} onChange={lifecycle.setSelected} optionFilterProp="label" maxCount={128} style={{ width: '100%' }} />
        {groups.some(g => g.default && lifecycle.selected.includes(g.id)) && <Alert type="info" showIcon title={t('appPlatform.defaultOUNote')} />}
        {lifecycle.groupsError && <Alert type="error" title={lifecycle.groupsError} />}
        {lifecycle.groupCursor && <Button disabled={busy || lifecycle.groupsLoading} onClick={() => void lifecycle.loadMoreGroups()}>{t('appPlatform.moreGroups')}</Button>}
      </Space>}
      {step === 'review' && !review && !intent && !actionError && <div style={{ textAlign: 'center' }}><Spin /></div>}
      {step === 'review' && review && !intent && <PlatformReviewDetails review={review} release={release} groups={groups} expired={expired} />}
      {step === 'progress' && <div role="status" aria-live="polite">
        {progressError && <Alert type="error" title={progressError} />}
        {progress && <Space orientation="vertical" size="small" style={{ width: '100%' }}>
          <StatusTag tone={progressTone}>{progress.control_paused ? t('err.app_platform_control_paused') : t(`appPlatform.stage.${progress.stage}`, { defaultValue: progress.stage })}</StatusTag>
          {progress.error_code && !progress.control_paused && <Alert type="error" title={t(`err.${progress.error_code}`, { defaultValue: t('appPlatform.operationNeedsReview') })} />}
        </Space>}
        {!progress && !progressError && <Spin />}
      </div>}
    </div>
  </Modal>
}
