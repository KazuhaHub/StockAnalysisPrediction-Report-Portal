import { describe, expect, it } from 'vitest'

// A page reports a failure through notifyError (lib/notify), which waits until the message has been
// seen and then keeps it long enough to read; antd's message.error is gone after three seconds,
// whether or not anyone looked. [Carbon notification; Atlassian flag]
const sources = import.meta.glob('./**/*.{ts,tsx}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const production = Object.entries(sources).filter(([p]) => !/\.test\.tsx?$/.test(p))

describe('error notifications', () => {
  it('has files to check', () => {
    expect(production.length).toBeGreaterThan(50)
  })

  it('never use message.error', () => {
    const offenders = production.filter(([, src]) => /\bmessage\.error\(/.test(src)).map(([f]) => f)
    expect(offenders, 'use notifyError from lib/notify').toEqual([])
  })
})
