"""Decide which of test.yml's job groups a pull request needs, from the files it changes.

Only pull requests are classified. A push to main and a manual run always run everything, because
scripts/release_guard.py tags only a commit whose push or manual run of test.yml passed in full.

Each changed file is one of four kinds:
  docs  docs/** and Markdown at the repository root: neither the Go nor the web jobs read them
  web   web/**: the web jobs only
  go    *.go: the Go jobs only
  both  anything else (workflows, scripts, fixtures, testdata, go.mod, Dockerfiles, deploy/, ...)
A file one side reads from the other side's ground is "both" whatever its place: the lists below
name them, and scripts/ci_changes_test.py scans the sources so that a new such read fails there.
An empty list of files runs everything: it means the diff said nothing, not that nothing changed.

A third output, platform, says whether a change touches the app platform's ground. It once gated the
pull request race lane for the platform; that lane is gone (the race detector runs on main only), so
nothing reads the output now, and it is kept because test.yml's changes job still publishes it.

Usage: python3 scripts/ci_changes.py [FILE ...]  (or the names on stdin); prints go=..., web=... and
platform=... lines for $GITHUB_OUTPUT.
"""
import sys

# Files under web/ or docs/ that Go tests read.
GO_READS = (
    'web/src/locales/',                             # internal/app/audit_gaps_test.go
    'web/src/api/types.ts',                         # internal/app/forecast_ops_test.go
    'docs/app-platform/183-machine-read-api.md',    # internal/app/app_platform_machine_token_schema_test.go
    'web/src/lib/operation-stages.generated.json',  # internal/appplatform/operation_stages_contract_test.go
)
# Go sources that web tests read.
WEB_READS = (
    'internal/app/export_sources.go',               # web/src/lib/exportFilter.test.ts
    'internal/appplatform/operation.go',            # web/src/pages/manage/operationSteps.test.ts (via the generated stage contract)
)

# Go sources the platform lane covers: the platform itself, and the shared core its tests stand on
# (the store and its statement cache, schema checks and migrations, the keyring and secret box its
# credentials are sealed with, the sessions its viewer requests check, the run queue).
PLATFORM_GO = (
    'internal/app/app_platform', 'internal/app/apps_', 'internal/apprunner/', 'internal/appplatform/',
    'cmd/app-runner/', 'cmd/app-task-worker/', 'cmd/app-inspect/',
    'internal/app/store', 'internal/app/migrate', 'internal/app/schema_catalog', 'internal/app/keyring',
    'internal/app/secretbox', 'internal/app/sessions', 'internal/queue/',
)


def _listed(path, prefixes):
    return any(path == p or (p.endswith('/') and path.startswith(p)) for p in prefixes)


def kind(path):
    """The kind of one path, before the cross-read exceptions."""
    if path.startswith('docs/') or ('/' not in path and path.endswith('.md')):
        return 'docs'
    if path.startswith('web/'):
        return 'web'
    if path.endswith('.go'):
        return 'go'
    return 'both'


def classify(paths):
    """{'go': bool, 'web': bool}: which job groups the changed paths need."""
    paths = [p for p in paths if p]
    if not paths:
        return {'go': True, 'web': True}
    need = {'go': False, 'web': False}
    for path in paths:
        k = kind(path)
        if _listed(path, GO_READS) or _listed(path, WEB_READS):
            k = 'both'
        if k in ('go', 'both'):
            need['go'] = True
        if k in ('web', 'both'):
            need['web'] = True
    return need


def needs_platform(paths):
    """Whether the changed paths touch the app platform's ground (no job reads this now)."""
    paths = [p for p in paths if p]
    if not paths:
        return True
    for path in paths:
        k = kind(path)
        if _listed(path, GO_READS) or _listed(path, WEB_READS):
            k = 'both'
        if k == 'both' or (k == 'go' and path.startswith(PLATFORM_GO)):
            return True
    return False


def main(argv=None, stdin=sys.stdin, stdout=sys.stdout):
    argv = sys.argv[1:] if argv is None else argv
    paths = argv or [line.strip() for line in stdin]
    for key, value in classify(paths).items():
        stdout.write(f'{key}={"true" if value else "false"}\n')
    stdout.write(f'platform={"true" if needs_platform(paths) else "false"}\n')


if __name__ == '__main__':
    main()
