import { afterAll, afterEach, expect } from 'vitest'
import { resetNotifications } from '../lib/notify'
import { cleanup, configure } from '@testing-library/react'
import { auditEnabled, drainPendingTimers, installTimerTracking } from './pendingTimers'

// Clear timers and animation frames still pending when a test FILE ends. antd/rc-* schedule delayed
// state updates (tooltip delay, debounced form values, motion end, click wave) and several have no
// unmount cleanup, so one can fire after the file's jsdom environment is gone. React then reaches
// for `window` and vitest reports `ReferenceError: window is not defined` as an unhandled error
// although every test passed, roaming between files on whichever loses a CPU race.
//
// Draining once per file, not per test, leaves timers a later test in the same file may legitimately
// depend on untouched. By then the last test's tree is unmounted, so nothing is left to observe the
// cleared callbacks. See pendingTimers.ts for the RP_TIMER_AUDIT=1 harness that lists the offenders.
installTimerTracking()

// afterAll hooks run in reverse registration order: the opt-in probe is registered first so it runs
// LAST, after the drain. The probe makes the leak deterministic: it removes `window` for 300ms, as
// environment teardown does, so any timer still pending reports the unhandled error.
//   RP_TEARDOWN_PROBE=1 RP_NO_TIMER_DRAIN=1 npx vitest run   # unhandled errors in each leaking file
//   RP_TEARDOWN_PROBE=1 npx vitest run                       # must report none
afterAll(async () => {
  if (process.env.RP_TEARDOWN_PROBE !== '1') return
  const g = globalThis as { window?: unknown }
  const w = g.window
  delete g.window
  await new Promise((resolve) => setTimeout(resolve, 300))
  g.window = w
})
afterAll(async () => {
  if (process.env.RP_NO_TIMER_DRAIN === '1') return
  const leaked = drainPendingTimers()
  if (auditEnabled() && leaked.length > 0) {
    process.stderr.write(`TIMER-LEAK ${expect.getState().testPath ?? ''} :: ${leaked.length} :: ${[...new Set(leaked)].join(' || ')}\n`)
  }
  // React's scheduler runs on setImmediate; let work queued by the final unmount flush while
  // `window` still exists, then clear whatever that work scheduled in turn.
  await new Promise((resolve) => setImmediate(resolve))
  drainPendingTimers()
})
// The notification store is module-global; a pop-up must not leak into the next test.
afterEach(resetNotifications)
// Unmount rendered React trees after each test so queries don't see stale DOM.
afterEach(cleanup)
// Registered last, so it runs FIRST: give the test's own in-flight async work (resolved fetches,
// debounced field values) a real 20ms to land while the tree is still mounted, before teardown.
// Without it a late resolution surfaces after the notification store reset and leaks a pop-up into
// the next test.
afterEach(async () => {
  await new Promise((resolve) => setTimeout(resolve, 20))
})

// waitFor / findBy* default to a 1s budget, which is not a statement about the code — it is a bet
// that a machine running the whole suite in parallel will schedule this one microtask promptly. It
// lost that bet roughly one run in eight: a provider whose fetch had not resolved yet still showed
// its pre-fetch fallback, and the assertion reported the fallback as if it were the final value.
// The failure roamed between files, because whichever one lost the CPU that run was the one to
// fail. Raising the ceiling costs a passing test nothing (waitFor returns as soon as the assertion
// holds) and only lengthens a genuine failure.
configure({ asyncUtilTimeout: 5000 })

// Ant Design asks for pseudo-element styles when measuring overlays. jsdom deliberately does not
// implement them and logs one warning per call; using the element's ordinary computed style is the
// closest layout-free substitute and keeps test output useful.
if (typeof window !== 'undefined') {
  const getComputedStyle = window.getComputedStyle.bind(window)
  window.getComputedStyle = ((element: Element) => getComputedStyle(element)) as typeof window.getComputedStyle
}

// jsdom in this runtime ships without Web Storage; install a minimal in-memory
// localStorage so modules that persist prefs (reader / prefs) work under test.
if (typeof window !== 'undefined' && !window.localStorage) {
  const mem = new Map<string, string>()
  const storage: Storage = {
    get length() {
      return mem.size
    },
    clear: () => mem.clear(),
    getItem: (k: string) => (mem.has(k) ? (mem.get(k) as string) : null),
    key: (i: number) => Array.from(mem.keys())[i] ?? null,
    removeItem: (k: string) => {
      mem.delete(k)
    },
    setItem: (k: string, v: string) => {
      mem.set(k, String(v))
    },
  }
  Object.defineProperty(window, 'localStorage', { value: storage, configurable: true })
}

// jsdom has no matchMedia; antd's responsive hooks (Grid.useBreakpoint, used by Tabs /
// Table / Row) call it on mount. Install an inert, never-matching implementation so
// components that rely on it render under test.
if (typeof window !== 'undefined' && !window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (query: string): MediaQueryList => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {}, // deprecated, but antd's observer still calls it
      removeListener: () => {},
      dispatchEvent: () => false,
    }),
  })
}

// jsdom has no ResizeObserver; antd 6 routes far more components through
// rc-resize-observer (it observes each element's box on mount) than v5 did, so its
// absence now throws during render. Install an inert stub — layout measurements are
// irrelevant to these component assertions.
if (typeof globalThis !== 'undefined' && !('ResizeObserver' in globalThis)) {
  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = ResizeObserverStub
}
