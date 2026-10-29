import dayjs from 'dayjs'
import type { ExportCond, ExportFilter, ExportGroup, ExportOperator, ExportRequest } from '../api/types'

// The export filter tree (ADR 0045) and everything the Export page does to it. The same tree
// drives the preview count, the preview rows and the exported file, and it travels in the URL, so
// every helper here is pure and returns a new tree.

export const MAX_CONDITIONS = 20
export const MAX_DEPTH = 3
/** Groups plus conditions, the server's exportMaxNodes. */
export const MAX_NODES = 50

export type Node = ExportGroup | ExportCond
export type Path = number[]

const OPERATORS: ExportOperator[] = ['is', 'is_not', 'in', 'not_in', 'contains', 'before', 'after', 'between']

export const isGroup = (n: Node): n is ExportGroup => 'items' in n
export const isCond = (n: Node): n is ExportCond => !isGroup(n)
export const emptyFilter = (): ExportFilter => ({ op: 'and', items: [] })

export function countConditions(n: Node): number {
  return isGroup(n) ? n.items.reduce((sum, i) => sum + countConditions(i), 0) : 1
}

/** Depth of the tree; the root group is depth 1. */
export function filterDepth(n: Node): number {
  return isGroup(n) ? 1 + n.items.reduce((max, i) => Math.max(max, isGroup(i) ? filterDepth(i) : 0), 0) : 0
}

export function validateFilter(f: ExportFilter): null | 'conditions' | 'depth' | 'nodes' {
  if (countConditions(f) > MAX_CONDITIONS) return 'conditions'
  if (filterDepth(f) > MAX_DEPTH) return 'depth'
  if (countNodes(f) > MAX_NODES) return 'nodes'
  return null
}

/** countNodes counts groups and conditions together, root included, as the server does. */
export function countNodes(n: Node): number {
  return isGroup(n) ? 1 + n.items.reduce((sum, c) => sum + countNodes(c), 0) : 1
}

// ---------------------------------------------------------------------------------------------
// URL encoding: compact base64url JSON. UTF-8 first, so a Chinese report type survives.

export function encodeFilter(f: ExportFilter): string {
  const bytes = new TextEncoder().encode(JSON.stringify(f))
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function wellFormed(n: unknown): n is Node {
  if (!n || typeof n !== 'object') return false
  const o = n as Record<string, unknown>
  if ('items' in o) {
    return (o.op === 'and' || o.op === 'or') && Array.isArray(o.items) && o.items.every(wellFormed)
  }
  return (
    typeof o.field === 'string' &&
    typeof o.operator === 'string' &&
    OPERATORS.includes(o.operator as ExportOperator) &&
    Array.isArray(o.values) &&
    o.values.every((v) => typeof v === 'string')
  )
}

/** decodeFilter returns null for anything that is not a well-formed filter tree. */
export function decodeFilter(s: string): ExportFilter | null {
  if (!s) return null
  try {
    const b64 = s.replace(/-/g, '+').replace(/_/g, '/')
    const bin = atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4))
    const bytes = Uint8Array.from(bin, (ch) => ch.charCodeAt(0))
    const v: unknown = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes))
    return wellFormed(v) && isGroup(v) ? v : null
  } catch {
    return null
  }
}

export function exportLink(source: string, filter?: ExportFilter): string {
  const p = new URLSearchParams({ source })
  if (filter && filter.items.length > 0) p.set('f', encodeFilter(filter))
  return `/manage/exports?${p}`
}

export function parseExportParams(p: URLSearchParams): { source?: string; filter?: ExportFilter } {
  const f = p.get('f')
  return { source: p.get('source') || undefined, filter: f ? (decodeFilter(f) ?? undefined) : undefined }
}

// ---------------------------------------------------------------------------------------------
// Completeness: a half-edited row lives in the tree while the admin types, but it never reaches
// the server or the URL.

function cleanCond(c: ExportCond): ExportCond | null {
  if (c.operator === 'in' || c.operator === 'not_in') {
    const values = c.values.filter((v) => v !== '')
    return values.length > 0 ? { ...c, values } : null
  }
  const need = c.operator === 'between' ? 2 : 1
  return c.values.length >= need && c.values.slice(0, need).every((v) => v !== '') ? { ...c, values: c.values.slice(0, need) } : null
}

/** Whether a condition is complete enough to be sent. */
export const isCompleteCond = (c: ExportCond): boolean => cleanCond(c) !== null

