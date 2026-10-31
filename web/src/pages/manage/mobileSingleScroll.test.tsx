/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it, vi } from 'vitest'
import { render } from '@testing-library/react'
import { App } from 'antd'
import ReaderExportPolicy from './exports/ReaderExportPolicy'

vi.mock('../../api/client', () => ({
  api: { get: vi.fn().mockResolvedValue({ groups: [] }) },
  errText: (e: unknown) => String(e),
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k, i18n: { language: 'en-US' } }) }))

const raw = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'index.css'), 'utf8')
const css = raw.replace(/\/\*[\s\S]*?\*\//g, '')

type Item = { at: number; header: string; body: string; bodyAt: number }

/** Top-level items in source order; an at-rule's body is the text of its nested rules. */
function topLevel(src: string, base = 0): Item[] {
  const found: Item[] = []
  let i = 0
  while (i < src.length) {
    const open = src.indexOf('{', i)
    if (open < 0) break
    const header = src.slice(i, open).trim()
    let depth = 1
    let j = open + 1
    while (j < src.length && depth > 0) {
      if (src[j] === '{') depth++
      else if (src[j] === '}') depth--
      j++
    }
    found.push({ at: base + i, header, body: src.slice(open + 1, j - 1), bodyAt: base + open + 1 })
    i = j
  }
  return found
}

const PHONE = '@media (max-width: 767px)'
const norm = (sel: string) => sel.replace(/\s+/g, ' ').trim()
const decls = (body: string) => body.split(';').map((d) => d.trim()).filter(Boolean).map((d) => ({
  prop: d.slice(0, d.indexOf(':')).trim(),
  value: d.slice(d.indexOf(':') + 1).replace(/!important/, '').trim(),
  important: /!important/.test(d),
}))

/** [ids, classes/attributes/pseudo-classes, elements] */
function specificity(sel: string): [number, number, number] {
  const flat = sel.replace(/:(has|not|is|where)\(/g, ' (').replace(/[()]/g, ' ')
  const ids = (flat.match(/#[\w-]+/g) ?? []).length
  const classes = (flat.match(/\.[\w-]+|\[[^\]]*\]|(?<!:):[\w-]+/g) ?? []).length
  const elements = (flat.match(/(^|[\s>+~])[a-z][\w-]*/g) ?? []).length
  return [ids, classes, elements]
}
const cmp = (x: number[], y: number[]) => {
  for (let i = 0; i < x.length; i++) if (x[i] !== y[i]) return x[i] - y[i]
  return 0
}
/** The compound selector that picks the styled element. */
const subject = (sel: string) => (norm(sel).split(/\s*[ >+~]\s*/).pop() ?? '').replace(/:(has|not|is|where)\(.*$/, '')

/** True when a top-level block's rules apply at a 375px-wide viewport (the phone blocks are the overrides). */
function appliesOnPhone(header: string): boolean {
  if (!header.startsWith('@media')) return !header.startsWith('@')
  if (header === PHONE) return false
  const max = header.match(/^@media \(max-width: (\d+)px\)$/)
  if (max) return Number(max[1]) >= 375
  const min = header.match(/^@media \(min-width: (\d+)px\)$/)
  if (min) return Number(min[1]) <= 375
  return false
}

const items = topLevel(css)
const phoneBlocks = items.filter((it) => it.header === PHONE)
const phone = phoneBlocks.map((b) => b.body).join('\n')

type Rule = { sel: string; at: number; important: Set<string>; props: Map<string, string> }
function rulesOf(list: Item[]): Rule[] {
  return list.flatMap((it) => it.header.split(',').map(norm).map((sel) => {
    const d = decls(it.body)
    return { sel, at: it.at, props: new Map(d.map((x) => [x.prop, x.value] as const)), important: new Set(d.filter((x) => x.important).map((x) => x.prop)) }
  }))
}
// Only the layout this change owns: the stylesheet has other phone rules for unrelated pages.
const OWNED = /\.rp-(policy-|scope-fold|reader-export|table-scroll|ou-targets|app-access|export-type-tree|admin-page-body|ou-console|ou-picker--manage)/
// Specific pairs that sit in different regions of the app and can never match the same element.
const UNRELATED = new Set([
  '.rp-table-scroll .ant-table table | .rp-fc-scroll .ant-table-content > table',
  '.rp-ou-targets .ant-table table | .rp-fc-scroll .ant-table-content > table',
  '.rp-policy-scope > .ant-typography | .rp-settings-field > div > .ant-typography',
])
const phoneRules = rulesOf(phoneBlocks.flatMap((b) => topLevel(b.body, b.bodyAt))).filter((r) => OWNED.test(r.sel))
const otherRules = rulesOf(
  items.filter((it) => !it.header.startsWith('@')).concat(items.filter((it) => appliesOnPhone(it.header)).flatMap((it) => topLevel(it.body, it.bodyAt))),
)

describe('phone layout has a single vertical scroll region', () => {
  it('uses one phone breakpoint for related rules', () => {
    expect(css).not.toMatch(/max-width:\s*760px/)
  })

  it('never lets another rule that applies on a phone beat a phone override (specificity, then order)', () => {
    const offenders: string[] = []
    for (const p of phoneRules) {
      for (const o of otherRules) {
        if (o.sel === p.sel && o.at < p.at) continue
        if (UNRELATED.has(`${p.sel} | ${o.sel}`)) continue
        if (subject(o.sel) !== subject(p.sel)) continue
        for (const [prop, value] of p.props) {
          // Agreeing values cannot cancel each other, whichever element both end up matching.
          if (o.props.get(prop) === undefined || o.props.get(prop) === value) continue
          const pImp = p.important.has(prop)
          const oImp = o.important.has(prop)
          const order = cmp(specificity(o.sel), specificity(p.sel)) || o.at - p.at
          const otherWins = oImp !== pImp ? oImp : order > 0
          if (otherWins) offenders.push(`${p.sel} ${prop} is beaten by ${o.sel}`)
        }
      }
    }
    expect(offenders).toEqual([])
  })

  it('the cascade check ranks specificity before order', () => {
    expect(cmp(specificity('.a .b .c'), specificity('.a .b'))).toBeGreaterThan(0)
    expect(cmp(specificity('#x'), specificity('.a .b .c'))).toBeGreaterThan(0)
    expect(cmp(specificity('.rp-admin-page-body:has(> .rp-policy-workspace)'), specificity('.rp-admin-page-body'))).toBeGreaterThan(0)
    expect(subject('.c .b')).toBe(subject('.a .b'))
  })

  it('lets the policy workspace grow with its content so only the page body scrolls', () => {
    expect(phone).toMatch(/\.rp-admin-page-body:has\(> \.rp-policy-workspace\)\s*\{[^}]*overflow:\s*auto/)
    expect(phone).toMatch(/\.rp-policy-workspace\s*\{[^}]*height:\s*auto/)
    expect(phone).toMatch(/\.rp-policy-scope,\s*\.rp-policy-content\s*\{[^}]*overflow:\s*visible/)
    expect(phone).not.toMatch(/\.rp-policy-scope\s*\{[^}]*max-height:\s*\d+px/)
  })

  it('drops the scrollbar gutter and end padding so content is as wide as the header', () => {
    expect(phone).toMatch(/\.rp-admin-page-body\s*\{[^}]*scrollbar-gutter:\s*auto[^}]*padding-inline-end:\s*0/)
  })

  it('leaves no capped tree on phones now that every directory tree folds', () => {
    expect(phone).not.toMatch(/max-height:\s*300px/)
    expect(phone).toMatch(/\.rp-scope-fold \.rp-ou-picker__tree\s*\{[^}]*max-height:\s*none/)
    expect(phone).toMatch(/\.rp-app-access__tree \.rp-ou-picker__tree\s*\{[^}]*max-height:\s*none/)
    expect(phone).toMatch(/\.rp-export-type-tree\s*\{[^}]*max-height:\s*none/)
  })

  it('stacks the reader export policy columns through a class, not an inline grid', () => {
    const { container } = render(<App><ReaderExportPolicy /></App>)
    expect(container.innerHTML).not.toContain('minmax(220px')
    expect(phone).toMatch(/\.rp-reader-export-policy\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)/)
  })

  it('scrolls wide tables inside a dedicated wrapper, not every antd table', () => {
    expect(phone).toMatch(/\.rp-table-scroll\s*\{[^}]*overflow-x:\s*auto/)
    expect(phone).not.toMatch(/\.ant-table-content\s*\{/)
    expect(phone).not.toMatch(/\.rp-admin-page-body \.ant-space-item/)
  })
})
