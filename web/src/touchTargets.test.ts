import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// On a touch screen the small controls get a target a finger can hit (WCAG 2.2 asks 24px; Apple
// 44pt) without changing how they look: measured at 375px, small switches were 28×16, bare
// checkboxes 16×16 and number-input arrows about 1px wide.
const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'index.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
const coarse = [...css.matchAll(/@media \(pointer: coarse\)\s*\{([\s\S]*?)\n\}/g)].map((m) => m[1]).join('\n')

describe('touch targets', () => {
  it('reads the stylesheet', () => {
    expect(css.length).toBeGreaterThan(10_000)
  })

  it('widens a switch’s hit area on touch screens', () => {
    expect(coarse).toMatch(/\.ant-switch::after\s*\{[^}]*inset:\s*-/)
  })

  it('widens an unlabelled checkbox’s hit area without moving it', () => {
    expect(coarse).toMatch(/\.ant-checkbox-wrapper[^{]*\{[^}]*padding:\s*10px[^}]*margin:\s*-10px/)
  })

  it('drops number-input arrows a finger cannot hit', () => {
    expect(coarse).toMatch(/\.ant-input-number-actions\s*\{[^}]*display:\s*none/)
  })

  it('stops a spinning status icon for readers who ask for less motion', () => {
    expect(css).toMatch(/prefers-reduced-motion: reduce\)\s*\{[^}]*\.anticon-spin[^{]*\{[^}]*animation:\s*none/)
  })
})
