// Fails when the ELK layout engine (elkjs, EPL-2.0) is present in the built SPA.
//
// elkjs arrives as a dependency of mermaid 12 and is used only for mermaid's optional ELK layout.
// Its licence is not compatible with this project's, so vite.config.ts aliases every elkjs import
// to src/lib/elkDisabled.ts and the ELK layout is not supported (doc 167 section 8). This check is
// the evidence behind that: the licence gate treats elkjs as "not shipped" only while it passes.
//
// The markers are the Java package names the ELK engine carries in its compiled output, and names that only
// elkjs's wrapper files use. scripts/forbid-elkjs.mjs is the primary guard (module ids); this scan is a second layer.
//
// (Original note:) The markers are the Java package names the ELK engine carries in its compiled output. They are
// distinctive on purpose: mermaid's own option keys ("elk.layered.*", "elk.algorithm") and its small
// loader chunk are not ELK code and must not trip the check.
//
//   node scripts/check-no-elk.mjs [dist-dir]      (default ../internal/web/dist)
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

export const ELK_MARKERS = [
  'org.eclipse.elk.alg.',
  'org.eclipse.elk.core.',
  // elkjs's own wrappers (lib/main.js, lib/elk-api.js) carry no engine class names but name these.
  'ELKNode',
  'elk-worker.min.js',
  'elk-api.js',
]

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) walk(path, out)
    else if (name !== '.gitkeep') out.push(path)
  }
  return out
}

/** Returns [{ file, marker }] for every built file that contains an ELK marker; throws when there is nothing to scan. */
export function findElk(dir) {
  const files = existsSync(dir) ? walk(dir) : []
  if (files.length === 0) throw new Error(`no files to scan in ${dir}: build the SPA first (npm run build)`)
  const hits = []
  for (const path of files) {
    const text = readFileSync(path, 'latin1')
    const marker = ELK_MARKERS.find((m) => text.includes(m))
    if (marker) hits.push({ file: relative(dir, path).split(sep).join('/'), marker })
  }
  return hits
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const dir = process.argv[2] ?? fileURLToPath(new URL('../../internal/web/dist', import.meta.url))
  let hits
  try {
    hits = findElk(dir)
  } catch (err) {
    console.error(`check-no-elk: ${err.message}`)
    process.exit(2)
  }
  if (hits.length > 0) {
    console.error('check-no-elk: ELK layout engine code is present in the build output:')
    for (const h of hits) console.error(`  ${h.file}  (${h.marker})`)
    console.error('The elkjs alias in vite.config.ts is not taking effect; see docs/app-platform/173-licence-policy-plan.md.')
    process.exit(1)
  }
  console.log('check-no-elk: no ELK engine code in the build output')
}
