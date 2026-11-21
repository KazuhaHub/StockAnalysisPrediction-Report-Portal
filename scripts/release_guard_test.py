"""Failure-path checks for release publication guards."""
import json
import os
import re
import unittest
from unittest.mock import patch
import subprocess
from release_guard import image_digest, validate_metadata, ci_ready, read_state, guard_draft, expected_assets, verify_target, licence_ready, check_ci, recorded_images, image_for, guard_fixed_image, check_move

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
        # A summary job says always() so that a failed dependency fails it instead of skipping it.
        if cond in ("github.event_name == 'pull_request'", "always() && github.event_name == 'pull_request'"):
            continue
        if cond == 'always()':
            cond = None
        # `!= 'pull_request' || <more>` runs on every push whatever <more> says.
        if cond is not None and cond.startswith("github.event_name != 'pull_request' ||"):
            cond = "github.event_name != 'pull_request'"
        if cond not in (None, "github.event_name != 'pull_request'"):
            raise AssertionError(f'teach push_run_jobs whether a push runs {name!r} (if: {cond})')
        shards = re.search(r'^\s+shard: \[([^\]]+)\]', block, re.M)
        if '${{ matrix.shard }}' in name:
            names |= {name.replace('${{ matrix.shard }}', s.strip()) for s in shards[1].split(',')}
        else:
            names.add(name)
    return names


def read_workflow(name):
    with open(os.path.join(HERE, '..', '.github', 'workflows', name)) as f:
        return f.read()


