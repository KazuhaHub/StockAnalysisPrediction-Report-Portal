import type { ExportPolicy, ExportPolicyEntry, ExportPolicyPatch } from '../api/types'

// Which endpoints the shared export components talk to (ADR 0046). The administrator's page and the
// reader's page are the same components; only the base path and a few affordances differ.
export type ExportMode = 'admin' | 'reader'

export const exportBase = (mode: ExportMode = 'admin'): string => (mode === 'reader' ? '/api/exports' : '/api/admin/exports')

// Why a reader's export cannot be submitted, or null when it can. The per-export limit is checked
// before the daily quota, the order the server enforces them in, so the page and the 422/429 it falls
// back on never disagree about which limit was hit.
export type ExportBlock = { kind: 'tooLarge'; n: number; max: number } | { kind: 'quota'; n: number; remaining: number } | null

export function exportBlock(total: number, policy: ExportPolicy | null | undefined): ExportBlock {
  if (!policy || policy.unlimited) return null
  if (total > policy.max_reports) return { kind: 'tooLarge', n: total, max: policy.max_reports }
  if (total > policy.remaining) return { kind: 'quota', n: total, remaining: policy.remaining }
  return null
}

// The largest export the reader could submit right now.
export const exportCeiling = (policy: ExportPolicy): number => Math.max(0, Math.min(policy.max_reports, policy.remaining))

type Field = 'allowed' | 'max_reports' | 'daily_quota'
// A number box that was cleared holds null until the administrator types again; such a draft is not
// savable (policyDraftValid).
export type PolicyDraft = {
  allowed: { inheriting: boolean; value: boolean }
  max_reports: { inheriting: boolean; value: number | null }
  daily_quota: { inheriting: boolean; value: number | null }
}

// A draft starts from what the OU has set itself; an inheriting field starts from the effective value
// so switching it to "set here" begins at what the OU already gets.
export function policyDraft(e: ExportPolicyEntry): PolicyDraft {
  return {
    allowed: { inheriting: e.allowed.own === null, value: e.allowed.own ?? e.allowed.effective },
    max_reports: { inheriting: e.max_reports.own === null, value: e.max_reports.own ?? e.max_reports.effective },
    daily_quota: { inheriting: e.daily_quota.own === null, value: e.daily_quota.own ?? e.daily_quota.effective },
  }
}

// Only what changed goes on the wire: null where the OU now inherits, the value where it overrides.
// The Default OU cannot inherit, so its fields are always written as values.
export function policyPatch(e: ExportPolicyEntry, d: PolicyDraft): ExportPolicyPatch {
  const patch: ExportPolicyPatch = {}
  const pick = (key: Field) => {
    const own: unknown = e[key].own
    const next: unknown = e.is_default || !d[key].inheriting ? d[key].value : null
    if (next !== own) (patch as Record<string, unknown>)[key] = next
  }
  pick('allowed')
  pick('max_reports')
  pick('daily_quota')
  return patch
}

// Whether every field that will be written holds a value in range (the server enforces the same
// bounds and answers 400 otherwise).
export function policyDraftValid(e: ExportPolicyEntry, d: PolicyDraft): boolean {
  const inRange = (f: { inheriting: boolean; value: number | null }, hi: number) =>
    (!e.is_default && f.inheriting) || (f.value !== null && Number.isInteger(f.value) && f.value >= 1 && f.value <= hi)
  return inRange(d.max_reports, 5000) && inRange(d.daily_quota, 100000)
}

// A daily quota below the per-export maximum is allowed but makes the quota the binding limit.
export const quotaBelowMax = (maxReports: number | null, dailyQuota: number | null): boolean =>
  maxReports !== null && dailyQuota !== null && dailyQuota < maxReports

// What a field would resolve to if this OU stopped overriding it: the parent's answer. While the OU
// inherits already its own resolved value is exactly that. The parent of a root-level OU is the
// Default OU, and the Default OU itself inherits from nobody.
export function inheritedView<K extends Field>(entry: ExportPolicyEntry, parent: ExportPolicyEntry | undefined, key: K): { value: ExportPolicyEntry[K]['effective']; source: number } {
  const own = entry[key]
  if (own.own === null || !parent) return { value: own.effective, source: own.source_ou }
  const up = parent[key]
  return { value: up.effective, source: up.source_ou }
}
