import copy
import importlib.util
import json
import re
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

SOURCE = Path(__file__).parent / "acceptance/ops_deploy_drills.py"
SPEC = importlib.util.spec_from_file_location("ops_deploy_drills", SOURCE)
drills = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(drills)

REPO = Path(__file__).resolve().parent.parent
DIGEST_A = "sha256:" + "a" * 64
DIGEST_B = "sha256:" + "b" * 64
COMMIT = "d0089c8895d5df541776ff1002cd27341db2b2b4"


class ReleaseSelectionTests(unittest.TestCase):
    def test_calver_key_is_numeric_and_revision_zero_is_distinct(self):
        self.assertEqual(drills.calver_key("v2026.46"), (2026, 46, 0))
        self.assertEqual(drills.calver_key("v2026.46.1"), (2026, 46, 1))
        self.assertGreater(drills.calver_key("v2026.46.1"), drills.calver_key("v2026.46"))
        self.assertGreater(drills.calver_key("v2026.10"), drills.calver_key("v2026.9"))
        self.assertGreater(drills.calver_key("v2026.44.25"), drills.calver_key("v2026.44.3"))
        for bad in ("2026.46", "v2026", "v26.46", "v2026.46-beta", "latest", "", None):
            self.assertIsNone(drills.calver_key(bad), bad)

    def test_newest_published_release(self):
        releases = [
            {"tag_name": "v2026.46", "draft": False, "prerelease": False},
            {"tag_name": "v2026.46.1", "draft": False, "prerelease": True},
            {"tag_name": "v2026.47", "draft": True, "prerelease": False},
            {"tag_name": "nightly", "draft": False, "prerelease": False},
            {"tag_name": "v2026.9", "draft": False, "prerelease": False},
        ]
        self.assertEqual(drills.newest_published_release(releases), "v2026.46.1")
        self.assertEqual(drills.newest_published_release(releases, "stable"), "v2026.46")
        with self.assertRaises(RuntimeError):
            drills.newest_published_release([{"tag_name": "v2026.47", "draft": True}])

    def test_release_metadata(self):
        good = json.dumps({"tag": "v2026.46.1", "commit": COMMIT, "image": drills.IMAGE, "digest": DIGEST_A})
        self.assertEqual(drills.parse_release_metadata(good, "v2026.46.1")["digest"], DIGEST_A)
        for change in ({"tag": "v2026.46"}, {"digest": "sha256:abc"}, {"commit": "d0089c8"}, {"image": ""}):
            doc = json.loads(good)
            doc.update(change)
            with self.assertRaises(ValueError, msg=str(change)):
                drills.parse_release_metadata(json.dumps(doc), "v2026.46.1")
        with self.assertRaises(ValueError):
            drills.parse_release_metadata(good, "v2026.45")

    def test_manifest_digest(self):
        self.assertEqual(drills.manifest_digest(json.dumps({"digest": DIGEST_A, "mediaType": "x"})), DIGEST_A)
        with self.assertRaises(ValueError):
            drills.manifest_digest(json.dumps({"mediaType": "x"}))


class ComposeTests(unittest.TestCase):
    def setUp(self):
        self.template = (REPO / "docker-compose.yml").read_text(encoding="utf-8")

    def test_the_repository_compose_has_exactly_one_portal_image_line(self):
        self.assertTrue(drills.compose_image_ref(self.template).startswith(drills.IMAGE + ":"))

    def test_rendering_changes_one_line_and_only_the_reference(self):
        ref = f"{drills.IMAGE}:v2026.46.1"
        out = drills.render_pinned_compose(self.template, ref)
        self.assertEqual(drills.compose_image_ref(out), ref)
        self.assertEqual(drills.changed_lines(self.template, out), 1)
        self.assertIn("image: postgres:16-alpine", out)
        self.assertEqual(out.replace(ref, drills.compose_image_ref(self.template)), self.template)

    def test_rendering_is_idempotent_for_the_same_reference(self):
        ref = f"{drills.IMAGE}:v2026.46.1"
        once = drills.render_pinned_compose(self.template, ref)
        self.assertEqual(drills.render_pinned_compose(once, ref), once)

    def test_zero_or_two_portal_images_are_refused(self):
        with self.assertRaises(ValueError):
            drills.render_pinned_compose("services:\n  db:\n    image: postgres:16\n", "x")
        two = "    image: ghcr.io/a/b:1\n    image: ghcr.io/a/b:2\n"
        with self.assertRaises(ValueError):
            drills.render_pinned_compose(two, "x")

    def test_changed_lines(self):
        self.assertEqual(drills.changed_lines("a\nb\n", "a\nb\n"), 0)
        self.assertEqual(drills.changed_lines("a\nb\n", "a\nc\n"), 1)
        self.assertGreater(drills.changed_lines("a\nb\n", "a\nb\nc\n"), 0)