def job_block(workflow, job_id):
    """The lines of one job in `workflow`, up to the next job."""
    jobs = workflow.split('\njobs:\n', 1)[1]
    match = re.search(rf'^  {re.escape(job_id)}:[ \t]*\n(.*?)(?=^  [A-Za-z0-9_-]+:[ \t]*$|\Z)', jobs, re.M | re.S)
    if not match:
        raise AssertionError(f'no job {job_id!r} in the workflow')
    return match[1]


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
                ['go-test','go test (checks + other packages)','web typecheck + build','web lint + typecheck + build','workflow lint',
                 'docker image (font gate + smoke)','go race full (other packages)',
                 'release targets (cross-compile)'] + [f'web tests ({i}/3)' for i in range(1,4)]]
        self.assertFalse(ci_ready(jobs))
        jobs += [{'name':f'go race full ({i}/4)', 'conclusion':'success'} for i in range(1,5)]
        self.assertFalse(ci_ready(jobs))
        jobs += [{'name':f'go test ({i}/4)', 'conclusion':'success'} for i in range(1,5)]
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

    def test_race_shards_agree_on_their_count(self):
        # The count is written twice in test.yml, the matrix and the "(N/4)" in the job name that
        # ci_ready waits for, and the partition that deals the tests out reads it from the matrix.
        # The name must say the same: once ci_ready is edited to match it, a name left at /4 over a
        # three-shard matrix passes the test above, and every check misreports its share. The
        # shards must be numbered 1..N, because only then does `NR % m == s % m` give each residue
        # to exactly one shard, and a shard dealt no tests must fail rather than race nothing green.
        block = job_block(read_workflow('test.yml'), 'go-race-full')
        shards = [int(s) for s in re.search(r'^\s+shard: \[([^\]]+)\]', block, re.M)[1].split(',')]
        self.assertEqual(shards, list(range(1, len(shards) + 1)))
        name = re.search(r'^    name: (.+?)\s*$', block, re.M)[1]
        self.assertEqual(re.search(r'\(\$\{\{ matrix\.shard \}\}/(\d+)\)', name)[1], str(len(shards)), name)
        partition = re.search(r'awk -v s="\$\{\{ matrix\.shard \}\}" -v m=("[^"]*"|\S+)', block)
        self.assertIsNotNone(partition, 'the partition is no longer the awk this test reads')
        self.assertIn(partition[1].strip('"'), ('${{ strategy.job-total }}', str(len(shards))))
        self.assertIn('test -n "$regex"', block)

    def test_go_test_shards_agree_on_their_count(self):
        # The same two-places count as the race shards, for the ordinary internal/app run that the
        # required check "go-test" summarises. The partition must read the matrix, the shards must
        # be numbered 1..N, a shard dealt nothing must fail, and each shard needs the database the
        # package's PostgreSQL variants run against, or they skip and the shard passes without them.
        block = job_block(read_workflow('test.yml'), 'go-test-shard')
        shards = [int(s) for s in re.search(r'^\s+shard: \[([^\]]+)\]', block, re.M)[1].split(',')]
        self.assertEqual(shards, list(range(1, len(shards) + 1)))
        name = re.search(r'^    name: (.+?)\s*$', block, re.M)[1]
        self.assertEqual(re.search(r'\(\$\{\{ matrix\.shard \}\}/(\d+)\)', name)[1], str(len(shards)), name)
        partition = re.search(r'awk -v s="\$\{\{ matrix\.shard \}\}" -v m=("[^"]*"|\S+)', block)
        self.assertIsNotNone(partition, 'the partition is no longer the awk this test reads')
        self.assertIn(partition[1].strip('"'), ('${{ strategy.job-total }}', str(len(shards))))
        self.assertIn('test -n "$regex"', block)
        self.assertIn('TEST_POSTGRES_DSN', block)
        self.assertIn('image: postgres:16', block)

    def test_web_test_shards_agree_on_their_count(self):
        # The same two-places count as the race shards: the matrix deals the files out through
        # vitest's --shard, and the "(N/3)" in the name is what ci_ready waits for.
        block = job_block(read_workflow('test.yml'), 'web-test')
        shards = [int(s) for s in re.search(r'^\s+shard: \[([^\]]+)\]', block, re.M)[1].split(',')]
        self.assertEqual(shards, list(range(1, len(shards) + 1)))
        name = re.search(r'^    name: (.+?)\s*$', block, re.M)[1]
        self.assertEqual(re.search(r'\(\$\{\{ matrix\.shard \}\}/(\d+)\)', name)[1], str(len(shards)), name)
        self.assertIn('--shard=${{ matrix.shard }}/${{ strategy.job-total }}', block)

    def test_summary_checks_fail_rather_than_skip(self):
        # "go race" and "web typecheck + build" are required checks that now only summarise their
        # shards. A job whose `needs` failed is skipped unless it says always(), and a skipped
        # required check counts as passed, so each must run always() and test every result itself.
        workflow = read_workflow('test.yml')
        for job_id in ('go-race', 'go-test', 'web-build'):
            with self.subTest(job=job_id):
                block = job_block(workflow, job_id)
                self.assertRegex(re.search(r'^    if: (.+?)\s*$', block, re.M)[1], r'^always\(\)')
                needs = re.search(r'^    needs: (.+?)\s*$', block, re.M)[1].strip('[]').split(',')
                for need in (n.strip() for n in needs):
                    self.assertIn(f'test "${{{{ needs.{need}.result }}}}" = success', block)

    def test_ci_compiles_every_release_target(self):
        # release.yml's build matrix is the list of platforms a release ships, and test.yml compiles
        # them before a tag: linux/amd64 in docker-image, the rest in release-targets. A platform
        # added to the release and not here would first compile during a release, which is what
        # release-targets exists to prevent. Both jobs must also run the release's own provenance
        # check on what they build.
        release = set(re.findall(r'^\s+- \{ goos: (\w+), goarch: (\w+),', read_workflow('release.yml'), re.M))
        self.assertEqual(len(release), 6, release)
        workflow = read_workflow('test.yml')
        docker = job_block(workflow, 'docker-image')
        cross = job_block(workflow, 'release-targets')
        loop = re.search(r'^\s+for target in ([^;]+); do$', cross, re.M)[1].split()
        compiled = {tuple(t.split('/')) for t in loop}
        self.assertEqual(len(compiled), len(loop), loop)
        self.assertIn('GOOS: linux', docker)
        self.assertIn('GOARCH: amd64', docker)
        compiled.add(('linux', 'amd64'))
        self.assertEqual(compiled, release)
        self.assertEqual({f'{goos}_{goarch}' for goos, goarch in release},
                         {n.split('_', 2)[2].split('.')[0] for n in expected_assets('vX') if n.startswith('report-portal_')})
        for block in (docker, cross):
            self.assertIn('sh scripts/check-release-build.sh', block)

    def test_arm64_binary_runs_before_the_tag_and_the_image(self):
        # test.yml compiles linux/arm64 and never runs it, so release.yml's smoke-arm64 is the one
        # place that binary executes before it ships. It has to run the release's own artifact on an
        # arm64 runner, and the tag and docker jobs have to wait for it: a pushed version tag cannot
        # be cut again and a fixed image tag is never replaced, so a smoke that finished after either
        # would be too late to matter. The draft Release needs the tag, so it waits too.
        workflow = read_workflow('release.yml')
        smoke = job_block(workflow, 'smoke-arm64')
        self.assertIn('    runs-on: ubuntu-24.04-arm\n', smoke)
        self.assertIn('name: report-portal-linux-arm64,', smoke)
        self.assertIn('http://127.0.0.1:18790/healthz', smoke)
        for job in ('tag', 'docker'):
            needs = re.search(r'^    needs: \[([^\]]*)\]', job_block(workflow, job), re.M)[1]
            self.assertIn('smoke-arm64', [n.strip() for n in needs.split(',')], job)
        needs = re.search(r'^    needs: \[([^\]]*)\]', job_block(workflow, 'release'), re.M)[1]
        self.assertIn('tag', [n.strip() for n in needs.split(',')])

    def test_release_events_only_relay_to_the_default_branch(self):
        # A release event runs release-channels.yml from the release's tag, so whatever job such an
        # event starts runs the copy that tag was cut with, and no later fix reaches it. The one job
        # a release event may start is the relay: no secret, no action, nothing but actions: write,
        # and a dispatch of this file on the default branch. Every other job holds RELEASE_PAT or
        # packages: write, so it must run only for a dispatch on the default branch, where the file
        # is main's own.
        workflow = read_workflow('release-channels.yml')
        ids = re.findall(r'^  ([A-Za-z0-9_-]+):[ \t]*$', workflow.split('\njobs:\n', 1)[1], re.M)
        self.assertIn('relay', ids)
        relay = job_block(workflow, 'relay')
        self.assertIn("    if: github.event_name == 'release'\n", relay)
        self.assertRegex(relay, r'\n    permissions:\n      actions: write\n    [a-z]')
        self.assertNotIn('secrets.', relay)
        self.assertNotIn('uses:', relay)
        self.assertIn('REF: ${{ github.event.repository.default_branch }}', relay)
        self.assertIn('gh workflow run release-channels.yml --repo "$REPO" --ref "$REF"', relay)
        gate = ("    if: github.event_name == 'workflow_dispatch' && "
                "github.ref == format('refs/heads/{0}', github.event.repository.default_branch)\n")
        for job in ids:
            if job != 'relay':
                with self.subTest(job=job):
                    self.assertIn(gate, job_block(workflow, job))

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

