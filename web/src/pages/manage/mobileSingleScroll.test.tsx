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

type Item = { at: number; header: string; body: string }

/** Top-level items in source order; an at-rule's body is the text of its nested rules. */
function topLevel(src: string): Item[] {
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
    found.push({ at: i, header, body: src.slice(open + 1, j - 1) })
    i = j
  }
  return found
}

const PHONE = '@media (max-width: 767px)'
const norm = (sel: string) => sel.replace(/\s+/g, ' ').trim()
const props = (body: string) => body.split(';').map((d) => d.split(':')[0].trim()).filter(Boolean)
const items = topLevel(css)
const phoneBlocks = items.filter((it) => it.header === PHONE)
const phone = phoneBlocks.map((b) => b.body).join('\n')

describe('phone layout has a single vertical scroll region', () => {
  it('uses one phone breakpoint for related rules', () => {
    expect(css).not.toMatch(/max-width:\s*760px/)
  })

  it('never lets a base rule that follows a phone override cancel it', () => {
    const offenders: string[] = []
    for (const block of phoneBlocks) {
      for (const rule of topLevel(block.body)) {
        const decls = new Set(props(rule.body))
        for (const sel of rule.header.split(',').map(norm)) {
          for (const later of items) {
            if (later.at <= block.at || later.header.startsWith('@')) continue
            if (!later.header.split(',').map(norm).includes(sel)) continue
            const shared = props(later.body).filter((d) => decls.has(d))
            if (shared.length) offenders.push(`${sel} { ${shared.join(', ')} }`)
          }
        }
      }
    }
    expect(offenders).toEqual([])
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

  it('removes fixed-height tree panes on page-level phone panes only', () => {
    expect(phone).toMatch(/\.rp-app-access__tree \.rp-ou-picker__tree\s*\{[^}]*max-height:\s*none/)
    expect(phone).toMatch(/\.rp-export-type-tree\s*\{[^}]*max-height:\s*none/)
    expect(phone).not.toMatch(/(^|\n)\s*\.rp-ou-picker--manage \.rp-ou-picker__tree\s*\{[^}]*max-height:\s*none/)
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
