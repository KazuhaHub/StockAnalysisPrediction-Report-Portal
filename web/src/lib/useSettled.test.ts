import { describe, expect, it } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useSettled } from './useSettled'

describe('useSettled', () => {
  it('is false until every request has answered, then stays true when the data is refreshed', () => {
    const { result, rerender } = renderHook(({ answered }) => useSettled(answered), { initialProps: { answered: false } })
    expect(result.current).toBe(false)
    rerender({ answered: true })
    expect(result.current).toBe(true)
    // A later refresh of the same data does not bring the loading state back over the rendered view.
    act(() => rerender({ answered: false }))
    expect(result.current).toBe(true)
  })
})
