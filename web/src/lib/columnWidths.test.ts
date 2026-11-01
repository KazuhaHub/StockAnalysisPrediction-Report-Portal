import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { clampColumnWidth, fillScrollWidth, storedColumnWidths, useColumnWidths } from './columnWidths'

describe('column width helpers', () => {
  it('clamps a dragged width between the column minimum and the shared maximum', () => {
    expect(clampColumnWidth(20, 160)).toBe(160)
    expect(clampColumnWidth(310.4, 160)).toBe(310)
    expect(clampColumnWidth(5000, 160)).toBe(1200)
    expect(clampColumnWidth(10, 64)).toBe(64)
  })

  it('keeps only usable stored widths', () => {
    expect(storedColumnWidths({ widths: { a: 200, b: 'x', c: 10, d: 20000, e: Number.NaN } })).toEqual({ a: 200 })
    expect(storedColumnWidths(null)).toEqual({})
    expect(storedColumnWidths('nope')).toEqual({})
  })

  it('sizes a fill grid to the sum of its minimums unless a column was widened', () => {
    expect(fillScrollWidth(['a', 'b'], {}, 160, 116)).toBe(2 * 160 + 116)
    expect(fillScrollWidth(['a', 'b'], { a: 400 }, 160, 116)).toBe(400 + 160 + 116)
    // A stored width below the minimum (the minimum was raised later) still counts as the minimum.
    expect(fillScrollWidth(['a'], { a: 100 }, 160, 0)).toBe(160)
    // A stored width for a column that is gone is ignored.
    expect(fillScrollWidth(['a'], { gone: 900 }, 160, 0)).toBe(160)
  })
})

describe('useColumnWidths', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => vi.restoreAllMocks())

  it('previews a drag without storing it, then stores the committed width', () => {
    const { result } = renderHook(() => useColumnWidths('t1', 160))
    act(() => result.current.resize('a', 300, false))
    expect(result.current.widths).toEqual({ a: 300 })
    expect(localStorage.getItem('rp.table.t1')).toBeNull()
    act(() => result.current.resize('a', 320, true))
    expect(result.current.widths).toEqual({ a: 320 })
    expect(JSON.parse(localStorage.getItem('rp.table.t1')!)).toEqual({ widths: { a: 320 } })
  })

  it('drops an abandoned drag', () => {
    const { result } = renderHook(() => useColumnWidths('t1', 160))
    act(() => result.current.resize('a', 300, false))
    act(() => result.current.cancel())
    expect(result.current.widths).toEqual({})
  })

  it('restores stored widths, and resets them', () => {
    localStorage.setItem('rp.table.t1', JSON.stringify({ widths: { a: 250 } }))
    const { result } = renderHook(() => useColumnWidths('t1', 160))
    expect(result.current.widths).toEqual({ a: 250 })
    act(() => result.current.reset())
    expect(result.current.widths).toEqual({})
    expect(JSON.parse(localStorage.getItem('rp.table.t1')!)).toEqual({ widths: {} })
  })

  it('recovers from corrupt storage and from storage that refuses writes', () => {
    localStorage.setItem('rp.table.t1', '{broken')
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('quota')
    })
    const { result } = renderHook(() => useColumnWidths('t1', 160))
    expect(result.current.widths).toEqual({})
    act(() => result.current.resize('a', 300, true))
    expect(result.current.widths).toEqual({ a: 300 })
  })

  it('keeps widths in memory only when it has no storage key', () => {
    const { result } = renderHook(() => useColumnWidths(undefined, 160))
    act(() => result.current.resize('a', 300, true))
    expect(result.current.widths).toEqual({ a: 300 })
    expect(localStorage.length).toBe(0)
  })
})
