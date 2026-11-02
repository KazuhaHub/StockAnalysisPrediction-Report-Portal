// Refreshes web/test-durations.json, the per-file seconds the balanced vitest sequencer
// (src/test/balancedSequencer.ts) uses to deal test files over the CI shards.
//
// Run it occasionally (`npm run test:durations`), for instance after adding or reworking a heavy
// page suite, and commit the result. Stale data only unbalances the shards; it never breaks them:
// a file missing from the table is weighed at the median, and a stale entry is ignored.
import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('.', import.meta.url)), '..')
const dir = mkdtempSync(join(tmpdir(), 'vitest-durations-'))
const out = join(dir, 'report.json')
try {
  const run = spawnSync('npx', ['vitest', 'run', '--reporter=json', `--outputFile=${out}`], {
    cwd: root,
    stdio: ['ignore', 'ignore', 'inherit'],
    shell: process.platform === 'win32',
  })
  if (run.status !== 0) {
    console.error('vitest failed; durations are not recorded from a failing run')
    process.exit(run.status ?? 1)
  }
  const report = JSON.parse(readFileSync(out, 'utf8'))
  const table = {}
  for (const file of report.testResults) {
    const path = relative(root, file.name).split('\\').join('/')
    table[path] = Math.round(((file.endTime - file.startTime) / 1000) * 10) / 10
  }
  const sorted = Object.fromEntries(Object.keys(table).sort().map(k => [k, table[k]]))
  writeFileSync(join(root, 'test-durations.json'), JSON.stringify(sorted, null, 2) + '\n')
  console.log(`recorded ${Object.keys(sorted).length} files`)
} finally {
  rmSync(dir, { recursive: true, force: true })
}
