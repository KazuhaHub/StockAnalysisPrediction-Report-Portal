// Tracks timers and animation frames scheduled while a test runs, so the shared teardown can clear
// whatever is still pending after the React trees are unmounted.
//
// Component libraries (antd, rc-*) schedule delayed state updates — tooltip open/close delay,
// debounced form values, motion end, click wave — and several have no unmount cleanup. A callback
// still pending when the test file ends fires while the jsdom environment is being torn down, React
// reaches for `window`, and vitest reports `ReferenceError: window is not defined` as an unhandled
// error even though every test passed. Clearing the leftovers after unmount is safe: nothing is
// mounted any more to observe them.
//
// Audit mode: with RP_TIMER_AUDIT=1 nothing is cleared before the report, and each test that leaves
// timers behind logs a TIMER-LEAK line with the stack that scheduled them, to find offenders:
//   RP_TIMER_AUDIT=1 npx vitest run 2>&1 | grep TIMER-LEAK

type Pending = { clear: () => void; stack: string }

const pending = new Map<unknown, Pending>()
const audit = typeof process !== 'undefined' && process.env.RP_TIMER_AUDIT === '1'
let installed = false

function where(): string {
  const lines = (new Error().stack ?? '').split('\n').slice(3)
  const own = lines.find((l) => !l.includes('node_modules/vitest') && !l.includes('pendingTimers'))
  const lib = lines.find((l) => l.includes('node_modules') && !l.includes('vitest') && !l.includes('react-dom'))
  return [own, lib].filter(Boolean).join(' <- ').replace(/\s+/g, ' ')
}

/* eslint-disable @typescript-eslint/no-explicit-any */
export function installTimerTracking(): void {
  if (installed) return
  installed = true
  const g = globalThis as Record<string, any>
  const realSet = g.setTimeout.bind(globalThis)
  const realClear = g.clearTimeout.bind(globalThis)
  const realSetI = g.setInterval.bind(globalThis)
  const realClearI = g.clearInterval.bind(globalThis)
  const realRaf = typeof g.requestAnimationFrame === 'function' ? g.requestAnimationFrame.bind(globalThis) : null
  const realCaf = typeof g.cancelAnimationFrame === 'function' ? g.cancelAnimationFrame.bind(globalThis) : null

  g.setTimeout = (fn: (...a: unknown[]) => void, ms?: number, ...args: unknown[]) => {
    const stack = audit ? where() : ''
    const id = realSet((...a: unknown[]) => {
      pending.delete(id)
      return fn(...a)
    }, ms, ...args)
    pending.set(id, { clear: () => realClear(id), stack })
    return id
  }
  g.clearTimeout = (id: unknown) => {
    pending.delete(id)
    return realClear(id)
  }
  g.setInterval = (fn: (...a: unknown[]) => void, ms?: number, ...args: unknown[]) => {
    const id = realSetI(fn, ms, ...args)
    pending.set(id, { clear: () => realClearI(id), stack: audit ? where() : '' })
    return id
  }
  g.clearInterval = (id: unknown) => {
    pending.delete(id)
    return realClearI(id)
  }
  if (realRaf && realCaf) {
    g.requestAnimationFrame = (fn: FrameRequestCallback) => {
      const id = realRaf((t: number) => {
        pending.delete(id)
        return fn(t)
      })
      pending.set(id, { clear: () => realCaf(id), stack: audit ? where() : '' })
      return id
    }
    g.cancelAnimationFrame = (id: number) => {
      pending.delete(id)
      return realCaf(id)
    }
  }
}

/** Clears every tracked timer still pending; returns their scheduling stacks (audit mode only). */
export function drainPendingTimers(): string[] {
  const out = [...pending.values()].map((p) => p.stack)
  for (const p of pending.values()) p.clear()
  pending.clear()
  return out
}

export function auditEnabled(): boolean {
  return audit
}
