import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// antd's Space is `display: inline-flex`, and an inline box ignores auto inline margins. A page column
// written as <Space style={{ maxWidth, marginInline: 'auto' }}> therefore sits at the start of the
// page instead of its centre, with everything in it (the notifications page's empty state looked off
// centre). A centred column is a block-level Flex (or a div). Pushing a Space to the end of a flex row
// with a one-sided `marginInlineStart` / `marginLeft: 'auto'` is a flex-item margin and still works.
const root = dirname(fileURLToPath(import.meta.url))

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap(name => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return sources(path)
    return /\.tsx$/.test(name) && !/\.test\.tsx$/.test(name) ? [path] : []
  })
}

export function centredSpaces(text: string): string[] {
  const out: string[] = []
  for (const m of text.matchAll(/<Space\b[^>]*?style=\{\{([^}]*)\}\}/g)) {
    if (/\bmarginInline\s*:\s*'auto'|\bmargin\s*:\s*'[^']*\bauto\b[^']*'/.test(m[1])) out.push(m[0])
  }
  return out
}

describe('centred columns', () => {
  it('spots a Space centred with auto margins, not one pushed to the end of a row', () => {
    expect(centredSpaces(`<Space orientation="vertical" style={{ width: '100%', maxWidth: 1000, marginInline: 'auto' }}>`)).toHaveLength(1)
    expect(centredSpaces(`<Space style={{ margin: '0 auto' }}>`)).toHaveLength(1)
    expect(centredSpaces(`<Space style={{ marginInlineStart: 'auto' }}>`)).toHaveLength(0)
    expect(centredSpaces(`<Flex vertical style={{ maxWidth: 1000, marginInline: 'auto' }}>`)).toHaveLength(0)
  })

  it('never centres an inline-flex Space with auto margins', () => {
    const offenders = sources(root).flatMap(path => centredSpaces(readFileSync(path, 'utf8')).map(tag => `${relative(root, path)}: ${tag}`))
    expect(offenders).toEqual([])
  })
})
