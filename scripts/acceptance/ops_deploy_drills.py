#!/usr/bin/env python3
"""OPS-05 and OPS-06 deployment drills on a disposable CI host.

The owner decided on 2026-11-15 that no real server is touched, so both drills run on a GitHub-hosted
runner against a Docker Compose stack in the shape of the beta deployment (docker-compose.yml with
only the report-portal image reference replaced by a pinned tag).

  OPS-05  deploy the newest published release image and verify that the version reference in the
          compose file is unchanged and that the loaded digest and version are what was released.
  OPS-06  take a protected recovery set with the released build, force a failed rollout, and restore
          the set, proving that compatibility and key recovery are checked before the prior version
          is activated again and that data and keys are intact afterwards.

The decision logic (release selection, compose rendering, digest and ledger checks, the evidence
verdicts and the activation-ordering proof) is plain functions with unit tests in
scripts/ops_deploy_drills_test.py. The Docker host is one class, DockerHost; the recovery sequence
talks to it through a narrow interface so the tests can prove its ordering with a fake.

Nothing here pushes to ghcr.io, reads a secret, or touches a server: the only registry written to is a
loopback registry this job starts, and every credential is a throwaway value generated for the drill.
"""
import argparse
import base64
import datetime
import hashlib
import hmac
import json
import os
import re
import secrets
import shutil
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.request

REPOSITORY = "KazuhaHub/StockAnalysisPrediction-Report-Portal"
IMAGE = "ghcr.io/kazuhahub/stockanalysisprediction-report-portal"
EVIDENCE_FORMAT = "portal.ops-deployment-drills/1-dev.1"
PROJECT = "opsdrill"
LIVE_DB = "reports"
ROLLBACK_COPY_DB = "reports_rollback_probe"
FAILED_DB = "reports_failed_rollout"
REHEARSAL_DB = "reports_rehearsal"
LEDGER_PREFIX = "mig:"
KEYRING_KEYS = ("keyring_salt", "keyring_wrapped_dek")
ACCEPTED_EVENTS = ("workflow_dispatch",)

SHA256_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
CALVER_RE = re.compile(r"^v(\d{4})\.(\d{1,2})(?:\.(\d+))?$")
# The report-portal image line only. The compose file also names the app-runner image (doc 187), on the same
# repository path with -app-runner, and that image is another service behind a profile: the drills never pin or
# start it.
IMAGE_LINE_RE = re.compile(r"^(\s*image:\s*)(ghcr\.io/(?!\S*-app-runner[:@\s])\S+)\s*$")


# ------------------------------------------------------------------ release selection

def calver_key(tag):
    """The numeric (year, week, revision) of a vYYYY.W[.R] tag, or None. A missing revision is 0 and a
    different number from .1 (ADR 0034); comparison is never lexical."""
    match = CALVER_RE.match(tag or "")
    if not match:
        return None
    year, week, revision = match.groups()
    return int(year), int(week), int(revision or 0)


def parse_release_metadata(text, tag):
    """The release-metadata.json the release workflow records on every release: tag, commit, image and
    the digest that was pushed. It is the independent statement of what was released."""
    meta = json.loads(text)
    for field in ("tag", "commit", "image", "digest"):
        if not isinstance(meta.get(field), str) or not meta[field]:
            raise ValueError(f"release metadata lacks {field}")
    if meta["tag"] != tag:
        raise ValueError(f"release metadata is for {meta['tag']}, not {tag}")
    if not SHA256_RE.match(meta["digest"]):
        raise ValueError("release metadata digest is not a sha256 digest")
    if not re.fullmatch(r"[0-9a-f]{40}", meta["commit"]):
        raise ValueError("release metadata commit is not a full SHA")
    return meta


def parse_cli_version(text):
    """(version, commit) from `report-portal version`, which prints `YYYY.W[.R] (commit, build date)`."""
    match = re.match(r"^\s*(\S+) \(([0-9a-f]+|none),", text or "")
    return (match.group(1), match.group(2)) if match else ("", "")


def manifest_digest(imagetools_json):
    """The digest of the manifest (or index) a tag resolves to, from `buildx imagetools inspect
    --format '{{json .Manifest}}'`."""
    digest = json.loads(imagetools_json).get("digest", "")
    if not SHA256_RE.match(digest):
        raise ValueError("imagetools output carries no sha256 digest")
    return digest


# ------------------------------------------------------------------ compose

# The Portal image is named by the report-portal service and by the app-task-worker service, which runs the same
# image and must move with it.
PORTAL_IMAGE_SERVICES = ("app-task-worker", "report-portal")


def compose_image_ref(text):
    """The report-portal image reference in a compose file. One ghcr.io image line per Portal-image service
    (report-portal and app-task-worker) must exist, and they must name the same reference."""
    refs = [m.group(2) for m in (IMAGE_LINE_RE.match(line) for line in text.splitlines()) if m]
    if len(refs) != len(PORTAL_IMAGE_SERVICES) or len(set(refs)) != 1:
        raise ValueError(f"expected {len(PORTAL_IMAGE_SERVICES)} identical ghcr.io image lines in the compose file, found {refs}")
    return refs[0]


def render_pinned_compose(template, ref):
    """The repository's docker-compose.yml with its report-portal image reference replaced by ref. Nothing
    else changes: the result differs from the template by exactly one line (the image line)."""
    compose_image_ref(template)
    out = []
    for line in template.splitlines(keepends=True):
        match = IMAGE_LINE_RE.match(line.rstrip("\r\n"))
        if match:
            ending = line[len(line.rstrip("\r\n")):]
            line = f"{match.group(1)}{ref}{ending}"
        out.append(line)
    return "".join(out)


def changed_line_numbers(a, b):
    """1-based numbers of the lines that differ between two texts. When the line counts differ the lines past
    the shorter text count as changed."""
    la, lb = a.splitlines(), b.splitlines()
    n = max(len(la), len(lb))
    return [i + 1 for i in range(n) if (la[i] if i < len(la) else None) != (lb[i] if i < len(lb) else None)]


def image_line_numbers(text):
    """1-based numbers of the report-portal image lines (the only lines the procedure rewrites)."""
    compose_image_ref(text)
    return [number for number, line in enumerate(text.splitlines(), 1) if IMAGE_LINE_RE.match(line)]


def config_diff_paths(a, b, prefix=""):
    """Dotted paths at which two parsed `docker compose config` documents differ, sorted. A list is compared
    whole, so a changed list is reported at the list's own path."""
    if isinstance(a, dict) and isinstance(b, dict):
        paths = []
        for key in sorted(set(a) | set(b)):
            here = f"{prefix}.{key}" if prefix else str(key)
            if key not in a or key not in b:
                paths.append(here)
            else:
                paths.extend(config_diff_paths(a[key], b[key], here))
        return sorted(paths)
    return [] if a == b else [prefix]


def sha256_hex(data):
    if isinstance(data, str):
        data = data.encode()
    return hashlib.sha256(data).hexdigest()


# ------------------------------------------------------------------ ledger and fingerprints

def parse_ledger(psql_output):
    """The migration ids from `SELECT k FROM meta WHERE k LIKE 'mig:%'` output, sorted."""
    ids = []
    for line in psql_output.splitlines():
        line = line.strip()
        if line.startswith(LEDGER_PREFIX):
            ids.append(line[len(LEDGER_PREFIX):])
    return sorted(ids)


def ledger_delta(before, after):
    """Steps recorded after the rollout that were not recorded before it."""
    return sorted(set(after) - set(before))


def refusal_names_steps(log, steps):
    """True when a start log shows the build refusing a ledger it does not know and names every step."""
    return bool(steps) and "which this build does not have" in log and all(step in log for step in steps)


def fingerprint(secret):
    """A short, non-reversible fingerprint of a 256-bit key, so evidence can say two keys are the same
    without containing either."""
    return hashlib.sha256(secret.encode()).hexdigest()[:16]


def parse_secret_key(config_text):
    match = re.search(r"""^\s*secret_key\s*:\s*["']?([^"'#\s]+)""", config_text, re.M)
    return match.group(1) if match else ""


def keyring_fingerprint(psql_output):
    """Fingerprint of the stored keyring rows from `SELECT k, v ... ORDER BY k` (pipe separated)."""
    rows = sorted(line.strip() for line in psql_output.splitlines() if line.strip())
    if len(rows) != len(KEYRING_KEYS) or not all(r.startswith(k) for r, k in zip(rows, sorted(KEYRING_KEYS))):
        raise ValueError("the keyring rows are not both present")
    return sha256_hex("\n".join(rows))[:16]


_REDACTIONS = [
    re.compile(r"(password:\s*)\S+", re.I),
    re.compile(r"(secret_key[\"']?\s*[:=]\s*[\"']?)[0-9a-zA-Z]{16,}", re.I),
    re.compile(r"(Bearer\s+)\S+", re.I),
]


def redact(text):
    """Logs go into the evidence artifact; the first-run admin password and any key never do."""
    for pattern in _REDACTIONS:
        text = pattern.sub(lambda m: m.group(1) + "[redacted]", text)
    return text


# ------------------------------------------------------------------ TOTP

