import { describe, expect, it } from 'vitest'
import { operationStages, stepModel } from './operationSteps'

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
    const model = stepModel({ action: 'install', stage: 'stage', terminal: false }, true, undefined)
    expect(model.map(s => [s.stage, s.status])).toEqual([
      ['queued', 'finish'], ['validate', 'finish'], ['download', 'finish'], ['stage', 'process'], ['prepare_data', 'wait'], ['start', 'wait'], ['probe', 'wait'], ['activate', 'wait'], ['complete', 'wait'],
    ])
  })
  it('finishes every step, the last included, when the operation is complete', () => {
    expect(stepModel({ action: 'disable', stage: 'complete', terminal: true }, false, undefined).every(s => s.status === 'finish')).toBe(true)
  })
  it('marks the stage a failure happened in as failed, from the last one seen running', () => {
    const model = stepModel({ action: 'update', stage: 'failed', terminal: true }, false, 'migrate')
    expect(model.map(s => s.status)).toEqual(['finish', 'finish', 'finish', 'finish', 'finish', 'finish', 'error', 'wait', 'wait'])
  })
  it('does not invent where a failure happened when no running stage was seen', () => {
    const model = stepModel({ action: 'update', stage: 'recovery_required', terminal: true }, false, undefined)
    expect(model.every(s => s.status === 'wait')).toBe(true)
  })
  it('puts a stage the list does not know where it was seen, never dropping it', () => {
    const model = stepModel({ action: 'install', stage: 'start', terminal: false }, false, undefined)
    expect(model.find(s => s.status === 'process')?.stage).toBe('start')
  })
})
