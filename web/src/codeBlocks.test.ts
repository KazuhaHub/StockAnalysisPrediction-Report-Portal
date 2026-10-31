import { describe, expect, it } from 'vitest'

// Code and payload blocks share one template, .rp-code in index.css: rounded, tinted, wrapped rather
// than scrolled sideways, bounded in height. A pre styled inline drifts (four did, one scrolled off a
// phone). Report bodies rendered from Markdown keep their own styles.
const sources = import.meta.glob('./**/*.tsx', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const production = Object.entries(sources).filter(([p]) => !/\.test\.tsx$/.test(p) && !/\/(Markdown|MermaidBlock)\.tsx$/.test(p))

describe('code blocks', () => {
  it('has files to check', () => {
    expect(production.length).toBeGreaterThan(50)
  })

  it('every <pre> uses the shared .rp-code template', () => {
    const offenders: string[] = []
    for (const [file, src] of production) {
      for (const m of src.matchAll(/<pre\b[^>]*>/g)) {
        if (!/className="rp-code"/.test(m[0]) || /\bstyle=/.test(m[0])) offenders.push(`${file}: ${m[0]}`)
      }
    }
    expect(offenders).toEqual([])
  })
})
