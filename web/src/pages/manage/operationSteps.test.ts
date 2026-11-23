import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { operationStages, stepModel } from './operationSteps'

// internal/appplatform/operation_stages_contract_test.go writes this file from the backend's own stage lists.
const contract = JSON.parse(readFileSync(join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'lib', 'operation-stages.generated.json'), 'utf8')) as Record<string, { managed: string[]; unmanaged: string[] }>

describe('the stage lists are the backend\'s', () => {
  it('match the checked-in contract for every action, managed or not', () => {
    expect(Object.keys(contract).length).toBeGreaterThan(8)
    for (const [action, lists] of Object.entries(contract)) {
      expect(operationStages(action, true), `${action} managed`).toEqual(lists.managed)
      expect(operationStages(action, false), `${action} unmanaged`).toEqual(lists.unmanaged)
    }
  })
  it('give an unmanaged app no stages for the actions only a backend has', () => {
    expect(operationStages('set_data_quota', false)).toEqual([])
    expect(operationStages('import_data', false)).toEqual([])
  })
})

describe('operationStages mirrors the order the backend runs an action in', () => {
  it('install: download, stage and prepare data, then activate (a managed app starts and is probed first)', () => {
    expect(operationStages('install', false)).toEqual(['queued', 'validate', 'download', 'stage', 'prepare_data', 'activate', 'complete'])
    expect(operationStages('install', true)).toEqual(['queued', 'validate', 'download', 'stage', 'prepare_data', 'start', 'probe', 'activate', 'complete'])
  })
  it('update: quiesce, snapshot and migrate around the new code', () => {
    expect(operationStages('update', false)).toEqual(['queued', 'validate', 'download', 'stage', 'quiesce', 'snapshot', 'migrate', 'activate', 'complete'])
  })
  it('disable, enable, repair, uninstall', () => {
    expect(operationStages('disable', false)).toEqual(['queued', 'validate', 'revoke', 'complete'])
    expect(operationStages('disable', true)).toEqual(['queued', 'validate', 'revoke', 'stop', 'complete'])
    expect(operationStages('enable', false)).toEqual(['queued', 'validate', 'activate', 'complete'])
    expect(operationStages('enable', true)).toEqual(['queued', 'validate', 'snapshot', 'start', 'probe', 'activate', 'complete'])
    expect(operationStages('repair', true)).toEqual(['queued', 'validate', 'download', 'stage', 'quiesce', 'start', 'probe', 'activate', 'complete'])
    expect(operationStages('uninstall', false)).toEqual(['queued', 'validate', 'revoke', 'remove_code', 'complete'])
    expect(operationStages('uninstall', true)).toEqual(['queued', 'validate', 'revoke', 'stop', 'remove_code', 'remove_data', 'complete'])
  })
  it('delete data, restore and import', () => {
    expect(operationStages('delete_data', true)).toEqual(['queued', 'validate', 'revoke', 'remove_data', 'complete'])
    expect(operationStages('restore', true)).toEqual(['queued', 'validate', 'revoke', 'quiesce', 'snapshot', 'restore_data', 'start', 'probe', 'activate', 'complete'])
    expect(operationStages('import_data', true)).toEqual(['queued', 'validate', 'quiesce', 'snapshot', 'import', 'start', 'probe', 'activate', 'complete'])
  })
  it('knows no stages for an action it was not told about', () => {
    expect(operationStages('teleport', true)).toEqual([])
  })
})