class LicenceGateTests(unittest.TestCase):
    """A release needs a green licence scan on the exact commit, as well as the full test run."""

    def test_licence_ready_needs_the_scan_job_green(self):
        self.assertTrue(licence_ready([{'name': 'licence scan', 'conclusion': 'success'}]))
        self.assertFalse(licence_ready([]))
        for bad in ('failure', 'cancelled', 'skipped', None):
            self.assertFalse(licence_ready([{'name': 'licence scan', 'conclusion': bad}]), bad)
        self.assertFalse(licence_ready([{'name': 'go-test', 'conclusion': 'success'}]))

    def test_licence_ready_matches_the_licence_workflow(self):
        # The guard names the job by display name, so a rename must fail here and not at release time.
        names = push_run_jobs(read_workflow('licence.yml'))
        self.assertEqual(names, {'licence scan'})
        self.assertTrue(licence_ready([{'name': n, 'conclusion': 'success'} for n in names]))

    def test_licence_workflow_runs_on_main_pushes_and_can_be_dispatched(self):
        # The guard verifies a run for the exact main commit; with no push trigger there would never be one.
        head = read_workflow('licence.yml').split('\njobs:\n', 1)[0]
        self.assertRegex(head, r'(?m)^  push:\n    branches: \[[^\]]*\bmain\b[^\]]*\]$')
        self.assertRegex(head, r'(?m)^  workflow_dispatch:')

    def test_licence_workflow_also_runs_on_the_integration_branch(self):
        # The platform work merges into integration/app-platform first; the scan must run on those pushes too.
        head = read_workflow('licence.yml').split('\njobs:\n', 1)[0]
        self.assertRegex(head, r'(?m)^  push:\n    branches: \[[^\]]*\bintegration/app-platform\b[^\]]*\]$')

    def test_licence_workflow_scans_every_release_platform(self):
        # A module can be linked on one GOOS/GOARCH and not another, so the Go SBOM is built per release target.
        release = read_workflow('release.yml')
        targets = set(re.findall(r'\{ goos: (\w+), goarch: (\w+),', release))
        self.assertEqual(len(targets), 6)
        licence = read_workflow('licence.yml')
        listed = set(re.findall(r'\b(linux|darwin|windows)/(amd64|arm64)\b', licence))
        self.assertEqual(listed, targets)

    def test_licence_workflow_cross_checks_the_lock_against_the_installed_tree(self):
        # The npm SBOM comes from package-lock.json, whose licence fields its integrity hashes do not cover; the
        # scan is given the installed node_modules so every installed package is checked against its own manifest.
        workflow = read_workflow('licence.yml')
        scan = workflow[workflow.index('licence_scan.py'):]
        self.assertIn('--node-modules web/node_modules', scan)
        self.assertLess(workflow.index('npm ci'), workflow.index('licence_scan.py'))

    def runs(self, workflow, conclusion='success', jobs=None):
        run = {'id': 1, 'event': 'push', 'status': 'completed', 'conclusion': conclusion, 'html_url': 'u/' + workflow}
        default = {'test.yml': [{'name': n, 'conclusion': 'success'} for n in sorted(
            push_run_jobs(read_workflow('test.yml')))], 'licence.yml': [{'name': 'licence scan', 'conclusion': 'success'}]}
        return run, default[workflow] if jobs is None else jobs

    def wait(self, licence_conclusion='success', licence_jobs=None, licence_runs=True):
        def fake(endpoint):
            if '/runs/1/jobs' in endpoint:
                return {'jobs': self.current_jobs}
            wf = 'licence.yml' if 'licence.yml' in endpoint else 'test.yml'
            self.current_jobs = self.runs(wf, licence_conclusion if wf == 'licence.yml' else 'success',
                                          licence_jobs if wf == 'licence.yml' else None)[1]
            if wf == 'licence.yml' and not licence_runs:
                return {'workflow_runs': []}
            return {'workflow_runs': [self.runs(wf, licence_conclusion if wf == 'licence.yml' else 'success')[0]]}
        with patch('release_guard.gh_json', side_effect=fake):
            check_ci('o/r', 'a' * 40)

    def test_green_tests_and_green_licence_pass(self):
        self.wait()

    def test_a_failed_licence_run_blocks_the_release(self):
        with self.assertRaisesRegex(RuntimeError, 'Licence scan did not pass'):
            self.wait(licence_conclusion='failure')

    def test_a_licence_run_without_the_scan_job_blocks_the_release(self):
        with self.assertRaisesRegex(RuntimeError, 'Licence scan did not pass'):
            self.wait(licence_jobs=[])

    def test_no_licence_run_for_the_commit_blocks_the_release(self):
        with self.assertRaisesRegex(RuntimeError, 'licence.yml'):
            self.wait(licence_runs=False)


