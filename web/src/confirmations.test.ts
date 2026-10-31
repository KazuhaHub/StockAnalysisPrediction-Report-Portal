import { describe, expect, it } from 'vitest'

// Confirmations go through components/Confirm (Confirm / confirmAction), which names the action on
// its button, focuses Cancel and states irreversibility. A raw Popconfirm or modal.confirm defaults
// to "OK"; 42 of 44 did. [Apple alerts; Geist modal]
const sources = import.meta.glob('./**/*.tsx', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const production = Object.entries(sources).filter(([p]) => !/\.test\.tsx$/.test(p) && p !== './components/Confirm.tsx')

describe('confirmations', () => {
  it('has files to check', () => {
    expect(production.length).toBeGreaterThan(50)
    expect(production.some(([, src]) => src.length > 1000)).toBe(true)
  })

  it('never use antd Popconfirm or modal.confirm directly', () => {
    const offenders: string[] = []
    for (const [file, src] of production) {
      if (/<Popconfirm\b/.test(src)) offenders.push(`${file}: <Popconfirm`)
      if (/\bmodal\.confirm\(/.test(src)) offenders.push(`${file}: modal.confirm(`)
    }
    expect(offenders, 'use Confirm or confirmAction from components/Confirm').toEqual([])
  })
})