def totp_code(secret, step):
    key = base64.b32decode(secret + "=" * ((8 - len(secret) % 8) % 8))
    digest = hmac.new(key, struct.pack(">Q", step), hashlib.sha1).digest()
    offset = digest[19] & 15
    return "%06d" % ((struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7FFFFFFF) % 1000000)


def pick_totp_step(used, now=None, sleep=time.sleep, clock=time.time):
    """A current or next 30-second step the portal has not accepted from this account, waiting for the next
    window when both are spent (a replayed step is refused)."""
    for _ in range(3):
        moment = clock() if now is None else now
        current = int(moment) // 30
        for step in (current, current + 1):
            if step not in used:
                return step
        if now is not None:
            break
        sleep(30 - (moment % 30) + 1)
    raise RuntimeError("no unused TOTP step available")


# ------------------------------------------------------------------ portal client and gate

class Portal:
    """A cookie-holding client for the portal's browser API, one per user."""

    def __init__(self, base, timeout=20):
        self.base, self.timeout, self.cookie = base.rstrip("/"), timeout, None

    def req(self, method, path, body=None, token=None, headers=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(self.base + path, data=data, method=method)
        if data is not None:
            request.add_header("Content-Type", "application/json")
        if self.cookie:
            request.add_header("Cookie", self.cookie)
        if token:
            request.add_header("Authorization", "Bearer " + token)
        for key, value in (headers or {}).items():
            request.add_header(key, value)
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                status, raw = response.getcode(), response.read().decode()
                for name, value in response.getheaders():
                    if name.lower() == "set-cookie" and "rp_session=" in value:
                        self.cookie = value.split(";")[0]
        except urllib.error.HTTPError as err:
            status, raw = err.code, err.read().decode()
            err.close()
        except (urllib.error.URLError, OSError) as err:
            return 0, {"_error": str(err)}
        try:
            return status, json.loads(raw) if raw else {}
        except json.JSONDecodeError:
            return status, {"_raw": raw[:200]}

    def login(self, user, password):
        return self.req("POST", "/api/login", {"username": user, "password": password})

    def login_totp(self, user, password, secret, used):
        """Both legs of a two-factor login. Returns (status, body, step) where step is the TOTP step
        spent, so the caller can avoid replaying it."""
        status, first = self.login(user, password)
        if status != 200 or not first.get("totp_required"):
            return status, first, None
        step = pick_totp_step(used)
        status, body = self.req("POST", "/api/login/2fa", {"token": first.get("token"), "code": totp_code(secret, step)})
        return status, body, step


class Fixtures:
    """Throwaway accounts and data the drill seeds through the product, and what it expects back."""

    def __init__(self, admin_user, admin_password, totp_user, totp_password, readers, token="", report_id=0,
                 marker="", totp_secret="", used_steps=None):
        self.admin_user, self.admin_password = admin_user, admin_password
        self.totp_user, self.totp_password = totp_user, totp_password
        self.readers = list(readers)
        self.token, self.report_id, self.marker, self.totp_secret = token, report_id, marker, totp_secret
        self.used_steps = set(used_steps or ())

    @classmethod
    def generate(cls, run_id):
        suffix = secrets.token_hex(3)
        return cls("drill-admin", "Adm-" + secrets.token_hex(8), "drill-totp", "Tot-" + secrets.token_hex(8),
                   [f"drill-reader-{i}" for i in (1, 2)], marker=f"ops-drill-{run_id}-{suffix}")

    def usernames(self):
        return sorted([self.admin_user, self.totp_user, *self.readers, "admin"])


def check_result(name, passed, detail="", **extra):
    return {"id": name, "passed": bool(passed), "detail": detail, **extra}


def run_gate(base, fx, expect_version=None, expect_commit_prefix=None):
    """The activation gate: every check runs, so a failing gate names exactly what is wrong.

    healthz        the process answers and its database is reachable
    admin_login    the password-only drill admin signs in
    version        /api/version reports the expected build (session gated, hence the admin)
    users          every fixture account survived
    report         the ingested report is readable with the minted API token
    totp_login     the two-factor account signs in with a code from its enrolled secret, which is only
                   possible when the stored keyring opens under the secret_key in force (key recovery)
    """
    checks = []
    status, _ = Portal(base).req("GET", "/healthz")
    checks.append(check_result("healthz", status == 200, f"status {status}"))

    admin = Portal(base)
    status, body = admin.login(fx.admin_user, fx.admin_password)
    admin_ok = status == 200 and not body.get("totp_required")
    checks.append(check_result("admin_login", admin_ok, f"status {status}"))

    if admin_ok:
        status, body = admin.req("GET", "/api/version")
        wanted = expect_version is None or body.get("version") == expect_version
        if expect_commit_prefix:
            wanted = wanted and str(body.get("commit", "")).startswith(expect_commit_prefix)
        checks.append(check_result("version", status == 200 and wanted,
                                   f"status {status} version={body.get('version')} commit={body.get('commit')}"))
        status, body = admin.req("GET", "/api/admin/users")
        names = {u.get("username") for u in body.get("users", [])} if status == 200 else set()
        missing = sorted(set(fx.usernames()) - names)
        checks.append(check_result("users", status == 200 and not missing, f"missing {missing}"))
    else:
        checks.append(check_result("version", False, "no admin session"))
        checks.append(check_result("users", False, "no admin session"))

    status, body = Portal(base).req("GET", f"/api/v1/reports/{fx.report_id}", token=fx.token)
    checks.append(check_result("report", status == 200 and fx.marker in json.dumps(body, ensure_ascii=False),
                               f"status {status}"))

    totp = Portal(base)
    status, body, step = totp.login_totp(fx.totp_user, fx.totp_password, fx.totp_secret, fx.used_steps)
    if step is not None and status == 200:
        fx.used_steps.add(step)  # only an accepted code is spent; a refused one was never consumed
    if status == 200:
        status, me = totp.req("GET", "/api/me")
        signed_in = status == 200 and me.get("user") == fx.totp_user
    else:
        signed_in = False
    code = body.get("code", "") if isinstance(body, dict) else ""
    checks.append(check_result("totp_login", signed_in, f"status {status} code={code}", status=status, code=code))
    return checks


def failed_checks(checks):
    return sorted(c["id"] for c in checks if not c["passed"])


def failed_for(checks, check_id, status, code):
    """True when the named check failed with exactly this HTTP status and error code, so a control that is
    meant to fail for one reason is not satisfied by an unrelated failure (an unreachable portal is status 0)."""
    return any(c["id"] == check_id and not c["passed"] and c.get("status") == status and c.get("code") == code
               for c in checks)


# ------------------------------------------------------------------ events and ordering proof

class Recorder:
    """Ordered, timestamped facts. The ordering proof reads these, so every gate and every mutation of
    the live stack is recorded as it happens, not reconstructed."""

    def __init__(self, clock=time.time):
        self.events, self._clock = [], clock

    def event(self, kind, ok=None, **detail):
        item = {"seq": len(self.events) + 1,
                "at": datetime.datetime.fromtimestamp(self._clock(), datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
                "kind": kind}
        if ok is not None:
            item["ok"] = bool(ok)
        item.update(detail)
        self.events.append(item)
        return item

    def first(self, kind):
        return next((e for e in self.events if e["kind"] == kind), None)


PRE_ACTIVATION_CHECKS = ("recovery_set_integrity", "restore_dry_run_compat", "rehearsal_key_gate")
LIVE_MUTATIONS = ("live_db_replaced", "live_restore", "activation")


def ordering_proof(events):
    """A record of one run, not the enforcement: compatibility and key recovery were checked before
    activation iff every pre-activation check passed and came before the first mutation of the live stack
    that restores the prior version, and the activation itself came out healthy. What makes the order
    impossible to get wrong is recovery_sequence, which the FakeHost tests pin; this reads the events one run
    actually produced. Returns (passed, explanation)."""
    first_mutation = min((e["seq"] for e in events if e["kind"] in LIVE_MUTATIONS), default=None)
    if first_mutation is None:
        return False, "the restore never reached the live stack"
    for kind in PRE_ACTIVATION_CHECKS:
        found = [e for e in events if e["kind"] == kind and e.get("ok") and e["seq"] < first_mutation]
        if not found:
            return False, f"{kind} did not pass before the live stack was changed"
    activation = next((e for e in events if e["kind"] == "activation"), None)
    if activation is None:
        return False, "the prior version was never activated"
    if not activation.get("ok"):
        return False, "the prior version was activated but did not become healthy"
    return True, f"all {len(PRE_ACTIVATION_CHECKS)} checks passed before event {first_mutation} and activation (event {activation['seq']}) was healthy"


# ------------------------------------------------------------------ recovery sequence

class RecoveryRefused(Exception):
    """A pre-activation check refused the recovery set; nothing live was changed."""


class RecoveryFailed(Exception):
    """A live step failed after the checks passed; the later steps did not run."""


def recovery_sequence(host, rec):
    """Restore the protected recovery set and reactivate the prior version.

    Every check that can refuse the set runs, and is recorded, before the live stack is touched:
    integrity of the set's files, a dry-run restore of the dump by the prior build (compatibility: the
    dump's migration level, schema and columns must be ones that build accepts), and a rehearsal of the
    whole restore into a scratch database with the recovered config, which signs the fixture
    two-factor account in (the keyring opens only under the recovered secret_key). Only then is the
    live database replaced and the prior version activated. Each live step reports (ok, detail) and
    is recorded with that real outcome; a failed step stops the sequence.
    """
    for kind, check, refusal in (("recovery_set_integrity", host.verify_set_integrity, "recovery set integrity"),
                                 ("restore_dry_run_compat", host.dry_run_restore, "restore dry run"),
                                 ("rehearsal_key_gate", host.rehearse_restore, "rehearsal gate")):
        ok, detail = check()
        rec.event(kind, ok, **detail)
        if not ok:
            raise RecoveryRefused(refusal)
    for kind, step in (("live_db_replaced", host.replace_live_database), ("live_restore", host.restore_live),
                       ("activation", host.activate_prior)):
        ok, detail = step()
        rec.event(kind, ok, **detail)
        if not ok:
            raise RecoveryFailed(kind)
    gate = host.final_gate()
    rec.event("final_gate", not failed_checks(gate), failed=failed_checks(gate))
    return gate


def verify_recovery_set(directory, manifest):
    """Integrity of a recovery set against its manifest: the file digests, and the secret_key fingerprint of
    the config file. The fingerprint is read whenever the config is readable, so a damaged dump never makes
    the key look wrong (and a wrong key is caught even when its file hash was rewritten)."""
    detail, ok = {}, True
    for part in ("dump", "config", "compose"):
        entry = manifest[part]
        try:
            with open(os.path.join(directory, entry["file"]), "rb") as f:
                actual = sha256_hex(f.read())
        except OSError:
            actual = "unreadable"
        same = actual == entry["sha256"]
        detail[part] = "match" if same else "MISMATCH"
        ok = ok and same
    try:
        with open(os.path.join(directory, manifest["config"]["file"]), encoding="utf-8") as f:
            key_same = fingerprint(parse_secret_key(f.read())) == manifest["config"]["secret_key_fp"]
    except OSError:
        key_same = False
    detail["secret_key_fp"] = "match" if key_same else "MISMATCH"
    return ok and key_same, detail


# ------------------------------------------------------------------ evidence

def evidence_blockers(env):
    """Why a run does not count as evidence: empty only for a workflow_dispatch run of this repository on the
    commit the dispatcher named (expected_commit is required, as OPS-08 does), with a pinned prior release."""
    blockers = []
    if env.get("GITHUB_ACTIONS") != "true":
        blockers.append("not a GitHub Actions run")
    if env.get("GITHUB_REPOSITORY") != REPOSITORY:
        blockers.append("repository is not " + REPOSITORY)
    event = env.get("GITHUB_EVENT_NAME", "")
    if event not in ACCEPTED_EVENTS:
        blockers.append(f"the event was {event or 'unknown'}, not {' or '.join(ACCEPTED_EVENTS)}; a push run is not evidence")
    sha = env.get("GITHUB_SHA", "")
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        blockers.append("no full commit SHA")
    expected = env.get("OPS_DRILL_EXPECTED_COMMIT", "")
    if event == "workflow_dispatch" and not expected:
        blockers.append("expected_commit is empty; a dispatch run must name the commit it measures, as OPS-08 does")
    if expected and not re.fullmatch(r"[0-9a-f]{40}", expected):
        blockers.append("expected_commit is not a full lower-case commit SHA")
    elif expected and expected != sha:
        blockers.append(f"expected_commit {expected[:12]} differs from the commit that ran {sha[:12]}")
    prior = env.get("OPS_DRILL_PRIOR_TAG", "")
    if prior and calver_key(prior) is None:
        blockers.append("the prior release tag is not a CalVer tag")
    return blockers


def run_block(env):
    server, repo, run = env.get("GITHUB_SERVER_URL", ""), env.get("GITHUB_REPOSITORY", ""), env.get("GITHUB_RUN_ID", "")
    blockers = evidence_blockers(env)
    return {"event": env.get("GITHUB_EVENT_NAME", ""), "commit": env.get("GITHUB_SHA", ""),
            "expected_commit": env.get("OPS_DRILL_EXPECTED_COMMIT", ""), "prior_release_tag": env.get("OPS_DRILL_PRIOR_TAG", ""),
            "ref": env.get("GITHUB_REF_NAME", ""), "run_id": run, "attempt": env.get("GITHUB_RUN_ATTEMPT", ""),
            "workflow_ref": env.get("GITHUB_WORKFLOW_REF", ""),
            "url": f"{server}/{repo}/actions/runs/{run}" if run else "",
            "evidence_blockers": blockers, "counts_as_evidence": not blockers}


def verdict(checks):
    failed = failed_checks(checks)
    return {"passed": not failed, "result": "passed" if not failed else "failed", "failed_checks": failed}


def _same_commit(full, short):
    return len(short) >= 7 and full.startswith(short)


def ops05_checks(f):
    """OPS-05: the platform candidate built from the commit under test is deployed through the repository
    compose with only the image line changed, and what runs is what was built. Every expected value comes
    from the build record (written before the deploy) or from the registry, never from the deploy itself."""
    build = f["build"]
    c = []
    c.append(check_result("deployed_compose_differs_from_reference_only_in_the_image_line",
                          f["config_diff_paths"] == [f"services.{name}.image" for name in PORTAL_IMAGE_SERVICES], ", ".join(f["config_diff_paths"])))
    c.append(check_result("deployed_compose_text_changes_only_the_image_line",
                          f["changed_lines"] == f["image_line_numbers"], f"lines {f['changed_lines']}"))
    c.append(check_result("container_reference_is_the_built_reference", f["container_config_image"] == build["ref"],
                          f["container_config_image"]))
    c.append(check_result("registry_digest_of_tag_equals_built_digest", f["registry_digest"] == build["digest"],
                          f"{f['registry_digest']} vs {build['digest']}"))
    c.append(check_result("container_image_digest_equals_registry_digest_of_tag", f["registry_digest"] in f["repo_digests"],
                          ", ".join(f["repo_digests"])))
    c.append(check_result("container_runs_the_built_image", f["container_image_id"] == build["image_id"],
                          f["container_image_id"][:19]))
    c.append(check_result("loaded_binary_is_the_built_binary",
                          bool(f["loaded_binary_sha256"]) and f["loaded_binary_sha256"] == build["binary_sha256"],
                          f["loaded_binary_sha256"][:16]))
    c.append(check_result("api_version_equals_built_version", f["api_version"] == build["version"], f["api_version"]))
    c.append(check_result("api_commit_is_the_commit_under_test", _same_commit(build["commit"], f["api_commit"]), f["api_commit"]))
    cli_version, cli_commit = parse_cli_version(f["cli_version"])
    c.append(check_result("cli_version_matches_the_build", cli_version == build["version"] and cli_commit == f["api_commit"],
                          f["cli_version"]))
    c.append(check_result("commit_under_test_is_the_built_commit",
                          f["run_commit"] == build["commit"] and f["expected_commit"] in ("", build["commit"]),
                          f"ran {f['run_commit'][:12]} built {build['commit'][:12]} expected {f['expected_commit'][:12]}"))
    c.append(check_result("healthy", f["healthy"], ""))
    return c


def released_checks(f):
    """The released prior build, deployed from its pinned tag (the baseline of OPS-06) and verified against the
    release workflow's own record of what it published."""
    rec = f["record"]
    c = []
    c.append(check_result("reference_matches_the_release_record", f["image_ref"] == f"{rec['image']}:{rec['tag']}", f["image_ref"]))
    c.append(check_result("registry_digest_equals_release_record", f["registry_digest"] == rec["digest"],
                          f"{f['registry_digest']} vs {rec['digest']}"))
    c.append(check_result("container_image_digest_equals_release_record", rec["digest"] in f["repo_digests"],
                          ", ".join(f["repo_digests"])))
    c.append(check_result("container_runs_the_expected_image", f["container_image_id"] == f["image_id"], f["container_image_id"][:19]))
    c.append(check_result("container_reference_is_the_pinned_tag", f["container_config_image"] == f["image_ref"],
                          f["container_config_image"]))
    c.append(check_result("loaded_binary_is_the_image_binary",
                          bool(f["loaded_binary_sha256"]) and f["loaded_binary_sha256"] == f["image_binary_sha256"],
                          f["loaded_binary_sha256"][:16]))
    c.append(check_result("api_version_equals_release_tag", f["api_version"] == rec["tag"], f["api_version"]))
    c.append(check_result("api_commit_is_the_release_commit", _same_commit(rec["commit"], f["api_commit"]), f["api_commit"]))
    cli_version, cli_commit = parse_cli_version(f["cli_version"])
    c.append(check_result("cli_version_matches_release", cli_version == rec["tag"].lstrip("v") and cli_commit == f["api_commit"],
                          f["cli_version"]))
    c.append(check_result("healthy", f["healthy"], ""))
    return c


def ops06_checks(f):
    """Checks for OPS-06 from the facts of the whole drill."""
    c = []
    c.append(check_result("baseline_gate_passed", not f["baseline_failed"], str(f["baseline_failed"])))
    c.append(check_result("recovery_set_verified_before_rollout", f["precheck_set_ok"], ""))
    c.append(check_result("rollout_applied_new_steps", bool(f["ledger_delta"]), ", ".join(f["ledger_delta"])))
    c.append(check_result("rollout_candidate_digest_verified", f["candidate_digest_ok"], f["candidate_digest"]))
    c.append(check_result("rollout_refused_by_activation_gate",
                          f["rollout_failed"] == ["totp_login"] and f["rollout_healthz"],
                          f"healthy process, failed {f['rollout_failed']}"))
    c.append(check_result("rollout_refused_for_the_key_reason", f["rollout_reason_ok"],
                          "totp_login failed with HTTP 401 bad_totp_code; the same account signed in under the old key"))
    c.append(check_result("prior_build_refuses_a_copy_of_the_migrated_database", f["rollback_refused"], f["rollback_state"]))
    c.append(check_result("rollback_demonstration_leaves_the_live_database_alone", f["rollback_on_copy"],
                          "run against a database copied from the migrated one"))
    c.append(check_result("newer_dump_refused_by_prior_build", f["negative_compat_refused"], f["negative_compat_detail"]))
    c.append(check_result("tampered_set_refused_by_integrity_check", f["negative_integrity_refused"], f["negative_integrity_detail"]))
    c.append(check_result("wrong_key_refused_by_rehearsal_gate", f["negative_key_refused"], f["negative_key_detail"]))
    passed, why = ordering_proof(f["events"])
    c.append(check_result("checks_precede_activation", passed, why))
    c.append(check_result("final_gate_passed", not f["final_failed"], str(f["final_failed"])))
    c.append(check_result("prior_container_is_the_expected_image",
                          f["final_container_image_id"] == f["expected_image_id"], f["final_container_image_id"][:19]))
    c.append(check_result("prior_container_digest_equals_release_record", f["recorded_digest"] in f["final_repo_digests"],
                          ", ".join(f["final_repo_digests"])))
    c.append(check_result("prior_image_unchanged_from_baseline",
                          f["final_container_image_id"] == f["baseline_container_image_id"], f["baseline_container_image_id"][:19]))
    c.append(check_result("prior_version_reported_by_the_portal", f["final_api_version"] == f["prior_tag"], f["final_api_version"]))
    c.append(check_result("version_reference_restored", f["compose_sha_final"] == f["compose_sha_baseline"],
                          f"{f['compose_sha_baseline'][:12]} -> {f['compose_sha_final'][:12]}"))
    c.append(check_result("secret_key_unchanged", f["key_fp_final"] == f["key_fp_baseline"], f["key_fp_final"]))
    c.append(check_result("keyring_unchanged", f["keyring_fp_final"] == f["keyring_fp_baseline"], f["keyring_fp_final"]))
    c.append(check_result("ledger_equals_pre_rollout", f["ledger_final"] == f["ledger_baseline"],
                          f"{len(f['ledger_final'])} steps"))
    c.append(check_result("data_rows_equal_pre_rollout", f["counts_final"] == f["counts_baseline"], json.dumps(f["counts_final"])))
    return c


NOT_PROVEN_COMMON = [
    "No real server, production database or production secret_key is involved: the host is a GitHub-hosted runner and every credential is generated for the drill.",
    "The stack is the repository's docker-compose.yml with only its image line replaced, not the server's beta configuration, and not the production host's reverse proxy, firewall, systemd ordering or shared PostgreSQL.",
]

OPS05_GAPS = [
    "The compose file is the repository's docker-compose.yml, not the server's own beta configuration; the server's `beta` channel reference, its env file and host-level parts are not exercised.",
    "The version-reference rule is unreconciled in the docs: docs 04 P13.5 and 07 section 7 say not to rewrite the server's `beta` channel reference to fixed tags, while the current production procedure is a single compose file with a fixed version tag (in use since v2026.43.5). The drill follows the current procedure and checks that only the image line differs; see the engineering note in doc 167 section 6.",
    "No app runner is deployed; see the runner and recovery-bundle follow-up in docs/app-platform/06-acceptance.md.",
    "The candidate is deployed onto an empty first-boot database, not the database copy that doc 167 section 6 names; its migrations run from the first-boot state of the compose stack.",
    "The candidate is built with Dockerfile.release and the release web bundle for linux/amd64 only and held in a loopback registry; it is not an image published by the release workflow.",
    "Doc 167 section 9 requires OPS-05 passed on staging; this is a CI host, not staging.",
    "A push-triggered run is not evidence; a workflow_dispatch run on the expected commit is still pending.",
]

OPS06_GAPS = [
    "The runtime half is not exercised: runner/Portal protocol compatibility, the runner journal, managed app data and image blobs are not in the recovery set; see the runner and recovery-bundle follow-up in docs/app-platform/06-acceptance.md.",
    "The recovery set is the existing logical dump plus the config directory holding secret_key, not the platform recovery bundle.",
    "The failed rollout is one injected fault (the config directory is not carried across) on top of the schema steps the candidate applies; a build that never starts, an image that cannot be pulled and a failing migration are not drilled.",
    "The stack is the repository's docker-compose.yml, not the server's beta configuration, and the host is a CI runner, not staging (doc 167 section 9).",
    "The two-factor sign-in is the key-recovery oracle: it proves the keyring opens under the recovered secret_key, not that every sealed secret in a real database would. The control fails with HTTP 401 bad_totp_code, which a wrong code also gives; the fixture sign-in succeeding under the old key is what ties it to the key.",
    "The restore goes into a fresh database because the prior build refuses to open a database whose ledger it does not know.",
    "A push-triggered run is not evidence; a workflow_dispatch run on the expected commit is still pending.",
]

OPS05_PROVES = [
    "The platform candidate built from the commit under test (Dockerfile.release and the web bundle, in a loopback registry) is deployed through the repository compose with only the report-portal image line changed (checked on the rendered `docker compose config` and on the file's lines).",
    "What runs is what was built: the container's image id, the digest its tag resolves to in the registry, the binary in the running process and the version and commit the portal reports all equal the build record written before the deploy.",
]

OPS06_PROVES = [
    "A recovery set taken with the released prior build is verified (file digests, secret_key fingerprint, dry-run restore) before the rollout.",
    "After a forced failed rollout the prior build refuses a copy of the migrated database, and the live database is first touched only after the restore's checks pass.",
    "The restore refuses a dump written by the newer build, a truncated set and a set with the wrong secret_key, each at the check meant to catch it, and accepts the good set in the recorded order, ending healthy.",
    "After restore the prior release is the same image as before the rollout, and the compose reference, secret_key fingerprint, keyring rows, migration ledger and fixture rows equal their pre-rollout values.",
]


def build_acceptance(ops05, ops06, artifact, run_url):
    """The committed acceptance document, generated from the run's evidence artifact. Both rows stay partial
    whatever the checks say: the gaps are properties of the drill, not of one run."""
    blockers = sorted(set(ops05["run"]["evidence_blockers"]) | set(ops06["run"]["evidence_blockers"]))
    failed = ops05["result"] != "passed" or ops06["result"] != "passed"

    def row(doc, name, proves, gaps):
        return {"row": name, "status": "failed" if doc["result"] != "passed" else "partial", "drill_result": doc["result"],
                "checks": [{k: v for k, v in c.items()} for c in doc["checks"]], "proves": proves, "gaps": gaps}

    return {
        "format": "portal.ops-deployment-drills-acceptance/1-dev.1",
        "status": "failed" if failed else "partial",
        "plan": "docs/app-platform/167-owner-decisions-remaining-acceptance.md section 6 (engineering note)",
        "run": {"url": run_url, **{k: ops05["run"][k] for k in ("event", "commit", "expected_commit", "prior_release_tag", "run_id", "attempt", "ref")}},
        "counts_as_evidence": not blockers, "evidence_blockers": blockers,
        "artifact": {k: artifact[k] for k in ("id", "name", "digest", "size_in_bytes")},
        "generated_utc": {"ops05": ops05["generated_at"], "ops06": ops06["generated_at"]},
        "host": ops05.get("host", {}),
        "rows": [row(ops05, "OPS-05", OPS05_PROVES, OPS05_GAPS), row(ops06, "OPS-06", OPS06_PROVES, OPS06_GAPS)],
        "ops05_facts": ops05["facts"], "ops05_released_prior": ops05.get("released_prior", {}),
        "ops06_events": ops06["events"],
        "ops06_facts": {k: v for k, v in ops06["facts"].items() if k not in ("events", "rollout_gate_checks")},
        "not_proven": NOT_PROVEN_COMMON,
    }


# ------------------------------------------------------------------ docker host

class DockerHost:
    """The disposable host: a compose stack from the repository's docker-compose.yml plus the one-off containers of the drill."""

    def __init__(self, work, repo_root, env, rec):
        self.work, self.repo, self.env, self.rec = work, repo_root, env, rec
        self.beta = os.path.join(work, "beta")
        self.compose_file = os.path.join(self.beta, "docker-compose.yml")
        self.reference_file = os.path.join(self.beta, "docker-compose.reference.yml")
        self.config_dir = os.path.join(self.beta, "config")
        self.set_dir = os.path.join(work, "recovery-set")
        self.out = os.path.join(work, "evidence")
        self.base = "http://127.0.0.1:8790"
        self.rehearsal_base = "http://127.0.0.1:8791"
        self.fx = None
        self.prior_ref = self.prior_digest = self.prior_tag = self.prior_commit = ""
        self.manifest = {}
        for d in (self.beta, self.set_dir, self.out):
            os.makedirs(d, exist_ok=True)

    # -- plumbing
    def sh(self, args, check=True, timeout=300, stdin=None, stdout=None, sudo=False):
        cmd = (["sudo", "-n"] if sudo else []) + list(args)
        proc = subprocess.run(cmd, capture_output=stdout is None, text=stdout is None, timeout=timeout, input=stdin,
                              stdout=stdout, check=False)
        if check and proc.returncode != 0:
            raise RuntimeError(f"{' '.join(args)[:200]} failed ({proc.returncode}): "
                               f"{redact((proc.stderr or '') if stdout is None else '')[-800:]}")
        return proc

    def compose(self, *args, **kw):
        base = ["docker", "compose", "-p", PROJECT, "-f", self.compose_file]
        return self.sh(base + list(args), **kw)

    def log(self, name, text):
        with open(os.path.join(self.out, name), "w", encoding="utf-8") as f:
            f.write(redact(text))

    def psql(self, db, sql):
        return self.compose("exec", "-T", "db", "psql", "-U", "portal", "-d", db, "-Atc", sql).stdout

    def psql_admin(self, sql):
        return self.compose("exec", "-T", "db", "psql", "-U", "portal", "-d", "postgres", "-Atc", sql).stdout

    # -- compose file and stack
    def write_compose(self, ref):
        with open(os.path.join(self.repo, "docker-compose.yml"), encoding="utf-8") as f:
            template = f.read()
        text = render_pinned_compose(template, ref)
        with open(self.compose_file, "w", encoding="utf-8") as f:
            f.write(text)
        # The compose file binds this directory read-only into the portal and Docker never creates it (doc 187
        # section 6): a static install makes it beside the compose file, as its header says.
        os.makedirs(os.path.join(os.path.dirname(self.compose_file), "app-runner-control"), exist_ok=True)
        with open(self.reference_file, "w", encoding="utf-8") as f:  # verbatim, beside the deployed file so paths resolve alike
            f.write(template)
        return text, changed_line_numbers(template, text), image_line_numbers(template)

    def compose_config(self, path):
        out = self.sh(["docker", "compose", "-p", PROJECT, "--project-directory", self.beta, "-f", path, "config",
                       "--format", "json"]).stdout
        return json.loads(out)

    def config_diff(self):
        """Where the stack that is deployed differs from the repository's compose, as parsed by compose itself."""
        return config_diff_paths(self.compose_config(self.reference_file), self.compose_config(self.compose_file))

    def reset_stack(self):
        """Tear the stack down with its volumes so the next deployment starts from a clean host."""
        self.compose("down", "-v", "--remove-orphans", check=False)
        self.sh(["rm", "-rf", self.config_dir, self.config_dir + ".not-carried"], sudo=True)

    def compose_sha(self):
        with open(self.compose_file, "rb") as f:
            return sha256_hex(f.read())

    def up(self, wait=True):
        """`docker compose up -d`; with wait, until the service reports healthy (or 180 s). A service that
        is expected to refuse to start is brought up without waiting."""
        extra = ["--wait", "--wait-timeout", "180"] if wait else []
        self.compose("up", "-d", *extra, timeout=420, check=False)

    def wait_healthy(self, base=None, seconds=150):
        deadline = time.time() + seconds
        while time.time() < deadline:
            status, _ = Portal(base or self.base, timeout=3).req("GET", "/healthz")
            if status == 200:
                return True
            time.sleep(2)
        return False

    def container(self, name="report-portal"):
        out = self.sh(["docker", "inspect", name]).stdout
        return json.loads(out)[0]

    def container_state(self, name="report-portal"):
        c = self.container(name)
        st = c["State"]
        return {"status": st.get("Status"), "exit_code": st.get("ExitCode"), "restarts": c.get("RestartCount", 0),
                "health": (st.get("Health") or {}).get("Status", "")}

    def cli(self, *args):
        """The portal CLI inside the running container (the documented runbook form)."""
        return self.compose("exec", "-T", "report-portal", "/app/report-portal", *args, check=False)

    # -- images
    def registry_digest(self, ref):
        out = self.sh(["docker", "buildx", "imagetools", "inspect", ref, "--format", "{{json .Manifest}}"]).stdout
        return manifest_digest(out)

    def tag_digest(self, ref):
        """The digest a tag resolves to in its registry right now: the loopback registry is asked directly,
        any other through buildx."""
        if ref.startswith("localhost:5000/"):
            name, tag = ref[len("localhost:5000/"):].rsplit(":", 1)
            return local_registry_digest(self, name, tag)
        return self.registry_digest(ref)

    def image_inspect(self, ref):
        return json.loads(self.sh(["docker", "image", "inspect", ref]).stdout)[0]

    def binary_sha_in_image(self, ref):
        out = self.sh(["docker", "run", "--rm", "--entrypoint", "sha256sum", ref, "/app/report-portal"]).stdout
        return out.split()[0]

    def loaded_binary_sha(self):
        # The process runs as the image's unprivileged user (PUID, default 10001); only that user may read
        # its own /proc/1/exe, so root in the container cannot.
        out = self.compose("exec", "-T", "--user", "10001:10001", "report-portal", "sha256sum", "/proc/1/exe").stdout
        return out.split()[0]

    # -- one-off CLI containers against a chosen database
    def one_off(self, ref, db, config_dir, args, mounts=(), name=None, detach=False, publish=None):
        cmd = ["docker", "run"] + (["-d"] if detach else ["--rm"]) + ["--network", f"{PROJECT}_default"]
        if name:
            cmd += ["--name", name]
        cmd += ["-e", "RP_DB_DRIVER=postgres", "-e",
                f"RP_DB_DSN=postgres://portal:portal@db:5432/{db}?sslmode=disable", "-v", f"{config_dir}:/app/config"]
        for m in mounts:
            cmd += ["-v", m]
        if publish:
            cmd += ["-p", publish]
        if detach:
            return self.sh(cmd + [ref], check=False)
        return self.sh(cmd + ["--entrypoint", "/app/report-portal", ref] + list(args), check=False)

    # -- fixtures
    def seed_fixtures(self, run_id):
        fx = Fixtures.generate(run_id)
        for user, pw, role in ((fx.admin_user, fx.admin_password, "admin"), (fx.totp_user, fx.totp_password, ""),
                               *[(r, "Rdr-" + secrets.token_hex(8), "") for r in fx.readers]):
            proc = self.cli("adduser", user, pw, *([role] if role else []))
            if proc.returncode != 0:
                raise RuntimeError(f"adduser {user} failed: {redact(proc.stderr)[-300:]}")
        admin = Portal(self.base)
        status, body = admin.login(fx.admin_user, fx.admin_password)
        if status != 200:
            raise RuntimeError(f"drill admin login failed: {status} {body}")
        status, mint = admin.req("POST", "/api/admin/tokens", {"name": "ops-drill", "scope": "all"})
        fx.token = mint.get("token") or mint.get("value") or ""
        if status != 200 or not fx.token:
            raise RuntimeError(f"token mint failed: {status}")
        today = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d")
        status, rep = Portal(self.base).req("POST", "/api/v1/reports", {
            "symbol": "600519", "date": today, "subtype": "估值分析", "title": f"OPS drill {fx.marker}",
            "body_md": f"## OPS drill\n{fx.marker}\n"}, token=fx.token)
        if status != 200 or not rep.get("id"):
            raise RuntimeError(f"report ingest failed: {status} {rep}")
        fx.report_id = rep["id"]
        user = Portal(self.base)
        status, _ = user.login(fx.totp_user, fx.totp_password)
        if status != 200:
            raise RuntimeError("two-factor drill user could not sign in to enrol")
        status, setup = user.req("POST", "/api/me/2fa/setup", headers={"X-Step-Up-Proof": fx.totp_password})
        fx.totp_secret = setup.get("secret", "")
        if status != 200 or not fx.totp_secret:
            raise RuntimeError(f"2fa setup failed: {status} {setup}")
        step = pick_totp_step(fx.used_steps)
        status, enabled = user.req("POST", "/api/me/2fa/enable", {"code": totp_code(fx.totp_secret, step)})
        fx.used_steps.add(step)
        if status != 200:
            raise RuntimeError(f"2fa enable failed: {status} {enabled}")
        self.fx = fx
        return fx

    # -- state snapshot
    def state(self, db=LIVE_DB):
        ledger = parse_ledger(self.psql(db, f"SELECT k FROM meta WHERE k LIKE '{LEDGER_PREFIX}%' ORDER BY k"))
        counts = {}
        for table in ("users", "reports", "api_tokens"):
            counts[table] = int(self.psql(db, f"SELECT count(*) FROM {table}").strip() or 0)
        keyring = keyring_fingerprint(self.psql(db, "SELECT k||'='||v FROM meta WHERE k IN "
                                                    f"({','.join(repr(k) for k in KEYRING_KEYS)}) ORDER BY k"))
        return {"ledger": ledger, "counts": counts, "keyring_fp": keyring}

    def reported_version(self):
        admin = Portal(self.base)
        admin.login(self.fx.admin_user, self.fx.admin_password)
        _, body = admin.req("GET", "/api/version")
        return {"version": body.get("version", ""), "commit": body.get("commit", "")}

    def secret_key_fp(self):
        text = self.compose("exec", "-T", "report-portal", "cat", "/app/config/config.yaml").stdout
        return fingerprint(parse_secret_key(text)), text

    # -- loaded identity
    def loaded_identity(self):
        """What is actually running: the container's image id and the digests of that image, the binary in
        memory and what the portal itself reports. The digests are those of the image the container runs,
        looked up by its id, not those of whatever a tag points at in the local store."""
        container = self.container()
        image = self.image_inspect(container["Image"])
        facts = {"container_image_id": container["Image"], "container_config_image": container["Config"]["Image"],
                 "repo_digests": [d.split("@", 1)[1] for d in image.get("RepoDigests", []) if "@" in d],
                 "loaded_binary_sha256": self.loaded_binary_sha(), "cli_version": self.cli("version").stdout.strip()}
        reported = self.reported_version()
        facts.update({"api_version": reported["version"], "api_commit": reported["commit"]})
        return facts

    # -- recovery set (a DockerHost implements the recovery_sequence interface)
    def capture_recovery_set(self):
        """The protected recovery set: a logical dump written by the prior build (ADR 0027), the config
        directory that holds secret_key, and the compose file that names the version."""
        dump = os.path.join(self.set_dir, "dump.jsonl")
        with open(dump, "wb") as f:
            proc = subprocess.run(["docker", "compose", "-p", PROJECT, "-f", self.compose_file, "exec", "-T",
                                   "report-portal", "/app/report-portal", "backup", "-"], stdout=f,
                                  stderr=subprocess.PIPE, timeout=300)
        if proc.returncode != 0:
            raise RuntimeError("backup failed: " + redact(proc.stderr.decode(errors="replace"))[-400:])
        key_fp, config_text = self.secret_key_fp()
        with open(os.path.join(self.set_dir, "config.yaml"), "w", encoding="utf-8") as f:
            f.write(config_text)
        os.chmod(os.path.join(self.set_dir, "config.yaml"), 0o600)
        shutil.copyfile(self.compose_file, os.path.join(self.set_dir, "docker-compose.yml"))
        with open(dump, "rb") as f:
            header = json.loads(f.readline())
        self.manifest = {
            "written_by": {"image": self.prior_ref, "digest": self.prior_digest, "tag": self.prior_tag},
            "dump": {"file": "dump.jsonl", "sha256": self.file_sha("dump.jsonl"), "migrations": header.get("migrations", []),
                     "app_version": header.get("app_version", "")},
            "config": {"file": "config.yaml", "sha256": self.file_sha("config.yaml"), "secret_key_fp": key_fp},
            "compose": {"file": "docker-compose.yml", "sha256": self.file_sha("docker-compose.yml")},
        }
        with open(os.path.join(self.set_dir, "manifest.json"), "w", encoding="utf-8") as f:
            json.dump(self.manifest, f, indent=2)
        return self.manifest

    def file_sha(self, name, directory=None):
        with open(os.path.join(directory or self.set_dir, name), "rb") as f:
            return sha256_hex(f.read())

    def verify_set_integrity(self, directory=None, manifest=None):
        return verify_recovery_set(directory or self.set_dir, manifest or self.manifest)

    def scratch_db(self, name):
        self.psql_admin(f"DROP DATABASE IF EXISTS {name} WITH (FORCE)")
        self.psql_admin(f"CREATE DATABASE {name} OWNER portal")

    def restore_cmd(self, db, config_dir, dump_dir, dump_name, force):
        args = ["restore", f"/set/{dump_name}"] + (["--force"] if force else [])
        proc = self.one_off(self.prior_ref, db, config_dir, args, mounts=[f"{dump_dir}:/set:ro"])
        return proc.returncode, (proc.stdout or "") + (proc.stderr or "")

    def dry_run_restore(self, dump_dir=None, dump_name="dump.jsonl", scratch="dryrun"):
        """Compatibility: the prior build reads the whole dump and reports what it would load, writing
        nothing from the dump. It does bootstrap the empty scratch database it is pointed at (schema and the
        18 seed rows a fresh database gets), which is why the report says it "would DELETE 18 existing rows":
        those are the scratch database's own seed rows, not live data. The live database is never opened."""
        self.scratch_db(REHEARSAL_DB)
        rc, out = self.restore_cmd(REHEARSAL_DB, self.rehearsal_config(), dump_dir or self.set_dir, dump_name, force=False)
        self.log(f"restore-dry-run-{scratch}.txt", out)
        return rc == 0 and "DRY RUN" in out, {"exit_code": rc, "summary": out.strip().splitlines()[-2:] if out.strip() else []}

    def rehearsal_config(self, source=None):
        """A fresh config directory for a scratch instance. The portal's entrypoint chowns a mounted
        directory to its own user, so a directory is never written twice."""
        self._rehearsal_n = getattr(self, "_rehearsal_n", 0) + 1
        directory = os.path.join(self.work, f"rehearsal-config-{self._rehearsal_n}")
        os.makedirs(directory)
        shutil.copyfile(source or os.path.join(self.set_dir, "config.yaml"), os.path.join(directory, "config.yaml"))
        os.chmod(os.path.join(directory, "config.yaml"), 0o600)
        return directory

    def rehearse_restore(self, config_source=None, dump_dir=None, label="rehearsal"):
        """Key recovery: restore the dump into a scratch database with the recovered config, start the
        prior build on it and run the activation gate, which signs in the two-factor account."""
        self.scratch_db(REHEARSAL_DB)
        config = self.rehearsal_config(config_source)
        rc, out = self.restore_cmd(REHEARSAL_DB, config, dump_dir or self.set_dir, "dump.jsonl", force=True)
        self.log(f"{label}-restore.txt", out)
        if rc != 0:
            return False, {"restore_exit": rc}
        self.sh(["docker", "rm", "-f", "ops-rehearsal"], check=False)
        self.one_off(self.prior_ref, REHEARSAL_DB, config, [], name="ops-rehearsal", detach=True,
                     publish="127.0.0.1:8791:8790")
        healthy = self.wait_healthy(self.rehearsal_base, 90)
        gate = run_gate(self.rehearsal_base, self.fx, self.prior_tag, self.prior_commit[:7]) if healthy else []
        self.log(f"{label}-container.log", self.sh(["docker", "logs", "ops-rehearsal"], check=False).stdout or "")
        self.sh(["docker", "rm", "-f", "ops-rehearsal"], check=False)
        failed = failed_checks(gate) if healthy else ["healthz"]
        return not failed, {"healthy": healthy, "failed": failed, "checks": gate}

    def replace_live_database(self):
        """The migrated database is kept under another name for forensics; the prior build restores
        into a fresh one, because it refuses to open a database whose ledger it does not know."""
        self.compose("stop", "report-portal")
        self.psql_admin(f"DROP DATABASE IF EXISTS {FAILED_DB} WITH (FORCE)")
        self.psql_admin(f"ALTER DATABASE {LIVE_DB} RENAME TO {FAILED_DB}")
        self.psql_admin(f"CREATE DATABASE {LIVE_DB} OWNER portal")
        exists = self.psql_admin(f"SELECT count(*) FROM pg_database WHERE datname IN ('{FAILED_DB}','{LIVE_DB}')").strip() == "2"
        return exists, {"kept_as": FAILED_DB}

    def restore_live(self):
        self.restore_config_into_live()
        rc, out = self.restore_cmd(LIVE_DB, self.config_dir, self.set_dir, "dump.jsonl", force=True)
        self.log("restore-live.txt", out)
        return rc == 0, {"exit_code": rc, "summary": out.strip().splitlines()[-1:]}

    def rollback_probe(self, new_steps):
        """The obvious rollback, the prior build started against the migrated database, run against a
        database copied from it so the live database is not touched before the restore's checks. The prior
        build must exit non-zero with the unknown steps named."""
        self.compose("stop", "report-portal")
        self.psql_admin(f"DROP DATABASE IF EXISTS {ROLLBACK_COPY_DB} WITH (FORCE)")
        self.psql_admin(f"CREATE DATABASE {ROLLBACK_COPY_DB} TEMPLATE {LIVE_DB} OWNER portal")
        name = "ops-rollback-probe"
        self.sh(["docker", "rm", "-f", name], check=False)
        self.one_off(self.prior_ref, ROLLBACK_COPY_DB, self.rehearsal_config(), [], name=name, detach=True)
        state = {}
        for _ in range(30):
            state = self.container_state(name)
            if state["status"] == "exited":
                break
            time.sleep(1)
        logs = (self.sh(["docker", "logs", name], check=False).stdout or "") + (self.sh(["docker", "logs", name], check=False).stderr or "")
        self.log("rollback-probe.log", logs)
        self.sh(["docker", "rm", "-f", name], check=False)
        self.psql_admin(f"DROP DATABASE IF EXISTS {ROLLBACK_COPY_DB} WITH (FORCE)")
        refused = state.get("status") == "exited" and state.get("exit_code") != 0 and refusal_names_steps(logs, new_steps)
        return refused, state

    def restore_config_into_live(self):
        """Put the recovered config.yaml (secret_key) back into the bind-mounted config directory."""
        self.sh(["rm", "-rf", self.config_dir], sudo=True)
        os.makedirs(self.config_dir)
        self.sh(["cp", os.path.join(self.set_dir, "config.yaml"), os.path.join(self.config_dir, "config.yaml")], sudo=True)
        self.sh(["chmod", "600", os.path.join(self.config_dir, "config.yaml")], sudo=True)

    def activate_prior(self):
        shutil.copyfile(os.path.join(self.set_dir, "docker-compose.yml"), self.compose_file)
        self.up()
        healthy = self.wait_healthy()
        if not healthy:
            self.log("activation-failed.log", self.compose("logs", "--no-color", "--tail", "200", check=False).stdout or "")
        return healthy, {"healthy": healthy, "compose_sha": self.compose_sha()}

    def final_gate(self):
        return run_gate(self.base, self.fx, self.prior_tag, self.prior_commit[:7])


# ------------------------------------------------------------------ drills

def now_iso():
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def local_registry_digest(host, name, tag):
    """The digest the loopback registry itself serves for a tag (Docker-Content-Digest)."""
    out = host.sh(["curl", "-fsI", "-H", "Accept: application/vnd.docker.distribution.manifest.v2+json, "
                   "application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json",
                   f"http://127.0.0.1:5000/v2/{name}/manifests/{tag}"]).stdout
    for line in out.splitlines():
        if line.lower().startswith("docker-content-digest:"):
            return line.split(":", 1)[1].strip()
    raise RuntimeError("the local registry returned no Docker-Content-Digest")


def load_candidate(env):
    """The build record the workflow wrote when it built the platform candidate (before any deploy): the
    reference in the loopback registry, the image id and pushed digest, the sha256 of the binary that went
    into the image, and the version and commit stamped into it."""
    with open(env["OPS_DRILL_CANDIDATE"], encoding="utf-8") as f:
        build = json.load(f)
    for key in ("ref", "image_id", "digest", "binary_sha256", "version", "commit"):
        if not build.get(key):
            raise RuntimeError(f"the candidate build record lacks {key}")
    return build


def resolve_release(host):
    """The pinned prior release and the release workflow's own record of what it published (tag, commit,
    image, digest). The tag is a required input, never chosen at run time."""
    tag = host.env.get("OPS_DRILL_PRIOR_TAG", "")
    if calver_key(tag) is None:
        raise RuntimeError("OPS_DRILL_PRIOR_TAG must be a pinned CalVer release tag")
    meta_dir = os.path.join(host.work, "release-metadata")
    os.makedirs(meta_dir, exist_ok=True)
    host.sh(["gh", "release", "download", tag, "-R", REPOSITORY, "-p", "release-metadata.json", "-D", meta_dir, "--clobber"])
    with open(os.path.join(meta_dir, "release-metadata.json"), encoding="utf-8") as f:
        meta = parse_release_metadata(f.read(), tag)
    return tag, meta


def drill_ops05(host, rec, build):
    """OPS-05: deploy the platform candidate through the repository compose and verify what is loaded."""
    ref = build["ref"]
    host.sh(["docker", "pull", ref], timeout=600)
    _, changed, image_line = host.write_compose(ref)
    rec.event("candidate_compose_written", True, reference=ref, changed_lines=changed)
    host.up()
    healthy = host.wait_healthy()
    rec.event("candidate_deployed", healthy)
    host.log("ops05-candidate-up.log", host.compose("logs", "--no-color", "--tail", "100", check=False).stdout or "")
    if not healthy:
        raise RuntimeError("the platform candidate did not become healthy")
    host.seed_fixtures(host.env.get("GITHUB_RUN_ID", "local"))  # first-run state and an admin session, through the product
    ident = host.loaded_identity()
    facts = {"mode": "candidate", "image_ref": ref, "build": build, "run_commit": host.env.get("GITHUB_SHA", ""),
             "expected_commit": host.env.get("OPS_DRILL_EXPECTED_COMMIT", ""),
             "config_diff_paths": host.config_diff(), "changed_lines": changed, "image_line_numbers": image_line,
             "registry_digest": host.tag_digest(ref), "healthy": healthy, **ident}
    return ops05_checks(facts), facts


def drill_released_prior(host, rec):
    """Deploy the pinned prior release from ghcr.io (the baseline OPS-06 rolls forward from) and verify it
    against the release workflow's record."""
    tag, meta = resolve_release(host)
    ref = f"{IMAGE}:{tag}"
    host.prior_ref, host.prior_digest, host.prior_tag, host.prior_commit = ref, meta["digest"], tag, meta["commit"]
    host.sh(["docker", "pull", ref], timeout=600)
    host.write_compose(ref)
    host.up()
    healthy = host.wait_healthy()
    rec.event("prior_deployed", healthy, tag=tag)
    host.log("released-prior-up.log", host.compose("logs", "--no-color", "--tail", "100", check=False).stdout or "")
    if not healthy:
        raise RuntimeError("the released prior build did not become healthy")
    host.seed_fixtures(host.env.get("GITHUB_RUN_ID", "local"))
    ident = host.loaded_identity()
    facts = {"tag": tag, "image_ref": ref, "record": meta, "registry_digest": host.registry_digest(ref),
             "image_id": host.image_inspect(ref)["Id"], "image_binary_sha256": host.binary_sha_in_image(ref),
             "healthy": healthy, **ident}
    return released_checks(facts), facts, meta


def drill_ops06(host, rec, build):
    fx = host.fx
    prior_ref = host.prior_ref
    baseline_sha = host.compose_sha()
    baseline_state = host.state()
    baseline_image_id = host.container()["Image"]
    key_fp, _ = host.secret_key_fp()
    baseline_gate = run_gate(host.base, fx, host.prior_tag, host.prior_commit[:7])
    rec.event("baseline_gate", not failed_checks(baseline_gate), failed=failed_checks(baseline_gate))

    # Protected recovery set, taken and verified with the build that is running now.
    host.capture_recovery_set()
    rec.event("recovery_set_captured", True, dump_migrations=len(host.manifest["dump"]["migrations"]),
              secret_key_fp=key_fp, keyring_fp=baseline_state["keyring_fp"])
    ok, detail = host.dry_run_restore(scratch="pre-rollout")
    precheck_set_ok = ok and host.verify_set_integrity()[0]
    rec.event("recovery_set_verified_before_rollout", precheck_set_ok, **detail)

    # Forced failed rollout: the candidate built in this job is deployed by editing the pinned tag, and the
    # config directory that holds secret_key is not carried across (a mis-mounted or recreated volume).
    cand_ref = build["ref"]
    host.sh(["docker", "pull", cand_ref], timeout=600)
    host.compose("stop", "report-portal")
    host.write_compose(cand_ref)
    host.sh(["mv", host.config_dir, host.config_dir + ".not-carried"], sudo=True)
    os.makedirs(host.config_dir, exist_ok=True)
    rec.event("rollout_started", True, reference=cand_ref, config_carried=False)
    host.up()
    healthy = host.wait_healthy()
    rec.event("rollout_process_started", healthy)
    host.log("rollout-candidate.log", host.compose("logs", "--no-color", "--tail", "200", check=False).stdout or "")
    container = host.container()
    candidate_digest_ok = (container["Config"]["Image"] == cand_ref and container["Image"] == build["image_id"]
                           and host.tag_digest(cand_ref) == build["digest"])
    after_state = host.state()
    delta = ledger_delta(baseline_state["ledger"], after_state["ledger"])
    rec.event("rollout_ledger_advanced", bool(delta), new_steps=delta)
    # A forensic dump by the build that migrated the database: it is what the prior build must refuse.
    newer_dir = os.path.join(host.work, "newer-set")
    os.makedirs(newer_dir, exist_ok=True)
    with open(os.path.join(newer_dir, "dump.jsonl"), "wb") as f:
        subprocess.run(["docker", "compose", "-p", PROJECT, "-f", host.compose_file, "exec", "-T", "report-portal",
                        "/app/report-portal", "backup", "-"], stdout=f, stderr=subprocess.DEVNULL, timeout=300, check=False)
    rollout_gate = run_gate(host.base, fx, None, None) if healthy else []
    rollout_failed = failed_checks(rollout_gate) if healthy else ["healthz"]
    rollout_version = host.reported_version()
    rec.event("rollout_gate", not rollout_failed, failed=rollout_failed,
              totp_login={k: c.get(k) for c in rollout_gate if c["id"] == "totp_login" for k in ("status", "code")})

    # The obvious rollback, by tag alone, is demonstrated on a copy of the migrated database. The live
    # database is not touched until the restore's own checks have passed.
    host.compose("stop", "report-portal")
    rb_refused, rb_state = host.rollback_probe(delta)
    live_unchanged = host.state()["ledger"] == after_state["ledger"]
    rec.event("rollback_by_tag_refused_on_a_copy", rb_refused and live_unchanged, state=rb_state, live_ledger_unchanged=live_unchanged)

    # Negative controls: each of the three pre-activation checks must refuse a bad set.
    neg_compat, neg_compat_detail = negative_compat(host, newer_dir)
    rec.event("negative_control_compat", neg_compat, **neg_compat_detail)
    neg_integrity, neg_integrity_detail = negative_integrity(host)
    rec.event("negative_control_integrity", neg_integrity, **neg_integrity_detail)
    neg_key, neg_key_detail = negative_key(host)
    rec.event("negative_control_key", neg_key, **neg_key_detail)

    # The restore itself.
    final_gate = recovery_sequence(host, rec)
    final_state = host.state()
    final_key_fp, _ = host.secret_key_fp()
    final = host.loaded_identity()
    facts = {
        "baseline_failed": failed_checks(baseline_gate), "precheck_set_ok": precheck_set_ok,
        "ledger_delta": delta, "candidate_digest_ok": candidate_digest_ok, "candidate_digest": build["digest"],
        "rollout_failed": rollout_failed, "rollout_healthz": healthy, "rollout_version": rollout_version,
        "rollout_reason_ok": failed_for(rollout_gate, "totp_login", 401, "bad_totp_code") and not baseline_failed_totp(baseline_gate),
        "rollout_reference": cand_ref, "rollout_gate_checks": rollout_gate,
        "rollback_refused": rb_refused, "rollback_on_copy": live_unchanged, "rollback_state": json.dumps(rb_state),
        "negative_compat_refused": neg_compat, "negative_compat_detail": json.dumps(neg_compat_detail),
        "negative_integrity_refused": neg_integrity, "negative_integrity_detail": json.dumps(neg_integrity_detail),
        "negative_key_refused": neg_key, "negative_key_detail": json.dumps(neg_key_detail),
        "events": rec.events, "final_failed": failed_checks(final_gate),
        "final_container_image_id": final["container_image_id"], "expected_image_id": host.image_inspect(prior_ref)["Id"],
        "baseline_container_image_id": baseline_image_id, "final_repo_digests": final["repo_digests"],
        "recorded_digest": host.prior_digest, "final_api_version": final["api_version"], "prior_tag": host.prior_tag,
        "compose_sha_baseline": baseline_sha, "compose_sha_final": host.compose_sha(),
        "key_fp_baseline": key_fp, "key_fp_final": final_key_fp,
        "keyring_fp_baseline": baseline_state["keyring_fp"], "keyring_fp_final": final_state["keyring_fp"],
        "ledger_baseline": baseline_state["ledger"], "ledger_final": final_state["ledger"],
        "counts_baseline": baseline_state["counts"], "counts_final": final_state["counts"],
    }
    return ops06_checks(facts), facts


def baseline_failed_totp(gate):
    """The same account signed in under the old key before the rollout, so the refusal is the key's doing."""
    return any(c["id"] == "totp_login" and not c["passed"] for c in gate)


def negative_compat(host, newer_dir):
    """A dump written by the newer build must be refused by the prior build's dry-run restore."""
    host.scratch_db(REHEARSAL_DB)
    rc, out = host.restore_cmd(REHEARSAL_DB, host.rehearsal_config(), newer_dir, "dump.jsonl", force=False)
    host.log("negative-compat-dry-run.txt", out)
    refused = rc != 0 and "newer" in out.lower()
    return refused, {"exit_code": rc, "message": redact(out.strip().splitlines()[-1] if out.strip() else "")}


def negative_integrity(host):
    """A recovery set whose dump was truncated must fail the integrity check and the dry run, and the key
    fingerprint must still read as the right key."""
    bad = os.path.join(host.work, "tampered-set")
    shutil.rmtree(bad, ignore_errors=True)
    shutil.copytree(host.set_dir, bad)
    path = os.path.join(bad, "dump.jsonl")
    with open(path, "rb") as f:
        data = f.read()
    with open(path, "wb") as f:
        f.write(data[: len(data) // 2])
    integrity_ok, detail = host.verify_set_integrity(bad, host.manifest)
    host.scratch_db(REHEARSAL_DB)
    rc, out = host.restore_cmd(REHEARSAL_DB, host.rehearsal_config(), bad, "dump.jsonl", force=False)
    host.log("negative-integrity-dry-run.txt", out)
    right_reason = detail["dump"] == "MISMATCH" and detail["secret_key_fp"] == "match"
    return (not integrity_ok and right_reason and rc != 0), {"integrity": detail, "dry_run_exit": rc}


def negative_key(host):
    """The config the failed rollout generated (a different secret_key) must fail the rehearsal, at the
    two-factor sign-in with HTTP 401 bad_totp_code, while every other check of the gate passes."""
    text = host.sh(["cat", os.path.join(host.config_dir, "config.yaml")], sudo=True).stdout
    other = os.path.join(host.work, "wrong-key-config.yaml")
    with open(other, "w", encoding="utf-8") as f:
        f.write(text)
    same_key = fingerprint(parse_secret_key(text)) == host.manifest["config"]["secret_key_fp"]
    ok, detail = host.rehearse_restore(config_source=other, label="negative-key")
    checks = detail.get("checks", [])
    totp = next((c for c in checks if c["id"] == "totp_login"), {})
    reason = failed_for(checks, "totp_login", 401, "bad_totp_code") and detail.get("failed") == ["totp_login"]
    return (not same_key and not ok and reason), {"wrong_key_differs": not same_key, "failed": detail.get("failed", []),
                                                  "totp_login": {"status": totp.get("status"), "code": totp.get("code")}}


def write_evidence(path, row, checks, facts, env, extra):
    doc = {"format": EVIDENCE_FORMAT, "row": row, "generated_at": now_iso(), "run": run_block(env),
           **verdict(checks), "checks": checks, "facts": facts, **extra}
    with open(path, "w", encoding="utf-8") as f:
        json.dump(doc, f, indent=2, ensure_ascii=False)
        f.write("\n")
    return doc


def host_info(host):
    def first_line(args):
        return host.sh(args, check=False).stdout.strip().splitlines()[:1]
    return {"runner": host.env.get("RUNNER_NAME", ""), "os": host.env.get("ImageOS", ""),
            "docker": first_line(["docker", "version", "--format", "{{.Server.Version}}"]),
            "compose": first_line(["docker", "compose", "version", "--short"])}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--work", required=True, help="scratch directory for the stack and the recovery set")
    parser.add_argument("--repo", default=".", help="repository checkout (the commit under test)")
    parser.add_argument("--out", required=True, help="directory for the evidence JSON and logs")
    args = parser.parse_args(argv)
    env = dict(os.environ)
    rec = Recorder()
    host = DockerHost(os.path.abspath(args.work), os.path.abspath(args.repo), env, rec)
    host.out = os.path.abspath(args.out)
    os.makedirs(host.out, exist_ok=True)
    info = host_info(host)
    exit_code = 0
    try:
        build = load_candidate(env)
        checks5, facts5 = drill_ops05(host, rec, build)
        host.reset_stack()
        released5, released_facts5, meta = drill_released_prior(host, rec)
        released_doc = {"checks": released5, "facts": released_facts5, "record": meta, **verdict(released5)}
        doc5 = write_evidence(os.path.join(host.out, "ops05-evidence.json"), "OPS-05", checks5, facts5, env,
                              {"host": info, "released_prior": released_doc, "events": list(rec.events),
                               "not_proven": NOT_PROVEN_COMMON, "gaps": OPS05_GAPS})
        print(f"OPS-05 {doc5['result']}: failed={doc5['failed_checks']}; released prior {released_doc['result']}: {released_doc['failed_checks']}")
        exit_code |= 0 if doc5["passed"] and released_doc["passed"] else 1
        checks6, facts6 = drill_ops06(host, rec, build)
        doc6 = write_evidence(os.path.join(host.out, "ops06-evidence.json"), "OPS-06", checks6, facts6, env,
                              {"host": info, "events": list(rec.events), "not_proven": NOT_PROVEN_COMMON, "gaps": OPS06_GAPS})
        print(f"OPS-06 {doc6['result']}: failed={doc6['failed_checks']}")
        exit_code |= 0 if doc6["passed"] else 1
    except Exception as err:  # the evidence of a drill that broke says so rather than being absent
        print(f"drill error: {err}", file=sys.stderr)
        crash = {"format": EVIDENCE_FORMAT, "row": "OPS-05/06", "generated_at": now_iso(), "run": run_block(env),
                 "passed": False, "result": "error", "error": redact(str(err)), "events": list(rec.events)}
        with open(os.path.join(host.out, "drill-error.json"), "w", encoding="utf-8") as f:
            json.dump(crash, f, indent=2)
        exit_code = 1
    finally:
        host.log("compose-final.log", host.compose("logs", "--no-color", "--tail", "300", check=False).stdout or "")
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
