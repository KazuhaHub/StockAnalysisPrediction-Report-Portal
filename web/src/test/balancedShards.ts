// Duration-balanced test sharding. vitest's own sharding sorts files by a hash of their path and
// slices equal counts, which ignores how long a file takes: three page suites of 16-25s landed in
// one shard and set the wall clock. Every shard runs this same pure function over the same file
// list and recorded durations, so they agree on the split without talking to each other.

/** Per-file cost that is not test time: module graph load, jsdom setup, worker hand-off. */
export const FILE_OVERHEAD_SECONDS = 0.8

export type Durations = Record<string, number>

function median(values: number[]): number {
  if (values.length === 0) return 0
  const sorted = [...values].sort((a, b) => a - b)
  const mid = sorted.length >> 1
  return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2
}

/** Expected cost of one file: its recorded seconds (the median of the recorded ones if it has
 * none yet) plus the per-file overhead. */
export function shardWeight(file: string, durations: Durations, overhead = FILE_OVERHEAD_SECONDS): number {
  const known = durations[file]
  return (known !== undefined ? known : median(Object.values(durations))) + overhead
}

/** Deals `files` over `count` shards, largest first, each to the currently lightest shard (ties go
 * to the lowest index). Sorting by weight then path makes the result independent of input order. */
export function assignShards(
  files: string[],
  durations: Durations,
  count: number,
  overhead = FILE_OVERHEAD_SECONDS,
): string[][] {
  const fallback = median(Object.values(durations))
  const weight = (f: string) => (durations[f] ?? fallback) + overhead
  const ordered = [...files].sort((a, b) => weight(b) - weight(a) || (a < b ? -1 : a > b ? 1 : 0))
  const shards: string[][] = Array.from({ length: count }, () => [])
  const load: number[] = new Array(count).fill(0)
  for (const f of ordered) {
    let lightest = 0
    for (let i = 1; i < count; i++) if (load[i] < load[lightest]) lightest = i
    shards[lightest].push(f)
    load[lightest] += weight(f)
  }
  return shards
}
