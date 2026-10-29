import { describe, expect, it } from 'vitest'
import type { ExportFilter, ExportCond } from '../api/types'
import {
  BELOW_ENTRY_CONDS,
  MAX_CONDITIONS,
  MAX_DEPTH,
  activePreset,
  addConditionAt,
  applyBelowEntryPreset,
  applySelection,
  clearBelowEntryPreset,
  isBelowEntryActive,
  auditFilterToTree,
  buildExportRequest,
  cleanFilter,
  coerceValues,
  countConditions,
  defaultOperator,
  isCompleteCond,
  decodeFilter,
  encodeFilter,
  exportLink,
  filterDepth,
  isChipExpressible,
  nestAt,
  parseExportParams,
  pickNode,
  presetCond,
  removeAt,
  removeChip,
  selectionOf,
  toggleKind,
  toggleSubtype,
  upsertChip,
  validateFilter,
  type TypeTree,
} from './exportFilter'

const c = (field: string, operator: ExportCond['operator'], ...values: string[]): ExportCond => ({ field, operator, values })
const and = (...items: ExportFilter['items']): ExportFilter => ({ op: 'and', items })

describe('filter URL encoding', () => {
  it('round-trips unicode and nested groups', () => {
    const f: ExportFilter = and(c('rtype', 'in', '汇总', '估值分析'), { op: 'or', items: [c('symbol', 'is', '600519'), c('q', 'contains', '白酒 ✓')] })
    const s = encodeFilter(f)
    expect(s).toMatch(/^[A-Za-z0-9_-]+$/)
    expect(decodeFilter(s)).toEqual(f)
  })
  it('rejects garbage and malformed trees', () => {
    expect(decodeFilter('!!!')).toBeNull()
    expect(decodeFilter(encodeFilter({ op: 'xor', items: [] } as never))).toBeNull()
    expect(decodeFilter(btoa('{"op":"and","items":[{"field":1}]}').replace(/=/g, ''))).toBeNull()
    expect(decodeFilter('')).toBeNull()
  })
})

describe('limits', () => {
  it('counts conditions and depth', () => {
    const f = and(c('a', 'is', '1'), { op: 'or', items: [c('b', 'is', '1'), { op: 'and', items: [c('c', 'is', '1')] }] })
    expect(countConditions(f)).toBe(3)
    expect(filterDepth(f)).toBe(3)
    expect(validateFilter(f)).toBeNull()
  })
  it('flags too many conditions and too deep trees', () => {
    const many = and(...Array.from({ length: MAX_CONDITIONS + 1 }, (_, i) => c(`f${i}`, 'is', 'x')))
    expect(validateFilter(many)).toBe('conditions')
    const deep: ExportFilter = and({ op: 'and', items: [{ op: 'and', items: [{ op: 'and', items: [c('a', 'is', '1')] }] }] })
    expect(filterDepth(deep)).toBe(MAX_DEPTH + 1)
    expect(validateFilter(deep)).toBe('depth')
  })
  it('builder refuses the 21st condition', () => {
    let f = and()
    for (let i = 0; i < MAX_CONDITIONS; i++) f = addConditionAt(f, [], c(`f${i}`, 'is', 'x'))!
    expect(countConditions(f)).toBe(MAX_CONDITIONS)
    expect(addConditionAt(f, [], c('extra', 'is', 'x'))).toBeNull()
  })
  it('nest wraps a condition and refuses past depth 3', () => {
    const f = and(c('a', 'is', '1'))
    const n1 = nestAt(f, [0])!
    expect(n1.items[0]).toEqual({ op: 'or', items: [c('a', 'is', '1')] })
    const n2 = nestAt(n1, [0, 0])!
    expect(filterDepth(n2)).toBe(3)
    expect(nestAt(n2, [0, 0, 0])).toBeNull()
  })
  it('removeAt removes by path without mutating', () => {
    const f = and(c('a', 'is', '1'), c('b', 'is', '2'))
    const r = removeAt(f, [0])
    expect(r.items).toEqual([c('b', 'is', '2')])
    expect(f.items).toHaveLength(2)
  })
})

describe('cleanFilter', () => {
  it('drops incomplete conditions and empty groups', () => {
    const f = and(c('a', 'in'), c('b', 'between', '2026-01-01'), c('c', 'is', 'x'), { op: 'or', items: [c('d', 'is')] })
    expect(cleanFilter(f)).toEqual(and(c('c', 'is', 'x')))
  })
})

