import { describe, expect, it } from 'vitest'
import { pageShowsJob } from './jobDetailStore'

describe('pageShowsJob', () => {
  it('matches the queue page and the batch console on the job parameter', () => {
    expect(pageShowsJob({ pathname: '/queue', search: '?job=7' }, 7)).toBe(true)
    expect(pageShowsJob({ pathname: '/apps/batch', search: '?x=1&job=7' }, 7)).toBe(true)
    expect(pageShowsJob({ pathname: '/queue', search: '?job=8' }, 7)).toBe(false)
    expect(pageShowsJob({ pathname: '/stock/600000', search: '?job=7' }, 7)).toBe(false)
  })
  it('compares numerically, like the queue page does', () => {
    expect(pageShowsJob({ pathname: '/queue', search: '?job=07' }, 7)).toBe(true)
    expect(pageShowsJob({ pathname: '/queue', search: '?job=abc' }, 7)).toBe(false)
    expect(pageShowsJob({ pathname: '/queue', search: '' }, 7)).toBe(false)
  })
})
