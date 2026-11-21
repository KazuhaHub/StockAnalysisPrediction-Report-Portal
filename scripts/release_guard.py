"""Fail-closed release checks. All network reads must succeed before publication."""
import argparse
import json
import os
import re
import subprocess
import time


def gh_json(endpoint):
    return json.loads(subprocess.check_output(['gh', 'api', endpoint], text=True))


def image_digest(image):
    result = subprocess.run(['docker', 'buildx', 'imagetools', 'inspect', image],
                            capture_output=True, text=True, timeout=120)
    if result.returncode:
        # Authentication, transport and rate-limit failures are never evidence of absence.
        if re.search(r'manifest unknown|: not found(?:\s|$)', result.stderr, re.I) and not re.search(
                r'unauthorized|denied|timeout|429|403', result.stderr, re.I):
            return None
        raise RuntimeError(f'Cannot inspect {image}: {result.stderr}')
    match = re.search(r'^Digest:\s+(sha256:[a-f0-9]{64})\s*$', result.stdout, re.M)
    if not match:
        raise RuntimeError(f'Registry returned no digest for {image}')
    return match[1]


DIGEST = re.compile(r'sha256:[a-f0-9]{64}')

# The app-runner image sits beside the Portal image and takes its name from it (doc 187 section 3): the same
# repository path with this suffix, and exactly the Portal image's tags.
RUNNER_IMAGE_SUFFIX = '-app-runner'
IMAGE_KEYS = ('portal', 'app-runner')


def image_for(key, portal_image):
    """The registry path of one image of a release, from the Portal image's."""
    if key == 'portal':
        return portal_image
    if key == 'app-runner':
        return portal_image + RUNNER_IMAGE_SUFFIX
    raise ValueError(f'Unknown image key {key!r}')


def recorded_images(meta, image):
    """The images a release recorded, as {key: (path, digest)}, each entry checked.

    A release from before the runner image has only the legacy top-level image and digest, and is a Portal-only
    release. A release that declares an `images` map is a release with a runner image, and the map must then
    carry both entries, each with the path its key implies and a valid digest, and its Portal entry must be the
    legacy one, because that is what older readers verify.
    """
    if 'images' not in meta:
        return {'portal': (image, meta['digest'])}
    images = meta['images']
    if not isinstance(images, dict) or set(images) != set(IMAGE_KEYS):
        raise ValueError('Release metadata images must name exactly the portal and app-runner images')
    found = {}
    for key in IMAGE_KEYS:
        entry = images[key]
        if not isinstance(entry, dict) or set(entry) != {'image', 'digest'}:
            raise ValueError(f'Release metadata has a malformed {key} image entry')
        if entry['image'] != image_for(key, image):
            raise ValueError(f'Release metadata {key} image does not match the repository')
        if not isinstance(entry['digest'], str) or not DIGEST.fullmatch(entry['digest']):
            raise ValueError(f'Release metadata has no valid {key} image digest')
        found[key] = (entry['image'], entry['digest'])
    if found['portal'] != (image, meta['digest']):
        raise ValueError('Release metadata Portal entry differs from its legacy image and digest')
    return found


def validate_metadata(meta, tag, image, commit):
    """Check a release's metadata and return the Portal digest, as every reader before the runner image did."""
    if any(meta.get(k) != v for k, v in [('tag', tag), ('image', image), ('commit', commit)]):
        raise ValueError('Release metadata does not match tag, repository and commit')
    if not isinstance(meta.get('digest'), str) or not DIGEST.fullmatch(meta['digest']):
        raise ValueError('Release metadata has no valid image digest')
    recorded_images(meta, image)
    return meta['digest']


