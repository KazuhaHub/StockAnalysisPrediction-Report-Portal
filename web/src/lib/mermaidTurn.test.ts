import { describe, expect, it } from 'vitest'
import { inMermaidTurn } from './mermaidTurn'

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
