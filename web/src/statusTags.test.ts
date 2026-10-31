import { describe, expect, it } from 'vitest'

// A status is said by colour, icon and words (components/StatusTag), never by a coloured antd Tag
// alone: 86 tags were colour-only. Category labels (a report type, a role, a scope) keep plain Tags;
// what this forbids is a plain Tag in a status colour. [Carbon status indicators]
const sources = import.meta.glob('./**/*.tsx', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const production = Object.entries(sources).filter(([p]) => !/\.test\.tsx$/.test(p) && p !== './components/StatusTag.tsx')

describe('status tags', () => {
  it('has files to check', () => {
    expect(production.length).toBeGreaterThan(50)
  })

  it('never colour a plain Tag as a status', () => {
    const offenders: string[] = []
    for (const [file, src] of production) {
      for (const m of src.matchAll(/<Tag\b[^>]*>/g)) {
        const tag = m[0]
        if (/color=\{?[^>]*['"](success|error|warning|processing)['"]/.test(tag) || /color=\{(JOB_STATUS_COLOR|STATUS_COLOR|EVALUATOR_TAG_COLOR)\b/.test(tag)) {
          offenders.push(`${file}: ${tag.slice(0, 90)}`)
        }
      }
    }
    expect(offenders, 'use StatusTag from components/StatusTag').toEqual([])
  })
})
