import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// An alert never sits flush against what is above or below it (the install progress block touching
// its Refresh button, the register-publisher warning under Add scope). One rule in index.css spaces
// every alert inside a generic block container; classed layout containers (rp-*, Flex, Space, Row)
// keep their own gap. Alerts carry no hand-set margin of their own, so the spacing cannot drift.
const root = dirname(fileURLToPath(import.meta.url))
const rules = readFileSync(join(root, 'index.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap(name => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return sources(path)
    return /\.tsx$/.test(name) && !/\.test\.tsx$/.test(name) ? [path] : []
  })
}

// The style props of every <Alert …> opening tag. Braces are balanced, so `onClose={() => …}` or
// `title={<>…</>}` before `style` does not end the tag early, and a `style` on JSX nested inside a
// prop (a <ul> in `description`) is not mistaken for the alert's own.
export function alertStyleProps(text: string): string[] {
  const out: string[] = []
  for (const m of text.matchAll(/<Alert\b/g)) {
    let depth = 0, start = -1
    for (let i = m.index! + 6; i < text.length; i++) {
      const c = text[i]
      if (depth === 0 && c === '>') break
      if (depth === 0 && c === '{' && /\bstyles?=$/.test(text.slice(i - 7, i))) start = i
      if (c === '{') depth++
      else if (c === '}' && --depth === 0 && start >= 0) { out.push(text.slice(start, i + 1)); start = -1 }
    }
  }
  return out
}

const containers = ['.ant-card-body', '.rp-admin-page-body', '.rp-settings-panel', '.ant-modal-body', '.ant-drawer-body', '.ant-form', '.ant-tabs-tabpane', '.ant-collapse-body', 'div:not([class])']

describe('alert spacing', () => {
  it('spaces an alert from the content before and after it', () => {
    expect(rules).toMatch(/\.ant-alert:not\(:last-child\)\s*\{[^}]*margin-block-end:\s*12px/)
    expect(rules).toMatch(/>\s*\*\s*\+\s*\.ant-alert\s*\{[^}]*margin-block-start:\s*12px/)
  })

  it('applies inside every generic block container, and not inside inline flex or grid divs', () => {
    const selector = rules.match(/:where\(([^{]*)\)\s*>\s*\.ant-alert:not/)?.[1] ?? ''
    for (const c of containers) expect(selector).toContain(c)
    for (const d of ['flex', 'inline-flex', 'grid', 'inline-grid']) expect(selector).toContain(`:not([style*="display: ${d}"])`)
  })

  it('finds an alert\'s own style wherever it sits in the tag', () => {
    expect(alertStyleProps('<Alert onClose={() => x()} style={{ marginBottom: 12 }} />')).toEqual(['{{ marginBottom: 12 }}'])
    expect(alertStyleProps('<Alert title={<>a</>}\n  styles={{ root: { margin: 0 } }} />')).toEqual(['{{ root: { margin: 0 } }}'])
    expect(alertStyleProps('<Alert description={<ul style={{ margin: 0 }} />} />')).toEqual([])
  })

  it('gives no alert a hand-set margin', () => {
    const offenders = sources(root).flatMap(path =>
      alertStyleProps(readFileSync(path, 'utf8'))
        .filter(style => /\bmargin/.test(style))
        .map(style => `${relative(root, path)}: ${style.slice(0, 80)}`))
    expect(offenders).toEqual([])
  })
})
