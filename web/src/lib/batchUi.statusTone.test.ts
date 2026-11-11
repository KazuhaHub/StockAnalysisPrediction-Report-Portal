import { describe, expect, it } from 'vitest'

import { JOB_STATUS_TONE } from './batchUi'
import zhCN from '../locales/zh-CN.json'

// Every status the UI can label must also have a tone. A status missing from the tone map falls
// back to 'neutral' (grey, minus icon), so a failed row would read like a non-event and a succeeded
// row would not match the green "finished" job tag beside it.
describe('JOB_STATUS_TONE', () => {
  const labelled = Object.keys(zhCN)
    .filter((k) => k.startsWith('batch.status.') && !k.endsWith('Hint'))
    .map((k) => k.slice('batch.status.'.length))

  it('covers every labelled batch status', () => {
    expect(labelled.length).toBeGreaterThan(0)
    const missing = labelled.filter((s) => !(s in JOB_STATUS_TONE))
    expect(missing).toEqual([])
  })

  it('gives item outcomes the tone their meaning implies', () => {
    expect(JOB_STATUS_TONE.succeeded).toBe('success')
    expect(JOB_STATUS_TONE.finished).toBe('success')
    expect(JOB_STATUS_TONE.partial).toBe('warning')
    expect(JOB_STATUS_TONE.failed).toBe('error')
    expect(JOB_STATUS_TONE.expired).toBe('neutral')
  })
})
