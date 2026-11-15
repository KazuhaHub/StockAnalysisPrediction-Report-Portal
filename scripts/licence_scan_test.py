"""Tests for the SBOM licence gate (scripts/licence_scan.py) and its checked-in policy."""
import json
import os
import subprocess
import sys
import tempfile
import unittest

from licence_scan import evaluate, load_policy, parse_expression, ExpressionError

HERE = os.path.dirname(os.path.abspath(__file__))

POLICY = {
    "allow": ["MIT", "BSD-3-Clause", "ISC", "Apache-2.0", "MPL-2.0", "0BSD"],
    "deny": ["GPL-2.0-only", "SSPL-1.0", "BUSL-1.1", "Commons-Clause", "EPL-2.0"],
    "own": {"licences": ["AGPL-3.0-only"], "components": ["github.com/KazuhaHub/"]},
    "election_only": ["FTL"],
    "elections": [
        {"component": "dompurify", "declared": "MPL-2.0 OR Apache-2.0", "elected": "Apache-2.0", "reason": "dual"},
        {"component": "github.com/golang/freetype", "declared": "GPL-2.0-or-later", "elected": "FTL", "reason": "dual"},
    ],
    "exclusions": [
        {"component": "elkjs", "requires": "elk-build-test", "reason": "ELK layout aliased to an empty module"},
    ],
}


def comp(name, lic=None, expr=None, group=None, version="1.0.0", lname=None):
    c = {"type": "library", "name": name, "version": version}
    if group:
        c["group"] = group
    if expr is not None:
        c["licenses"] = [{"expression": expr}]
    elif lname is not None:
        c["licenses"] = [{"license": {"name": lname}}]
    elif lic:
        ids = [lic] if isinstance(lic, str) else lic
        c["licenses"] = [{"license": {"id": i}} for i in ids]
    return c


def run(components, verified=(), policy=POLICY):
    return evaluate({"components": components}, policy, set(verified))


def one(components, **kw):
    r = run(components, **kw)
    return r["components"][0], r


class ExpressionTests(unittest.TestCase):
    def test_single_id(self):
        self.assertEqual(parse_expression("MIT"), ("id", "MIT"))

    def test_or_and_precedence(self):
        # AND binds tighter than OR.
        self.assertEqual(
            parse_expression("MIT OR ISC AND Apache-2.0"),
            ("or", [("id", "MIT"), ("and", [("id", "ISC"), ("id", "Apache-2.0")])]),
        )

    def test_parentheses(self):
        self.assertEqual(
            parse_expression("(MIT OR ISC) AND Apache-2.0"),
            ("and", [("or", [("id", "MIT"), ("id", "ISC")]), ("id", "Apache-2.0")]),
        )

    def test_with_exception_stays_one_atom(self):
        self.assertEqual(parse_expression("GPL-2.0-only WITH Classpath-exception-2.0"),
                         ("id", "GPL-2.0-only WITH Classpath-exception-2.0"))

    def test_operators_are_case_insensitive(self):
        self.assertEqual(parse_expression("MIT or ISC"), ("or", [("id", "MIT"), ("id", "ISC")]))

    def test_malformed_expression_raises(self):
        for bad in ["", "MIT OR", "(MIT", "MIT ISC", "AND MIT", "MIT )"]:
            with self.assertRaises(ExpressionError, msg=bad):
                parse_expression(bad)


