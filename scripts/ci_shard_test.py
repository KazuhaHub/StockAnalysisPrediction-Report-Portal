"""Checks for the test partition test.yml deals its Go shards with."""
import io
import os
import re
import unittest

from ci_shard import load_timings, partition, main

HERE = os.path.dirname(os.path.abspath(__file__))
TIMINGS = os.path.join(HERE, '..', '.github', 'ci', 'internal-app-test-timings.txt')


class PartitionTests(unittest.TestCase):
    def test_every_test_lands_in_exactly_one_shard(self):
        tests = [f'Test{i:03d}' for i in range(50)]
        timings = {f'Test{i:03d}': float(i % 7) for i in range(0, 50, 2)}
        for total in (1, 2, 3, 4, 6):
            with self.subTest(total=total):
                shards = [partition(tests, timings, s, total) for s in range(1, total + 1)]
                dealt = [t for shard in shards for t in shard]
                self.assertEqual(sorted(dealt), sorted(tests))

    def test_known_durations_balance_the_shards(self):
        # Round-robin by name put 243s on one shard and 594s on another: the order of names says
        # nothing about cost. The heaviest test goes first, each to the lightest shard.
        timings = {'TestA': 30.0, 'TestB': 20.0, 'TestC': 10.0, 'TestD': 10.0, 'TestE': 5.0, 'TestF': 5.0}
        loads = [sum(timings[t] for t in partition(list(timings), timings, s, 2)) for s in (1, 2)]
        self.assertEqual(sorted(loads), [40.0, 40.0])

    def test_without_durations_it_is_round_robin(self):
        # The race subset has no recorded durations, so it keeps the deal it had: by position.
        tests = ['TestA', 'TestB', 'TestC', 'TestD', 'TestE']
        self.assertEqual(partition(tests, {}, 1, 2), ['TestA', 'TestC', 'TestE'])
        self.assertEqual(partition(tests, {}, 2, 2), ['TestB', 'TestD'])

    def test_a_new_test_is_still_dealt(self):
        # A test added after the durations were recorded is not in the file. It must run somewhere,
        # never nowhere: a stale file costs balance, not coverage.
        timings = {'TestOld': 100.0}
        shards = [partition(['TestOld', 'TestNew'], timings, s, 2) for s in (1, 2)]
        self.assertEqual(sorted(t for shard in shards for t in shard), ['TestNew', 'TestOld'])

    def test_light_tests_are_dealt_by_count(self):
        # Thousands of near-zero tests dealt by duration pile onto one shard, and its -run pattern
        # nears the kernel's 128 KiB limit on one argument. Under a second they are dealt by count.
        tests = [f'Test{i:04d}' for i in range(1000)]
        timings = {t: 0.01 for t in tests} | {'TestHeavy': 50.0}
        sizes = [len(partition(tests + ['TestHeavy'], timings, s, 4)) for s in range(1, 5)]
        self.assertLessEqual(max(sizes) - min(sizes), 2, sizes)

    def test_the_recorded_shards_stay_well_under_the_argument_limit(self):
        timings = load_timings(TIMINGS)
        for s in range(1, 5):
            pattern = '^(' + '|'.join(partition(list(timings), timings, s, 4)) + ')$'
            self.assertLess(len(pattern.encode()), 64 * 1024, s)

    def test_the_deal_does_not_depend_on_input_order(self):
        # Every shard computes the partition on its own, so the result must be a function of the
        # set of tests alone.
        tests = [f'Test{i}' for i in range(20)]
        timings = {t: float(len(t) % 3) for t in tests}
        for s in (1, 2, 3):
            self.assertEqual(sorted(partition(tests, timings, s, 3)), sorted(partition(list(reversed(tests)), timings, s, 3)))

    def test_shard_numbers_are_checked(self):
        for shard, total in ((0, 4), (5, 4), (1, 0)):
            with self.subTest(shard=shard, total=total), self.assertRaises(ValueError):
                partition(['TestA'], {}, shard, total)

    def test_main_prints_an_anchorable_alternation(self):
        out = io.StringIO()
        main(['--shard', '1', '--of', '2'], io.StringIO('TestA\nTestB\nTestC\n\n'), out)
        self.assertEqual(out.getvalue(), 'TestA|TestC\n')

    def test_main_prints_nothing_for_an_empty_shard(self):
        # The workflow's `test -n "$regex"` turns this into a failure, so an empty shard never
        # passes green with nothing run.
        out = io.StringIO()
        main(['--shard', '2', '--of', '2'], io.StringIO('TestA\n'), out)
        self.assertEqual(out.getvalue(), '')


class TimingsFileTests(unittest.TestCase):
    def test_the_recorded_file_parses(self):
        # The file is the go-test-shard-N-timings artifacts concatenated: "<seconds>s  <name>".
        timings = load_timings(TIMINGS)
        self.assertGreater(len(timings), 1000)
        self.assertTrue(all(re.fullmatch(r'(Test|Fuzz)\w+', name) for name in timings))

    def test_load_reads_the_artifact_format(self):
        path = os.path.join(HERE, '__ci_shard_timings_fixture.txt')
        with open(path, 'w') as f:
            f.write('    25.02s  TestSlow\n     0.00s  TestFast\n\n')
        try:
            self.assertEqual(load_timings(path), {'TestSlow': 25.02, 'TestFast': 0.0})
        finally:
            os.remove(path)


if __name__ == '__main__':
    unittest.main()
