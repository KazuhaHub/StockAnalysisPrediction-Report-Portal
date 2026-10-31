import { describe, expect, it } from 'vitest'

// A view's first load shows a skeleton in the shape of what is coming (LoadGate, or its
// LoadingPlaceholder), after a short delay; a large spinner is kept for the app shell starting up,
// before there is any page to draw the shape of [Primer loading; Geist skeleton]. Small spinners
// for a single action, and Spin's `spinning` overlay for a refresh over rows already shown, are fine.
const sources = import.meta.glob('./**/*.tsx', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const production = Object.entries(sources).filter(([p]) => !/\.test\.tsx$/.test(p))

const SHELL = ['./App.tsx', './appAccess.tsx']

describe('loading states', () => {
  it('has files to check', () => {
    expect(production.length).toBeGreaterThan(50)
  })

  it('draws no large first-load spinner outside the app shell', () => {
    const offenders = production
      .filter(([f, src]) => !SHELL.includes(f) && /<Spin\s+size="large"/.test(src))
      .map(([f]) => f)
    expect(offenders, 'use LoadGate or LoadingPlaceholder').toEqual([])
  })
})
