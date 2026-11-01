import { describe, expect, it } from 'vitest'

// A view's first load goes through LoadGate or its LoadingPlaceholder: one shared, centred loading
// ring with its words, shown from the first frame, rather than a large spinner hand-placed in each
// view. The app shell starting up keeps its own. Small spinners for a single action, and Spin's
// `spinning` overlay for a refresh over rows already shown, are fine.
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
