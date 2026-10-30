import type { AppAccessOU, AppLayoutEntry, AppSummary, UserGroupRow } from '../../api/types'

// Shared test data for the Apps administration pages. Two OUs: Default (1) and Research (2, under Default).
export const groups = [
  { id: 1, name: 'Default', is_default: true, members: 0, weight: 0, parent_id: 0 },
  { id: 2, name: 'Research', is_default: false, members: 3, weight: null, parent_id: 1 },
] as unknown as UserGroupRow[]

export const acc = (ou: number, own: boolean | null, effective: boolean, source_ou: number): AppAccessOU => ({
  ou,
  own,
  effective,
  source_ou,
  inherited: own === null && source_ou !== 0,
})

export const layout = (): AppLayoutEntry[] => [
  // Installed app, closed in both OUs.
  { key: 'hello', name: 'Hello', builtin: false, access: [acc(1, false, false, 1), acc(2, null, false, 1)] },
  // Built-in, open everywhere through the system default.
  { key: 'builtin:batch', name: 'batch', builtin: true, access: [acc(1, null, true, 0), acc(2, null, true, 0)] },
  // Built-in, open on Default, overridden closed on Research.
  { key: 'builtin:quotes', name: 'quotes', builtin: true, access: [acc(1, null, true, 0), acc(2, false, false, 2)] },
]

export const installed: AppSummary[] = [{ id: 'hello', name: 'Hello', icon: 'H', version: '1.2.0', entry: 'index.html', scopes: ['query', 'extension.read'] }]
