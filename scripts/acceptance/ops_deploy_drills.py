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
FAILED_DB = "reports_failed_rollout"
REHEARSAL_DB = "reports_rehearsal"
LEDGER_PREFIX = "mig:"
KEYRING_KEYS = ("keyring_salt", "keyring_wrapped_dek")
ACCEPTED_EVENTS = ("workflow_dispatch", "push")

SHA256_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
CALVER_RE = re.compile(r"^v(\d{4})\.(\d{1,2})(?:\.(\d+))?$")
IMAGE_LINE_RE = re.compile(r"^(\s*image:\s*)(ghcr\.io/\S+)\s*$")


# ------------------------------------------------------------------ release selection

def calver_key(tag):
    """The numeric (year, week, revision) of a vYYYY.W[.R] tag, or None. A missing revision is 0 and a
    different number from .1 (ADR 0034); comparison is never lexical."""
    match = CALVER_RE.match(tag or "")
    if not match:
        return None
    year, week, revision = match.groups()
    return int(year), int(week), int(revision or 0)


def newest_published_release(releases, channel="beta"):
    """The tag of the newest published release. The beta channel includes pre-releases, the stable one
    does not; a draft is never published, and a tag that is not CalVer is ignored."""
    best = None
    for release in releases:
        if release.get("draft"):
            continue
        if channel == "stable" and release.get("prerelease"):
            continue
        key = calver_key(release.get("tag_name", ""))
        if key is None:
            continue
        if best is None or key > best[0]:
            best = (key, release["tag_name"])
    if best is None:
        raise RuntimeError(f"no published {channel} release found")
    return best[1]


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

def compose_image_ref(text):
    """The report-portal image reference in a compose file; exactly one ghcr.io image line must exist."""
    refs = [m.group(2) for m in (IMAGE_LINE_RE.match(line) for line in text.splitlines()) if m]
    if len(refs) != 1:
        raise ValueError(f"expected one ghcr.io image line in the compose file, found {len(refs)}")
    return refs[0]


def render_pinned_compose(template, ref):
    """The beta compose shape with its image reference pinned to ref. Nothing else changes: the result
    differs from the template by exactly one line, which changed_lines asserts."""
    compose_image_ref(template)
    out = []
    for line in template.splitlines(keepends=True):
        match = IMAGE_LINE_RE.match(line.rstrip("\r\n"))
        if match:
            ending = line[len(line.rstrip("\r\n")):]
            line = f"{match.group(1)}{ref}{ending}"
        out.append(line)
    return "".join(out)


def changed_lines(a, b):
    """The number of lines that differ between two texts with the same line count (0 when identical)."""
    la, lb = a.splitlines(), b.splitlines()
    if len(la) != len(lb):
        return max(len(la), len(lb))
    return sum(1 for x, y in zip(la, lb) if x != y)


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


def check_result(name, passed, detail=""):
    return {"id": name, "passed": bool(passed), "detail": detail}


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
    checks.append(check_result("totp_login", signed_in, f"status {status} code={body.get('code', '')}"))
    return checks


def failed_checks(checks):
    return sorted(c["id"] for c in checks if not c["passed"])


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
    """Compatibility and key recovery are checked before activation iff every pre-activation check
    passed and each came before the first mutation of the live stack that restores the prior version.
    Returns (passed, explanation)."""
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
    return True, f"all {len(PRE_ACTIVATION_CHECKS)} checks passed before event {first_mutation} and activation (event {activation['seq']})"


# ------------------------------------------------------------------ recovery sequence

class RecoveryRefused(Exception):
    """A pre-activation check refused the recovery set; nothing live was changed."""


