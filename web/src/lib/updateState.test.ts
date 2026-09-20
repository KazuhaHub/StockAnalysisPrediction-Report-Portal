import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { deferredTarget, deferTarget, normalizePolicy, useUpdateState } from './updateState'

const get = vi.fn()
vi.mock('../api/client', () => ({ api: { get: (...a: unknown[]) => get(...a) } }))

const workerState = { ready: false }
vi.mock('./swUpdate', () => ({ useSWUpdateReady: () => workerState.ready }))

const a = { version: 'v2026.38.1', commit: 'aaaaaaa', buildDate: '2026-08-10T00:00:00Z' }

beforeEach(() => {
  get.mockReset()
  get.mockResolvedValue(a) // the baseline: the server serves what this page is running
  workerState.ready = false
  sessionStorage.clear()
  // The page's own identity is what the bundle was compiled from; stamp it so the page is a known
  // build rather than the "dev" diagnostic.
  vi.stubEnv('VITE_BUILD_VERSION', a.version)
  vi.stubEnv('VITE_BUILD_COMMIT', a.commit)
  vi.stubEnv('VITE_BUILD_DATE', a.buildDate)
})
afterEach(() => vi.unstubAllEnvs())

const poll = () => renderHook(() => useUpdateState(5))

describe('useUpdateState', () => {
  it('reports nothing to prompt about while the server serves the page’s own build', async () => {
    get.mockResolvedValue(a)
    const { result } = poll()
    await waitFor(() => expect(get.mock.calls.length).toBeGreaterThan(1))
    expect(result.current.target).toBeNull()
    expect(result.current.kind).toBeNull()
    expect(result.current.page).toEqual(a)
  })

  it('names a newer target and keeps it while the prompt is open', async () => {
    const b = { version: 'v2026.38.2', commit: 'bbbbbbb', buildDate: '2026-08-11T00:00:00Z' }
    get.mockResolvedValue(b)
    const { result } = poll()
    await waitFor(() => expect(result.current.kind).toBe('newer'))
    expect(result.current.target).toEqual(b)
  })

  // A second deploy under the same tab, still open. The prompt must follow the target it would
  // actually load, not the one it first noticed.
  it('moves the target when the server changes again before the reader refreshes', async () => {
    const b = { version: 'v2026.38.2', commit: 'bbbbbbb', buildDate: '2026-08-11T00:00:00Z' }
    const c = { version: 'v2026.38.3', commit: 'ccccccc', buildDate: '2026-08-12T00:00:00Z' }
    get.mockResolvedValueOnce(b).mockResolvedValue(c)
    const { result } = poll()
    await waitFor(() => expect(result.current.target).toEqual(c))
    expect(result.current.kind).toBe('newer')
  })

  it('describes a lower number as a rollback, never as a new release', async () => {
    get.mockResolvedValue({ version: 'v2026.37.4', commit: 'ddddddd', buildDate: '2026-08-01T00:00:00Z' })
    const { result } = poll()
    await waitFor(() => expect(result.current.kind).toBe('rollback'))
  })

  it('uses the generic wording for a same-number rebuild', async () => {
    get.mockResolvedValue({ ...a, commit: 'eeeeeee' })
    const { result } = poll()
    await waitFor(() => expect(result.current.kind).toBe('same'))
    expect(result.current.target?.commit).toBe('eeeeeee')
  })

  // The server rolling back ONTO the page's build means this page is current again: the prompt has
  // nothing left to ask for. Driven by the visibility check rather than the 5ms poll, because a
  // target that exists for one interval and is then cleared is not something a poller is guaranteed
  // to observe — the point here is the transition, so the transition is what the test drives.
  it('clears the target when the server returns to the page’s build', async () => {
    const b = { version: 'v2026.38.2', commit: 'bbbbbbb', buildDate: '2026-08-11T00:00:00Z' }
    get.mockResolvedValueOnce(b).mockResolvedValue(a)
    const { result } = renderHook(() => useUpdateState(60_000))
    await waitFor(() => expect(result.current.target).toEqual(b))

    document.dispatchEvent(new Event('visibilitychange'))
    await waitFor(() => {
      expect(result.current.target).toBeNull()
      // Read together with the target: two separate reads can straddle a re-render.
      expect(result.current.kind).toBeNull()
    })
  })

  it('keeps the effective policy fresh, so an escalation reaches an open tab', async () => {
    get.mockResolvedValueOnce({ ...a, updatePromptPolicy: 'dismissible' }).mockResolvedValue({ ...a, updatePromptPolicy: 'required' })
    const { result } = poll()
    // The later answer wins: the policy is re-read every tick, not captured once at boot.
    await waitFor(() => expect(result.current.policy).toBe('required'))
  })

  it('treats an unreadable policy as the default rather than forcing a refresh', async () => {
    get.mockResolvedValue({ ...a, updatePromptPolicy: 'whatever' })
    const { result } = poll()
    await waitFor(() => expect(get.mock.calls.length).toBeGreaterThan(1))
    expect(result.current.policy).toBe('dismissible')
  })

  // Offline, or a session that ended while the tab sat idle. Neither is news that a deploy landed.
  it('never manufactures an update from a failed check', async () => {
    get.mockResolvedValue(null)
    const { result } = poll()
    await waitFor(() => expect(result.current.failed).toBe(true))
    expect(result.current.target).toBeNull()
    expect(result.current.kind).toBeNull()
  })

  it('reports a waiting service worker as a generic page update with no version attached', async () => {
    get.mockResolvedValue(a)
    workerState.ready = true
    const { result } = poll()
    await waitFor(() => expect(result.current.kind).toBe('same'))
    expect(result.current.target).toBeNull()
    expect(result.current.workerReady).toBe(true)
  })
})

describe('normalizePolicy', () => {
  it('accepts the three policies and degrades anything else to the default', () => {
    expect(normalizePolicy('dismissible')).toBe('dismissible')
    expect(normalizePolicy('persistent')).toBe('persistent')
    expect(normalizePolicy('required')).toBe('required')
    for (const bad of ['', null, undefined, 'always', 7, {}]) {
      expect(normalizePolicy(bad), String(bad)).toBe('dismissible')
    }
  })
})

describe('deferral', () => {
  it('remembers one target for this tab session', () => {
    expect(deferredTarget()).toBeNull()
    deferTarget('v2026.38.2@bbbbbbb@2026-08-11T00:00:00Z')
    expect(deferredTarget()).toBe('v2026.38.2@bbbbbbb@2026-08-11T00:00:00Z')
  })

  it('never throws when storage is unavailable', () => {
    const failing = {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {
        throw new Error('denied')
      },
    }
    const real = Object.getOwnPropertyDescriptor(window, 'sessionStorage')
    Object.defineProperty(window, 'sessionStorage', { configurable: true, value: failing })
    try {
      expect(deferredTarget()).toBeNull()
      expect(() => deferTarget('x')).not.toThrow()
    } finally {
      if (real) Object.defineProperty(window, 'sessionStorage', real)
    }
  })
})
