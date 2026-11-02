import { readFileSync } from 'node:fs'
import { relative, resolve } from 'node:path'
import { BaseSequencer } from 'vitest/node'
import type { TestSpecification } from 'vitest/node'
import { assignShards, type Durations } from './balancedShards.ts'

// Replaces vitest's hash-and-slice sharding with the duration-balanced split in balancedShards.ts.
// Durations come from web/test-durations.json (refresh with `npm run test:durations`); a missing
// or unreadable file just means every file weighs the same, which is still a valid split.
// Ordering inside a shard is left to the base class.
export default class BalancedSequencer extends BaseSequencer {
  async shard(files: TestSpecification[]): Promise<TestSpecification[]> {
    const { root, shard } = this.ctx.config
    if (!shard) return files
    const rel = (spec: TestSpecification) => relative(root, spec.moduleId).split('\\').join('/')
    let durations: Durations = {}
    try {
      durations = JSON.parse(readFileSync(resolve(root, 'test-durations.json'), 'utf8'))
    } catch {
      // No recorded durations: fall back to equal weights.
    }
    const byPath = new Map(files.map(spec => [rel(spec), spec]))
    const mine = assignShards([...byPath.keys()], durations, shard.count)[shard.index - 1]
    return mine.map(p => byPath.get(p)!)
  }
}
