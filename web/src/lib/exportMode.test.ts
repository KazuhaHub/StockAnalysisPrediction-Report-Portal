import { describe, expect, it } from 'vitest'
import type { ExportPolicy, ExportPolicyEntry, ExportPolicyResult } from '../api/types'
import { exportBase, exportBlock, exportCeiling, inheritedView, policyDraft, policyDraftValid, policyPatch, quotaBelowMax } from './exportMode'
import { exportDownloadUrl } from './exportJobs'

const policy = (over: Partial<ExportPolicy> = {}): ExportPolicy => ({
  allowed: true, max_reports: 200, daily_quota: 1000, used_today: 120, remaining: 880, unlimited: false, ...over,
})

const res = <T,>(own: T | null, effective: T, source_ou: number): ExportPolicyResult<T> => ({ own, effective, source_ou, inherited: own === null, available: true })
const entry = (over: Partial<ExportPolicyEntry> = {}): ExportPolicyEntry => ({
  id: 7, name: 'Research', parent_id: 1, is_default: false,
  allowed: res<boolean>(null, true, 1), max_reports: res<number>(null, 200, 1), daily_quota: res<number>(50, 50, 7), ...over,
})

describe('exportBase', () => {
  it('picks the endpoint family by mode', () => {
    expect(exportBase()).toBe('/api/admin/exports')
    expect(exportBase('admin')).toBe('/api/admin/exports')
    expect(exportBase('reader')).toBe('/api/exports')
    expect(exportDownloadUrl('a b', 'reader')).toBe('/api/exports/a%20b/file')
    expect(exportDownloadUrl('x')).toBe('/api/admin/exports/x/file')
  })
})

describe('exportBlock', () => {
  it('lets an export through at exactly the limits', () => {
    expect(exportBlock(200, policy())).toBeNull()
    expect(exportBlock(100, policy({ remaining: 100 }))).toBeNull()
  })
  it('names the per-export limit first, as the server enforces it', () => {
    expect(exportBlock(201, policy({ remaining: 5 }))).toEqual({ kind: 'tooLarge', n: 201, max: 200 })
  })
  it('names the daily allowance when only that is short', () => {
    expect(exportBlock(150, policy({ remaining: 149 }))).toEqual({ kind: 'quota', n: 150, remaining: 149 })
  })
  it('never blocks an administrator or an unknown policy', () => {
    expect(exportBlock(99999, policy({ unlimited: true }))).toBeNull()
    expect(exportBlock(99999, null)).toBeNull()
  })
  it('reports the largest export that would pass', () => {
    expect(exportCeiling(policy())).toBe(200)
    expect(exportCeiling(policy({ remaining: 30 }))).toBe(30)
    expect(exportCeiling(policy({ remaining: 0 }))).toBe(0)
  })
})

describe('policy draft and patch', () => {
  it('starts an inheriting field from its effective value', () => {
    const d = policyDraft(entry())
    expect(d.allowed).toEqual({ inheriting: true, value: true })
    expect(d.daily_quota).toEqual({ inheriting: false, value: 50 })
  })
  it('sends nothing when nothing changed', () => {
    const e = entry()
    expect(policyPatch(e, policyDraft(e))).toEqual({})
  })
  it('sends a value for an override and null for a return to inheriting', () => {
    const e = entry()
    const d = policyDraft(e)
    d.max_reports = { inheriting: false, value: 20 }
    d.daily_quota = { ...d.daily_quota, inheriting: true }
    expect(policyPatch(e, d)).toEqual({ max_reports: 20, daily_quota: null })
  })
  it('sends only the fields that changed', () => {
    const e = entry()
    const d = policyDraft(e)
    d.allowed = { inheriting: false, value: false }
    expect(policyPatch(e, d)).toEqual({ allowed: false })
  })
  it('never sends null for the Default OU, which inherits from nobody', () => {
    const e = entry({ is_default: true, allowed: res(false, false, 1), max_reports: res(200, 200, 1), daily_quota: res(1000, 1000, 1) })
    const d = policyDraft(e)
    d.max_reports = { inheriting: true, value: 300 }
    expect(policyPatch(e, d)).toEqual({ max_reports: 300 })
  })
})

describe('policyDraftValid', () => {
  it('refuses a cleared or out-of-range number that would be written', () => {
    const e = entry()
    const d = policyDraft(e)
    expect(policyDraftValid(e, d)).toBe(true)
    d.daily_quota = { inheriting: false, value: null }
    expect(policyDraftValid(e, d)).toBe(false)
    d.daily_quota = { inheriting: false, value: 100001 }
    expect(policyDraftValid(e, d)).toBe(false)
    d.daily_quota = { inheriting: true, value: null } // inheriting: the box is not written
    expect(policyDraftValid(e, d)).toBe(true)
    d.max_reports = { inheriting: false, value: 5000 }
    expect(policyDraftValid(e, d)).toBe(true)
    d.max_reports = { inheriting: false, value: 0 }
    expect(policyDraftValid(e, d)).toBe(false)
  })
  it('requires a value on the Default OU even when its draft says inheriting', () => {
    const e = entry({ is_default: true })
    const d = policyDraft(e)
    d.max_reports = { inheriting: true, value: null }
    expect(policyDraftValid(e, d)).toBe(false)
  })
})

describe('inheritedView', () => {
  it("shows the parent's answer when the OU has its own value", () => {
    const parent = entry({ id: 1, is_default: true, daily_quota: res(1000, 1000, 1) })
    expect(inheritedView(entry(), parent, 'daily_quota')).toEqual({ value: 1000, source: 1 })
  })
  it('shows its own resolved value, and where it came from, while it inherits', () => {
    expect(inheritedView(entry(), entry({ id: 1 }), 'max_reports')).toEqual({ value: 200, source: 1 })
  })
})

describe('quotaBelowMax', () => {
  it('flags a quota that is the binding limit', () => {
    expect(quotaBelowMax(200, 150)).toBe(true)
    expect(quotaBelowMax(200, 200)).toBe(false)
  })
})