def recovery_sequence(host, rec):
    """Restore the protected recovery set and reactivate the prior version.

    Every check that can refuse the set runs, and is recorded, before the live stack is touched:
    integrity of the set's files, a dry-run restore of the dump by the prior build (compatibility: the
    dump's migration level, schema and columns must be ones that build accepts), and a rehearsal of the
    whole restore into a scratch database with the recovered config, which signs the fixture
    two-factor account in (the keyring opens only under the recovered secret_key). Only then is the
    live database replaced and the prior version activated.
    """
    ok, detail = host.verify_set_integrity()
    rec.event("recovery_set_integrity", ok, **detail)
    if not ok:
        raise RecoveryRefused("recovery set integrity")
    ok, detail = host.dry_run_restore()
    rec.event("restore_dry_run_compat", ok, **detail)
    if not ok:
        raise RecoveryRefused("restore dry run")
    ok, detail = host.rehearse_restore()
    rec.event("rehearsal_key_gate", ok, **detail)
    if not ok:
        raise RecoveryRefused("rehearsal gate")
    rec.event("live_db_replaced", True, **host.replace_live_database())
    rec.event("live_restore", True, **host.restore_live())
    rec.event("activation", True, **host.activate_prior())
    gate = host.final_gate()
    rec.event("final_gate", not failed_checks(gate), failed=failed_checks(gate))
    return gate


# ------------------------------------------------------------------ evidence

def evidence_blockers(env):
    blockers = []
    if env.get("GITHUB_ACTIONS") != "true":
        blockers.append("not a GitHub Actions run")
    if env.get("GITHUB_REPOSITORY") != REPOSITORY:
        blockers.append("repository is not " + REPOSITORY)
    event = env.get("GITHUB_EVENT_NAME", "")
    if event not in ACCEPTED_EVENTS:
        blockers.append(f"the event was {event or 'unknown'}, not one of {', '.join(ACCEPTED_EVENTS)}")
    if not re.fullmatch(r"[0-9a-f]{40}", env.get("GITHUB_SHA", "")):
        blockers.append("no full commit SHA")
    if env.get("OPS_DRILL_RELEASE_TAG"):
        blockers.append("a release tag was pinned by input, not the newest published one")
    return blockers


def run_block(env):
    server, repo, run = env.get("GITHUB_SERVER_URL", ""), env.get("GITHUB_REPOSITORY", ""), env.get("GITHUB_RUN_ID", "")
    return {"event": env.get("GITHUB_EVENT_NAME", ""), "commit": env.get("GITHUB_SHA", ""),
            "ref": env.get("GITHUB_REF_NAME", ""), "run_id": run, "attempt": env.get("GITHUB_RUN_ATTEMPT", ""),
            "workflow_ref": env.get("GITHUB_WORKFLOW_REF", ""),
            "url": f"{server}/{repo}/actions/runs/{run}" if run else "",
            "evidence_blockers": evidence_blockers(env)}


def verdict(checks):
    failed = failed_checks(checks)
    return {"passed": not failed, "result": "passed" if not failed else "failed", "failed_checks": failed}


