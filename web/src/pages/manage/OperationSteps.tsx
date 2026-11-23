import { useRef } from 'react'
import { Steps } from 'antd'
import { useTranslation } from 'react-i18next'
import { stepModel } from './operationSteps'

const SETTLED = ['complete', 'failed', 'recovery_required']
/** Stages only a backend app's operation has: seeing one tells a resumed page that the app is managed. */
const MANAGED_STAGES = ['start', 'probe', 'stop', 'remove_data']

interface Reading { operation_id: string; action: string; stage: string; terminal: boolean }

/**
 * The last stage this page saw an operation running in. A failure arrives without the stage it happened in, so this
 * stands for it. It lives with the dialog, not the list, because the list is gone while a refresh reads again.
 */
export function useLastRunning(progress: Reading | null): string | undefined {
  const seen = useRef<{ operation: string; stage: string } | null>(null)
  if (progress && seen.current && seen.current.operation !== progress.operation_id) seen.current = null
  if (progress && !SETTLED.includes(progress.stage)) seen.current = { operation: progress.operation_id, stage: progress.stage }
  return progress && seen.current?.operation === progress.operation_id ? seen.current.stage : undefined
}

/**
 * The stages of a running operation as a list: the ones behind, the one running now, the ones to come, and the one a
 * failure stopped at. The stage names and their order are the backend's (`operationSteps.ts`); nothing is added here.
 * `managed` is what the release says when the page knows it; a page resumed after a reload infers it from the stage.
 */
export default function OperationSteps({ progress, managed, lastRunning }: { progress: Reading; managed?: boolean; lastRunning?: string }) {
  const { t } = useTranslation()
  const known = managed ?? MANAGED_STAGES.includes(progress.stage)
  const items = stepModel(progress, known, lastRunning)
  if (!items.length) return null
  const current = Math.max(0, items.findIndex(item => item.status === 'process' || item.status === 'error'))
  return <Steps className="rp-operation-steps" size="small" orientation="vertical" current={progress.stage === 'complete' ? items.length - 1 : current}
    items={items.map(item => ({ title: t(`appPlatform.stage.${item.stage}`), status: item.status }))} />
}