class ReleaseGateTests(unittest.TestCase):
    """The gate looks once: a release holds no runner while CI runs, and builds nothing CI has not passed."""

    def test_an_unfinished_ci_run_refuses_at_once(self):
        run = {'id': 1, 'event': 'push', 'status': 'in_progress', 'conclusion': None, 'html_url': 'u/test.yml'}
        with patch('release_guard.gh_json', return_value={'workflow_runs': [run]}):
            with self.assertRaisesRegex(RuntimeError, 'has not finished.*failed jobs.*u/test.yml'):
                check_ci('o/r', 'a' * 40)

    def test_builds_start_only_after_the_gate(self):
        release = read_workflow('release.yml')
        needs = RunnerImageWorkflowTests.needs
        self.assertIn('ci', needs(self, release, 'web'))
        self.assertIn('ci', needs(self, release, 'build-runner'))
        self.assertIn('web', needs(self, release, 'build'))
        gate = job_block(release, 'ci')
        self.assertIn('release_guard.py check-ci', gate)
        self.assertLessEqual(int(re.search(r'timeout-minutes: (\d+)', gate)[1]), 5)


PORTAL = 'ghcr.io/owner/repo'
RUNNER = PORTAL + '-app-runner'
SHA = 'c' * 40


def metadata(portal_digest='sha256:' + 'a' * 64, runner_digest='sha256:' + 'b' * 64, images=True):
    """release-metadata.json as record-metadata writes it, or as a release before the runner image has it."""
    meta = {'tag': 'v2026.47.1', 'image': PORTAL, 'commit': SHA, 'digest': portal_digest, 'recorded_at': '2026-11-20T00:00:00Z'}
    if images:
        meta['images'] = {'portal': {'image': PORTAL, 'digest': portal_digest},
                          'app-runner': {'image': RUNNER, 'digest': runner_digest}}
    return meta