describe('chips', () => {
  it('is expressible for one AND group of unique-field conditions', () => {
    expect(isChipExpressible(and())).toBe(true)
    expect(isChipExpressible(and(c('a', 'is', '1'), c('b', 'in', '2')))).toBe(true)
    expect(isChipExpressible({ op: 'or', items: [c('a', 'is', '1')] })).toBe(true)
  })
  it('is not expressible with OR, nesting, or a repeated field', () => {
    expect(isChipExpressible({ op: 'or', items: [c('a', 'is', '1'), c('b', 'is', '1')] })).toBe(false)
    expect(isChipExpressible(and({ op: 'or', items: [c('a', 'is', '1')] }))).toBe(false)
    expect(isChipExpressible(and(c('a', 'is', '1'), c('a', 'is_not', '2')))).toBe(false)
  })
  it('upserts one chip per field and removes by field', () => {
    let f = upsertChip(and(), c('a', 'is', '1'))
    f = upsertChip(f, c('b', 'in', 'x'))
    f = upsertChip(f, c('a', 'is_not', '2'))
    expect(f.items).toEqual([c('a', 'is_not', '2'), c('b', 'in', 'x')])
    expect(removeChip(f, 'a').items).toEqual([c('b', 'in', 'x')])
  })
})

describe('type tree selection', () => {
  const tree: TypeTree = [
    { kind: '投资决策', subtypes: ['汇总', '估值分析'] },
    { kind: '行业研究', subtypes: ['白酒'] },
  ]
  it('a whole kind becomes a kind-in condition', () => {
    const sel = toggleKind(tree, { kinds: [], subtypes: [] }, '投资决策')
    expect(applySelection(and(), sel).items).toEqual([c('kind', 'in', '投资决策')])
  })
  it('individual subtypes become an rtype-in condition', () => {
    const sel = toggleSubtype(tree, toggleSubtype(tree, { kinds: [], subtypes: [] }, '汇总'), '白酒')
    expect(applySelection(and(), sel).items).toEqual([c('rtype', 'in', '汇总', '白酒')])
  })
  it('checking a kind absorbs its individually-checked subtypes', () => {
    const sel = toggleKind(tree, { kinds: [], subtypes: ['汇总', '白酒'] }, '投资决策')
    expect(sel).toEqual({ kinds: ['投资决策'], subtypes: ['白酒'] })
  })
  it('unchecking one subtype of a whole kind expands the rest', () => {
    const sel = toggleSubtype(tree, { kinds: ['投资决策'], subtypes: [] }, '汇总')
    expect(sel).toEqual({ kinds: [], subtypes: ['估值分析'] })
  })
  it('single mode selects exactly one node and toggles it off', () => {
    let sel = pickNode({ kinds: [], subtypes: [] }, { kind: '行业研究' })
    expect(sel).toEqual({ kinds: ['行业研究'], subtypes: [] })
    sel = pickNode(sel, { subtype: '汇总' })
    expect(sel).toEqual({ kinds: [], subtypes: ['汇总'] })
    expect(pickNode(sel, { subtype: '汇总' })).toEqual({ kinds: [], subtypes: [] })
  })
  it('stays in sync with chips: reads root conditions and replaces them', () => {
    const f = and(c('kind', 'in', '投资决策'), c('rtype', 'in', '白酒'), c('symbol', 'is', '600519'))
    expect(selectionOf(f)).toEqual({ kinds: ['投资决策'], subtypes: ['白酒'] })
    const next = applySelection(f, { kinds: [], subtypes: [] })
    expect(next.items).toEqual([c('symbol', 'is', '600519')])
  })
  it('ignores an OR root', () => {
    expect(selectionOf({ op: 'or', items: [c('kind', 'in', 'x')] })).toEqual({ kinds: [], subtypes: [] })
  })
})

describe('time presets', () => {
  it('builds a between condition on the time field', () => {
    expect(presetCond('at', 'today', '2026-10-29')).toEqual(c('at', 'between', '2026-10-29', '2026-10-29'))
    expect(presetCond('at', '7d', '2026-10-29')).toEqual(c('at', 'between', '2026-10-23', '2026-10-29'))
    expect(presetCond('at', '30d', '2026-10-29')).toEqual(c('at', 'between', '2026-09-30', '2026-10-29'))
  })
  it('recognises the active preset and calls anything else custom', () => {
    expect(activePreset(and(c('at', 'between', '2026-10-23', '2026-10-29')), 'at', '2026-10-29')).toBe('7d')
    expect(activePreset(and(c('at', 'between', '2026-01-01', '2026-02-01')), 'at', '2026-10-29')).toBe('custom')
    expect(activePreset(and(c('at', 'after', '2026-01-01')), 'at', '2026-10-29')).toBe('custom')
    expect(activePreset(and(), 'at', '2026-10-29')).toBe('all')
  })
})