class VerdictTests(unittest.TestCase):
    def test_allowed(self):
        c, r = one([comp("left-pad", "MIT")])
        self.assertEqual(c["status"], "pass")
        self.assertTrue(r["ok"])

    def test_allow_list_is_case_insensitive(self):
        c, _ = one([comp("x", expr="mit")])
        self.assertEqual(c["status"], "pass")

    def test_denied(self):
        c, r = one([comp("evil", "GPL-2.0-only")])
        self.assertEqual(c["status"], "fail")
        self.assertEqual(c["reason"], "denied")
        self.assertFalse(r["ok"])

    def test_denied_licence_in_deny_list_but_not_gpl(self):
        for lic in ["SSPL-1.0", "BUSL-1.1", "Commons-Clause", "EPL-2.0"]:
            c, _ = one([comp("p", lic)])
            self.assertEqual((c["status"], c["reason"]), ("fail", "denied"), lic)

    def test_unknown_licence_fails(self):
        c, r = one([comp("mystery", "Some-Odd-1.0")])
        self.assertEqual((c["status"], c["reason"]), ("fail", "unknown"))
        self.assertFalse(r["ok"])

    def test_licence_outside_both_lists_is_unknown_not_allowed(self):
        # LGPL-3.0-only is on neither list: the gate fails closed.
        c, _ = one([comp("p", "LGPL-3.0-only")])
        self.assertEqual((c["status"], c["reason"]), ("fail", "unknown"))

    def test_no_licence_at_all_fails(self):
        c, _ = one([comp("silent")])
        self.assertEqual((c["status"], c["reason"]), ("fail", "no-licence"))

    def test_name_only_licence_is_not_guessed(self):
        c, _ = one([comp("p", lname="Some custom terms")])
        self.assertEqual((c["status"], c["reason"]), ("fail", "unknown"))

    def test_name_only_licence_that_is_an_allowed_id_passes(self):
        c, _ = one([comp("p", lname="MIT")])
        self.assertEqual(c["status"], "pass")

    def test_malformed_expression_fails_closed(self):
        c, _ = one([comp("p", expr="MIT OR")])
        self.assertEqual((c["status"], c["reason"]), ("fail", "unparseable"))

    def test_gate_fails_when_any_component_fails(self):
        r = run([comp("a", "MIT"), comp("b", "SSPL-1.0"), comp("c", "ISC")])
        self.assertFalse(r["ok"])
        self.assertEqual([c["name"] for c in r["failures"]], ["b"])

    def test_empty_sbom_fails(self):
        # An SBOM with no components means the generator broke, not that the tree is clean.
        r = evaluate({"components": []}, POLICY, set())
        self.assertFalse(r["ok"])
        self.assertIn("no components", r["problems"][0])


class ExpressionVerdictTests(unittest.TestCase):
    def test_or_passes_when_any_choice_allowed(self):
        c, _ = one([comp("p", expr="GPL-2.0-only OR MIT")])
        self.assertEqual(c["status"], "pass")

    def test_or_fails_when_no_choice_allowed(self):
        c, _ = one([comp("p", expr="GPL-2.0-only OR SSPL-1.0")])
        self.assertEqual((c["status"], c["reason"]), ("fail", "denied"))

    def test_or_with_unknown_and_denied_reports_unknown_or_denied(self):
        c, _ = one([comp("p", expr="GPL-2.0-only OR Weird-1.0")])
        self.assertEqual(c["status"], "fail")

    def test_and_needs_every_term_allowed(self):
        c, _ = one([comp("p", expr="MIT AND GPL-2.0-only")])
        self.assertEqual((c["status"], c["reason"]), ("fail", "denied"))
        c, _ = one([comp("p", expr="MIT AND ISC")])
        self.assertEqual(c["status"], "pass")

    def test_nested(self):
        c, _ = one([comp("p", expr="(GPL-2.0-only AND MIT) OR (ISC AND Apache-2.0)")])
        self.assertEqual(c["status"], "pass")

    def test_several_licence_entries_are_conjunctive(self):
        # CycloneDX lists every detected licence of a component; all of them apply.
        c, _ = one([comp("p", ["MIT", "GPL-2.0-only"])])
        self.assertEqual((c["status"], c["reason"]), ("fail", "denied"))
        c, _ = one([comp("p", ["MIT", "ISC"])])
        self.assertEqual(c["status"], "pass")

    def test_with_exception_is_not_allowed_by_its_base(self):
        c, _ = one([comp("p", expr="MIT WITH Some-Exception")])
        self.assertEqual((c["status"], c["reason"]), ("fail", "unknown"))


class OwnComponentTests(unittest.TestCase):
    def test_own_agpl_passes(self):
        c, _ = one([comp("github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol", "AGPL-3.0-only")])
        self.assertEqual((c["status"], c["via"]), ("pass", "own"))

    def test_third_party_agpl_fails(self):
        c, _ = one([comp("github.com/someone/else", "AGPL-3.0-only")])
        self.assertEqual(c["status"], "fail")

    def test_own_prefix_must_match_whole_namespace(self):
        c, _ = one([comp("github.com/KazuhaHubEvil/x", "AGPL-3.0-only")])
        self.assertEqual(c["status"], "fail")

    def test_own_component_still_cannot_carry_a_denied_licence(self):
        c, _ = one([comp("github.com/KazuhaHub/x", "GPL-2.0-only")])
        self.assertEqual(c["status"], "fail")

    def test_scoped_npm_name_uses_group(self):
        policy = dict(POLICY, own={"licences": ["AGPL-3.0-only"], "components": ["@report-portal/"]})
        c, _ = one([comp("sdk", "AGPL-3.0-only", group="@report-portal")], policy=policy)
        self.assertEqual((c["status"], c["via"]), ("pass", "own"))