class LedgerAndKeyTests(unittest.TestCase):
    def test_ledger_parse_and_delta(self):
        before = drills.parse_ledger("mig:0002\nmig:0001\n\nnot-a-ledger-row\n")
        self.assertEqual(before, ["0001", "0002"])
        self.assertEqual(drills.ledger_delta(before, ["0001", "0002", "0015", "0016"]), ["0015", "0016"])
        self.assertEqual(drills.ledger_delta(before, before), [])

    def test_refusal_message_matches_the_portal_wording(self):
        message = ("2026/11/15 db: this database records migration 0015, 0016, which this build does not have: "
                   "it was written by a newer release. Start it with a release that knows that step")
        self.assertTrue(drills.refusal_names_steps(message, ["0015", "0016"]))
        self.assertFalse(drills.refusal_names_steps(message, ["0015", "0016", "0017"]))
        self.assertFalse(drills.refusal_names_steps(message, []))
        self.assertFalse(drills.refusal_names_steps("listening on :8790 0015", ["0015"]))

    def test_secret_key_parsing_ignores_the_previous_key_and_comments(self):
        text = 'listen: ":8790"\n# secret_key: "commented0000000000"\nsecret_key_previous: "oldoldoldold"\nsecret_key: "abcd1234abcd1234"  # note\n'
        self.assertEqual(drills.parse_secret_key(text), "abcd1234abcd1234")
        self.assertEqual(drills.parse_secret_key("listen: x\n"), "")

    def test_fingerprints_distinguish_keys_and_never_contain_them(self):
        a, b = "a" * 64, "b" * 64
        self.assertEqual(drills.fingerprint(a), drills.fingerprint(a))
        self.assertNotEqual(drills.fingerprint(a), drills.fingerprint(b))
        self.assertNotIn(a[:16], drills.fingerprint(a))
        self.assertEqual(len(drills.fingerprint(a)), 16)

    def test_keyring_fingerprint_needs_both_rows(self):
        both = "keyring_salt=AAA\nkeyring_wrapped_dek=BBB\n"
        self.assertEqual(drills.keyring_fingerprint(both), drills.keyring_fingerprint("keyring_wrapped_dek=BBB\nkeyring_salt=AAA"))
        self.assertNotEqual(drills.keyring_fingerprint(both), drills.keyring_fingerprint("keyring_salt=AAA\nkeyring_wrapped_dek=CCC\n"))
        with self.assertRaises(ValueError):
            drills.keyring_fingerprint("keyring_salt=AAA\n")
        with self.assertRaises(ValueError):
            drills.keyring_fingerprint("")

    def test_redaction_removes_first_run_password_and_keys(self):
        text = "  username: admin\n    password: sLHUUKp86KpidB\nsecret_key: \"369a4c19f8259cc02542efe3a0c3a5fa\"\nAuthorization: Bearer abc.def\n"
        out = drills.redact(text)
        for leaked in ("sLHUUKp86KpidB", "369a4c19f8259cc0", "abc.def"):
            self.assertNotIn(leaked, out)
        self.assertIn("username: admin", out)


