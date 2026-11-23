"""Checks for the pull-request change classification that lets test.yml skip unaffected jobs."""
import os
import re
import subprocess
import unittest

from ci_changes import classify, kind, needs_platform, GO_READS, WEB_READS

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, '..'))


class ClassifyTests(unittest.TestCase):
    def test_docs_only_needs_nothing(self):
        self.assertEqual(classify(['docs/app-platform/187-compose.md', 'README.md']), {'go': False, 'web': False})

    def test_web_only_needs_no_go(self):
        self.assertEqual(classify(['web/src/pages/Home.tsx', 'web/package-lock.json']), {'go': False, 'web': True})

    def test_go_sources_only_need_no_web(self):
        self.assertEqual(classify(['internal/app/server.go', 'cmd/app-runner/main.go']), {'go': True, 'web': False})

    def test_anything_else_needs_both(self):
        # Unknown ground runs everything: a workflow, a script, a fixture, the module files.
        for path in ('.github/workflows/test.yml', 'scripts/release_guard.py', 'internal/app/testdata/x.json',
                     'go.mod', 'go.sum', 'Dockerfile.release', 'deploy/app-runner/service.example.json',
                     'internal/apprunner/driver/seccomp/README.md', 'web'):
            with self.subTest(path=path):
                self.assertEqual(classify([path]), {'go': True, 'web': True})

    def test_files_the_other_side_reads_need_both(self):
        for path in ('web/src/locales/en-US.json', 'docs/app-platform/183-machine-read-api.md',
                     'internal/app/export_sources.go'):
            with self.subTest(path=path):
                self.assertEqual(classify([path]), {'go': True, 'web': True})

    def test_no_files_runs_everything(self):
        # An empty list means the diff said nothing, never that nothing changed.
        self.assertEqual(classify([]), {'go': True, 'web': True})

    def test_the_union_of_the_files(self):
        self.assertEqual(classify(['docs/x.md', 'web/src/a.ts', 'internal/app/a.go']), {'go': True, 'web': True})


class PlatformTests(unittest.TestCase):
    """The app platform's own race lane (and journal's race shards) run only for a change that can
    reach them. The list errs towards running: anything that is not a plain Go or docs file counts."""

    def test_portal_only_go_and_docs_need_no_platform_lane(self):
        self.assertFalse(needs_platform(['internal/app/server.go', 'internal/app/sso_admin.go',
                                         'internal/batch/run.go', 'docs/x.md', 'web/src/a.ts']))

    def test_platform_ground_needs_it(self):
        for path in ('internal/app/app_platform_launch.go', 'internal/app/app_platform_x_test.go',
                     'internal/apprunner/journal/store.go', 'internal/appplatform/descriptor.go',
                     'cmd/app-runner/main.go', 'internal/app/apps_store.go'):
            with self.subTest(path=path):
                self.assertTrue(needs_platform([path]))

    def test_shared_core_needs_it(self):
        # What the platform's tests stand on: the store, its schema checks and migrations, the keyring
        # and secret box its credentials are sealed with, the sessions its viewer requests check.
        for path in ('internal/app/store.go', 'internal/app/store_stmtcache.go', 'internal/app/migrate.go',
                     'internal/app/migrate_steps.go', 'internal/app/schema_catalog.go',
                     'internal/app/keyring_store.go', 'internal/app/secretbox.go', 'internal/app/sessions.go',
                     'internal/queue/queue.go'):
            with self.subTest(path=path):
                self.assertTrue(needs_platform([path]))

    def test_anything_not_plain_go_or_docs_needs_it(self):
        for path in ('.github/workflows/test.yml', 'go.mod', 'go.sum', 'internal/app/testdata/x.json',
                     'scripts/ci_shard.py', 'web/src/locales/en-US.json'):
            with self.subTest(path=path):
                self.assertTrue(needs_platform([path]))

    def test_no_files_runs_it(self):
        self.assertTrue(needs_platform([]))

    def test_main_prints_platform(self):
        import io
        from ci_changes import main
        out = io.StringIO()
        main(['internal/app/server.go'], stdout=out)
        self.assertEqual(out.getvalue(), 'go=true\nweb=false\nplatform=false\n')


def tracked(*patterns):
    return subprocess.check_output(['git', 'ls-files', '--', *patterns], cwd=ROOT, text=True).split()


def covered(path, prefixes):
    return any(path == p or (p.endswith('/') and path.startswith(p)) for p in prefixes)


class CrossReadTests(unittest.TestCase):
    """The exceptions are only as good as their list. These scan the sources for a file one side
    reads from ground the other side owns, so a new cross-read fails here instead of letting a pull
    request skip the job that would have caught its break."""

    def scan(self, patterns, literal, base_of=os.path.dirname):
        found = set()
        for path in tracked(*patterns):
            with open(os.path.join(ROOT, path), encoding='utf-8') as f:
                text = f.read()
            for rel in re.findall(literal, text):
                found.add(os.path.normpath(os.path.join(base_of(path), rel)))
            for args in re.findall(r'filepath\.Join\(((?:"[^"]*",\s*)+"[^"]*")', text):
                parts = re.findall(r'"([^"]*)"', args)
                if parts[0] == '..':
                    found.add(os.path.normpath(os.path.join(base_of(path), *parts)))
        return {p for p in found if not p.startswith('..')}

    def test_go_reads_from_web_and_docs_are_listed(self):
        found = self.scan(['*.go'], r'"((?:\.\./)+[^"]+)"')
        # A read of a whole directory (web, docs, web/src) crosses too, though the path alone says both.
        crossing = {p for p in found if kind(p) in ('web', 'docs') or p.split('/')[0] in ('web', 'docs')}
        self.assertTrue(crossing, 'the scan no longer finds the reads it was written for')
        for path in sorted(crossing):
            with self.subTest(path=path):
                self.assertTrue(covered(path, GO_READS) or covered(path + '/', GO_READS), path)

    def test_web_reads_from_go_and_docs_are_listed(self):
        found = self.scan(['web/src/*.ts', 'web/src/*.tsx', 'web/*.ts', 'web/*.js', 'web/*.mjs', 'web/*.cjs'],
                          r"""['"]((?:\.\./)+[^'"]+)['"]""")
        # vitest runs in web/, so a path resolved against process.cwd() starts there.
        found |= self.scan(['web/src/*.ts', 'web/src/*.tsx'], r"""process\.cwd\(\),\s*['"]([^'"]+)['"]""",
                           base_of=lambda _: 'web')
        # A directory outside web/ may hold Go sources or docs whatever its own path says. Vite's
        # output directory (web/vite.config.ts) is written by the build, never read by a test.
        found = {p for p in found if p != 'web' and not p.startswith('web/') and p != 'internal/web/dist'}
        crossing = {p for p in found if kind(p) in ('go', 'docs') or p.split('/')[0] == 'docs'
                    or os.path.isdir(os.path.join(ROOT, p))}
        self.assertTrue(crossing, 'the scan no longer finds the reads it was written for')
        for path in sorted(crossing):
            with self.subTest(path=path):
                self.assertTrue(covered(path, WEB_READS), path)

    def test_listed_reads_exist(self):
        for path in GO_READS + WEB_READS:
            with self.subTest(path=path):
                self.assertTrue(os.path.exists(os.path.join(ROOT, path)), path)


if __name__ == '__main__':
    unittest.main()
