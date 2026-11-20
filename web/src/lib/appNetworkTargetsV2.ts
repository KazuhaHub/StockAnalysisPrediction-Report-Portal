// Administrator side of the network.http destination review (doc 175 sections 2.2 and 2.6). The
// console and the in-app dialog both plan a change, show the SERVER's diff, and consume the plan;
// nothing here, and nothing an app sent, is shown to the administrator as text of its own.
import { api } from '../api/client'
import type { NetworkTargetRequest } from './network-v2'

const root = '/api/admin/app-platform'

export interface PolicyTarget {
  id: string
  origin: string
  paths: string[]
  methods: string[]
  network: string
  request_bytes?: number
  response_bytes?: number
  deadline_ms?: number
  network_target_sha256: string
}
export interface PolicyBounds {
  request_bytes: number
  response_bytes: number
  deadline_ms: number
  concurrency: number
}
export interface NetworkPolicyView {
  format: string
  targets: PolicyTarget[]
  policy_sha256: string
  bounds: PolicyBounds
}
export interface AllowlistEntry { prefix: string; ports: number[]; plain_http: boolean }
export interface AddedTarget extends PolicyTarget { plain_http: boolean; allowlist: AllowlistEntry | null }
export interface LimitsChange { id: string; old: PolicyBounds; new: PolicyBounds }
export interface ReviewDiff {
  added: AddedTarget[]
  removed: PolicyTarget[]
  paused_bindings: string[]
  bounds: { old: PolicyBounds; new: PolicyBounds }
  narrowing: LimitsChange[]
  allowlist: { old_sha256: string; new_sha256: string }
}
export interface CapabilityPlan {
  plan_id: string
  plan_sha256: string
  expires_ms: string
  policy_revision: string
  diff: ReviewDiff
}
export interface NetworkPolicyState { policyRevision: string; policy: NetworkPolicyView }
export interface BoundsChange {
  requestBytes?: number
  responseBytes?: number
  deadlineMs?: number
  concurrency?: number
}
export interface NetworkChange {
  add: readonly NetworkTargetRequest[]
  remove: readonly string[]
  bounds?: BoundsChange
}

const sha = /^[a-f0-9]{64}$/
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const counter = /^[1-9][0-9]{0,18}$/

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}
function strings(value: unknown): value is string[] {
  return Array.isArray(value) && value.every(item => typeof item === 'string')
}
function count(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}
function bounds(value: unknown): value is PolicyBounds {
  return record(value) && count(value.request_bytes) && count(value.response_bytes) && count(value.deadline_ms) && count(value.concurrency)
}
function target(value: unknown): value is PolicyTarget {
  return record(value) && typeof value.id === 'string' && typeof value.origin === 'string' && strings(value.paths) && strings(value.methods) &&
    typeof value.network === 'string' && typeof value.network_target_sha256 === 'string' &&
    (['request_bytes', 'response_bytes', 'deadline_ms'] as const).every(key => value[key] === undefined || count(value[key]))
}
function allowlist(value: unknown): value is AllowlistEntry {
  return record(value) && typeof value.prefix === 'string' && Array.isArray(value.ports) && value.ports.every(count) && typeof value.plain_http === 'boolean'
}
function added(value: unknown): value is AddedTarget {
  return target(value) && typeof (value as unknown as AddedTarget).plain_http === 'boolean' &&
    ((value as unknown as AddedTarget).allowlist === null || allowlist((value as unknown as AddedTarget).allowlist))
}

export function parseNetworkPolicy(value: unknown): NetworkPolicyView | null {
  if (!record(value) || typeof value.format !== 'string' || typeof value.policy_sha256 !== 'string' || !sha.test(value.policy_sha256) ||
    !Array.isArray(value.targets) || !value.targets.every(target) || !bounds(value.bounds)) return null
  return value as unknown as NetworkPolicyView
}

export function parseCapabilityPlan(value: unknown): CapabilityPlan | null {
  if (!record(value) || typeof value.plan_id !== 'string' || !uuid.test(value.plan_id) || typeof value.plan_sha256 !== 'string' || !sha.test(value.plan_sha256) ||
    typeof value.expires_ms !== 'string' || typeof value.policy_revision !== 'string' || !counter.test(value.policy_revision) || !record(value.diff)) return null
  const diff = value.diff
  // The server omits nothing, but a Go nil slice arrives as null.
  const add = diff.added ?? [], removed = diff.removed ?? [], narrowing = diff.narrowing ?? []
  if (!Array.isArray(add) || !add.every(added) || !Array.isArray(removed) || !removed.every(target) ||
    !Array.isArray(narrowing) || !narrowing.every(item => record(item) && typeof item.id === 'string' && bounds(item.old) && bounds(item.new)) ||
    !strings(diff.paused_bindings) || !record(diff.bounds) || !bounds(diff.bounds.old) || !bounds(diff.bounds.new) ||
    !record(diff.allowlist) || typeof diff.allowlist.old_sha256 !== 'string' || typeof diff.allowlist.new_sha256 !== 'string') return null
  return { ...value, diff: { ...diff, added: add, removed, narrowing } } as unknown as CapabilityPlan
}

// The two values every plan names: the policy revision it was made against and the digest of the
// policy bytes. Both come from the settings response, never from the caller.
export async function readNetworkPolicy(installation: string, signal?: AbortSignal): Promise<NetworkPolicyState | null> {
  const settings = await api.get<{ policy_revision?: unknown; networkPolicy?: unknown }>(`${root}/installations/${installation}/settings`, { signal })
  const policy = parseNetworkPolicy(settings.networkPolicy)
  if (!policy || typeof settings.policy_revision !== 'string' || !counter.test(settings.policy_revision)) return null
  return { policyRevision: settings.policy_revision, policy }
}

// A plan never carries `bounds` unless the console administrator edited them; the in-app request
// cannot, because its caller is the app.
export async function planNetworkChange(installation: string, state: NetworkPolicyState, change: NetworkChange, signal?: AbortSignal): Promise<CapabilityPlan> {
  const body: Record<string, unknown> = {
    capability: 'network.http',
    policyRevision: state.policyRevision,
    previousPolicySha256: state.policy.policy_sha256,
    add: change.add,
    remove: change.remove,
  }
  if (change.bounds && Object.keys(change.bounds).length) body.bounds = change.bounds
  const plan = parseCapabilityPlan(await api.post<unknown>(`${root}/installations/${installation}/capability-policy/plan`, body, signal ? { signal } : undefined))
  if (!plan) throw new Error('invalid capability policy plan')
  return plan
}

export async function consumeNetworkPlan(plan: CapabilityPlan): Promise<{ policyRevision: string; pausedBindings: string[] }> {
  const result = await api.post<{ ok?: unknown; policy_revision?: unknown; paused_bindings?: unknown }>(
    `${root}/capability-policy-reviews/${plan.plan_id}`, { sha256: plan.plan_sha256 })
  if (result.ok !== true || typeof result.policy_revision !== 'string' || !counter.test(result.policy_revision)) throw new Error('invalid capability policy result')
  return { policyRevision: result.policy_revision, pausedBindings: strings(result.paused_bindings) ? result.paused_bindings : [] }
}