class ElectionTests(unittest.TestCase):
    def test_dompurify_elected_apache(self):
        c, _ = one([comp("dompurify", expr="(MPL-2.0 OR Apache-2.0)")])
        self.assertEqual(c["status"], "pass")
        self.assertEqual(c["via"], "election")
        self.assertEqual(c["elected"], "Apache-2.0")

    def test_election_matches_regardless_of_parentheses_and_case(self):
        c, _ = one([comp("dompurify", expr="Apache-2.0 or MPL-2.0")])
        # Operand order differs from the recorded declaration: the election is stale, but both
        # choices are allowed on their merits so the component still passes.
        self.assertEqual(c["status"], "pass")

    def test_freetype_needs_its_election(self):
        c, _ = one([comp("github.com/golang/freetype", "GPL-2.0-or-later")])
        self.assertEqual(c["status"], "pass")
        self.assertEqual((c["via"], c["elected"]), ("election", "FTL"))

    def test_freetype_fails_without_election_record(self):
        policy = dict(POLICY, elections=[])
        c, _ = one([comp("github.com/golang/freetype", "GPL-2.0-or-later")], policy=policy)
        self.assertEqual(c["status"], "fail")

    def test_election_only_licence_is_not_a_free_choice(self):
        # FTL offered in an OR with only another unallowed choice: allowed only by election.
        c, _ = one([comp("other", expr="GPL-2.0-only OR FTL")])
        self.assertEqual((c["status"], c["reason"]), ("fail", "election-required"))

    def test_election_only_licence_alone_requires_election(self):
        c, _ = one([comp("other", "FTL")])
        self.assertEqual((c["status"], c["reason"]), ("fail", "election-required"))

    def test_election_only_licence_passes_next_to_an_allowed_choice(self):
        c, _ = one([comp("other", expr="FTL OR MIT")])
        self.assertEqual(c["status"], "pass")

    def test_stale_election_is_ignored_and_reported(self):
        # Upstream changed its declaration: the recorded election no longer describes it.
        c, r = one([comp("github.com/golang/freetype", "GPL-3.0-only")])
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("freetype" in w and "stale" in w for w in r["warnings"]))

    def test_election_may_not_elect_a_denied_licence(self):
        policy = dict(POLICY, elections=[{"component": "p", "declared": "SSPL-1.0 OR MIT", "elected": "SSPL-1.0", "reason": "x"}])
        c, _ = one([comp("p", expr="SSPL-1.0 OR MIT")], policy=policy)
        # Passes on the merits (MIT) but not via the election.
        self.assertEqual(c["status"], "pass")
        self.assertNotEqual(c.get("via"), "election")
        c, _ = one([comp("p", "SSPL-1.0")], policy=dict(policy, elections=[{"component": "p", "declared": "SSPL-1.0", "elected": "SSPL-1.0", "reason": "x"}]))
        self.assertEqual(c["status"], "fail")

    def test_unused_election_is_a_warning_not_a_failure(self):
        r = run([comp("left-pad", "MIT")])
        self.assertTrue(r["ok"])
        self.assertTrue(any("dompurify" in w and "not present" in w for w in r["warnings"]))


class ExclusionTests(unittest.TestCase):
    def test_exclusion_valid_while_the_check_passed(self):
        c, r = one([comp("elkjs", "EPL-2.0")], verified={"elk-build-test"})
        self.assertEqual((c["status"], c["via"]), ("pass", "exclusion"))
        self.assertTrue(r["ok"])

    def test_exclusion_invalid_when_the_check_did_not_pass(self):
        c, r = one([comp("elkjs", "EPL-2.0")], verified=set())
        self.assertEqual((c["status"], c["reason"]), ("fail", "exclusion-unverified"))
        self.assertFalse(r["ok"])
        self.assertIn("elk-build-test", c["detail"])

    def test_exclusion_applies_to_every_version(self):
        c, _ = one([comp("elkjs", "EPL-2.0", version="0.10.0")], verified={"elk-build-test"})
        self.assertEqual(c["via"], "exclusion")

    def test_exclusion_does_not_leak_to_other_packages(self):
        c, _ = one([comp("elk", "EPL-2.0")], verified={"elk-build-test"})
        self.assertEqual((c["status"], c["reason"]), ("fail", "denied"))

    def test_exclusion_matches_the_full_group_name(self):
        c, _ = one([comp("elkjs", "EPL-2.0", group="@evil")], verified={"elk-build-test"})
        self.assertEqual(c["status"], "fail")

    def test_unknown_verification_name_does_not_satisfy(self):
        c, _ = one([comp("elkjs", "EPL-2.0")], verified={"something-else"})
        self.assertEqual(c["status"], "fail")