/** Adjusts a value list to the arity of a newly chosen operator. */
export function coerceValues(operator: ExportOperator, values: string[]): string[] {
  if (operator === 'in' || operator === 'not_in') return values
  if (operator === 'between') return values.length >= 2 ? values.slice(0, 2) : []
  return values.slice(0, 1)
}

/** `in` is the natural way to pick enum values; otherwise the field's first operator. */
export function defaultOperator(f: { type: string; operators: ExportOperator[] }): ExportOperator {
  if (f.type === 'enum' && f.operators.includes('in')) return 'in'
  return f.operators[0] ?? 'is'
}

function cleanGroup(g: ExportGroup): ExportGroup | null {
  const items: Node[] = []
  for (const i of g.items) {
    const n = isGroup(i) ? cleanGroup(i) : cleanCond(i)
    if (n) items.push(n)
  }
  return items.length > 0 ? { op: g.op, items } : null
}

export function cleanFilter(f: ExportFilter): ExportFilter {
  return cleanGroup(f) ?? { op: f.op, items: [] }
}

// ---------------------------------------------------------------------------------------------
// Chips: the Filter tab can show a tree only when it is one AND group with a condition per field.

export function isChipExpressible(f: ExportFilter): boolean {
  if (f.items.length > 1 && f.op !== 'and') return false
  if (!f.items.every(isCond)) return false
  const fields = (f.items as ExportCond[]).map((c) => c.field)
  return new Set(fields).size === fields.length
}

export const chipsOf = (f: ExportFilter): ExportCond[] => f.items.filter(isCond)

export function upsertChip(f: ExportFilter, cond: ExportCond): ExportFilter {
  const i = f.items.findIndex((n) => isCond(n) && n.field === cond.field)
  const items = [...f.items]
  if (i >= 0) items[i] = cond
  else items.push(cond)
  return { op: 'and', items }
}

export function removeChip(f: ExportFilter, field: string): ExportFilter {
  return { op: 'and', items: f.items.filter((n) => !(isCond(n) && n.field === field)) }
}

// ---------------------------------------------------------------------------------------------
// Builder: path-addressed edits. A refused edit (limit reached) returns null so the UI can say why.

function nodeAt(root: Node, path: Path): Node | undefined {
  let n: Node | undefined = root
  for (const i of path) {
    if (!n || !isGroup(n)) return undefined
    n = n.items[i]
  }
  return n
}

function mapAt(root: ExportGroup, path: Path, fn: (n: Node) => Node | null): ExportGroup {
  if (path.length === 0) return (fn(root) as ExportGroup | null) ?? root
  const [head, ...rest] = path
  const items: Node[] = []
  root.items.forEach((n, i) => {
    if (i !== head) return void items.push(n)
    if (rest.length === 0) {
      const r = fn(n)
      if (r) items.push(r)
    } else if (isGroup(n)) items.push(mapAt(n, rest, fn))
    else items.push(n)
  })
  return { ...root, items }
}

export function addConditionAt(f: ExportFilter, groupPath: Path, cond: ExportCond): ExportFilter | null {
  const g = nodeAt(f, groupPath)
  if (!g || !isGroup(g) || countConditions(f) >= MAX_CONDITIONS || countNodes(f) >= MAX_NODES) return null
  return mapAt(f, groupPath, (n) => ({ ...(n as ExportGroup), items: [...(n as ExportGroup).items, cond] }))
}

export function updateAt(f: ExportFilter, path: Path, node: Node): ExportFilter {
  return mapAt(f, path, () => node)
}

export function removeAt(f: ExportFilter, path: Path): ExportFilter {
  return path.length === 0 ? { ...f, items: [] } : mapAt(f, path, () => null)
}

export function setOpAt(f: ExportFilter, groupPath: Path, op: 'and' | 'or'): ExportFilter {
  return mapAt(f, groupPath, (n) => (isGroup(n) ? { ...n, op } : n))
}

/** nestAt wraps the node at `path` in a new group of the opposite operator. */
export function nestAt(f: ExportFilter, path: Path): ExportFilter | null {
  if (path.length === 0) return null
  const parent = nodeAt(f, path.slice(0, -1))
  const target = nodeAt(f, path)
  if (!parent || !isGroup(parent) || !target) return null
  const next = mapAt(f, path, (n) => ({ op: parent.op === 'and' ? 'or' : 'and', items: [n] }))
  return filterDepth(next) > MAX_DEPTH || countNodes(next) > MAX_NODES ? null : next
}

