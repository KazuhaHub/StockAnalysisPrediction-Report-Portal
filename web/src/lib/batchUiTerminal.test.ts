import { describe, expect, it } from 'vitest'
import { isTerminal } from './batchUi'

describe('isTerminal', () => {
  it('counts finished, cancelled and expired runs as settled, and nothing else', () => {
    for (const s of ['finished', 'cancelled', 'expired']) expect(isTerminal(s)).toBe(true)
    for (const s of ['queued', 'running', 'cancelling', 'untracked']) expect(isTerminal(s)).toBe(false)
  })
})