class CliTests(unittest.TestCase):
    def scan(self, sbom, *extra):
        with tempfile.TemporaryDirectory() as d:
            sp = os.path.join(d, "sbom.json")
            pp = os.path.join(d, "policy.json")
            with open(sp, "w") as f:
                json.dump(sbom, f)
            with open(pp, "w") as f:
                json.dump(POLICY, f)
            p = subprocess.run([sys.executable, "-I", os.path.join(HERE, "licence_scan.py"), "--policy", pp, *extra, sp],
                               capture_output=True, text=True)
            return p

    def test_exit_zero_and_summary_when_clean(self):
        p = self.scan({"components": [comp("a", "MIT")]})
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("1 component", p.stdout)

    def test_exit_one_and_report_names_the_offender(self):
        p = self.scan({"components": [comp("a", "MIT"), comp("bad", "SSPL-1.0", version="9.9.9")]})
        self.assertEqual(p.returncode, 1)
        self.assertIn("bad", p.stdout)
        self.assertIn("9.9.9", p.stdout)
        self.assertIn("SSPL-1.0", p.stdout)

    def test_verified_flag_enables_exclusion(self):
        sbom = {"components": [comp("elkjs", "EPL-2.0")]}
        self.assertEqual(self.scan(sbom).returncode, 1)
        self.assertEqual(self.scan(sbom, "--verified", "elk-build-test").returncode, 0)

    def test_evidence_licences_are_read_when_none_are_asserted(self):
        c = comp("a")
        c["evidence"] = {"licenses": [{"license": {"id": "MIT"}}]}
        self.assertEqual(self.scan({"components": [c]}).returncode, 0)

    def test_unreadable_sbom_is_exit_two(self):
        with tempfile.TemporaryDirectory() as d:
            sp = os.path.join(d, "sbom.json")
            with open(sp, "w") as f:
                f.write("not json")
            pp = os.path.join(d, "policy.json")
            with open(pp, "w") as f:
                json.dump(POLICY, f)
            p = subprocess.run([sys.executable, "-I", os.path.join(HERE, "licence_scan.py"), "--policy", pp, sp],
                               capture_output=True, text=True)
            self.assertEqual(p.returncode, 2)

    def test_json_report_is_written(self):
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "r.json")
            p = self.scan({"components": [comp("a", "MIT")]}, "--report", out)
            self.assertEqual(p.returncode, 0)
            with open(out) as f:
                rep = json.load(f)
            self.assertTrue(rep["ok"])


class CheckedInPolicyTests(unittest.TestCase):
    """The policy file in the repository is the decision of doc 167 section 8."""

    @classmethod
    def setUpClass(cls):
        cls.policy = load_policy(os.path.join(HERE, "licence-policy.json"))

    def test_allow_list_is_exactly_the_owner_list(self):
        self.assertEqual(
            sorted(self.policy["allow"]),
            sorted(["MIT", "BSD-2-Clause", "BSD-3-Clause", "ISC", "Apache-2.0", "0BSD", "Zlib", "Unlicense",
                    "CC0-1.0", "BlueOak-1.0.0", "Python-2.0", "MPL-2.0"]),
        )

    def test_denied_families_are_listed(self):
        for lic in ["GPL-2.0-only", "SSPL-1.0", "BUSL-1.1", "Commons-Clause", "EPL-1.0", "EPL-2.0"]:
            self.assertIn(lic, self.policy["deny"])

    def test_nothing_is_both_allowed_and_denied(self):
        self.assertFalse(set(self.policy["allow"]) & set(self.policy["deny"]))

    def test_elections_are_the_two_owner_decisions(self):
        got = {e["component"]: e["elected"] for e in self.policy["elections"]}
        self.assertEqual(got, {"dompurify": "Apache-2.0", "github.com/golang/freetype": "FTL"})

    def test_only_exclusion_is_elkjs_gated_on_the_build_test(self):
        self.assertEqual([(e["component"], e["requires"]) for e in self.policy["exclusions"]],
                         [("elkjs", "elk-build-test")])

    def test_every_election_has_a_reason(self):
        for e in self.policy["elections"] + self.policy["exclusions"]:
            self.assertTrue(e.get("reason"))

    def test_elected_licence_is_allowed_or_election_only(self):
        for e in self.policy["elections"]:
            self.assertTrue(e["elected"] in self.policy["allow"] or e["elected"] in self.policy["election_only"])

    def test_own_licence_is_agpl_only(self):
        self.assertEqual(self.policy["own"]["licences"], ["AGPL-3.0-only"])


if __name__ == "__main__":
    unittest.main()
