import { describe, expect, it } from 'vitest'
import { UndoHistory } from './undoHistory'

describe('UndoHistory', () => {
  it('undoes and redoes in order', () => {
    const h = new UndoHistory<string>(10)
    h.push('a')
    h.push('b')
    expect(h.undo('c')).toBe('b')
    expect(h.undo('b')).toBe('a')
    expect(h.undo('a')).toBeUndefined()
    expect(h.redo('a')).toBe('b')
    expect(h.redo('b')).toBe('c')
    expect(h.redo('c')).toBeUndefined()
  })

  it('drops the redo branch on a new push', () => {
    const h = new UndoHistory<string>(10)
    h.push('a')
    expect(h.undo('b')).toBe('a')
    h.push('a')
    expect(h.redo('x')).toBeUndefined()
  })

  it('keeps only the newest entries up to the limit', () => {
    const h = new UndoHistory<number>(50)
    for (let i = 0; i < 55; i++) h.push(i)
    const seen: number[] = []
    let current = 55
    for (let snap = h.undo(current); snap !== undefined; snap = h.undo(current)) {
      seen.push(snap)
      current = snap
    }
    expect(seen).toHaveLength(50)
    expect(seen[seen.length - 1]).toBe(5)
  })

  it('clears both directions', () => {
    const h = new UndoHistory<string>(10)
    h.push('a')
    h.undo('b')
    h.clear()
    expect(h.undo('b')).toBeUndefined()
    expect(h.redo('b')).toBeUndefined()
  })
})
