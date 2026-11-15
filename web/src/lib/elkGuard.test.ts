import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import ELK from './elkDisabled'
import { ELK_MARKERS, findElk } from '../../scripts/check-no-elk.mjs'

// The ELK layout engine (elkjs, EPL-2.0) must never reach the build output: doc 167 section 8 aliases
// it to an empty module, and the licence gate's "not shipped" exclusion for elkjs is valid only while
// this check passes. These tests pin the detector itself; CI runs it against the real build.

const dirs: string[] = []
function dist(files: Record<string, string>): string {
  const dir = mkdtempSync(join(tmpdir(), 'elk-guard-'))
  dirs.push(dir)
  for (const [name, body] of Object.entries(files)) {
    const path = join(dir, name)
    mkdirSync(join(path, '..'), { recursive: true })
    writeFileSync(path, body)
  }
  return dir
}
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true })
})

describe('findElk', () => {
  it('finds nothing in an output without ELK code', () => {
    const dir = dist({ 'assets/index.js': 'console.log("hello")', 'index.html': '<html></html>' })
    expect(findElk(dir)).toEqual([])
  })

  it('reports the file and marker when ELK engine class names are present', () => {
    const dir = dist({
      'assets/elk-AAAA.js': 'var a="org.eclipse.elk.alg.layered.p1cycles.GreedyCycleBreaker";',
      'assets/other.js': 'ok',
    })
    const hits = findElk(dir)
    expect(hits).toHaveLength(1)
    expect(hits[0].file).toBe('assets/elk-AAAA.js')
    expect(hits[0].marker).toBe('org.eclipse.elk.alg.')
  })

  it('finds the core packages and nested directories', () => {
    const dir = dist({ 'assets/deep/x.js': 'new org.eclipse.elk.core.Foo' })
    expect(findElk(dir).map((h) => h.file)).toEqual(['assets/deep/x.js'])
  })

  it('does not flag mermaid\'s own ELK option names or loader chunk', () => {
    // Mermaid writes layout options as "elk.layered.*" and keeps a small loader chunk named elk-*.js;
    // neither is ELK code.
    const dir = dist({
      'assets/elk-XYZ.js': 'const o={"elk.layered.spacing.nodeNodeBetweenLayers":"20","elk.algorithm":"layered"};throw new Error("ELK layout is not supported")',
      'assets/mermaid.core.js': 'flowchart-elk; layout:"elk"',
    })
    expect(findElk(dir)).toEqual([])
  })

  it('looks inside source maps and html too', () => {
    const dir = dist({ 'assets/a.js.map': '{"sourcesContent":["org.eclipse.elk.core.x"]}' })
    expect(findElk(dir)).toHaveLength(1)
  })

  it('fails closed on a missing or empty output directory', () => {
    expect(() => findElk(join(tmpdir(), 'elk-guard-does-not-exist'))).toThrow(/no files/i)
    expect(() => findElk(dist({}))).toThrow(/no files/i)
  })

  it('exposes distinctive markers', () => {
    expect(ELK_MARKERS).toContain('org.eclipse.elk.alg.')
    expect(ELK_MARKERS).toContain('org.eclipse.elk.core.')
  })
})

describe('elkDisabled', () => {
  it('fails closed with a clear message when something constructs the engine', () => {
    expect(() => new ELK()).toThrow(/ELK layout is not supported/)
  })
})