class ChannelMoveTests(unittest.TestCase):
    """A channel never moves to a release without a runner image while its current target has one (doc 187)."""

    def move(self, current, target, allow=False):
        releases = {'v2026.47.1': metadata(), 'v2026.46.1': metadata(images=False), 'v2026.45.1': metadata(images=False)}

        def fetch(repo, tag):
            meta = dict(releases[tag])
            meta['tag'] = tag
            return meta, SHA

        with patch('release_guard.fetch_release_metadata', side_effect=fetch):
            check_move('owner/repo', PORTAL, current, target, allow)

    def test_moving_to_an_older_portal_only_release_from_one_with_a_runner_image_is_refused(self):
        with self.assertRaisesRegex(RuntimeError, 'runner image'):
            self.move('v2026.47.1', 'v2026.46.1')

    def test_the_explicit_flag_allows_it(self):
        self.move('v2026.47.1', 'v2026.46.1', allow=True)

    def test_other_moves_are_unaffected(self):
        self.move('v2026.46.1', 'v2026.47.1')   # forward to a release with a runner image
        self.move('v2026.45.1', 'v2026.46.1')   # between Portal-only releases
        self.move('', 'v2026.46.1')             # a channel that has never been set
        self.move('v2026.47.1', 'v2026.47.1')   # no move at all

    def test_a_current_target_that_cannot_be_read_is_refused_not_assumed_to_have_none(self):
        with patch('release_guard.fetch_release_metadata', side_effect=lambda repo, tag: (_ for _ in ()).throw(RuntimeError('gone')) if tag == 'v2026.47.1' else (dict(metadata(images=False), tag=tag), SHA)):
            with self.assertRaises(RuntimeError):
                check_move('owner/repo', PORTAL, 'v2026.47.1', 'v2026.46.1')

    def test_the_channel_workflow_checks_each_move_and_offers_the_flag(self):
        workflow = read_workflow('release-channels.yml')
        head = workflow.split('\njobs:\n', 1)[0]
        self.assertRegex(head, r'allow_runner_regress:\n\s+description:.*\n\s+required: false\n\s+type: boolean\n\s+default: false')
        block = job_block(workflow, 'reconcile')
        check, apply = block.index('release_guard.py check-move'), block.index('Apply the channels')
        self.assertLess(block.index('Verify every target exists'), check)
        self.assertLess(check, apply)
        self.assertIn('CURRENT_LATEST', block[check - 600:check + 400])
        self.assertIn('CURRENT_BETA', block[check - 600:check + 600])
        self.assertIn('${ALLOW:+--allow-runner-regress}', block[check:apply])


class FixedImageGuardTests(unittest.TestCase):
    """A fixed tag is never replaced, and a half-published release is never recovered as Portal-only."""

    def guard(self, ref, existing):
        with patch('release_guard.image_digest', side_effect=lambda r: existing.get(r)):
            guard_fixed_image(ref)

    def test_nothing_published_passes(self):
        self.guard(PORTAL + ':v1', {})
        self.guard(RUNNER + ':v1', {})

    def test_an_existing_runner_tag_is_refused_and_points_at_a_rerun_not_at_recovery(self):
        with self.assertRaisesRegex(RuntimeError, r'runner image exists.*Re-run the failed jobs'):
            self.guard(RUNNER + ':v1', {RUNNER + ':v1': 'sha256:' + 'a' * 64})

    def test_a_portal_tag_with_a_runner_tag_is_not_sent_to_the_portal_only_recovery(self):
        existing = {PORTAL + ':v1': 'sha256:' + 'a' * 64, RUNNER + ':v1': 'sha256:' + 'b' * 64}
        with self.assertRaises(RuntimeError) as raised:
            self.guard(PORTAL + ':v1', existing)
        self.assertNotIn('recover-release-metadata.py to verify', str(raised.exception))
        self.assertIn('Re-run the failed jobs', str(raised.exception))

    def test_a_portal_only_tag_still_points_at_recovery(self):
        with self.assertRaisesRegex(RuntimeError, 'recover-release-metadata.py to verify'):
            self.guard(PORTAL + ':v1', {PORTAL + ':v1': 'sha256:' + 'a' * 64})

    def test_the_runner_job_runs_the_amd64_image_before_it_pushes_the_fixed_tag(self):
        block = job_block(read_workflow('release.yml'), 'docker-runner')
        load, push = block.index('load: true'), block.index('push: true')
        probe = block.index('scripts/probe-runner-image.sh')
        self.assertLess(load, probe)
        self.assertLess(probe, push)
        self.assertEqual(block.count('push: true'), 1)
        self.assertEqual(block.count('push: false') + block.count('push: true'), block.count('docker/build-push-action'))


