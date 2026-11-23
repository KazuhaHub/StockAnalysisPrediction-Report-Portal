// The stages an application operation moves through, in the order the backend runs them (internal/appplatform
// operationStages). The status read reports only the current stage, so the list is what turns it into steps.

export type OperationStage = string
export type StepStatus = 'finish' | 'process' | 'wait' | 'error'
export interface StepItem { stage: OperationStage; status: StepStatus }

/** The ordered stages of one action. `managed` is a backend app (it has a runtime to start, probe and stop). */
export function operationStages(action: string, managed: boolean): OperationStage[] {
  const stages: OperationStage[] = ['queued', 'validate']
  const startAndProbe = () => { if (managed) stages.push('start', 'probe') }
  switch (action) {
    case 'install': stages.push('download', 'stage', 'prepare_data'); startAndProbe(); stages.push('activate'); break
    case 'update': stages.push('download', 'stage', 'quiesce', 'snapshot', 'migrate'); startAndProbe(); stages.push('activate'); break
    case 'import_data': if (!managed) return []; stages.push('quiesce', 'snapshot', 'import', 'start', 'probe', 'activate'); break
    case 'set_data_quota': if (!managed) return []; stages.push('prepare_data'); break
    case 'disable': stages.push('revoke'); if (managed) stages.push('stop'); break
    case 'enable': if (managed) stages.push('snapshot'); startAndProbe(); stages.push('activate'); break
    case 'repair': stages.push('download', 'stage', 'quiesce'); startAndProbe(); stages.push('activate'); break
    case 'uninstall': stages.push('revoke'); if (managed) stages.push('stop', 'remove_code', 'remove_data'); else stages.push('remove_code'); break
    case 'delete_data': stages.push('revoke', 'remove_data'); break
    case 'restore': stages.push('revoke', 'quiesce', 'snapshot', 'restore_data'); startAndProbe(); stages.push('activate'); break
    case 'restore_import': stages.push('restore_data'); break
    default: return []
  }
  stages.push('complete')
  return stages
}

const FAILED = ['failed', 'recovery_required']
export interface StepReading { action: string; stage: string; terminal: boolean; revision?: string }

/**
 * Where a failed operation failed. Revision n is the n-th stage (queued is 1) and a failure leaves it one past the
 * stage it happened in, so the failed stage is the one at revision - 2 (Go: SettleStartNeverRan reads it the same
 * way). A failure settled afterwards (the restore, import and never-started cases) sits one revision further, so that
 * rare case marks the step after the failed one. Without a usable revision it falls back to the last stage seen running,
 * or to nothing: no guess.
 */
export function failedStage(progress: StepReading, stages: OperationStage[], lastRunning: OperationStage | undefined): OperationStage | undefined {
  const revision = Number(progress.revision)
  if (Number.isInteger(revision) && revision >= 2 && revision - 2 < stages.length - 1) return stages[revision - 2]
  return lastRunning
}

/**
 * The steps of an operation and where each stands. A step the poll never saw (the operation moved two stages between
 * reads) is finished all the same: position is all that counts.
 */
export function stepModel(progress: StepReading, managed: boolean, lastRunning: OperationStage | undefined): StepItem[] {
  let stages = operationStages(progress.action, managed)
  if (!stages.length) return []
  const failed = FAILED.includes(progress.stage)
  const at = failed ? failedStage(progress, stages, lastRunning) : progress.stage
  // A stage the list does not hold is added before the last step, not dropped.
  if (at && !stages.includes(at)) stages = [...stages.slice(0, -1), at, 'complete']
  const index = at ? stages.indexOf(at) : -1
  return stages.map((stage, i) => {
    if (progress.stage === 'complete') return { stage, status: 'finish' as const }
    if (index < 0) return { stage, status: 'wait' as const }
    if (i < index) return { stage, status: 'finish' as const }
    if (i === index) return { stage, status: failed ? 'error' as const : 'process' as const }
    return { stage, status: 'wait' as const }
  })
}
