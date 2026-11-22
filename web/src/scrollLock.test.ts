import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// Opening a drawer or a dialog must not jump the page to the top (the header Tasks / queue drawer
// did). antd locks the page by putting `overflow-y: hidden` on <body>. That reaches the viewport only
// while <html> itself keeps the default `overflow: visible`; any other value on <html> (even
// `overflow-x: clip`) stops the propagation, so the 100%-tall body becomes its own clipped scroll box
// and the window's scroll position drops to 0. Sideways clipping therefore lives on <body>, whose
// value is handed to the viewport.
const root = dirname(fileURLToPath(import.meta.url))
const rules = readFileSync(join(root, 'index.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')

export function declarationsFor(css: string, element: string): string[] {
  const out: string[] = []
  for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = m[1].split(',').map(s => s.trim())
    if (selectors.includes(element)) out.push(m[2])
  }
  return out
}

describe('page scroll lock', () => {
  it('finds rules by exact selector', () => {
    expect(declarationsFor('html, body { overflow-x: clip; } html .x { overflow: auto; }', 'html')).toEqual([' overflow-x: clip; '])
  })

  it('leaves <html> at the default overflow so a drawer keeps the scroll position', () => {
    for (const element of ['html', ':root']) for (const block of declarationsFor(rules, element)) expect(block).not.toMatch(/\boverflow(-[xy])?\s*:/)
  })

  it('still clips sideways page scroll on <body>', () => {
    expect(declarationsFor(rules, 'body').join(';')).toMatch(/overflow-x\s*:\s*clip/)
  })
})