class RunnerImageMetadataTests(unittest.TestCase):
    """Doc 187 section 12: per-image digests, with older releases still validating."""

    def test_the_new_format_validates_and_returns_the_portal_digest(self):
        self.assertEqual(validate_metadata(metadata(), 'v2026.47.1', PORTAL, SHA), 'sha256:' + 'a' * 64)
        self.assertEqual(recorded_images(metadata(), PORTAL), {'portal': (PORTAL, 'sha256:' + 'a' * 64), 'app-runner': (RUNNER, 'sha256:' + 'b' * 64)})

    def test_a_release_from_before_the_runner_image_still_validates_for_portal_only(self):
        legacy = metadata(images=False)
        self.assertEqual(validate_metadata(legacy, 'v2026.47.1', PORTAL, SHA), 'sha256:' + 'a' * 64)
        self.assertEqual(recorded_images(legacy, PORTAL), {'portal': (PORTAL, 'sha256:' + 'a' * 64)})

    def test_image_names_follow_the_portal_image(self):
        self.assertEqual(image_for('portal', PORTAL), PORTAL)
        self.assertEqual(image_for('app-runner', PORTAL), RUNNER)
        with self.assertRaises(ValueError):
            image_for('docs', PORTAL)

    def test_a_declared_images_map_must_be_whole_and_valid(self):
        # Each of these is a release that says it has a runner image and does not prove it. All refuse.
        def broken(mutate):
            meta = metadata()
            mutate(meta)
            return meta
        cases = {
            'no runner entry': lambda m: m['images'].pop('app-runner'),
            'no portal entry': lambda m: m['images'].pop('portal'),
            'an extra image': lambda m: m['images'].update({'docs': {'image': PORTAL + '-docs', 'digest': 'sha256:' + 'd' * 64}}),
            'a short runner digest': lambda m: m['images']['app-runner'].update(digest='sha256:abc'),
            'an uppercase runner digest': lambda m: m['images']['app-runner'].update(digest='sha256:' + 'B' * 64),
            'a runner digest that is not text': lambda m: m['images']['app-runner'].update(digest=7),
            'no runner digest': lambda m: m['images']['app-runner'].pop('digest'),
            'a runner path that is not the portal image with -app-runner': lambda m: m['images']['app-runner'].update(image=PORTAL),
            'a runner path in another repository': lambda m: m['images']['app-runner'].update(image='ghcr.io/other/repo-app-runner'),
            'an extra field in an entry': lambda m: m['images']['app-runner'].update(note='x'),
            'a portal entry that differs from the legacy keys': lambda m: m['images']['portal'].update(digest='sha256:' + 'e' * 64),
            'an images value that is a list': lambda m: m.update(images=[]),
            'an images value that is null': lambda m: m.update(images=None),
            'an entry that is not an object': lambda m: m['images'].update({'app-runner': 'sha256:' + 'b' * 64}),
        }
        for name, mutate in cases.items():
            with self.subTest(name):
                with self.assertRaises(ValueError):
                    validate_metadata(broken(mutate), 'v2026.47.1', PORTAL, SHA)

    def test_the_legacy_keys_are_still_held_to_tag_repository_and_commit(self):
        for key, value in (('tag', 'v2026.1.1'), ('image', 'ghcr.io/other/repo'), ('commit', 'd' * 40), ('digest', 'sha256:nope')):
            with self.subTest(key):
                meta = metadata()
                meta[key] = value
                with self.assertRaises(ValueError):
                    validate_metadata(meta, 'v2026.47.1', PORTAL, SHA)