def ci_ready(jobs):
    required = {'go-test', 'go test (checks + other packages)', 'web typecheck + build', 'web lint + typecheck + build', 'workflow lint',
                'docker image (font gate + smoke)', 'go race full (other packages)',
                'release targets (cross-compile)'}
    required.update(f'go race full ({i}/4)' for i in range(1, 5))
    required.update(f'go test ({i}/4)' for i in range(1, 5))
    required.update(f'web tests ({i}/3)' for i in range(1, 4))
    passed = {j['name'] for j in jobs if j.get('conclusion') == 'success'}
    return required <= passed


LICENCE_JOB = 'licence scan'


def licence_ready(jobs):
    """licence.yml's one job, by display name, is green: the SBOM licence gate of doc 167 section 8."""
    return any(j['name'] == LICENCE_JOB and j.get('conclusion') == 'success' for j in jobs)


def wait_workflow(repo, sha, workflow, ready, label):
    deadline = time.monotonic() + 1800
    while time.monotonic() < deadline:
        runs = gh_json(f'repos/{repo}/actions/workflows/{workflow}/runs?head_sha={sha}&per_page=100')['workflow_runs']
        runs = [r for r in runs if r['event'] in ('push', 'workflow_dispatch')]
        if not runs:
            raise RuntimeError(f'No {label} run for this commit; dispatch {workflow} on its branch first')
        run = max(runs, key=lambda r: r['id'])
        if run['status'] == 'completed':
            jobs = gh_json(f'repos/{repo}/actions/runs/{run["id"]}/jobs?per_page=100')['jobs']
            if run['conclusion'] != 'success' or not ready(jobs):
                raise RuntimeError(f'{label[0].upper()}{label[1:]} did not pass: {run["html_url"]}')
            print(f'Verified {label}: {run["html_url"]}')
            return
        print(f'Waiting for {label}: {run["html_url"]}', flush=True)
        time.sleep(15)
    raise RuntimeError(f'Timed out waiting for {label}')


def wait_ci(repo, sha):
    wait_workflow(repo, sha, 'test.yml', ci_ready, 'full CI')
    # A separate workflow, so the test.yml job names above stay the whole of ci_ready. It runs on every
    # push to main, so the exact commit being released has a run.
    wait_workflow(repo, sha, 'licence.yml', licence_ready, 'licence scan')


def expected_assets(tag):
    return {f'report-portal_{tag}_{platform}.{ext}' for platform, ext in (
        ('linux_amd64', 'tar.gz'), ('linux_arm64', 'tar.gz'),
        ('darwin_amd64', 'tar.gz'), ('darwin_arm64', 'tar.gz'),
        ('windows_amd64', 'zip'), ('windows_arm64', 'zip'))} | {'SHA256SUMS.txt', 'release-metadata.json'}


def read_state(repo):
    pages = json.loads(subprocess.check_output(
        ['gh', 'api', f'repos/{repo}/actions/variables?per_page=100', '--paginate', '--slurp'], text=True))
    variables = {v['name']: v['value'] for page in pages for v in page['variables']}
    for output, name in [('current_latest', 'CHANNEL_LATEST_TAG'), ('current_beta', 'CHANNEL_BETA_TAG'),
                         ('latest_override', 'CHANNEL_LATEST_OVERRIDE'), ('beta_override', 'CHANNEL_BETA_OVERRIDE')]:
        value = variables.get(name, '')
        if '\n' in value or '\r' in value:
            raise ValueError('Channel state must be a single line')
        print(f'{output}={value}')


def guard_draft(repo, tag):
    # Listing distinguishes a missing release from all transport/authentication failures.
    pages = json.loads(subprocess.check_output(
        ['gh', 'api', f'repos/{repo}/releases?per_page=100', '--paginate', '--slurp'], text=True))
    release = next((r for page in pages for r in page if r['tag_name'] == tag), None)
    if release is not None and not release['draft']:
        raise RuntimeError('Release is already published; cut a new version')


