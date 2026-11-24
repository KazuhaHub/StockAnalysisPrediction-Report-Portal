import { Button, Progress, Tooltip } from 'antd'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import StatusTag from '../../components/StatusTag'
import type { PlatformOperationRow } from '../../api/appOperations'
import { operationOutcome, operationProgress, operationVersions } from './operationSummary'

/** The words for an operation's action ("Update", "Disable", ...). An action this page does not know is shown as it is. */
export const actionLabel = (t: TFunction, action: string) => t(`appPlatform.ops.action.${action}`, { defaultValue: action })

/** Why an operation failed, in the words of its public error code. */
export const failureReason = (t: TFunction, code?: string) => t(`err.${code ?? 'app_platform_operation_failed'}`, { defaultValue: t('appPlatform.operationNeedsReview') })

/** "Update · 1.1.0": what an operation did and to which version. */
export function operationTitle(t: TFunction, row: Pick<PlatformOperationRow, 'action' | 'from_version' | 'to_version'>): string {
  const { to } = operationVersions(row)
  const action = actionLabel(t, row.action)
  return to ? t('appPlatform.ops.actionVersion', { action, version: to }) : action
}

/**
 * What an app card says about its newest operation. While one runs: its action and target version, the step it is
 * at out of the steps its action has, the name of that stage and a small progress bar. After it ends: whether it
 * succeeded or why it failed, kept until the next operation starts. Pressing either opens the operation's step list.
 */
export default function InstallationOperationLine({ row, onOpen }: { row: PlatformOperationRow; onOpen: (opener: HTMLElement) => void }) {
  const { t } = useTranslation()
  const outcome = operationOutcome(row)
  const progress = operationProgress(row)
  const title = operationTitle(t, row)
  const open = (e: React.MouseEvent<HTMLElement>) => { e.stopPropagation(); onOpen(e.currentTarget) }
  if (outcome === 'running') {
    const stage = t(`appPlatform.stage.${row.stage}`, { defaultValue: row.stage })
    return <Button type="text" size="small" className="rp-op-line" aria-label={`${title}. ${t('appPlatform.ops.openProgress')}`} onClick={open}>
      <span className="rp-op-line-title">{title}</span>
      <span className="rp-op-line-stage">{progress ? `${t('appPlatform.ops.step', { step: progress.step, total: progress.total })} · ${stage}` : stage}</span>
      {progress && <Progress size="small" percent={progress.percent} showInfo={false} aria-label={stage} />}
    </Button>
  }
  const failed = outcome === 'failed'
  const reason = failed ? failureReason(t, row.error_code) : ''
  return <Tooltip title={failed ? reason : undefined}>
    <Button type="text" size="small" className="rp-op-line rp-op-line--result" aria-label={`${t(failed ? 'appPlatform.ops.failed' : 'appPlatform.ops.succeeded', { action: actionLabel(t, row.action) })}. ${t('appPlatform.ops.openProgress')}`} onClick={open}>
      <StatusTag tone={failed ? 'error' : 'success'}>{t(failed ? 'appPlatform.ops.failed' : 'appPlatform.ops.succeeded', { action: actionLabel(t, row.action) })}</StatusTag>
      {failed && <span className="rp-op-line-reason">{reason}</span>}
    </Button>
  </Tooltip>
}