class TotpTests(unittest.TestCase):
    def test_rfc6238_vector(self):
        secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"  # the RFC's ASCII secret 12345678901234567890
        self.assertEqual(drills.totp_code(secret, 59 // 30), "287082")
        self.assertEqual(drills.totp_code(secret, 1111111109 // 30), "081804")

    def test_step_choice_skips_spent_steps(self):
        self.assertEqual(drills.pick_totp_step({100}, now=3029), 101)
        self.assertEqual(drills.pick_totp_step(set(), now=3029), 100)
        with self.assertRaises(RuntimeError):
            drills.pick_totp_step({100, 101}, now=3029)

    def test_step_choice_waits_for_the_next_window(self):
        clock = [3029.0]
        slept = []

        def sleep(seconds):
            slept.append(seconds)
            clock[0] += 30

        step = drills.pick_totp_step({100, 101}, sleep=sleep, clock=lambda: clock[0])
        self.assertEqual(step, 102)
        self.assertTrue(slept)


class FakePortal(BaseHTTPRequestHandler):
    """A portal that answers the gate's routes. `behaviour` selects what is broken."""
    behaviour = {}
    SECRET = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

    def log_message(self, *args):
        pass

    def send(self, status, body, cookie=False):
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        if cookie:
            self.send_header("Set-Cookie", "rp_session=abc; Path=/; HttpOnly")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def body(self):
        length = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(length) or b"{}")

    def do_GET(self):
        b = self.behaviour
        if self.path == "/healthz":
            return self.send(200 if not b.get("down") else 503, {"ok": True})
        if self.path == "/api/version":
            return self.send(200, {"version": b.get("version", "v1"), "commit": "abcdef1"})
        if self.path == "/api/admin/users":
            names = ["admin", "drill-admin", "drill-totp", "drill-reader-1", "drill-reader-2"]
            return self.send(200, {"users": [{"username": n} for n in names if n not in b.get("lost_users", ())]})
        if self.path.startswith("/api/v1/reports/"):
            ok = self.headers.get("Authorization") == "Bearer tok" and not b.get("lost_report")
            return self.send(200 if ok else 404, {"body_md": "## x\nMARK\n"} if ok else {})
        if self.path == "/api/me":
            return self.send(200, {"user": "drill-totp"})
        self.send(404, {})

    def do_POST(self):
        data = self.body()
        if self.path == "/api/login":
            if data.get("username") == "drill-totp":
                return self.send(200, {"totp_required": True, "token": "t1"})
            if data.get("password") != "pw-admin" or self.behaviour.get("no_admin"):
                return self.send(401, {})
            return self.send(200, {"user": data["username"]}, cookie=True)
        if self.path == "/api/login/2fa":
            if self.behaviour.get("wrong_key"):
                return self.send(401, {"code": "bad_totp_code"})
            step = int(self.behaviour.get("step"))
            return self.send(200 if data.get("code") == drills.totp_code(self.SECRET, step) else 401,
                             {"user": "drill-totp"}, cookie=True)
        self.send(404, {})


class PortalClientTests(unittest.TestCase):
    def setUp(self):
        FakePortal.behaviour = {}
        self.server = HTTPServer(("127.0.0.1", 0), FakePortal)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}"
        self.fx = drills.Fixtures("drill-admin", "pw-admin", "drill-totp", "pw-totp", ["drill-reader-1", "drill-reader-2"],
                                  token="tok", report_id=7, marker="MARK", totp_secret=FakePortal.SECRET)
        self.step = drills.pick_totp_step(set())
        FakePortal.behaviour["step"] = self.step

    def tearDown(self):
        if self.server is not None:
            self.server.shutdown()
            self.server.server_close()

    def run_gate(self, **kw):
        return drills.run_gate(self.base, self.fx, kw.get("version"), kw.get("commit"))

    def test_a_healthy_portal_passes_every_check(self):
        checks = self.run_gate(version="v1", commit="abcdef")
        self.assertEqual(drills.failed_checks(checks), [])
        self.assertEqual([c["id"] for c in checks], ["healthz", "admin_login", "version", "users", "report", "totp_login"])
        self.assertIn(self.step, self.fx.used_steps)

    def test_a_lost_key_fails_only_the_two_factor_login(self):
        FakePortal.behaviour["wrong_key"] = True
        self.assertEqual(drills.failed_checks(self.run_gate()), ["totp_login"])

    def test_the_wrong_version_fails_the_version_check(self):
        FakePortal.behaviour["version"] = "v2"
        self.assertEqual(drills.failed_checks(self.run_gate(version="v1")), ["version"])
        self.fx.used_steps.clear()
        self.assertEqual(drills.failed_checks(self.run_gate(version="v2", commit="zzz")), ["version"])

    def test_lost_data_is_named(self):
        FakePortal.behaviour["lost_users"] = ("drill-reader-2",)
        FakePortal.behaviour["lost_report"] = True
        self.assertEqual(drills.failed_checks(self.run_gate()), ["report", "users"])

    def test_an_unreachable_portal_fails_without_raising(self):
        self.server.shutdown()
        self.server.server_close()
        self.server = None
        failed = drills.failed_checks(self.run_gate())
        self.assertEqual(failed, ["admin_login", "healthz", "report", "totp_login", "users", "version"])

    def test_a_dead_admin_login_fails_version_and_users_too(self):
        FakePortal.behaviour["no_admin"] = True
        self.assertEqual(drills.failed_checks(self.run_gate()), ["admin_login", "users", "version"])


class OrderingTests(unittest.TestCase):
    def events(self, order, ok=None):
        rec = drills.Recorder(clock=lambda: 0)
        for kind in order:
            rec.event(kind, (ok or {}).get(kind, True))
        return rec.events

    GOOD = ["recovery_set_integrity", "restore_dry_run_compat", "rehearsal_key_gate", "live_db_replaced",
            "live_restore", "activation", "final_gate"]

    def test_checks_before_activation_pass(self):
        passed, why = drills.ordering_proof(self.events(self.GOOD))
        self.assertTrue(passed, why)

    def test_a_check_after_the_first_live_change_fails(self):
        order = ["recovery_set_integrity", "restore_dry_run_compat", "live_db_replaced", "rehearsal_key_gate", "live_restore", "activation"]
        self.assertFalse(drills.ordering_proof(self.events(order))[0])

    def test_a_failed_check_fails_even_when_ordered(self):
        self.assertFalse(drills.ordering_proof(self.events(self.GOOD, {"rehearsal_key_gate": False}))[0])

    def test_a_missing_check_fails(self):
        self.assertFalse(drills.ordering_proof(self.events([k for k in self.GOOD if k != "restore_dry_run_compat"]))[0])

    def test_no_activation_or_no_restore_fails(self):
        self.assertFalse(drills.ordering_proof(self.events(self.GOOD[:5]))[0])
        self.assertFalse(drills.ordering_proof(self.events(self.GOOD[:3]))[0])
        self.assertFalse(drills.ordering_proof([])[0])

    def test_events_are_numbered_and_timestamped_in_order(self):
        rec = drills.Recorder(clock=lambda: 1_700_000_000)
        rec.event("a", True, x=1)
        rec.event("b")
        self.assertEqual([e["seq"] for e in rec.events], [1, 2])
        self.assertEqual(rec.events[0]["at"], "2023-11-14T22:13:20Z")
        self.assertNotIn("ok", rec.events[1])
        self.assertEqual(rec.first("b")["seq"], 2)


class FakeHost:
    """The recovery interface, recording every call so the order and the abort behaviour can be asserted."""

    def __init__(self, refuse=None, final_failed=()):
        self.calls, self.refuse, self.final_failed = [], refuse, final_failed

    def _check(self, name):
        self.calls.append(name)
        return self.refuse != name, {"name": name}

    def verify_set_integrity(self):
        return self._check("verify_set_integrity")

    def dry_run_restore(self):
        return self._check("dry_run_restore")

    def rehearse_restore(self):
        return self._check("rehearse_restore")

    def replace_live_database(self):
        self.calls.append("replace_live_database")
        return {}

    def restore_live(self):
        self.calls.append("restore_live")
        return {}

    def activate_prior(self):
        self.calls.append("activate_prior")
        return {}

    def final_gate(self):
        self.calls.append("final_gate")
        return [{"id": i, "passed": i not in self.final_failed, "detail": ""} for i in ("healthz", "totp_login")]


class RecoverySequenceTests(unittest.TestCase):
    def test_checks_run_in_order_before_any_live_change(self):
        host, rec = FakeHost(), drills.Recorder()
        drills.recovery_sequence(host, rec)
        self.assertEqual(host.calls, ["verify_set_integrity", "dry_run_restore", "rehearse_restore", "replace_live_database",
                                      "restore_live", "activate_prior", "final_gate"])
        self.assertTrue(drills.ordering_proof(rec.events)[0])

    def test_each_refusal_stops_before_the_live_stack_is_touched(self):
        live = {"replace_live_database", "restore_live", "activate_prior", "final_gate"}
        for refusal in ("verify_set_integrity", "dry_run_restore", "rehearse_restore"):
            host, rec = FakeHost(refuse=refusal), drills.Recorder()
            with self.assertRaises(drills.RecoveryRefused, msg=refusal):
                drills.recovery_sequence(host, rec)
            self.assertFalse(live & set(host.calls), refusal)
            self.assertFalse(drills.ordering_proof(rec.events)[0])

    def test_a_failing_final_gate_is_recorded_and_fails_the_proof_data(self):
        host, rec = FakeHost(final_failed=("totp_login",)), drills.Recorder()
        gate = drills.recovery_sequence(host, rec)
        self.assertEqual(drills.failed_checks(gate), ["totp_login"])
        self.assertFalse(rec.first("final_gate")["ok"])


def ops05_facts():
    tag = "v2026.46.1"
    return {"tag": tag, "image_ref": f"{drills.IMAGE}:{tag}", "template_changed_lines": 1,
            "compose_sha_before": "c" * 64, "compose_sha_after": "c" * 64,
            "ref_before": f"{drills.IMAGE}:{tag}", "ref_after": f"{drills.IMAGE}:{tag}",
            "registry_digest": DIGEST_A, "recorded_digest": DIGEST_A, "recorded_commit": COMMIT, "healthy": True,
            "container_image_id": "sha256:" + "1" * 64, "image_id": "sha256:" + "1" * 64,
            "container_config_image": f"{drills.IMAGE}:{tag}", "repo_digests": [DIGEST_A],
            "loaded_binary_sha256": "e" * 64, "image_binary_sha256": "e" * 64,
            "api_version": tag, "api_commit": COMMIT[:7], "cli_version": f"{tag.lstrip('v')} ({COMMIT[:7]}, 2026-11-13T02:41:15Z)"}


class Ops05Tests(unittest.TestCase):
    def test_good_facts_pass(self):
        self.assertEqual(drills.failed_checks(drills.ops05_checks(ops05_facts())), [])
        self.assertTrue(drills.verdict(drills.ops05_checks(ops05_facts()))["passed"])

    def test_each_wrong_fact_fails_exactly_its_check(self):
        cases = {
            "reference_pinned_by_tag": {"image_ref": f"{drills.IMAGE}:latest"},
            "compose_differs_from_beta_template_by_one_line": {"template_changed_lines": 2},
            "version_reference_unchanged_after_deploy": {"compose_sha_after": "d" * 64},
            "registry_digest_equals_release_record": {"registry_digest": DIGEST_B},
            "pulled_image_digest_equals_release_record": {"repo_digests": [DIGEST_B]},
            "container_runs_the_pulled_image": {"container_image_id": "sha256:" + "2" * 64},
            "container_reference_is_the_pinned_tag": {"container_config_image": f"{drills.IMAGE}:latest"},
            "loaded_binary_is_the_image_binary": {"loaded_binary_sha256": "f" * 64},
            "api_version_equals_release_tag": {"api_version": "v2026.45"},
            "api_commit_is_the_release_commit": {"api_commit": "1234567"},
            "cli_version_matches_release": {"cli_version": "dev (none, unknown)"},
            "healthy": {"healthy": False},
        }
        for check, change in cases.items():
            facts = ops05_facts()
            facts.update(change)
            failed = drills.failed_checks(drills.ops05_checks(facts))
            self.assertIn(check, failed, check)
        self.assertEqual(len(cases), len(drills.ops05_checks(ops05_facts())))

    def test_cli_version_parsing(self):
        self.assertEqual(drills.parse_cli_version("2026.46.1 (d0089c8, 2026-11-13T02:41:15Z)\n"), ("2026.46.1", "d0089c8"))
        self.assertEqual(drills.parse_cli_version("dev (none, unknown)"), ("dev", "none"))
        self.assertEqual(drills.parse_cli_version("garbage"), ("", ""))
        facts = ops05_facts()
        facts["cli_version"] = f"2026.46 ({COMMIT[:7]}, x)"
        self.assertIn("cli_version_matches_release", drills.failed_checks(drills.ops05_checks(facts)))
        facts["cli_version"] = "2026.46.1 (1234567, x)"
        self.assertIn("cli_version_matches_release", drills.failed_checks(drills.ops05_checks(facts)))

    def test_a_changed_reference_with_the_same_digest_still_fails_the_unchanged_check(self):
        facts = ops05_facts()
        facts["ref_after"] = f"{drills.IMAGE}:v2026.46"
        self.assertEqual(drills.failed_checks(drills.ops05_checks(facts)), ["version_reference_unchanged_after_deploy"])


def ops06_facts():
    rec = drills.Recorder(clock=lambda: 0)
    for kind in OrderingTests.GOOD:
        rec.event(kind, True)
    ledger = ["0001", "0002"]
    return {"baseline_failed": [], "precheck_set_ok": True, "ledger_delta": ["0015"], "candidate_digest_ok": True,
            "candidate_digest": DIGEST_B, "rollout_failed": ["totp_login"], "rollout_healthz": True,
            "rollback_refused": True, "rollback_healthy": False, "rollback_state": "{}",
            "negative_compat_refused": True, "negative_compat_detail": "{}",
            "negative_integrity_refused": True, "negative_integrity_detail": "{}",
            "negative_key_refused": True, "negative_key_detail": "{}", "events": rec.events, "final_failed": [],
            "final_digest_ok": True, "final_digest": DIGEST_A, "compose_sha_baseline": "c" * 64,
            "compose_sha_final": "c" * 64, "key_fp_baseline": "k" * 16, "key_fp_final": "k" * 16,
            "keyring_fp_baseline": "r" * 16, "keyring_fp_final": "r" * 16, "ledger_baseline": ledger,
            "ledger_final": list(ledger), "counts_baseline": {"users": 5, "reports": 1}, "counts_final": {"users": 5, "reports": 1}}


class Ops06Tests(unittest.TestCase):
    def test_good_facts_pass(self):
        self.assertEqual(drills.failed_checks(drills.ops06_checks(ops06_facts())), [])

    def test_each_wrong_fact_fails_its_check(self):
        cases = {
            "baseline_gate_passed": {"baseline_failed": ["totp_login"]},
            "recovery_set_verified_before_rollout": {"precheck_set_ok": False},
            "rollout_applied_new_steps": {"ledger_delta": []},
            "rollout_candidate_digest_verified": {"candidate_digest_ok": False},
            "rollout_refused_by_activation_gate": {"rollout_failed": []},
            "prior_build_refuses_the_migrated_database": {"rollback_healthy": True},
            "newer_dump_refused_by_prior_build": {"negative_compat_refused": False},
            "tampered_set_refused_by_integrity_check": {"negative_integrity_refused": False},
            "wrong_key_refused_by_rehearsal_gate": {"negative_key_refused": False},
            "final_gate_passed": {"final_failed": ["users"]},
            "prior_version_loaded_by_digest": {"final_digest_ok": False},
            "version_reference_restored": {"compose_sha_final": "d" * 64},
            "secret_key_unchanged": {"key_fp_final": "z" * 16},
            "keyring_unchanged": {"keyring_fp_final": "z" * 16},
            "ledger_equals_pre_rollout": {"ledger_final": ["0001", "0002", "0015"]},
            "data_rows_equal_pre_rollout": {"counts_final": {"users": 4, "reports": 1}},
        }
        for check, change in cases.items():
            facts = ops06_facts()
            facts.update(change)
            self.assertIn(check, drills.failed_checks(drills.ops06_checks(facts)), check)

    def test_a_rollout_that_failed_for_the_wrong_reason_is_not_accepted(self):
        facts = ops06_facts()
        facts["rollout_failed"] = ["totp_login", "users"]
        self.assertIn("rollout_refused_by_activation_gate", drills.failed_checks(drills.ops06_checks(facts)))

    def test_events_out_of_order_fail_the_ordering_check(self):
        facts = ops06_facts()
        rec = drills.Recorder(clock=lambda: 0)
        for kind in ("live_db_replaced", "recovery_set_integrity", "restore_dry_run_compat", "rehearsal_key_gate", "activation"):
            rec.event(kind, True)
        facts["events"] = rec.events
        self.assertEqual(drills.failed_checks(drills.ops06_checks(facts)), ["checks_precede_activation"])

    def test_check_names_are_unique(self):
        for checks in (drills.ops05_checks(ops05_facts()), drills.ops06_checks(ops06_facts())):
            ids = [c["id"] for c in checks]
            self.assertEqual(len(ids), len(set(ids)))


class EvidenceTests(unittest.TestCase):
    ENV = {"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": drills.REPOSITORY, "GITHUB_EVENT_NAME": "workflow_dispatch",
           "GITHUB_SHA": COMMIT, "GITHUB_RUN_ID": "42", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_REF_NAME": "main"}

    def test_a_clean_run_has_no_blockers_and_a_url(self):
        self.assertEqual(drills.evidence_blockers(self.ENV), [])
        self.assertEqual(drills.run_block(self.ENV)["url"], f"https://github.com/{drills.REPOSITORY}/actions/runs/42")

    def test_blockers(self):
        for change in ({"GITHUB_ACTIONS": ""}, {"GITHUB_REPOSITORY": "someone/else"}, {"GITHUB_EVENT_NAME": "schedule"},
                       {"GITHUB_SHA": "abc"}, {"OPS_DRILL_RELEASE_TAG": "v2026.40"}):
            env = dict(self.ENV, **change)
            self.assertTrue(drills.evidence_blockers(env), change)
        self.assertEqual(drills.evidence_blockers(dict(self.ENV, GITHUB_EVENT_NAME="push")), [])

    def test_the_verdict_lists_what_failed(self):
        checks = [drills.check_result("a", True), drills.check_result("b", False), drills.check_result("c", False)]
        self.assertEqual(drills.verdict(checks), {"passed": False, "result": "failed", "failed_checks": ["b", "c"]})

    def test_evidence_is_written_without_secrets(self):
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "e.json"
            facts = ops05_facts()
            doc = drills.write_evidence(str(path), "OPS-05", drills.ops05_checks(facts), facts, self.ENV, {"host": {}})
            on_disk = json.loads(path.read_text(encoding="utf-8"))
            self.assertEqual(on_disk["row"], "OPS-05")
            self.assertTrue(on_disk["passed"])
            self.assertEqual(on_disk, json.loads(json.dumps(doc)))
            self.assertNotRegex(path.read_text(encoding="utf-8"), re.compile(r"password|secret_key\"?:\s*\"[0-9a-f]{20}", re.I))


class WorkflowStaticTests(unittest.TestCase):
    """The workflow must not be able to publish or hold secrets, whatever the script does."""

    def setUp(self):
        self.text = (REPO / ".github/workflows/ops-deployment-drills.yml").read_text(encoding="utf-8")

    def test_no_secrets_and_no_registry_login_or_push_to_ghcr(self):
        self.assertNotIn("secrets.", self.text.replace("secrets.GITHUB_TOKEN", ""))
        self.assertNotRegex(self.text, r"docker/login-action|docker login")
        self.assertNotRegex(self.text, r"docker push ghcr\.io")

    def test_permissions_are_read_only(self):
        self.assertRegex(self.text, r"(?m)^permissions:\n  contents: read\n")
        self.assertNotRegex(self.text, r"(?m)^\s+(packages|contents|id-token|actions):\s*write")


if __name__ == "__main__":
    unittest.main()