def ops05_checks(f):
    """Checks for OPS-05 from the facts the host collected after deploying a released image."""
    c = []
    c.append(check_result("reference_pinned_by_tag", f["image_ref"] == f"{IMAGE}:{f['tag']}", f["image_ref"]))
    c.append(check_result("compose_differs_from_beta_template_by_one_line", f["template_changed_lines"] == 1,
                          f"{f['template_changed_lines']} lines"))
    c.append(check_result("version_reference_unchanged_after_deploy",
                          f["compose_sha_before"] == f["compose_sha_after"] and f["ref_before"] == f["ref_after"],
                          f"{f['compose_sha_before'][:12]} -> {f['compose_sha_after'][:12]}"))
    c.append(check_result("registry_digest_equals_release_record", f["registry_digest"] == f["recorded_digest"],
                          f"{f['registry_digest']} vs {f['recorded_digest']}"))
    c.append(check_result("pulled_image_digest_equals_release_record", f["recorded_digest"] in f["repo_digests"],
                          ", ".join(f["repo_digests"])))
    c.append(check_result("container_runs_the_pulled_image", f["container_image_id"] == f["image_id"],
                          f"{f['container_image_id'][:19]}"))
    c.append(check_result("container_reference_is_the_pinned_tag", f["container_config_image"] == f["image_ref"],
                          f["container_config_image"]))
    c.append(check_result("loaded_binary_is_the_image_binary",
                          bool(f["loaded_binary_sha256"]) and f["loaded_binary_sha256"] == f["image_binary_sha256"],
                          f["loaded_binary_sha256"][:16]))
    c.append(check_result("api_version_equals_release_tag", f["api_version"] == f["tag"], f["api_version"]))
    c.append(check_result("api_commit_is_the_release_commit", f["recorded_commit"].startswith(f["api_commit"]) and len(f["api_commit"]) >= 7,
                          f["api_commit"]))
    cli_version, cli_commit = parse_cli_version(f["cli_version"])
    c.append(check_result("cli_version_matches_release", cli_version == f["tag"].lstrip("v") and cli_commit == f["api_commit"],
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
    c.append(check_result("prior_build_refuses_the_migrated_database",
                          f["rollback_refused"] and not f["rollback_healthy"], f["rollback_state"]))
    c.append(check_result("newer_dump_refused_by_prior_build", f["negative_compat_refused"], f["negative_compat_detail"]))
    c.append(check_result("tampered_set_refused_by_integrity_check", f["negative_integrity_refused"], f["negative_integrity_detail"]))
    c.append(check_result("wrong_key_refused_by_rehearsal_gate", f["negative_key_refused"], f["negative_key_detail"]))
    passed, why = ordering_proof(f["events"])
    c.append(check_result("checks_precede_activation", passed, why))
    c.append(check_result("final_gate_passed", not f["final_failed"], str(f["final_failed"])))
    c.append(check_result("prior_version_loaded_by_digest", f["final_digest_ok"], f["final_digest"]))
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
    "The deployment procedure exercised is the compose procedure of the beta configuration, not the production host's reverse proxy, firewall, systemd ordering or its shared PostgreSQL.",
]


# ------------------------------------------------------------------ docker host

class DockerHost:
    """The disposable host: a compose stack in the beta shape plus the one-off containers of the drill."""

    def __init__(self, work, repo_root, env, rec):
        self.work, self.repo, self.env, self.rec = work, repo_root, env, rec
        self.beta = os.path.join(work, "beta")
        self.compose_file = os.path.join(self.beta, "docker-compose.yml")
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
        return text, changed_lines(template, text)

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
    def loaded_identity(self, ref, expect_digest):
        """What is actually running: the container's image, its digests, the binary in memory and what
        the portal itself reports."""
        container = self.container()
        image = self.image_inspect(ref)
        facts = {"container_image_id": container["Image"], "image_id": image["Id"],
                 "container_config_image": container["Config"]["Image"],
                 "repo_digests": [d.split("@", 1)[1] for d in image.get("RepoDigests", []) if "@" in d],
                 "loaded_binary_sha256": self.loaded_binary_sha(), "image_binary_sha256": self.binary_sha_in_image(ref),
                 "cli_version": self.cli("version").stdout.strip()}
        admin = Portal(self.base)
        admin.login(self.fx.admin_user, self.fx.admin_password)
        _, ver = admin.req("GET", "/api/version")
        facts.update({"api_version": ver.get("version", ""), "api_commit": ver.get("commit", ""),
                      "digest_ok": expect_digest in facts["repo_digests"]})
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
        directory, manifest = directory or self.set_dir, manifest or self.manifest
        detail, ok = {}, True
        for part in ("dump", "config", "compose"):
            entry = manifest[part]
            try:
                actual = self.file_sha(entry["file"], directory)
            except OSError as err:
                actual = f"unreadable: {err}"
            same = actual == entry["sha256"]
            detail[part] = "match" if same else "MISMATCH"
            ok = ok and same
        text = open(os.path.join(directory, "config.yaml"), encoding="utf-8").read() if ok else ""
        key_same = bool(text) and fingerprint(parse_secret_key(text)) == manifest["config"]["secret_key_fp"]
        detail["secret_key_fp"] = "match" if key_same else "MISMATCH"
        return ok and key_same, detail

    def scratch_db(self, name):
        self.psql_admin(f"DROP DATABASE IF EXISTS {name} WITH (FORCE)")
        self.psql_admin(f"CREATE DATABASE {name} OWNER portal")

    def restore_cmd(self, db, config_dir, dump_dir, dump_name, force):
        args = ["restore", f"/set/{dump_name}"] + (["--force"] if force else [])
        proc = self.one_off(self.prior_ref, db, config_dir, args, mounts=[f"{dump_dir}:/set:ro"])
        return proc.returncode, (proc.stdout or "") + (proc.stderr or "")

    def dry_run_restore(self, dump_dir=None, dump_name="dump.jsonl", scratch="dryrun"):
        """Compatibility: the prior build reads the whole dump against an empty scratch database and
        reports what it would load. Nothing is written; the live database is not opened."""
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
        return {"kept_as": FAILED_DB}

    def restore_live(self):
        self.restore_config_into_live()
        rc, out = self.restore_cmd(LIVE_DB, self.config_dir, self.set_dir, "dump.jsonl", force=True)
        self.log("restore-live.txt", out)
        if rc != 0:
            raise RuntimeError("live restore failed: " + out[-400:])
        return {"exit_code": rc, "summary": out.strip().splitlines()[-1:]}

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
        return {"healthy": healthy, "compose_sha": self.compose_sha()}

    def final_gate(self):
        return run_gate(self.base, self.fx, self.prior_tag, self.prior_commit[:7])


# ------------------------------------------------------------------ drills

def now_iso():
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def resolve_release(host):
    """Newest published release (beta channel, pre-releases included) and the independent record of what
    it contains. The GitHub API is called with the job's own read-only token; the image is public."""
    env = host.env
    tag = env.get("OPS_DRILL_RELEASE_TAG") or ""
    if not tag:
        out = host.sh(["gh", "api", f"repos/{REPOSITORY}/releases?per_page=30"]).stdout
        tag = newest_published_release(json.loads(out), "beta")
    meta_dir = os.path.join(host.work, "release-metadata")
    os.makedirs(meta_dir, exist_ok=True)
    host.sh(["gh", "release", "download", tag, "-R", REPOSITORY, "-p", "release-metadata.json", "-D", meta_dir, "--clobber"])
    with open(os.path.join(meta_dir, "release-metadata.json"), encoding="utf-8") as f:
        meta = parse_release_metadata(f.read(), tag)
    return tag, meta


def drill_ops05(host, rec):
    tag, meta = resolve_release(host)
    ref = f"{IMAGE}:{tag}"
    host.prior_ref, host.prior_digest, host.prior_tag, host.prior_commit = ref, meta["digest"], tag, meta["commit"]
    registry = host.registry_digest(ref)
    rec.event("release_resolved", True, tag=tag, recorded_digest=meta["digest"], registry_digest=registry)
    host.sh(["docker", "pull", ref], timeout=600)
    text, changed = host.write_compose(ref)
    before_sha, before_ref = host.compose_sha(), compose_image_ref(text)
    rec.event("compose_written", True, sha256=before_sha, reference=before_ref)
    host.up()
    healthy = host.wait_healthy()
    rec.event("deployed", healthy)
    host.log("ops05-compose-up.log", host.compose("logs", "--no-color", "--tail", "100", check=False).stdout or "")
    if not healthy:
        raise RuntimeError("the released image did not become healthy")
    # First-run state is created by the product; the drill's fixtures are seeded through it as well.
    host.seed_fixtures(host.env.get("GITHUB_RUN_ID", "local"))
    ident = host.loaded_identity(ref, meta["digest"])
    after_text = open(host.compose_file, encoding="utf-8").read()
    facts = {"tag": tag, "image_ref": ref, "template_changed_lines": changed,
             "compose_sha_before": before_sha, "compose_sha_after": host.compose_sha(),
             "ref_before": before_ref, "ref_after": compose_image_ref(after_text),
             "registry_digest": registry, "recorded_digest": meta["digest"], "recorded_commit": meta["commit"],
             "healthy": healthy, **{k: ident[k] for k in (
                 "container_image_id", "image_id", "container_config_image", "repo_digests", "loaded_binary_sha256",
                 "image_binary_sha256", "api_version", "api_commit", "cli_version")}}
    checks = ops05_checks(facts)
    return checks, facts, meta


def local_registry_digest(host, name, tag):
    """The digest the loopback registry itself serves for a tag (Docker-Content-Digest)."""
    out = host.sh(["curl", "-fsI", "-H", "Accept: application/vnd.docker.distribution.manifest.v2+json, "
                   "application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json",
                   f"http://127.0.0.1:5000/v2/{name}/manifests/{tag}"]).stdout
    for line in out.splitlines():
        if line.lower().startswith("docker-content-digest:"):
            return line.split(":", 1)[1].strip()
    raise RuntimeError("the local registry returned no Docker-Content-Digest")


def candidate_image(host, rec, base_ref):
    """The commit under test as an image in a loopback registry this job owns: the released runtime
    layers plus a binary built from the checkout, version-stamped as a candidate. Nothing is pushed
    to ghcr.io."""
    sha = host.env["GITHUB_SHA"]
    short = sha[:7]
    build_dir = os.path.join(host.work, "candidate")
    os.makedirs(build_dir, exist_ok=True)
    host.sh(["bash", "-c",
             "set -euo pipefail; mod=$(go list -m); "
             f"CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags \"-s -w -X ${{mod}}/internal/version.Version=drill-candidate-{short} "
             f"-X ${{mod}}/internal/version.Commit={short}\" -o {build_dir}/report-portal ./cmd/report-portal"], timeout=900)
    with open(os.path.join(build_dir, "Dockerfile"), "w", encoding="utf-8") as f:
        f.write(f"FROM {base_ref}\nCOPY report-portal /app/report-portal\n")
    registry_id = host.sh(["docker", "run", "-d", "-p", "127.0.0.1:5000:5000", "--label", "org.portal.fixture=ops-drill",
                           "registry:3"]).stdout.strip()
    rec.event("local_registry_started", True, container=registry_id[:12])
    ref = f"localhost:5000/report-portal-candidate:drill-{short}"
    for _ in range(20):
        if host.sh(["curl", "-fs", "http://127.0.0.1:5000/v2/"], check=False).returncode == 0:
            break
        time.sleep(0.5)
    host.sh(["docker", "build", "-t", ref, build_dir], timeout=600)
    host.sh(["docker", "push", ref], timeout=600)
    pushed = host.image_inspect(ref)["RepoDigests"]
    digest = next(d.split("@", 1)[1] for d in pushed if d.startswith("localhost:5000/"))
    registry = local_registry_digest(host, "report-portal-candidate", f"drill-{short}")
    return ref, digest, registry, short


def drill_ops06(host, rec, ops05_facts):
    fx = host.fx
    prior_ref = host.prior_ref
    baseline_sha = host.compose_sha()
    baseline_state = host.state()
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

    # Candidate image for the failed rollout.
    cand_ref, cand_digest, cand_registry, short = candidate_image(host, rec, prior_ref)
    host.sh(["docker", "pull", cand_ref], check=False)

    # Forced failed rollout: the candidate is deployed by editing the pinned tag, and the config
    # directory that holds secret_key is not carried across (a mis-mounted or recreated volume).
    host.compose("stop", "report-portal")
    host.write_compose(cand_ref)
    host.sh(["mv", host.config_dir, host.config_dir + ".not-carried"], sudo=True)
    os.makedirs(host.config_dir, exist_ok=True)
    rec.event("rollout_started", True, reference=cand_ref, config_carried=False)
    host.up()
    healthy = host.wait_healthy()
    rec.event("rollout_process_started", healthy)
    host.log("rollout-candidate.log", host.compose("logs", "--no-color", "--tail", "200", check=False).stdout or "")
    cand_loaded = host.container()["Image"]
    cand_image_id = host.image_inspect(cand_ref)["Id"]
    reference_ok = host.container()["Config"]["Image"] == cand_ref
    candidate_digest_ok = reference_ok and cand_loaded == cand_image_id and cand_digest == cand_registry
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
    rollout_version = host.reported_version()
    rollout_failed = failed_checks(rollout_gate) if healthy else ["healthz"]
    rec.event("rollout_gate", not rollout_failed, failed=rollout_failed)

    # Roll back by the obvious route: the prior tag against the database the candidate migrated.
    host.compose("stop", "report-portal")
    host.write_compose(prior_ref)
    host.up(wait=False)
    time.sleep(25)
    state = host.container_state()
    rb_log = host.compose("logs", "--no-color", "--tail", "100", "report-portal", check=False).stdout or ""
    host.log("rollback-without-restore.log", rb_log)
    rb_healthy = Portal(host.base, timeout=3).req("GET", "/healthz")[0] == 200
    rb_refused = refusal_names_steps(rb_log, delta)
    rec.event("rollback_without_restore", rb_refused and not rb_healthy, state=state, healthy=rb_healthy)
    host.compose("stop", "report-portal")

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
    final_ident = host.loaded_identity(prior_ref, host.prior_digest)
    facts = {
        "baseline_failed": failed_checks(baseline_gate), "precheck_set_ok": precheck_set_ok,
        "ledger_delta": delta, "candidate_digest_ok": candidate_digest_ok, "candidate_digest": cand_digest,
        "rollout_failed": rollout_failed, "rollout_healthz": healthy, "rollout_version": rollout_version,
        "rollout_reference": cand_ref, "rollout_gate_checks": rollout_gate,
        "rollback_refused": rb_refused, "rollback_healthy": rb_healthy, "rollback_state": json.dumps(state),
        "negative_compat_refused": neg_compat, "negative_compat_detail": json.dumps(neg_compat_detail),
        "negative_integrity_refused": neg_integrity, "negative_integrity_detail": json.dumps(neg_integrity_detail),
        "negative_key_refused": neg_key, "negative_key_detail": json.dumps(neg_key_detail),
        "events": rec.events, "final_failed": failed_checks(final_gate),
        "final_digest_ok": final_ident["digest_ok"] and final_ident["api_version"] == host.prior_tag,
        "final_digest": host.prior_digest,
        "compose_sha_baseline": baseline_sha, "compose_sha_final": host.compose_sha(),
        "key_fp_baseline": key_fp, "key_fp_final": final_key_fp,
        "keyring_fp_baseline": baseline_state["keyring_fp"], "keyring_fp_final": final_state["keyring_fp"],
        "ledger_baseline": baseline_state["ledger"], "ledger_final": final_state["ledger"],
        "counts_baseline": baseline_state["counts"], "counts_final": final_state["counts"],
    }
    return ops06_checks(facts), facts


def negative_compat(host, newer_dir):
    """A dump written by the newer build must be refused by the prior build's dry-run restore."""
    host.scratch_db(REHEARSAL_DB)
    rc, out = host.restore_cmd(REHEARSAL_DB, host.rehearsal_config(), newer_dir, "dump.jsonl", force=False)
    host.log("negative-compat-dry-run.txt", out)
    refused = rc != 0 and "newer" in out.lower()
    return refused, {"exit_code": rc, "message": redact(out.strip().splitlines()[-1] if out.strip() else "")}


def negative_integrity(host):
    """A recovery set whose dump was truncated must fail the integrity check and the dry run."""
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
    return (not integrity_ok and rc != 0), {"integrity": detail, "dry_run_exit": rc}


def negative_key(host):
    """The config that the failed rollout generated (a different secret_key) must fail the rehearsal."""
    lost = os.path.join(host.config_dir)
    text = host.sh(["cat", os.path.join(lost, "config.yaml")], sudo=True).stdout
    other = os.path.join(host.work, "wrong-key-config.yaml")
    with open(other, "w", encoding="utf-8") as f:
        f.write(text)
    same_key = fingerprint(parse_secret_key(text)) == host.manifest["config"]["secret_key_fp"]
    ok, detail = host.rehearse_restore(config_source=other, label="negative-key")
    failed = detail.get("failed", [])
    return (not same_key and not ok and "totp_login" in failed), {"wrong_key_differs": not same_key, "failed": failed}


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
        checks5, facts5, meta = drill_ops05(host, rec)
        doc5 = write_evidence(os.path.join(host.out, "ops05-evidence.json"), "OPS-05", checks5, facts5, env,
                              {"host": info, "release_record": meta, "events": list(rec.events),
                               "not_proven": NOT_PROVEN_COMMON})
        print(f"OPS-05 {doc5['result']}: failed={doc5['failed_checks']}")
        exit_code |= 0 if doc5["passed"] else 1
        checks6, facts6 = drill_ops06(host, rec, facts5)
        doc6 = write_evidence(os.path.join(host.out, "ops06-evidence.json"), "OPS-06", checks6, facts6, env,
                              {"host": info, "events": list(rec.events), "not_proven": NOT_PROVEN_COMMON})
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