class VerifyTargetTests(unittest.TestCase):
    """verify-target verifies every recorded image's fixed tag against its recorded digest."""

    def verify(self, meta, registry, key='portal'):
        asked = []

        def digest(ref):
            asked.append(ref)
            return registry[ref]

        with patch('release_guard.image_digest', side_effect=digest), \
                patch('release_guard.subprocess.check_output', return_value=json.dumps(meta)), \
                patch('release_guard.gh_json') as gh:
            gh.side_effect = [
                {'draft': False, 'assets': [{'name': name, 'id': 1} for name in expected_assets('v2026.47.1')]},
                {'object': {'type': 'tag', 'sha': 't'}},
                {'object': {'type': 'commit', 'sha': SHA}},
            ]
            return verify_target('owner/repo', 'v2026.47.1', PORTAL, key), asked

    def registry(self, portal='sha256:' + 'a' * 64, runner='sha256:' + 'b' * 64):
        return {f'{PORTAL}:v2026.47.1': portal, f'{RUNNER}:v2026.47.1': runner}

    def test_both_images_are_verified_whichever_key_is_asked(self):
        for key, want in (('portal', 'sha256:' + 'a' * 64), ('app-runner', 'sha256:' + 'b' * 64)):
            with self.subTest(key):
                digest, asked = self.verify(metadata(), self.registry(), key)
                self.assertEqual(digest, want)
                self.assertEqual(set(asked), {f'{PORTAL}:v2026.47.1', f'{RUNNER}:v2026.47.1'})
                self.assertEqual(len(asked), 2)
        digest, _ = self.verify(metadata(), self.registry(), 'all')
        self.assertEqual(digest.splitlines(), ['portal sha256:' + 'a' * 64, 'app-runner sha256:' + 'b' * 64])

    def test_a_moved_runner_tag_refuses_the_release_even_when_portal_is_asked(self):
        # Neither channel tag may move to half a release.
        with self.assertRaisesRegex(RuntimeError, 'app-runner image tag no longer matches recorded digest'):
            self.verify(metadata(), self.registry(runner='sha256:' + 'f' * 64), 'portal')

    def test_a_moved_portal_tag_refuses_the_release_when_the_runner_is_asked(self):
        with self.assertRaisesRegex(RuntimeError, 'portal image tag no longer matches recorded digest'):
            self.verify(metadata(), self.registry(portal='sha256:' + 'f' * 64), 'app-runner')

    def test_a_missing_runner_tag_refuses(self):
        # image_digest returns None for an absent manifest, which is never the recorded digest.
        with self.assertRaisesRegex(RuntimeError, 'app-runner image tag'):
            self.verify(metadata(), {f'{PORTAL}:v2026.47.1': 'sha256:' + 'a' * 64, f'{RUNNER}:v2026.47.1': None})

    def test_a_declared_runner_image_with_no_valid_digest_refuses_before_the_registry_is_asked(self):
        meta = metadata()
        meta['images']['app-runner']['digest'] = 'sha256:short'
        asked = []
        with self.assertRaises(ValueError):
            with patch('release_guard.image_digest', side_effect=lambda ref: asked.append(ref)):
                self.verify(meta, {})
        self.assertEqual(asked, [])

    def test_an_older_release_is_verified_for_portal_only(self):
        digest, asked = self.verify(metadata(images=False), self.registry(), 'portal')
        self.assertEqual((digest, asked), ('sha256:' + 'a' * 64, [f'{PORTAL}:v2026.47.1']))
        # Promotion moves only the Portal tag for it: the runner key yields nothing to point at.
        digest, asked = self.verify(metadata(images=False), self.registry(), 'app-runner')
        self.assertEqual((digest, asked), ('', [f'{PORTAL}:v2026.47.1']))
        digest, _ = self.verify(metadata(images=False), self.registry(), 'all')
        self.assertEqual(digest, 'portal sha256:' + 'a' * 64)

    def test_an_unknown_key_is_refused(self):
        with self.assertRaises(ValueError):
            verify_target('owner/repo', 'v2026.47.1', PORTAL, 'docs')