describe('stepModel', () => {
  it('marks what is behind as finished, the current stage as running, and what is ahead as waiting', () => {
    const model = stepModel({ action: 'install', stage: 'stage', terminal: false, revision: '4' }, true, undefined)
    expect(model.map(s => [s.stage, s.status])).toEqual([
      ['queued', 'finish'], ['validate', 'finish'], ['download', 'finish'], ['stage', 'process'], ['prepare_data', 'wait'], ['start', 'wait'], ['probe', 'wait'], ['activate', 'wait'], ['complete', 'wait'],
    ])
  })
  it('finishes every step, the last included, when the operation is complete', () => {
    expect(stepModel({ action: 'disable', stage: 'complete', terminal: true }, false, undefined).every(s => s.status === 'finish')).toBe(true)
  })
  it('places a failure by the revision, so a page opened after a reload marks the right step', () => {
    // update, unmanaged: queued=1 ... migrate=7, so a failure in migrate leaves revision 8.
    const model = stepModel({ action: 'update', stage: 'failed', terminal: true, revision: '8' }, false, undefined)
    expect(model.map(s => s.status)).toEqual(['finish', 'finish', 'finish', 'finish', 'finish', 'finish', 'error', 'wait', 'wait'])
    // recovery_required is read the same way.
    expect(stepModel({ action: 'install', stage: 'recovery_required', terminal: true, revision: '4' }, false, undefined).find(s => s.status === 'error')?.stage).toBe('download')
  })
  it('trusts the revision over the last stage seen running', () => {
    const model = stepModel({ action: 'update', stage: 'failed', terminal: true, revision: '6' }, false, 'migrate')
    expect(model.find(s => s.status === 'error')?.stage).toBe('quiesce')
  })
  it('marks no step for a failed operation of an action that can be settled, unless the stage was seen running', () => {
    // Managed install: start is the 6th stage. A failure there leaves revision 7; SettleNeverStarted makes it 8, which
    // reads like a failure at probe. The reply cannot tell them apart, so nothing is marked.
    const settled = stepModel({ action: 'install', stage: 'failed', terminal: true, revision: '8' }, true, undefined)
    expect(settled.every(s => s.status === 'wait')).toBe(true)
    // SettleStartNeverRan closes an enable or repair that ended at start.
    expect(stepModel({ action: 'enable', stage: 'failed', terminal: true, revision: '5' }, true, undefined).every(s => s.status === 'wait')).toBe(true)
    expect(stepModel({ action: 'repair', stage: 'failed', terminal: true, revision: '9' }, true, undefined).every(s => s.status === 'wait')).toBe(true)
    // Seen live, a direct failure marks the right step.
    expect(stepModel({ action: 'install', stage: 'failed', terminal: true, revision: '7' }, true, 'start').find(s => s.status === 'error')?.stage).toBe('start')
    // Seen at download, then stage and prepare_data passed between two polls and it failed at prepare_data (revision 6):
    // the stage seen last is not where it failed, so nothing is marked rather than the wrong step.
    expect(stepModel({ action: 'install', stage: 'failed', terminal: true, revision: '6' }, false, 'download').every(s => s.status !== 'error')).toBe(true)
  })
  it('still reads recovery_required from the revision, for any action', () => {
    expect(stepModel({ action: 'enable', stage: 'recovery_required', terminal: true, revision: '5' }, true, undefined).find(s => s.status === 'error')?.stage).toBe('start')
  })
  it('does not invent where a failure happened without a revision or a stage seen', () => {
    expect(stepModel({ action: 'update', stage: 'recovery_required', terminal: true }, false, undefined).every(s => s.status === 'wait')).toBe(true)
  })
  it('finishes a stage the polls skipped over', () => {
    // Seen at quiesce, then at migrate: snapshot was never observed but is behind.
    const model = stepModel({ action: 'update', stage: 'migrate', terminal: false, revision: '7' }, false, 'quiesce')
    expect(model.find(s => s.stage === 'snapshot')?.status).toBe('finish')
    expect(model.find(s => s.stage === 'migrate')?.status).toBe('process')
  })
  it('puts a stage the list does not know where it was seen, never dropping it', () => {
    const model = stepModel({ action: 'install', stage: 'start', terminal: false }, false, undefined)
    expect(model.find(s => s.status === 'process')?.stage).toBe('start')
  })
})
