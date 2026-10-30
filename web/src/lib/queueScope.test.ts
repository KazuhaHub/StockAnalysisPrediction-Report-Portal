import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { readQueueScope, useQueueScope, withQueueScope, writeQueueScope } from './queueScope'

afterEach(() => localStorage.clear())

describe('queue scope', () => {
  it('defaults an administrator to everyone and remembers the pick', () => {
    expect(readQueueScope()).toBe('all')
    writeQueueScope('mine')
    expect(readQueueScope()).toBe('mine')
  })

  it('keeps every view on the same pick', () => {
    const a = renderHook(() => useQueueScope(true))
    const b = renderHook(() => useQueueScope(true))
    act(() => a.result.current[1]('mine'))
    expect(b.result.current[0]).toBe('mine')
  })

  it('is always mine for a non-administrator and never sends the parameter', () => {
    writeQueueScope('all')
    const { result } = renderHook(() => useQueueScope(false))
    expect(result.current[0]).toBe('mine')
    expect(withQueueScope('/api/admin/batch/jobs', 'mine', false)).toBe('/api/admin/batch/jobs')
  })

  it('adds scope=mine only for an administrator who asked for it', () => {
    expect(withQueueScope('/api/admin/batch/jobs?limit=300', 'mine', true)).toBe('/api/admin/batch/jobs?limit=300&scope=mine')
    expect(withQueueScope('/api/admin/batch/queue', 'mine', true)).toBe('/api/admin/batch/queue?scope=mine')
    expect(withQueueScope('/api/admin/batch/queue', 'all', true)).toBe('/api/admin/batch/queue')
  })
})