class RunnerImageWorkflowTests(unittest.TestCase):
    """The runner image rides the same CI-gated chain as the Portal image (doc 187 section 12)."""

    def needs(self, workflow, job):
        return [n.strip() for n in re.search(r'^    needs: \[([^\]]*)\]', job_block(workflow, job), re.M)[1].split(',')]

    def setUp(self):
        self.release = read_workflow('release.yml')

    def test_the_runner_image_needs_the_same_chain_as_the_portal_image(self):
        # `docker` needs release (which needs tag, which needs ci, build, build-runner and smoke-arm64).
        self.assertEqual(self.needs(self.release, 'docker-runner'), ['setup', 'build-runner', 'release', 'smoke-arm64'])
        for need in ('release', 'smoke-arm64'):
            self.assertIn(need, self.needs(self.release, 'docker'))
        self.assertIn('tag', self.needs(self.release, 'release'))
        tag = self.needs(self.release, 'tag')
        for need in ('ci', 'build', 'build-runner', 'smoke-arm64'):
            self.assertIn(need, tag)

    def test_the_runner_job_rechecks_the_tag_and_refuses_to_replace_a_fixed_tag(self):
        block = job_block(self.release, 'docker-runner')
        self.assertIn('name: Recheck the immutable tag before image publication', block)
        self.assertIn('test "$actual" = "$EXPECTED_SHA"', block)
        self.assertLess(block.index('Recheck the immutable tag'), block.index('docker/build-push-action'))
        self.assertRegex(block, r'release_guard\.py guard "\$\{IMAGE\}:\$\{VERSION\}"')
        self.assertIn('-app-runner\n          VERSION', block)
        self.assertLess(block.index('release_guard.py guard'), block.index('docker/build-push-action'))

    def test_the_runner_job_pushes_only_the_fixed_tag_with_its_own_cache(self):
        block = job_block(self.release, 'docker-runner')
        self.assertIn('file: Dockerfile.app-runner', block)
        self.assertIn('platforms: linux/amd64,linux/arm64', block)
        self.assertIn('-app-runner:${{ needs.setup.outputs.tag }}', block)
        self.assertIn('-app-runner:buildcache', block)
        self.assertNotIn(':latest', block)
        self.assertNotIn(':beta', block)
        self.assertRegex(block, r'outputs:\n      digest: \$\{\{ steps\.push\.outputs\.digest \}\}')
        self.assertIn('org.opencontainers.image.revision=${{ needs.setup.outputs.sha }}', block)
        self.assertIn('org.opencontainers.image.version=${{ needs.setup.outputs.tag }}', block)
        # Every third-party action is pinned to a commit, like the other jobs that hold packages: write.
        for use in re.findall(r'uses: (docker/[^\s]+)', block):
            self.assertRegex(use, r'@[0-9a-f]{40}$', use)

    def test_each_architecture_of_the_image_is_run_by_digest_before_anything_is_recorded(self):
        block = job_block(self.release, 'docker-runner')
        self.assertIn('${{ steps.push.outputs.digest }}', block)
        self.assertIn('scripts/probe-runner-image.sh "$IMAGE" linux/amd64', block)
        arm = job_block(self.release, 'smoke-runner-image-arm64')
        self.assertIn('    runs-on: ubuntu-24.04-arm\n', arm)
        self.assertIn('needs.docker-runner.outputs.digest', arm)
        self.assertIn('scripts/probe-runner-image.sh "$IMAGE" linux/arm64', arm)
        self.assertEqual(self.needs(self.release, 'smoke-runner-image-arm64'), ['setup', 'docker-runner'])

    def test_metadata_is_recorded_by_its_own_job_after_both_images(self):
        self.assertEqual(self.needs(self.release, 'record-metadata'), ['setup', 'docker', 'docker-runner', 'smoke-runner-image-arm64'])
        # The step moved out of docker: the Portal job no longer writes the file.
        self.assertNotIn('release-metadata.json', job_block(self.release, 'docker'))
        record = job_block(self.release, 'record-metadata')
        self.assertIn('images: {portal: {image: $image, digest: $digest}', record)
        self.assertIn('"app-runner": {image: ($image + "-app-runner"), digest: $runner_digest}', record)
        # The legacy keys keep Portal's values, so older readers and older releases still validate.
        self.assertIn('{tag: $tag, commit: $commit, image: $image, digest: $digest, recorded_at: $recorded,', record)
        self.assertIn('gh release upload "$VERSION" release-metadata.json --repo "$REPO" --clobber', record)

    def test_publish_cannot_run_without_the_runner_image(self):
        needs = self.needs(self.release, 'publish')
        for need in ('docker', 'docker-runner', 'smoke-runner-image-arm64', 'record-metadata'):
            self.assertIn(need, needs)

    def test_the_release_job_refuses_to_replace_draft_assets_when_either_image_exists(self):
        block = job_block(self.release, 'release')
        self.assertIn('release_guard.py guard "$IMAGE"', block)
        self.assertIn('release_guard.py guard "$RUNNER_IMAGE"', block)

    def test_the_runner_binary_carries_the_release_identity(self):
        block = job_block(self.release, 'build-runner')
        for var in ('Version=${VERSION}', 'Commit=${SHORT_SHA}', 'BuildDate=${BUILD_DATE}'):
            self.assertIn('internal/version.' + var, block)
        self.assertIn('out/app-runner version', block)

    def test_channels_verify_both_images_before_either_tag_moves(self):
        channels = job_block(read_workflow('release-channels.yml'), 'reconcile')
        verify, apply = channels.index('Verify every target exists'), channels.index('Apply the channels')
        self.assertLess(verify, apply)
        self.assertIn('--image-key all', channels[verify:apply])
        # Both tags are pointed by digest in the one apply step, the runner's only when the release recorded it.
        step = channels[apply:]
        self.assertIn('docker buildx imagetools create -t "${IMAGE}:$1" "${IMAGE}@${digest}"', step)
        self.assertIn('docker buildx imagetools create -t "${IMAGE}-app-runner:$1" "${IMAGE}-app-runner@${runner_digest}"', step)
        self.assertEqual(step.count('imagetools create'), 2)

    def test_workflow_lint_covers_the_entry_and_its_tests(self):
        test = read_workflow('test.yml')
        self.assertIn('deploy/app-runner/portal-app-runner-entry', test)
        self.assertIn('sh scripts/app_runner_entry_test.sh', test)


if __name__ == '__main__':
    unittest.main()