/** Whether the node at `path` can still be nested without passing the depth limit. */
export function canNestAt(f: ExportFilter, path: Path): boolean {
  return nestAt(f, path) !== null
}

// ---------------------------------------------------------------------------------------------
// Report type tree: kind (parent) -> subtype (child), kept as conditions on the root group so it
// is the same state the chips show.

export type TypeTree = { kind: string; subtypes: string[] }[]
export interface TypeSelection {
  kinds: string[]
  subtypes: string[]
}
export const KIND_FIELD = 'kind'
export const SUBTYPE_FIELD = 'rtype'

export function selectionOf(f: ExportFilter): TypeSelection {
  const sel: TypeSelection = { kinds: [], subtypes: [] }
  if (f.op !== 'and') return sel
  for (const n of f.items) {
    if (!isCond(n) || (n.operator !== 'in' && n.operator !== 'is')) continue
    if (n.field === KIND_FIELD) sel.kinds = [...n.values]
    if (n.field === SUBTYPE_FIELD) sel.subtypes = [...n.values]
  }
  return sel
}

export function applySelection(f: ExportFilter, sel: TypeSelection): ExportFilter {
  if (f.op === 'or' && f.items.length > 1) return f
  const items = f.items.filter((n) => !(isCond(n) && (n.field === KIND_FIELD || n.field === SUBTYPE_FIELD)))
  if (sel.kinds.length) items.push({ field: KIND_FIELD, operator: 'in', values: sel.kinds })
  if (sel.subtypes.length) items.push({ field: SUBTYPE_FIELD, operator: 'in', values: sel.subtypes })
  return { op: 'and', items }
}

const without = (xs: string[], drop: string[]) => xs.filter((x) => !drop.includes(x))

export function toggleKind(tree: TypeTree, sel: TypeSelection, kind: string): TypeSelection {
  if (sel.kinds.includes(kind)) return { kinds: without(sel.kinds, [kind]), subtypes: sel.subtypes }
  const subs = tree.find((k) => k.kind === kind)?.subtypes ?? []
  return { kinds: [...sel.kinds, kind], subtypes: without(sel.subtypes, subs) }
}

export function toggleSubtype(tree: TypeTree, sel: TypeSelection, subtype: string): TypeSelection {
  const parent = tree.find((k) => k.subtypes.includes(subtype))
  if (parent && sel.kinds.includes(parent.kind)) {
    // Unchecking one child of a wholly checked kind leaves its siblings explicitly checked.
    return {
      kinds: without(sel.kinds, [parent.kind]),
      subtypes: [...sel.subtypes, ...parent.subtypes.filter((s) => s !== subtype && !sel.subtypes.includes(s))],
    }
  }
  return {
    kinds: sel.kinds,
    subtypes: sel.subtypes.includes(subtype) ? without(sel.subtypes, [subtype]) : [...sel.subtypes, subtype],
  }
}

/** Single-select mode: the picked node is the whole selection; picking it again clears it. */
export function pickNode(sel: TypeSelection, node: { kind: string } | { subtype: string }): TypeSelection {
  if ('kind' in node) return sel.kinds.length === 1 && sel.kinds[0] === node.kind && !sel.subtypes.length ? { kinds: [], subtypes: [] } : { kinds: [node.kind], subtypes: [] }
  return sel.subtypes.length === 1 && sel.subtypes[0] === node.subtype && !sel.kinds.length ? { kinds: [], subtypes: [] } : { kinds: [], subtypes: [node.subtype] }
}

export type CheckState = 'on' | 'partial' | 'off'
export function kindCheckState(tree: TypeTree, sel: TypeSelection, kind: string): CheckState {
  if (sel.kinds.includes(kind)) return 'on'
  const subs = tree.find((k) => k.kind === kind)?.subtypes ?? []
  const n = subs.filter((s) => sel.subtypes.includes(s)).length
  return n === 0 ? 'off' : n === subs.length ? 'on' : 'partial'
}

// ---------------------------------------------------------------------------------------------
// Time presets act on the source's time field with a civil-date `between`.

export type TimePreset = 'all' | 'today' | '7d' | '30d' | 'custom'
const PRESET_DAYS: Record<'today' | '7d' | '30d', number> = { today: 0, '7d': 6, '30d': 29 }

export function presetCond(timeField: string, preset: 'today' | '7d' | '30d', today: string): ExportCond {
  const from = dayjs(today).subtract(PRESET_DAYS[preset], 'day').format('YYYY-MM-DD')
  return { field: timeField, operator: 'between', values: [from, today] }
}

