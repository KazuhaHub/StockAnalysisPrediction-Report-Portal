// The operation history of an application installation (GET /api/admin/app-platform/operations). It is read-only: a row
// says what ran, between which versions, how far it got and how it ended. The progress of one run is still read from
// `operations/{id}`; the list only tells the console which runs exist.

export interface PlatformOperationRow {
  operation_id: string
  installation_id: string
  action: string
  stage: string
  managed: boolean
  /** The operation's state revision, a decimal string: it places a failure on a step. */
  revision: string
  terminal: boolean
  /** The public error code of a failed operation, translated by the page. */
  error_code?: string
  /** Milliseconds since the epoch, as decimal strings. */
  created_ms: string
  updated_ms: string
  completed_ms?: string
  from_version?: string
  to_version?: string
}

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const decimal = /^[0-9]{1,18}$/
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const optionalText = (value: unknown) => value === undefined || (typeof value === 'string' && value.length <= 128)

/** Reads the `items` of a history reply. Anything that is not exactly that shape yields null, never a partial list. */
export function parseOperationRows(value: unknown): PlatformOperationRow[] | null {
  if (!object(value) || !Array.isArray(value.items) || value.items.length > 500) return null
  const rows: PlatformOperationRow[] = []
  for (const item of value.items) {
    if (!object(item)) return null
    const { operation_id, installation_id, action, stage, managed, revision, terminal, error_code, created_ms, updated_ms, completed_ms, from_version, to_version } = item
    if (typeof operation_id !== 'string' || !uuid.test(operation_id) || typeof installation_id !== 'string' || !uuid.test(installation_id)
      || typeof action !== 'string' || !action || typeof stage !== 'string' || !stage || typeof managed !== 'boolean' || typeof terminal !== 'boolean'
      || typeof revision !== 'string' || !decimal.test(revision) || typeof created_ms !== 'string' || !decimal.test(created_ms)
      || typeof updated_ms !== 'string' || !decimal.test(updated_ms) || (completed_ms !== undefined && (typeof completed_ms !== 'string' || !decimal.test(completed_ms)))
      || !optionalText(error_code) || !optionalText(from_version) || !optionalText(to_version)) return null
    rows.push({
      operation_id, installation_id, action, stage, managed, revision, terminal, created_ms, updated_ms,
      ...(typeof error_code === 'string' && error_code ? { error_code } : {}),
      ...(typeof completed_ms === 'string' ? { completed_ms } : {}),
      ...(typeof from_version === 'string' && from_version ? { from_version } : {}),
      ...(typeof to_version === 'string' && to_version ? { to_version } : {}),
    })
  }
  return rows
}