def verify_target(repo, tag, image, key='portal'):
    """Verify a release is promotable and return the digest recorded for `key`.

    Every image the release recorded is checked, whichever key is asked for: a fixed tag that no longer matches
    its recorded digest refuses the whole release, so neither channel tag can move to half a release. `key` is
    'portal', 'app-runner' or 'all'. For one key the result is its digest, or '' when a Portal-only release
    recorded none; for 'all' it is one `<key> <digest>` line per recorded image.
    """
    if key not in IMAGE_KEYS + ('all',):
        raise ValueError(f'Unknown image key {key!r}')
    release = gh_json(f'repos/{repo}/releases/tags/{tag}')
    if release['draft']:
        raise RuntimeError('Cannot promote a draft release')
    if not expected_assets(tag) <= {a['name'] for a in release['assets']}:
        raise RuntimeError('Release assets are incomplete')
    ref = gh_json(f'repos/{repo}/git/ref/tags/{tag}')['object']
    if ref['type'] != 'tag':
        raise RuntimeError('Release tag must be annotated')
    commit = gh_json(f'repos/{repo}/git/tags/{ref["sha"]}')['object']
    if commit['type'] != 'commit':
        raise RuntimeError('Release tag must point directly to a commit')
    asset = next(a for a in release['assets'] if a['name'] == 'release-metadata.json')
    raw = subprocess.check_output(['gh', 'api', '-H', 'Accept: application/octet-stream',
                                   f'repos/{repo}/releases/assets/{asset["id"]}'])
    meta = json.loads(raw)
    validate_metadata(meta, tag, image, commit['sha'])
    images = recorded_images(meta, image)
    for name, (path, digest) in images.items():
        if image_digest(f'{path}:{tag}') != digest:
            raise RuntimeError(f'Fixed {name} image tag no longer matches recorded digest')
    if key == 'all':
        return '\n'.join(f'{name} {images[name][1]}' for name in IMAGE_KEYS if name in images)
    return images[key][1] if key in images else ''


def guard_fixed_image(ref):
    """Refuse when a fixed image tag already exists: it is never rebuilt or replaced.

    The message depends on what is published. A release with a runner image is never recovered as Portal-only
    (that would drop the runner image from its metadata), so its message points at re-running the failed jobs of
    the original run, which read the images by digest. Only a Portal-only tag points at the recovery script.
    """
    if image_digest(ref) is None:
        return
    path, _, tag = ref.rpartition(':')
    portal = path[:-len(RUNNER_IMAGE_SUFFIX)] if path.endswith(RUNNER_IMAGE_SUFFIX) else path
    has_runner = path.endswith(RUNNER_IMAGE_SUFFIX) or image_digest(f'{image_for("app-runner", portal)}:{tag}') is not None
    if has_runner:
        raise RuntimeError('Fixed image already exists, and a runner image exists for this tag; do not rebuild or '
                           'replace draft assets. Re-run the failed jobs of the original run (they read both images by '
                           'digest); scripts/recover-release-metadata.py refuses a release that has a runner image.')
    raise RuntimeError('Fixed image already exists; do not rebuild or replace draft assets. '
                       'Use scripts/recover-release-metadata.py to verify and recover metadata.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['guard', 'wait-ci', 'verify-target', 'state', 'guard-draft'])
    parser.add_argument('value', nargs='?')
    parser.add_argument('--repo', default=os.environ.get('GITHUB_REPOSITORY'))
    parser.add_argument('--image')
    parser.add_argument('--image-key', choices=IMAGE_KEYS + ('all',), default='portal',
                        help='verify-target: which recorded digest to print (every recorded image is verified)')
    args = parser.parse_args()
    if args.command != 'state' and not args.value:
        parser.error('value is required for this command')
    if args.command == 'guard':
        guard_fixed_image(args.value)
    elif args.command == 'state':
        read_state(args.repo)
    elif args.command == 'guard-draft':
        guard_draft(args.repo, args.value)
    elif args.command == 'wait-ci':
        wait_ci(args.repo, args.value)
    else:
        print(verify_target(args.repo, args.value, args.image, args.image_key))


if __name__ == '__main__':
    main()