export function activePreset(f: ExportFilter, timeField: string, today: string): TimePreset {
  const cond = f.items.find((n): n is ExportCond => isCond(n) && n.field === timeField)
  if (!cond) return 'all'
  for (const p of ['today', '7d', '30d'] as const) {
    const want = presetCond(timeField, p, today)
    if (cond.operator === 'between' && cond.values[0] === want.values[0] && cond.values[1] === want.values[1]) return p
  }
  return 'custom'
}

// ---------------------------------------------------------------------------------------------
// Forecast screen preset: the newest forecast of each stock, kept only where the latest price is
// below the entry price. Two chips, set and cleared together.

export const BELOW_ENTRY_CONDS: ExportCond[] = [
  { field: 'latest_per_symbol', operator: 'is', values: ['yes'] },
  { field: 'below_entry', operator: 'is', values: ['yes'] },
]

const hasCond = (f: ExportFilter, c: ExportCond) =>
  f.items.some((n) => isCond(n) && n.field === c.field && n.operator === c.operator && n.values.join() === c.values.join())

export const isBelowEntryActive = (f: ExportFilter): boolean => BELOW_ENTRY_CONDS.every((c) => hasCond(f, c))

export function applyBelowEntryPreset(f: ExportFilter): ExportFilter {
  return BELOW_ENTRY_CONDS.reduce(upsertChip, f)
}

export function clearBelowEntryPreset(f: ExportFilter): ExportFilter {
  return BELOW_ENTRY_CONDS.reduce((acc, c) => removeChip(acc, c.field), f)
}

// ---------------------------------------------------------------------------------------------
// Audit page hand-off.

export interface AuditFilterInput {
  action?: string
  actor?: string
  ip?: string
  q?: string
  since?: string
}

export function auditFilterToTree(a: AuditFilterInput): ExportFilter {
  const items: Node[] = []
  const add = (field: string, operator: ExportOperator, v?: string) => {
    const value = (v ?? '').trim()
    if (value) items.push({ field, operator, values: [value] })
  }
  add('action', 'in', a.action)
  add('actor', 'is', a.actor)
  add('ip', 'is', a.ip)
  add('q', 'contains', a.q)
  add('at', 'after', a.since)
  return { op: 'and', items }
}

// ---------------------------------------------------------------------------------------------
// Run history hand-off. Only the filters the runs source declares travel; the free-text search and
// the derived "scheduled" status have no field there and are dropped.

export interface RunsFilterInput {
  status?: string
  createdBy?: string
  target?: string
}

export function runsFilterToTree(r: RunsFilterInput): ExportFilter {
  const items: Node[] = []
  const add = (field: string, v?: string) => {
    const value = (v ?? '').trim()
    if (value) items.push({ field, operator: 'is', values: [value] })
  }
  add('status', r.status)
  add('created_by', r.createdBy)
  add('target', r.target)
  return { op: 'and', items }
}

// ---------------------------------------------------------------------------------------------
// Export request.

export function buildExportRequest(i: {
  source: string
  format: ExportRequest['format']
  filter: ExportFilter
  /** Left out for a reader, who does not choose columns. */
  columns?: string[]
  name?: string
  tz?: 'panel' | 'utc'
  options?: ExportRequest['options']
}): ExportRequest {
  const body: ExportRequest = { source: i.source, format: i.format, filter: cleanFilter(i.filter) }
  if (i.columns) body.columns = i.columns
  const name = (i.name ?? '').trim()
  if (name) body.name = name
  if (i.tz) body.tz = i.tz
  if (i.source === 'reports' && i.format === 'md_zip' && i.options) body.options = i.options
  return body
}

// Column choice per source, remembered in this browser only.
const colKey = (source: string) => `rp.export.columns.${source}`

export function loadColumns(source: string, available: { key: string; default: boolean }[]): string[] {
  const defaults = available.filter((c) => c.default).map((c) => c.key)
  try {
    const raw = JSON.parse(localStorage.getItem(colKey(source)) ?? 'null')
    if (Array.isArray(raw)) {
      const keys = raw.filter((k): k is string => typeof k === 'string' && available.some((c) => c.key === k))
      if (keys.length > 0) return keys
    }
  } catch {
    // Storage can be blocked or hold something else; the defaults are always right.
  }
  return defaults.length > 0 ? defaults : available.map((c) => c.key)
}

export function saveColumns(source: string, keys: string[]): void {
  try {
    localStorage.setItem(colKey(source), JSON.stringify(keys))
  } catch {
    // Per-viewer convenience only.
  }
}
