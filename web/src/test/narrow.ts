import { afterEach, beforeEach } from 'vitest'

/**
 * Registers hooks that make `matchMedia` answer the phone query with `narrow`. The returned
 * controller flips the answer and notifies subscribers, as a real viewport crossing would.
 */
export function mockNarrowViewport(initial = true) {
  const real = window.matchMedia
  const state = { narrow: initial, listeners: [] as Array<() => void> }
  beforeEach(() => {
    state.narrow = initial
    state.listeners = []
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: (query: string) => ({
        get matches() { return state.narrow && query.includes('767') },
        media: query,
        addEventListener: (_: string, fn: () => void) => { state.listeners.push(fn) },
        removeEventListener: () => {},
      }),
    })
  })
  afterEach(() => { Object.defineProperty(window, 'matchMedia', { configurable: true, value: real }) })
  return {
    set(narrow: boolean) {
      state.narrow = narrow
      state.listeners.forEach((fn) => fn())
    },
  }
}
