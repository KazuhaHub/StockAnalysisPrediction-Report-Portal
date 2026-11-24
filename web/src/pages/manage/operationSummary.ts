import type { PlatformOperationRow } from '../../api/appOperations'
import { operationStages } from './operationSteps'

// What the console says about one operation of the history: how far a running one is, how a finished one ended, how
// long it took and which versions it moved between. The stage list and its order are the backend's (operationSteps.ts).

export type OperationOutcome = 'running' | 'succeeded' | 'failed'

export function operationOutcome(row: Pick<PlatformOperationRow, 'stage' | 'terminal'>): OperationOutcome {
  if (!row.terminal) return 'running'
  return row.stage === 'complete' ? 'succeeded' : 'failed'
}

export interface OperationProgress {
  /** The 1-based number of the stage the operation is in. */
  step: number
  total: number
  stage: string
  /** Whole percent for a progress bar: the stages behind the current one, out of the stages in front of `complete`. */
  percent: number
}

/** Where a running operation is in its action's stages; null once it has ended or for an action with no stage list. */
export function operationProgress(row: Pick<PlatformOperationRow, 'action' | 'stage' | 'terminal' | 'managed'>): OperationProgress | null {
  if (row.terminal) return null
  const stages = operationStages(row.action, row.managed)
  const index = stages.indexOf(row.stage)
  if (index < 0) return null
  return { step: index + 1, total: stages.length, stage: row.stage, percent: Math.round(index * 100 / Math.max(1, stages.length - 1)) }
}

/** Milliseconds from start to completion; undefined while it runs or when the two times do not make sense. */
export function operationDurationMs(row: Pick<PlatformOperationRow, 'created_ms' | 'completed_ms'>): number | undefined {
  if (row.completed_ms === undefined) return undefined
  const span = Number(row.completed_ms) - Number(row.created_ms)
  return Number.isSafeInteger(span) && span >= 0 ? span : undefined
}

/** The versions an operation moved between (an update or a switch), or the one it acted on. */
export function operationVersions(row: Pick<PlatformOperationRow, 'from_version' | 'to_version'>): { from?: string; to?: string } {
  const { from_version: from, to_version: to } = row
  if (from && to && from !== to) return { from, to }
  const only = to ?? from
  return only ? { to: only } : {}
}
