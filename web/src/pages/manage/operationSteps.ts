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
    case 'import_data': stages.push('quiesce', 'snapshot', 'import', 'start', 'probe', 'activate'); break
    case 'set_data_quota': stages.push('prepare_data'); break
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

/**
 * The steps of an operation and where each stands. A failure is reported without the stage it happened in, so it is
 * placed at the last stage this page saw running (`lastRunning`); with none seen it is not guessed at.
 */
export function stepModel(progress: { action: string; stage: string; terminal: boolean }, managed: boolean, lastRunning: OperationStage | undefined): StepItem[] {
  let stages = operationStages(progress.action, managed)
  if (!stages.length) return []
  const failed = FAILED.includes(progress.stage)
  const at = failed ? lastRunning : progress.stage
  // A stage the list does not hold (a managed app seen before its runtime stages) is added before the last step, not dropped.
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
