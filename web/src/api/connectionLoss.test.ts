import { describe, expect, it } from 'vitest'
import { ApiError, isConnectionError } from './client'
import { loadFailure } from '../lib/loadFailure'

const t = (k: string) => (k === 'err.network_unavailable' ? 'Unable to connect' : k === 'common.error' ? 'Something went wrong' : k)

describe('isConnectionError', () => {
  it('is true for the two codes the client records as a connection loss', () => {
    expect(isConnectionError(new ApiError(0, 'network_unavailable', 'network_unavailable'))).toBe(true)
    expect(isConnectionError(new ApiError(502, 'server_unavailable', 'server_unavailable'))).toBe(true)
  })
  it('is false for any other failure', () => {
    expect(isConnectionError(new ApiError(500, 'boom', 'internal'))).toBe(false)
    expect(isConnectionError(new ApiError(403, 'no'))).toBe(false)
    expect(isConnectionError(new Error('network_unavailable'))).toBe(false)
    expect(isConnectionError(null)).toBe(false)
    expect(isConnectionError('network_unavailable')).toBe(false)
  })
})

describe('loadFailure', () => {
  it('answers null for a connection loss so the view keeps its state', () => {
    expect(loadFailure(new ApiError(0, 'network_unavailable', 'network_unavailable'), t)).toBeNull()
  })
  it('answers the user-facing text for every other failure', () => {
    expect(loadFailure(new ApiError(500, 'boom', 'internal'), t)).toBe('boom')
    expect(loadFailure(new Error('x'), t)).toBe('Something went wrong')
  })
})
