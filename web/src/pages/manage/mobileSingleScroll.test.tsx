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

const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'index.css'), 'utf8')

/** Every declaration block inside an `@media (max-width: 76Npx)` query, concatenated. */
function phoneCss(): string {
  const out: string[] = []
  const re = /@media \(max-width: 76\dpx\)\s*\{/g
  let m: RegExpExecArray | null
  while ((m = re.exec(css))) {
    let depth = 1
    let i = re.lastIndex
    while (i < css.length && depth > 0) {
      if (css[i] === '{') depth++
      else if (css[i] === '}') depth--
      i++
    }
    out.push(css.slice(re.lastIndex, i - 1))
  }
  return out.join('\n')
}

describe('phone layout has a single vertical scroll region', () => {
  const phone = phoneCss()

  it('lets the policy workspace grow with its content so only the page body scrolls', () => {
    expect(phone).toMatch(/\.rp-admin-page-body:has\(> \.rp-policy-workspace\)\s*\{[^}]*overflow:\s*auto/)
    expect(phone).toMatch(/\.rp-policy-workspace\s*\{[^}]*height:\s*auto/)
    expect(phone).toMatch(/\.rp-policy-scope,\s*\.rp-policy-content\s*\{[^}]*overflow:\s*visible/)
    expect(phone).not.toMatch(/\.rp-policy-scope\s*\{[^}]*max-height:\s*\d+px/)
  })

  it('removes fixed-height tree panes on phones', () => {
    expect(phone).not.toMatch(/max-height:\s*300px/)
    expect(phone).toMatch(/\.rp-app-access__tree \.rp-ou-picker__tree\s*\{[^}]*max-height:\s*none/)
    expect(phone).toMatch(/\.rp-export-type-tree\s*\{[^}]*max-height:\s*none/)
  })

  it('stacks the reader export policy columns through a class, not an inline grid', () => {
    const { container } = render(<App><ReaderExportPolicy /></App>)
    expect(container.innerHTML).not.toContain('minmax(220px')
    expect(css).toMatch(/\.rp-reader-export-policy\s*\{[^}]*grid-template-columns/)
    expect(phone).toMatch(/\.rp-reader-export-policy\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)/)
  })

  it('scrolls wide tables inside their own container', () => {
    expect(phone).toMatch(/\.rp-admin-page-body \.ant-table-content\s*\{[^}]*overflow-x:\s*auto/)
  })
})