describe('links and URL state', () => {
  it('round-trips through exportLink and parseExportParams', () => {
    const f = and(c('action', 'in', 'report.read'))
    const link = exportLink('audit', f)
    expect(link.startsWith('/manage/exports?')).toBe(true)
    const parsed = parseExportParams(new URLSearchParams(link.split('?')[1]))
    expect(parsed).toEqual({ source: 'audit', filter: f })
  })
  it('omits f for an empty filter and tolerates a bad one', () => {
    expect(exportLink('reports')).toBe('/manage/exports?source=reports')
    expect(parseExportParams(new URLSearchParams('source=runs&f=!!'))).toEqual({ source: 'runs', filter: undefined })
  })
  it('translates audit page filters', () => {
    expect(auditFilterToTree({ action: 'report.read', actor: ' bob ', ip: '', q: 'x', since: '2026-10-01' })).toEqual(
      and(c('action', 'in', 'report.read'), c('actor', 'is', 'bob'), c('detail', 'contains', 'x'), c('at', 'after', '2026-10-01')),
    )
    expect(auditFilterToTree({})).toEqual(and())
  })
})

describe('export request payload', () => {
  const base = { source: 'reports', format: 'md_zip' as const, filter: and(c('a', 'is', '1'), c('b', 'in')), columns: ['id', 'title'], name: '  Q3  ', tz: 'panel' as const }
  it('includes report options only for reports md_zip and cleans the filter', () => {
    const body = buildExportRequest({ ...base, options: { front_matter: true, index_csv: true, layout: 'date' } })
    expect(body).toEqual({
      source: 'reports', format: 'md_zip', filter: and(c('a', 'is', '1')), columns: ['id', 'title'], name: 'Q3', tz: 'panel',
      options: { front_matter: true, index_csv: true, layout: 'date' },
    })
  })
  it('drops options and an empty name for log formats', () => {
    const body = buildExportRequest({ ...base, source: 'audit', format: 'csv', name: ' ', options: { layout: 'date' }, tz: 'utc' })
    expect(body).toEqual({ source: 'audit', format: 'csv', filter: and(c('a', 'is', '1')), columns: ['id', 'title'], tz: 'utc' })
  })
})

describe('condition helpers', () => {
  it('coerces value arity when the operator changes', () => {
    expect(coerceValues('is', ['a', 'b'])).toEqual(['a'])
    expect(coerceValues('in', ['a', 'b'])).toEqual(['a', 'b'])
    expect(coerceValues('between', ['a'])).toEqual([])
    expect(coerceValues('between', ['a', 'b', 'c'])).toEqual(['a', 'b'])
  })
  it('picks in for enums and the first operator otherwise', () => {
    expect(defaultOperator({ type: 'enum', operators: ['is', 'in'] })).toBe('in')
    expect(defaultOperator({ type: 'text', operators: ['contains', 'is'] })).toBe('contains')
  })
  it('knows when a condition is complete', () => {
    expect(isCompleteCond(c('a', 'between', '1'))).toBe(false)
    expect(isCompleteCond(c('a', 'between', '1', '2'))).toBe(true)
  })
})

describe('below-entry preset', () => {
  const sym: ExportCond = { field: 'symbol', operator: 'is', values: ['600519'] }
  it('adds both chips, keeps the others, and is idempotent', () => {
    const f = applyBelowEntryPreset({ op: 'and', items: [sym] })
    expect(f.items).toEqual([sym, ...BELOW_ENTRY_CONDS])
    expect(applyBelowEntryPreset(f)).toEqual(f)
    expect(isBelowEntryActive(f)).toBe(true)
  })
  it('is active only when both chips say yes', () => {
    expect(isBelowEntryActive({ op: 'and', items: [BELOW_ENTRY_CONDS[1]] })).toBe(false)
    expect(isBelowEntryActive({ op: 'and', items: [BELOW_ENTRY_CONDS[0], { ...BELOW_ENTRY_CONDS[1], values: ['no'] }] })).toBe(false)
  })
  it('clears only its own chips', () => {
    const f = applyBelowEntryPreset({ op: 'and', items: [sym] })
    expect(clearBelowEntryPreset(f)).toEqual({ op: 'and', items: [sym] })
  })
})
