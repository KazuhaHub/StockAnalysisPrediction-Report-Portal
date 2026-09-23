"""Failure-path checks for release publication guards."""
import json
import os
import re
import unittest
from unittest.mock import patch
import subprocess
from release_guard import image_digest, validate_metadata, ci_ready, read_state, guard_draft, expected_assets, verify_target

# CI runs `python3 -B -m unittest discover -s scripts` from the repository root, so every file
# these tests read is found relative to this one rather than to the working directory.
HERE = os.path.dirname(os.path.abspath(__file__))


def push_run_jobs(workflow):
    """The check names one push run of `workflow` reports, read from the file itself.

    A job whose `if:` keeps it off a push is left out, since a release waits on a push run. A
    condition this does not recognise is an error rather than a guess: counting a job the run
    skips would make the gate look satisfiable when it is not.
    """
    names = set()
    jobs = workflow.split('\njobs:\n', 1)[1]
    for block in re.split(r'^  [A-Za-z0-9_-]+:[ \t]*$', jobs, flags=re.M)[1:]:
        name = re.search(r'^    name: (.+?)\s*$', block, re.M)[1]
        cond = re.search(r'^    if: (.+?)\s*$', block, re.M)
        cond = cond[1] if cond else None
        if cond == "github.event_name == 'pull_request'":
            continue
        if cond not in (None, "github.event_name != 'pull_request'"):
            raise AssertionError(f'teach push_run_jobs whether a push runs {name!r} (if: {cond})')
        shards = re.search(r'^\s+shard: \[([^\]]+)\]', block, re.M)
        if '${{ matrix.shard }}' in name:
            names |= {name.replace('${{ matrix.shard }}', s.strip()) for s in shards[1].split(',')}
        else:
            names.add(name)
    return names

class GuardTests(unittest.TestCase):
    @patch('release_guard.subprocess.run')
    def test_registry_failure_is_not_absence(self, run):
        run.return_value = subprocess.CompletedProcess([], 1, '', 'unauthorized')
        with self.assertRaises(RuntimeError):
            image_digest('registry/image:v2026.38')

    @patch('release_guard.subprocess.run')
    def test_missing_manifest_is_absent(self, run):
        run.return_value = subprocess.CompletedProcess([], 1, '', 'manifest unknown')
        self.assertIsNone(image_digest('registry/image:v2026.38'))

    @patch('release_guard.subprocess.check_output')
    def test_state_read_failure_aborts(self, read):
        read.side_effect = subprocess.CalledProcessError(1, 'gh')
        with self.assertRaises(subprocess.CalledProcessError):
            read_state('owner/repo')

    @patch('release_guard.subprocess.check_output')
    def test_published_release_cannot_be_replaced(self, read):
        read.return_value = '[[{"tag_name":"v2026.38","draft":false}]]'
        with self.assertRaises(RuntimeError):
            guard_draft('owner/repo', 'v2026.38')

    def test_expected_assets_identify_all_platforms(self):
        assets = expected_assets('v2026.38')
        self.assertEqual(len(assets), 8)
        self.assertIn('report-portal_v2026.38_windows_arm64.zip', assets)
        self.assertIn('release-metadata.json', assets)

    def test_metadata_rejects_wrong_commit(self):
        with self.assertRaises(ValueError):
            validate_metadata({'tag':'v2026.38','image':'registry/image','commit':'bad',
                               'digest':'sha256:'+'a'*64}, 'v2026.38', 'registry/image', 'b'*40)

    @patch('release_guard.image_digest', return_value='sha256:' + 'b'*64)
    @patch('release_guard.subprocess.check_output')
    @patch('release_guard.gh_json')
    def test_promotion_rejects_changed_registry_tag(self, gh, read, digest):
        import json
        gh.side_effect = [
            {'draft': False, 'assets': [{'name':name, 'id':1} for name in expected_assets('v2026.38')]},
            {'object': {'type':'tag','sha':'t'}},
            {'object': {'type':'commit','sha':'c'*40}},
        ]
        read.return_value = json.dumps({'tag':'v2026.38','image':'registry/image',
                                       'commit':'c'*40,'digest':'sha256:'+'a'*64})
        with self.assertRaisesRegex(RuntimeError, 'recorded digest'):
            verify_target('owner/repo', 'v2026.38', 'registry/image')

    def test_ci_requires_all_full_race_shards(self):
        jobs = [{'name':name, 'conclusion':'success'} for name in
                ['go-test','web typecheck + build','workflow lint','docker image (font gate + smoke)',
                 'go race full (other packages)']]
        self.assertFalse(ci_ready(jobs))
        jobs += [{'name':f'go race full ({i}/4)', 'conclusion':'success'} for i in range(1,5)]
        self.assertTrue(ci_ready(jobs))

    def test_ci_ready_matches_test_workflow(self):
        # ci_ready names test.yml's jobs by display name, and nothing else ties the two together:
        # a renamed job or a changed shard count stays green on every pull request and first fails
        # at release time, as "Full CI did not pass" on a run that passed. So the gate must accept
        # exactly the jobs a push run of test.yml reports: all of them green is ready, and any one
        # missing is not.
        with open(os.path.join(HERE, '..', '.github', 'workflows', 'test.yml')) as f:
            names = push_run_jobs(f.read())
        self.assertTrue(ci_ready([{'name': n, 'conclusion': 'success'} for n in names]), sorted(names))
        for missing in sorted(names):
            with self.subTest(missing=missing):
                self.assertFalse(ci_ready([{'name': n, 'conclusion': 'success'} for n in names - {missing}]))

    def test_expected_assets_satisfy_channel_decision(self):
        # The release asset list is kept twice: expected_assets, which verify-target holds a
        # Release to, and assets_complete in channel_targets.sh, which picks what the channels
        # point at and calls itself "the same bar the release job enforces". Drift either way
        # fails after publication: a complete Release is never promoted, or an incomplete one is
        # picked and then refused. So a Release carrying exactly expected_assets must be the
        # :latest candidate, and one missing any single asset must be skipped as incomplete.
        tag = 'v2026.39'
        releases = [{'tag_name': tag, 'draft': False, 'prerelease': False,
                     'assets': [{'name': n} for n in sorted(expected_assets(tag))]}]
        short = {}
        for i in range(len(expected_assets(tag))):
            other = f'v2026.30.{i + 1}'
            assets = sorted(expected_assets(other))
            short[other] = assets.pop(i)
            releases.append({'tag_name': other, 'draft': False, 'prerelease': False,
                             'assets': [{'name': n} for n in assets]})
        env = {k: v for k, v in os.environ.items()
               if k not in ('CURRENT_LATEST', 'CURRENT_BETA', 'LATEST_OVERRIDE', 'BETA_OVERRIDE', 'FORCE')}
        out = subprocess.run(['sh', os.path.join(HERE, 'channel_targets.sh')], input=json.dumps(releases),
                             capture_output=True, text=True, check=True, env=env).stdout
        self.assertIn(f'LATEST_TARGET={tag}\n', out)
        skipped = set(re.search(r'^SKIPPED_INCOMPLETE=(.*)$', out, re.M)[1].split(','))
        self.assertEqual(skipped, set(short), {t: short[t] for t in set(short) - skipped})

if __name__ == '__main__':
    unittest.main()
