"""Deal a Go package's top-level tests over CI shards by their recorded durations.

The Go toolchain has no -shard flag, so test.yml lists the tests, and each shard prints its share as
a -run alternation. Dealing by position balanced the count of tests but not their time (one shard
of internal/app carried 594s of tests, another 243s), so tests with a recorded duration are dealt
heaviest first, each to the lightest shard. Tests under a second, and any test with no recorded
duration (one added since the file was written, or every test when no file is given), are dealt by
position in name order, which without any durations is exactly the old round-robin.
A stale file therefore costs balance, never coverage: every listed test lands in exactly one shard.

Usage: go test -list ... | python3 scripts/ci_shard.py --shard N --of M [--timings FILE]
"""
import argparse
import sys

# Tests at or over this many seconds are dealt by duration; the rest, and any test without a recorded
# duration, by position. Dealing the light ones by duration too balances time but not count: thousands
# of near-zero tests pile onto whichever shard is lightest at the end, and that shard's -run pattern
# grows toward the kernel's 128 KiB limit on one argument. Under a second each, by count they even out.
HEAVY_SECONDS = 1.0


def load_timings(path):
    """Read "<seconds>s  <name>" lines, the format of test.yml's go-test-shard-N-timings artifacts."""
    timings = {}
    with open(path, encoding='utf-8') as f:
        for line in f:
            fields = line.split()
            if len(fields) != 2:
                continue
            timings[fields[1]] = float(fields[0].rstrip('s'))
    return timings


def partition(tests, timings, shard, total):
    """The tests of shard `shard` (1-based) of `total`, a function of the set of tests alone."""
    if total < 1 or not 1 <= shard <= total:
        raise ValueError(f'shard {shard} of {total} does not exist')
    names = sorted(set(tests))
    heavy = sorted((n for n in names if timings.get(n, 0.0) >= HEAVY_SECONDS), key=lambda n: (-timings[n], n))
    light = [n for n in names if timings.get(n, 0.0) < HEAVY_SECONDS]
    loads = [0.0] * total
    owner = {}
    for name in heavy:
        target = min(range(total), key=lambda i: (loads[i], i))
        loads[target] += timings[name]
        owner[name] = target
    for i, name in enumerate(light):
        owner[name] = i % total
    return [n for n in names if owner[n] == shard - 1]


def main(argv=None, stdin=sys.stdin, stdout=sys.stdout):
    parser = argparse.ArgumentParser(description=__doc__.split('\n', 1)[0])
    parser.add_argument('--shard', type=int, required=True)
    parser.add_argument('--of', type=int, required=True)
    parser.add_argument('--timings')
    args = parser.parse_args(argv)
    tests = [line.strip() for line in stdin if line.strip()]
    timings = load_timings(args.timings) if args.timings else {}
    mine = partition(tests, timings, args.shard, args.of)
    if mine:
        stdout.write('|'.join(mine) + '\n')


if __name__ == '__main__':
    main()
