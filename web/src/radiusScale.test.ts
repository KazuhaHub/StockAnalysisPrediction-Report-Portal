import { describe, expect, it } from 'vitest'

// One radius scale for the portal, set as theme tokens in App.tsx: XS 4, SM 6, base 8, LG 12, plus
// pills (999px) and circles (50%). A number written anywhere else is how the UI drifted into thirteen
// different radii, some of them square-looking 1-3px corners on surfaces; this keeps it from drifting
// back. Use var(--ant-border-radius[-xs|-sm|-lg]) in CSS and token.borderRadius[XS|SM|LG] inline.

// Vite's raw glob rather than node:fs, as in antdStaticApi.test.ts.
const css = import.meta.glob('./**/*.css', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const tsx = import.meta.glob('./**/*.{ts,tsx}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const production = Object.entries(tsx).filter(([p]) => !/\.test\.tsx?$/.test(p))

const ALLOWED_CSS = /^(?:(?:var\(--ant-border-radius(?:-xs|-sm|-lg)?\)|999px|50%|inherit|0)\s*)+$/

describe('radius scale', () => {
  it('has files to check', () => {
    expect(Object.keys(css).length).toBeGreaterThan(0)
    expect(production.length).toBeGreaterThan(50)
  })

  it('CSS radii come from the theme tokens', () => {
    const offenders: string[] = []
    for (const [file, src] of Object.entries(css)) {
      for (const m of src.matchAll(/border(?:-(?:top|bottom)-(?:left|right))?-radius:\s*([^;}]+)/g)) {
        if (!ALLOWED_CSS.test(m[1].trim())) offenders.push(`${file}: ${m[0]}`)
      }
    }
    expect(offenders, 'use var(--ant-border-radius[-xs|-sm|-lg]), 999px or 50%').toEqual([])
  })

  it('inline radii come from the theme tokens', () => {
    const offenders: string[] = []
    // Any border radius key, corner ones and component tokens included (borderTopRightRadius,
    // itemBorderRadius), and any expression after it: a number in a ternary counts as much as a bare one.
    for (const [file, src] of production) {
      for (const m of src.matchAll(/\b(border[A-Za-z]*Radius|[a-z][A-Za-z]*BorderRadius[A-Za-z]*)\s*:\s*([^,}\n]+)/g)) {
        const value = m[2].replace(/token\.\w+|RADIUS\.\w+|'var\(--ant-border-radius[^)]*\)'|'50%'/g, '')
        if (/(?<![\w.])(?!0\b|999\b)\d+(?:px)?\b/.test(value)) offenders.push(`${file}: ${m[0].trim()}`)
      }
    }
    // App.tsx declares the scale itself. An app icon rounds in proportion to whatever size it is drawn
    // at (a quarter of its side, like a launcher icon), so it is not a step on the scale.
    const exempt = (o: string) => o.startsWith('./App.tsx') || o === './pages/manage/appsAdminParts.tsx: borderRadius: Math.round(size / 4)'
    expect(offenders.filter((o) => !exempt(o)), 'use token.borderRadius[XS|SM|LG]').toEqual([])
  })
})
