import { describe, expect, it } from 'vitest'
import { parseOperationRows, type PlatformOperationRow } from '../../api/appOperations'
import { operationDurationMs, operationOutcome, operationProgress, operationVersions, RESULT_BADGE_MS, showsOperationLine } from './operationSummary'

const row = (extra: Partial<PlatformOperationRow> = {}): PlatformOperationRow => ({
  operation_id: '11111111-1111-4111-8111-111111111111', installation_id: '22222222-2222-4222-8222-222222222222',
  action: 'update', stage: 'download', managed: false, revision: '4', terminal: false, created_ms: '1000', updated_ms: '2000', from_version: '1.0.0', to_version: '1.1.0', ...extra,
})

describe('parseOperationRows', () => {
  it('reads the items of the history', () => {
    expect(parseOperationRows({ items: [row()] })).toEqual([row()])
  })
  it('refuses a reply that is not the history, so a half-read list is never shown', () => {
    expect(parseOperationRows(null)).toBeNull()
    expect(parseOperationRows({})).toBeNull()
    expect(parseOperationRows({ items: [{ ...row(), action: 7 }] })).toBeNull()
    expect(parseOperationRows({ items: [{ ...row(), created_ms: 'soon' }] })).toBeNull()
  })
})

describe('operationProgress', () => {
  it('counts the step of a running operation out of the stages its action runs', () => {
    // update, frontend only: queued, validate, download, stage, quiesce, snapshot, migrate, activate, complete
    const p = operationProgress(row())
    expect(p).toMatchObject({ step: 3, total: 9, stage: 'download' })
    expect(p!.percent).toBeGreaterThan(0)
    expect(p!.percent).toBeLessThan(100)
  })
  it('uses the backend stages when the operation is managed', () => {
    expect(operationProgress(row({ managed: true, stage: 'start' }))).toMatchObject({ step: 8, total: 11, stage: 'start' })
  })
  it('has nothing to count for a finished operation or an action with no stage list', () => {
    expect(operationProgress(row({ stage: 'complete', terminal: true }))).toBeNull()
    expect(operationProgress(row({ action: 'nonsense' }))).toBeNull()
  })
})

describe('operationOutcome', () => {
  it('tells running, succeeded and failed apart', () => {
    expect(operationOutcome(row())).toBe('running')
    expect(operationOutcome(row({ stage: 'complete', terminal: true }))).toBe('succeeded')
    expect(operationOutcome(row({ stage: 'failed', terminal: true }))).toBe('failed')
    expect(operationOutcome(row({ stage: 'recovery_required', terminal: true }))).toBe('failed')
  })
})

describe('operationDurationMs', () => {
  it('is the time from start to completion, and nothing while it runs', () => {
    expect(operationDurationMs(row({ stage: 'complete', terminal: true, completed_ms: '66000' }))).toBe(65000)
    expect(operationDurationMs(row())).toBeUndefined()
    expect(operationDurationMs(row({ completed_ms: '500', stage: 'failed', terminal: true }))).toBeUndefined()
  })
})

describe('operationVersions', () => {
  it('names the versions an action moved between, or the one it acted on', () => {
    expect(operationVersions(row())).toEqual({ from: '1.0.0', to: '1.1.0' })
    expect(operationVersions(row({ action: 'install', from_version: undefined, to_version: '1.0.0' }))).toEqual({ to: '1.0.0' })
    expect(operationVersions(row({ action: 'disable', from_version: '1.0.0', to_version: '1.0.0' }))).toEqual({ to: '1.0.0' })
  })
})

describe('showsOperationLine (what an app card says about its newest operation)', () => {
  const now = 10 * RESULT_BADGE_MS
  const done = (stage: string, ago: number) => row({ stage, terminal: true, completed_ms: String(now - ago), updated_ms: String(now - ago) })
  it('always shows a running operation', () => {
    expect(showsOperationLine(row(), now)).toBe(true)
  })
  it('keeps a failure until the next operation, however old', () => {
    expect(showsOperationLine(done('failed', 30 * RESULT_BADGE_MS), now)).toBe(true)
    expect(showsOperationLine(done('recovery_required', 5 * RESULT_BADGE_MS), now)).toBe(true)
  })
  it('shows a success for 24 hours after it completed, then drops it', () => {
    expect(showsOperationLine(done('complete', 60_000), now)).toBe(true)
    expect(showsOperationLine(done('complete', RESULT_BADGE_MS), now)).toBe(true)
    expect(showsOperationLine(done('complete', RESULT_BADGE_MS + 1), now)).toBe(false)
  })
})
