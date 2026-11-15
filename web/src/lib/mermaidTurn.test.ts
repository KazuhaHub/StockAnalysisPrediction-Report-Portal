import { afterEach, describe, expect, it, vi } from 'vitest'
import { inMermaidTurn, MERMAID_TURN_TIMEOUT_MS } from './mermaidTurn'

const tick = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

describe('inMermaidTurn', () => {
  it('runs turns one at a time, in the order asked', async () => {
    const log: string[] = []
    const a = inMermaidTurn(async () => {
      log.push('a start')
      await tick(10)
      log.push('a end')
    })
    const b = inMermaidTurn(async () => {
      log.push('b start')
      await tick(1)
      log.push('b end')
    })
    await Promise.all([a, b])
    expect(log).toEqual(['a start', 'a end', 'b start', 'b end'])
  })

  it('returns the value of its step', async () => {
    await expect(inMermaidTurn(async () => 42)).resolves.toBe(42)
  })

  it('does not let a failed turn stop the next one', async () => {
    const failing = inMermaidTurn(async () => {
      throw new Error('boom')
    })
    const next = inMermaidTurn(async () => 'ok')
    await expect(failing).rejects.toThrow('boom')
    await expect(next).resolves.toBe('ok')
  })
})

describe('inMermaidTurn timeout', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('gives a turn 15 seconds', () => {
    expect(MERMAID_TURN_TIMEOUT_MS).toBe(15_000)
  })

  it('rejects a turn that never settles and lets the next one run', async () => {
    vi.useFakeTimers()
    const stuck = inMermaidTurn(() => new Promise<never>(() => undefined))
    const stuckResult = stuck.then(
      () => 'resolved',
      (error: Error) => error.message,
    )
    let ran = false
    const next = inMermaidTurn(async () => {
      ran = true
      return 'next'
    })
    await vi.advanceTimersByTimeAsync(MERMAID_TURN_TIMEOUT_MS - 1)
    expect(ran).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    await expect(stuckResult).resolves.toMatch(/timed out/i)
    await expect(next).resolves.toBe('next')
    expect(ran).toBe(true)
  })

  it('does not time a turn that settles in time, and clears its timer', async () => {
    vi.useFakeTimers()
    const done = inMermaidTurn(async () => 'fast')
    await expect(done).resolves.toBe('fast')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('times each turn from its own start, not from when it was asked for', async () => {
    vi.useFakeTimers()
    const first = inMermaidTurn(() => new Promise<string>((resolve) => setTimeout(() => resolve('first'), 10_000)))
    const second = inMermaidTurn(() => new Promise<string>((resolve) => setTimeout(() => resolve('second'), 10_000)))
    await vi.advanceTimersByTimeAsync(20_000)
    await expect(first).resolves.toBe('first')
    await expect(second).resolves.toBe('second')
  })
})
