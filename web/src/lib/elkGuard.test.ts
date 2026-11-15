import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import ELK from './elkDisabled'
import { ELK_MARKERS, findElk } from '../../scripts/check-no-elk.mjs'
import { elkjsModules, forbidElkjs } from '../../scripts/forbid-elkjs.mjs'

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

// The marker scan reads text, and two of elkjs's files (lib/elk-api.js, lib/main.js) are thin
// wrappers that carry no engine class names. The module graph is the primary guard: any module id
// under node_modules/elkjs/ fails the build, whatever the file contains.
describe('elkjsModules', () => {
  it('finds module ids under node_modules/elkjs, on any path separator and for any file', () => {
    const ids = [
      '/repo/web/node_modules/elkjs/lib/elk-api.js',
      '/repo/web/node_modules/elkjs/lib/main.js',
      'C:\\repo\\web\\node_modules\\elkjs\\lib\\elk.bundled.js',
      '/repo/web/node_modules/mermaid/dist/mermaid.core.mjs',
      '/repo/web/node_modules/not-elkjs/index.js',
      '/repo/web/node_modules/elkjs-fake/index.js',
      '/repo/web/src/lib/elkDisabled.ts',
    ]
    expect(elkjsModules(ids)).toEqual([ids[0], ids[1], ids[2]])
  })

  it('finds a nested copy under another package', () => {
    expect(elkjsModules(['/r/node_modules/a/node_modules/elkjs/lib/main.js'])).toHaveLength(1)
  })
})

describe('forbidElkjs plugin', () => {
  const chunk = (modules: Record<string, unknown>) => ({ type: 'chunk', fileName: 'assets/x.js', modules })

  it('fails the build when a chunk contains an elkjs module', () => {
    const plugin = forbidElkjs()
    expect(() =>
      plugin.generateBundle({}, { 'assets/x.js': chunk({ '/r/node_modules/elkjs/lib/main.js': {} }) }),
    ).toThrow(/elkjs/)
  })

  it('names the chunk and the module in the failure', () => {
    const plugin = forbidElkjs()
    expect(() =>
      plugin.generateBundle({}, { 'assets/x.js': chunk({ '/r/node_modules/elkjs/lib/elk-api.js': {} }) }),
    ).toThrow(/assets\/x\.js.*elk-api\.js|elk-api\.js.*assets\/x\.js/s)
  })

  it('passes a bundle without elkjs, assets included', () => {
    const plugin = forbidElkjs()
    expect(() =>
      plugin.generateBundle(
        {},
        {
          'assets/x.js': chunk({ '/r/src/lib/elkDisabled.ts': {}, '/r/node_modules/mermaid/dist/a.mjs': {} }),
          'assets/y.css': { type: 'asset', fileName: 'assets/y.css' },
        },
      ),
    ).not.toThrow()
  })

  it('also rejects an elkjs module as it is resolved, before anything is bundled', () => {
    const plugin = forbidElkjs()
    expect(() => plugin.moduleParsed({ id: '/r/node_modules/elkjs/lib/main.js' })).toThrow(/elkjs/)
    expect(() => plugin.moduleParsed({ id: '/r/src/lib/elkDisabled.ts' })).not.toThrow()
  })
})

describe('marker scan scope', () => {
  it('keeps to the engine package names and leaves generic words to the module-id guard', () => {
    // Words such as ELKNode or elk-api.js can appear in unrelated code (a changelog, a comment, another
    // library), and a false alarm would train people to ignore the check. The wrappers they would catch are
    // covered by forbidElkjs, which looks at module ids.
    expect(ELK_MARKERS).toEqual(['org.eclipse.elk.alg.', 'org.eclipse.elk.core.'])
    const dir = dist({ 'a.js': '// see elk-api.js and elk-worker.min.js, class ELKNode', 'b.js': 'const ELKNode = 1' })
    expect(findElk(dir)).toEqual([])
  })
})
