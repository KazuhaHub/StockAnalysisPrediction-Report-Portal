import { describe, expect, test } from 'vitest'
import {
  beginOperation,
  createPrototype,
  advanceOperation,
  visibleApps,
  visibleStatus,
  configureDigest,
  saveNote,
} from './model'

function finish(model: ReturnType<typeof createPrototype>) {
  for (let i = 0; model.operation?.phase === 'running' && i < 20; i++)
    model = advanceOperation(model, 'none')
  return model
}

describe('application platform interaction model', () => {
  test('public market status cannot disclose an installation hidden from the reader', () => {
    const model = finish(
      beginOperation(createPrototype(), 'admin', true, 'install', 'digest', {
        access: 'admins',
        notifications: false,
      }),
    )
    expect(visibleStatus(model, 'reader', 'digest')).toBe('accessRequired')
    expect(visibleStatus(model, 'admin', 'digest')).toBe('setup')
  })
  test('retained data requires explicit reuse and fresh access selection on reinstall', () => {
    const removed = finish(
      beginOperation(
        saveNote(createPrototype(), 'Retained note'),
        'admin',
        true,
        'uninstall',
        'notes',
      ),
    )
    expect(
      beginOperation(removed, 'admin', true, 'install', 'notes', {
        access: 'admins',
        notifications: false,
      }),
    ).toBe(removed)
    const reinstalled = finish(
      beginOperation(removed, 'admin', true, 'install', 'notes', {
        access: 'admins',
        notifications: false,
        reuseRetained: true,
      }),
    )
    expect(reinstalled.note).toBe('Retained note')
    expect(visibleApps(reinstalled, 'reader')).not.toContain('notes')
  })
  test('managed install is one operation and needs no internal URL or signing key', () => {
    let model = beginOperation(
      createPrototype(),
      'admin',
      true,
      'install',
      'digest',
      { access: 'internal', notifications: false },
    )
    const id = model.operation?.id
    model = beginOperation(model, 'admin', true, 'install', 'digest', {
      access: 'internal',
      notifications: true,
    })
    expect(model.operation?.id).toBe(id)
    model = finish(model)
    expect(model.apps.digest.status).toBe('setup')
    expect(model.apps.digest.notifications).toBe(false)
    model = configureDigest(model, 'admin', 'Example digest')
    expect(model.apps.digest.status).toBe('ready')
    expect(visibleApps(model, 'reader')).toContain('digest')
  })

  test('runtime absence blocks managed installation but permits a static app', () => {
    const initial = createPrototype()
    expect(
      beginOperation(initial, 'admin', false, 'install', 'digest', {
        access: 'admins',
        notifications: false,
      }),
    ).toBe(initial)
    let model = finish(
      beginOperation(initial, 'admin', true, 'uninstall', 'notes'),
    )
    model = finish(
      beginOperation(model, 'admin', false, 'install', 'notes', {
        access: 'internal',
        notifications: false,
        reuseRetained: true,
      }),
    )
    expect(model.apps.notes.status).toBe('ready')
  })

  test('readers cannot install, configure, update, disable or reveal admin-only apps', () => {
    const initial = createPrototype()
    for (const action of [
      'install',
      'update',
      'disable',
      'uninstall',
    ] as const) {
      expect(
        beginOperation(
          initial,
          'reader',
          true,
          action,
          action === 'install' ? 'digest' : 'notes',
        ),
      ).toBe(initial)
    }
    let model = finish(
      beginOperation(initial, 'admin', true, 'install', 'digest', {
        access: 'admins',
        notifications: false,
      }),
    )
    expect(configureDigest(model, 'reader', 'Ignored')).toBe(model)
    expect(visibleApps(model, 'reader')).not.toContain('digest')
    model = configureDigest(model, 'admin', 'Admin sample')
    expect(visibleApps(model, 'reader')).not.toContain('digest')
  })

  test('failed update retains the old version and saved notes; recovery is a separate action', () => {
    let model = saveNote(createPrototype(), 'A retained note')
    model = beginOperation(model, 'admin', true, 'update', 'notes')
    model = advanceOperation(model, 'none')
    model = advanceOperation(model, 'download')
    expect(model.operation?.phase).toBe('failed')
    expect(model.apps.notes.version).toBe('1.0.0')
    expect(model.note).toBe('A retained note')
    expect(advanceOperation(model, 'none')).toBe(model)
  })

  test('uninstall retains data and explicit deletion is only available after removal', () => {
    const initial = saveNote(createPrototype(), 'Retain this')
    expect(beginOperation(initial, 'admin', true, 'delete', 'notes')).toBe(
      initial,
    )
    let model = finish(
      beginOperation(initial, 'admin', true, 'uninstall', 'notes'),
    )
    expect(model.apps.notes.retained).toBe(true)
    expect(model.note).toBe('Retain this')
    model = finish(beginOperation(model, 'admin', true, 'delete', 'notes'))
    expect(model.apps.notes.retained).toBe(false)
    expect(model.note).toBe('')
  })

  test('disable removes the openable state and enable restores it without a version update', () => {
    let model = finish(
      beginOperation(createPrototype(), 'admin', true, 'disable', 'notes'),
    )
    expect(model.apps.notes.status).toBe('disabled')
    model = finish(beginOperation(model, 'admin', true, 'enable', 'notes'))
    expect(model.apps.notes.status).toBe('ready')
    expect(model.apps.notes.version).toBe('1.0.0')
  })
})
