// Synthetic in-memory interaction state. This is not the platform executor,
// authorization service, durable storage or a substitute for backend acceptance.
export type AppID = 'notes' | 'digest'
export type Role = 'admin' | 'reader'
export type Access = 'admins' | 'internal'
export type Action =
  | 'install'
  | 'update'
  | 'disable'
  | 'enable'
  | 'uninstall'
  | 'delete'
  | 'restore'
export type Failure = 'none' | 'download' | 'readiness'
export type Stage =
  | 'validate'
  | 'download'
  | 'prepare'
  | 'snapshot'
  | 'migrate'
  | 'start'
  | 'readiness'
  | 'activate'
  | 'revoke'
  | 'stop'
  | 'remove'
  | 'erase'
  | 'restore'
export type AppStatus = 'available' | 'ready' | 'setup' | 'disabled' | 'removed'

export const catalogue: Record<
  AppID,
  { managed: boolean; version: string; icon: string }
> = {
  notes: { managed: false, version: '1.1.0', icon: '📝' },
  digest: { managed: true, version: '1.0.0', icon: '📬' },
}
export interface Installation {
  status: AppStatus
  version: string
  access: Access
  notifications: boolean
  retained: boolean
  digestTitle: string
  backup?: { version: string; note: string }
}
export interface PrototypeModel {
  apps: Record<AppID, Installation>
  note: string
  sequence: number
  operation?: {
    id: number
    app: AppID
    action: Action
    stages: Stage[]
    index: number
    phase: 'running' | 'failed' | 'complete'
    review?: { access: Access; notifications: boolean; reuseRetained?: boolean }
  }
}

export function createPrototype(): PrototypeModel {
  const common = {
    access: 'admins' as const,
    notifications: false,
    retained: false,
    digestTitle: '',
  }
  return {
    apps: {
      notes: {
        ...common,
        status: 'ready',
        version: '1.0.0',
        access: 'internal',
      },
      digest: { ...common, status: 'available', version: '' },
    },
    note: '',
    sequence: 0,
  }
}
export function visibleApps(model: PrototypeModel, role: Role): AppID[] {
  return (Object.keys(model.apps) as AppID[]).filter(
    (id) =>
      !['available', 'removed'].includes(model.apps[id].status) &&
      (role === 'admin' || model.apps[id].access === 'internal'),
  )
}
export function visibleStatus(
  model: PrototypeModel,
  role: Role,
  id: AppID,
): AppStatus | 'accessRequired' {
  return role === 'reader' && !visibleApps(model, role).includes(id)
    ? 'accessRequired'
    : model.apps[id].status
}
export function beginOperation(
  model: PrototypeModel,
  role: Role,
  runtime: boolean,
  action: Action,
  id: AppID,
  review?: { access: Access; notifications: boolean; reuseRetained?: boolean },
): PrototypeModel {
  if (role !== 'admin' || model.operation?.phase === 'running') return model
  const app = model.apps[id],
    managed = catalogue[id].managed
  if (
    managed &&
    !runtime &&
    ['install', 'update', 'enable', 'restore'].includes(action)
  )
    return model
  const installed = !['available', 'removed'].includes(app.status)
  if (
    action === 'install' &&
    (installed || !review || (app.retained && !review.reuseRetained))
  )
    return model
  if (action === 'delete' && (app.status !== 'removed' || !app.retained))
    return model
  if (action === 'enable' && app.status !== 'disabled') return model
  if (action === 'disable' && !['ready', 'setup'].includes(app.status))
    return model
  if (
    action === 'update' &&
    (!['ready', 'setup'].includes(app.status) ||
      app.version === catalogue[id].version)
  )
    return model
  if (action === 'restore' && (!installed || !app.backup)) return model
  if (action === 'uninstall' && !installed) return model
  const start: Stage[] = managed ? ['start', 'readiness'] : []
  const sequences: Record<Action, Stage[]> = {
    install: ['validate', 'download', 'prepare', ...start, 'activate'],
    update: [
      'validate',
      'download',
      'snapshot',
      'migrate',
      ...start,
      'activate',
    ],
    disable: ['revoke', ...(managed ? ['stop' as const] : [])],
    enable: ['validate', ...start, 'activate'],
    uninstall: ['revoke', ...(managed ? ['stop' as const] : []), 'remove'],
    delete: ['revoke', 'erase'],
    restore: ['revoke', 'snapshot', 'restore', ...start, 'activate'],
  }
  return {
    ...model,
    sequence: model.sequence + 1,
    operation: {
      id: model.sequence + 1,
      app: id,
      action,
      stages: sequences[action],
      index: 0,
      phase: 'running',
      review,
    },
  }
}
export function advanceOperation(
  model: PrototypeModel,
  failure: Failure,
): PrototypeModel {
  const op = model.operation
  if (!op || op.phase !== 'running') return model
  if (failure !== 'none' && op.stages[op.index] === failure)
    return { ...model, operation: { ...op, phase: 'failed' } }
  if (op.index + 1 < op.stages.length)
    return { ...model, operation: { ...op, index: op.index + 1 } }
  let note = model.note
  const previous = model.apps[op.app],
    next = { ...previous }
  switch (op.action) {
    case 'install':
      next.version = catalogue[op.app].version
      next.status =
        catalogue[op.app].managed && !next.digestTitle ? 'setup' : 'ready'
      next.retained = false
      next.access = op.review!.access
      next.notifications = op.review!.notifications
      break
    case 'update':
      next.backup = { version: previous.version, note }
      next.version = catalogue[op.app].version
      if (op.review) {
        next.access = op.review.access
        next.notifications = op.review.notifications
      }
      break
    case 'disable':
      next.status = 'disabled'
      break
    case 'enable':
      next.status = op.app === 'digest' && !next.digestTitle ? 'setup' : 'ready'
      break
    case 'uninstall':
      next.status = 'removed'
      next.retained = true
      next.notifications = false
      break
    case 'delete':
      next.retained = false
      next.digestTitle = ''
      next.backup = undefined
      if (op.app === 'notes') note = ''
      break
    case 'restore':
      next.version = previous.backup!.version
      if (op.app === 'notes') note = previous.backup!.note
      next.status = op.app === 'digest' ? 'setup' : 'ready'
      next.notifications = false
      next.backup = undefined
      break
  }
  return {
    ...model,
    note,
    apps: { ...model.apps, [op.app]: next },
    operation: { ...op, phase: 'complete' },
  }
}
export function configureDigest(
  model: PrototypeModel,
  role: Role,
  title: string,
): PrototypeModel {
  if (
    role !== 'admin' ||
    model.apps.digest.status !== 'setup' ||
    !title.trim() ||
    title.length > 80
  )
    return model
  return {
    ...model,
    apps: {
      ...model.apps,
      digest: {
        ...model.apps.digest,
        status: 'ready',
        digestTitle: title.trim(),
      },
    },
  }
}
export function saveNote(model: PrototypeModel, note: string): PrototypeModel {
  return model.apps.notes.status === 'ready' && note.length <= 10_000
    ? { ...model, note }
    : model
}
