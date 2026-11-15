"""Tests for the SBOM licence gate (scripts/licence_scan.py) and its checked-in policy."""
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest

import licence_scan
from licence_scan import evaluate, load_policy, parse_expression, ExpressionError

HERE = os.path.dirname(os.path.abspath(__file__))

APACHE_TEXT = b"Apache License\nVersion 2.0, January 2004\n"
MPL_TEXT = b"Mozilla Public License Version 2.0\n"
FREETYPE_TEXT = b"Use of the Freetype-Go software is subject to your choice of exactly one of\nthe following two licenses:\n"


def sha(data):
    return hashlib.sha256(data).hexdigest()


POLICY = {
    "allow": ["MIT", "BSD-3-Clause", "ISC", "Apache-2.0", "MPL-2.0", "0BSD"],
    "deny": ["GPL-2.0-only", "SSPL-1.0", "BUSL-1.1", "Commons-Clause", "EPL-2.0", "GPL-3.0-only"],
    "own": {"licences": ["AGPL-3.0-only"], "components": ["github.com/KazuhaHub/"]},
    "election_only": ["FTL"],
    "elections": [
        {"component": "dompurify", "declared": "expression:(MPL-2.0 OR Apache-2.0)", "offered": "MPL-2.0 OR Apache-2.0",
         "elected": "Apache-2.0", "reason": "dual",
         "evidence": [{"file": "LICENSE", "sha256": sha(APACHE_TEXT)}, {"file": "LICENSE-MPL", "sha256": sha(MPL_TEXT)}]},
        {"component": "github.com/golang/freetype", "declared": "id:GPL-2.0-or-later",
         "offered": "GPL-2.0-or-later OR FTL", "elected": "FTL", "reason": "dual",
         "evidence": [{"file": "LICENSE", "sha256": sha(FREETYPE_TEXT)}]},
    ],
    "exclusions": [
        {"component": "elkjs", "requires": "elk-build-test", "reason": "ELK layout aliased to an empty module"},
    ],
}

# The licence files the two elections were pinned to, as a reader would find them.
ELECTION_FILES = {("dompurify", "LICENSE"): APACHE_TEXT, ("dompurify", "LICENSE-MPL"): MPL_TEXT,
                  ("github.com/golang/freetype", "LICENSE"): FREETYPE_TEXT}


def comp(name, lic=None, expr=None, group=None, version="1.0.0", lname=None, purl=None):
    c = {"type": "library", "name": name, "version": version}
    if purl:
        c["purl"] = purl
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


def run(components, verified=(), policy=POLICY, files=None):
    files = ELECTION_FILES if files is None else files
    reader = lambda c, name: files.get((c["name"], name))  # noqa: E731
    return evaluate({"components": components}, policy, set(verified), reader)


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


def golang(name, lic="AGPL-3.0-only"):
    return comp(name, lic, purl=f"pkg:golang/{name}@v1.0.0")


class OwnComponentTests(unittest.TestCase):
    def test_own_agpl_passes(self):
        c, _ = one([golang("github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol")])
        self.assertEqual((c["status"], c["via"]), ("pass", "own"))

    def test_third_party_agpl_fails(self):
        c, _ = one([golang("github.com/someone/else")])
        self.assertEqual(c["status"], "fail")

    def test_own_prefix_must_match_whole_namespace(self):
        c, _ = one([golang("github.com/KazuhaHubEvil/x")])
        self.assertEqual(c["status"], "fail")

    def test_own_component_still_cannot_carry_a_denied_licence(self):
        c, _ = one([golang("github.com/KazuhaHub/x", "GPL-2.0-only")])
        self.assertEqual(c["status"], "fail")

    def test_a_component_with_no_purl_is_not_own(self):
        c, _ = one([comp("github.com/KazuhaHub/x", "AGPL-3.0-only")])
        self.assertEqual(c["status"], "fail")

    def test_a_purl_of_another_ecosystem_is_not_own_for_a_go_module_name(self):
        c, _ = one([comp("github.com/KazuhaHub/x", "AGPL-3.0-only", purl="pkg:pypi/x@1")])
        self.assertEqual(c["status"], "fail")


class ElectionTests(unittest.TestCase):
    def test_dompurify_elected_apache(self):
        c, _ = one([comp("dompurify", expr="(MPL-2.0 OR Apache-2.0)")])
        self.assertEqual(c["status"], "pass")
        self.assertEqual(c["via"], "election")
        self.assertEqual(c["elected"], "Apache-2.0")

    def test_a_declaration_written_differently_is_stale_but_still_passes_on_its_merits(self):
        # Exact match: no parentheses and a different order is another declaration. Both choices are
        # allowed on their merits, so the component still passes, but not through the election.
        c, r = one([comp("dompurify", expr="Apache-2.0 OR MPL-2.0")])
        self.assertEqual((c["status"], c["via"]), ("pass", "allow"))
        self.assertTrue(any("stale election" in w for w in r["warnings"]))

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


GO_TEXT = b"Copyright 2009 The Go Authors.\n\nRedistribution and use in source and binary forms...\n"
OTHER_TEXT = GO_TEXT + b"An extra clause.\n"
REVIEWED_POLICY = dict(POLICY, reviewed=[
    {"components": ["golang.org/x/sys", "golang.org/x/text"], "reviewed_as": "BSD-3-Clause", "file": "LICENSE",
     "declared": "id:BSD-Source-Code",
     "sha256": hashlib.sha256(GO_TEXT).hexdigest(), "reason": "Standard Go BSD 3-clause text."},
    {"components": ["khroma"], "reviewed_as": "MIT", "file": "license", "declared": "",
     "sha256": hashlib.sha256(b"The MIT License (MIT)\n").hexdigest(), "reason": "MIT text, no manifest field."},
])


class ReviewedTests(unittest.TestCase):
    """A reviewed-as record stands for the licence the owner read, pinned to the text that ships."""

    def check(self, component, files, policy=REVIEWED_POLICY):
        return one([component], policy=policy, files=files)

    def test_matching_hash_passes(self):
        c, r = self.check(comp("golang.org/x/sys", "BSD-Source-Code"), {("golang.org/x/sys", "LICENSE"): GO_TEXT})
        self.assertEqual((c["status"], c["via"], c["reviewed_as"]), ("pass", "reviewed", "BSD-3-Clause"))
        self.assertTrue(r["ok"])

    def test_changed_text_fails_and_says_stale(self):
        c, r = self.check(comp("golang.org/x/sys", "BSD-Source-Code"), {("golang.org/x/sys", "LICENSE"): OTHER_TEXT})
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("stale" in w and "golang.org/x/sys" in w for w in r["warnings"]))

    def test_missing_licence_file_fails(self):
        c, r = self.check(comp("golang.org/x/sys", "BSD-Source-Code"), {})
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("golang.org/x/sys" in w and "not found" in w for w in r["warnings"]))

    def test_no_reader_means_no_file_and_so_a_failure(self):
        c, _ = one([comp("golang.org/x/sys", "BSD-Source-Code")], policy=REVIEWED_POLICY)
        self.assertEqual(c["status"], "fail")

    def test_unlisted_component_fails_even_with_the_same_text(self):
        c, _ = self.check(comp("golang.org/x/other", "BSD-Source-Code"), {("golang.org/x/other", "LICENSE"): GO_TEXT})
        self.assertEqual(c["status"], "fail")

    def test_one_hash_covers_every_listed_module(self):
        r = run([comp("golang.org/x/sys", "BSD-Source-Code"), comp("golang.org/x/text", "BSD-Source-Code")],
                policy=REVIEWED_POLICY,
                files={("golang.org/x/sys", "LICENSE"): GO_TEXT, ("golang.org/x/text", "LICENSE"): GO_TEXT})
        self.assertTrue(r["ok"])

    def test_each_module_is_hashed_on_its_own(self):
        r = run([comp("golang.org/x/sys", "BSD-Source-Code"), comp("golang.org/x/text", "BSD-Source-Code")],
                policy=REVIEWED_POLICY,
                files={("golang.org/x/sys", "LICENSE"): GO_TEXT, ("golang.org/x/text", "LICENSE"): OTHER_TEXT})
        self.assertEqual([c["name"] for c in r["failures"]], ["golang.org/x/text"])

    def test_record_applies_to_a_component_with_no_declared_licence(self):
        c, _ = self.check(comp("khroma"), {("khroma", "license"): b"The MIT License (MIT)\n"})
        self.assertEqual((c["status"], c["via"], c["reviewed_as"]), ("pass", "reviewed", "MIT"))

    def test_record_cannot_override_a_denied_declaration(self):
        pol = dict(REVIEWED_POLICY, reviewed=[dict(REVIEWED_POLICY["reviewed"][0], declared="id:GPL-2.0-only")])
        c, r = self.check(comp("golang.org/x/sys", "GPL-2.0-only"), {("golang.org/x/sys", "LICENSE"): GO_TEXT}, pol)
        self.assertEqual((c["status"], c["reason"]), ("fail", "denied"))
        self.assertTrue(any("denied" in w for w in r["warnings"]))

    def test_record_must_review_to_an_allowed_licence(self):
        policy = dict(REVIEWED_POLICY, reviewed=[dict(REVIEWED_POLICY["reviewed"][0], reviewed_as="LGPL-3.0-only")])
        c, r = self.check(comp("golang.org/x/sys", "BSD-Source-Code"), {("golang.org/x/sys", "LICENSE"): GO_TEXT}, policy)
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("not allowed" in w for w in r["warnings"]))

    def test_unused_record_is_a_warning(self):
        r = run([comp("left-pad", "MIT")], policy=REVIEWED_POLICY, files={})
        self.assertTrue(r["ok"])
        self.assertTrue(any("khroma" in w and "not present" in w for w in r["warnings"]))

    def test_an_allowed_component_needs_no_record_or_file(self):
        c, _ = self.check(comp("golang.org/x/sys", "BSD-3-Clause"), {})
        self.assertEqual((c["status"], c["via"]), ("pass", "allow"))

    def test_cli_reads_the_go_module_cache_and_node_modules(self):
        with tempfile.TemporaryDirectory() as d:
            mod = os.path.join(d, "gomod", "golang.org", "x", "sys@v0.1.0")
            os.makedirs(mod)
            with open(os.path.join(mod, "LICENSE"), "wb") as f:
                f.write(GO_TEXT)
            nm = os.path.join(d, "node_modules", "khroma")
            os.makedirs(nm)
            with open(os.path.join(nm, "license"), "wb") as f:
                f.write(b"The MIT License (MIT)\n")
            with open(os.path.join(nm, "package.json"), "w") as f:
                f.write('{"version": "2.1.0"}')
            sbom = {"components": [
                {"name": "golang.org/x/sys", "version": "v0.1.0", "purl": "pkg:golang/golang.org/x/sys@v0.1.0?type=module",
                 "licenses": [{"license": {"id": "BSD-Source-Code"}}]},
                {"name": "khroma", "version": "2.1.0", "purl": "pkg:npm/khroma@2.1.0"}]}
            sp, pp = os.path.join(d, "s.json"), os.path.join(d, "p.json")
            with open(sp, "w") as f:
                json.dump(sbom, f)
            with open(pp, "w") as f:
                json.dump(REVIEWED_POLICY, f)
            args = [sys.executable, "-I", os.path.join(HERE, "licence_scan.py"), "--policy", pp,
                    "--go-mod-cache", os.path.join(d, "gomod"), "--node-modules", os.path.join(d, "node_modules"), sp]
            p = subprocess.run(args, capture_output=True, text=True)
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
            self.assertIn("2 reviewed", p.stdout)
            with open(os.path.join(mod, "LICENSE"), "ab") as f:
                f.write(b"tampered")
            p = subprocess.run(args, capture_output=True, text=True)
            self.assertEqual(p.returncode, 1)
            self.assertIn("golang.org/x/sys", p.stdout)

    def test_go_module_cache_escapes_capital_letters(self):
        with tempfile.TemporaryDirectory() as d:
            mod = os.path.join(d, "github.com", "!azure", "x@v1.0.0")
            os.makedirs(mod)
            with open(os.path.join(mod, "LICENSE"), "wb") as f:
                f.write(GO_TEXT)
            reader = licence_scan.make_reader(go_mod_cache=d)
            comp_ = {"name": "github.com/Azure/x", "version": "v1.0.0", "purl": "pkg:golang/github.com/Azure/x@v1.0.0"}
            self.assertEqual(reader(comp_, "LICENSE"), GO_TEXT)

    def test_reader_refuses_to_leave_the_package_directory(self):
        with tempfile.TemporaryDirectory() as d:
            os.makedirs(os.path.join(d, "node_modules", "p"))
            with open(os.path.join(d, "node_modules", "p", "package.json"), "w") as f:
                f.write('{"version": "1"}')
            with open(os.path.join(d, "node_modules", "secret"), "wb") as f:
                f.write(b"x")
            reader = licence_scan.make_reader(node_modules=os.path.join(d, "node_modules"))
            self.assertIsNone(reader({"name": "p", "version": "1", "purl": "pkg:npm/p@1"}, "../secret"))



def with_children(parent, *children):
    parent = dict(parent)
    parent["components"] = [dict(c) for c in children]
    return parent


def ref(c, bom_ref):
    return dict(c, **{"bom-ref": bom_ref})


class NestedComponentTests(unittest.TestCase):
    """cyclonedx-npm nests a package that is not hoisted inside its parent's components."""

    def test_nested_denied_package_under_an_allowed_parent_fails(self):
        parent = with_children(ref(comp("parent", "MIT"), "root|parent"), ref(comp("child", "GPL-2.0-only"), "root|parent|child"))
        r = run([parent])
        self.assertFalse(r["ok"])
        self.assertEqual([c["name"] for c in r["failures"]], ["child"])
        self.assertEqual(len(r["components"]), 2)

    def test_nesting_is_followed_to_any_depth(self):
        deep = ref(comp("c", "SSPL-1.0"), "r|a|b|c")
        tree = with_children(ref(comp("a", "MIT"), "r|a"), with_children(ref(comp("b", "MIT"), "r|a|b"), deep))
        r = run([tree])
        self.assertEqual([c["name"] for c in r["failures"]], ["c"])
        self.assertEqual(len(r["components"]), 3)

    def test_the_same_bom_ref_is_judged_once(self):
        child = ref(comp("tslib", "0BSD"), "r|tslib@2")
        r = run([ref(with_children(comp("a", "MIT"), child), "r|a"), ref(with_children(comp("b", "MIT"), child), "r|b")])
        self.assertEqual(len(r["components"]), 3)

    def test_copies_with_one_bom_ref_but_different_licences_are_each_judged(self):
        ok, bad = ref(comp("tslib", "MIT"), "r|tslib@2"), ref(comp("tslib", "GPL-2.0-only"), "r|tslib@2")
        r = run([with_children(comp("a", "MIT"), ok), with_children(comp("b", "MIT"), bad)])
        self.assertFalse(r["ok"])
        self.assertEqual([c["name"] for c in r["failures"]], ["tslib"])
        self.assertEqual(len(r["components"]), 4)

    def test_copies_with_one_bom_ref_and_the_same_licence_data_are_judged_once(self):
        a, b = ref(comp("tslib", "MIT"), "r|tslib@2"), ref(comp("tslib", "MIT"), "r|tslib@2")
        self.assertEqual(len(run([a, b])["components"]), 1)

    def test_distinct_bom_refs_of_one_package_are_each_judged(self):
        r = run([ref(with_children(comp("a", "MIT"), ref(comp("katex", "MIT"), "r|a|katex")), "r|a"),
                 ref(comp("katex", "MIT"), "r|katex")])
        self.assertEqual(len(r["components"]), 3)

    def test_a_nested_component_can_use_an_exclusion_or_election(self):
        r = run([with_children(comp("host", "MIT"), comp("elkjs", "EPL-2.0"))], verified={"elk-build-test"})
        self.assertTrue(r["ok"])

    def test_cli_counts_nested_components_across_sboms(self):
        with tempfile.TemporaryDirectory() as d:
            pp = os.path.join(d, "p.json")
            with open(pp, "w") as f:
                json.dump(POLICY, f)
            excerpt = os.path.join(HERE, "testdata", "nested_real_excerpt.json")
            p = subprocess.run([sys.executable, "-I", os.path.join(HERE, "licence_scan.py"), "--policy", pp, excerpt],
                               capture_output=True, text=True)
            self.assertEqual(p.returncode, 0, p.stdout)
            # echarts+tslib, katex+commander, mermaid+katex, rehype-katex+katex: 4 parents, 4 nested.
            self.assertIn("8 components", p.stdout)

    def test_the_real_sbom_excerpt_has_nested_packages(self):
        with open(os.path.join(HERE, "testdata", "nested_real_excerpt.json")) as f:
            doc = json.load(f)
        self.assertEqual(sum(len(c.get("components", [])) for c in doc["components"]), 4)


class UnidentifiedLicenceTests(unittest.TestCase):
    def test_entry_with_only_a_url_is_unknown(self):
        c = comp("p")
        c["licenses"] = [{"license": {"url": "https://example.com/terms"}}]
        r, _ = one([c])
        self.assertEqual((r["status"], r["reason"]), ("fail", "unknown"))

    def test_entry_with_only_text_is_unknown(self):
        c = comp("p")
        c["licenses"] = [{"license": {"text": {"content": "do what you like"}}}]
        r, _ = one([c])
        self.assertEqual((r["status"], r["reason"]), ("fail", "unknown"))

    def test_an_unidentified_entry_next_to_an_allowed_one_still_fails(self):
        c = comp("p", "MIT")
        c["licenses"].append({"license": {"url": "https://example.com/terms"}})
        r, _ = one([c])
        self.assertEqual(r["status"], "fail")

    def test_an_empty_licence_list_is_still_no_licence(self):
        c = comp("p")
        c["licenses"] = []
        r, _ = one([c])
        self.assertEqual(r["reason"], "no-licence")


class ReviewedDeclarationTests(unittest.TestCase):
    """A reviewed-as record is pinned to the declaration it was reviewed against, as an election is."""

    FILES = {("golang.org/x/sys", "LICENSE"): GO_TEXT}

    def test_extra_gpl_term_makes_the_record_stale(self):
        c, r = one([comp("golang.org/x/sys", expr="BSD-Source-Code AND GPL-3.0-only")],
                   policy=REVIEWED_POLICY, files=self.FILES)
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("stale" in w and "declar" in w for w in r["warnings"]))

    def test_extra_agpl_term_makes_the_record_stale(self):
        c, _ = one([comp("golang.org/x/sys", expr="BSD-Source-Code AND AGPL-3.0-or-later")],
                   policy=REVIEWED_POLICY, files=self.FILES)
        self.assertEqual(c["status"], "fail")

    def test_a_different_single_declaration_makes_the_record_stale(self):
        c, _ = one([comp("golang.org/x/sys", "GPL-3.0-only")], policy=REVIEWED_POLICY, files=self.FILES)
        self.assertEqual(c["status"], "fail")

    def test_the_pinned_declaration_still_passes(self):
        c, _ = one([comp("golang.org/x/sys", "BSD-Source-Code")], policy=REVIEWED_POLICY, files=self.FILES)
        self.assertEqual(c["via"], "reviewed")

    def test_surrounding_whitespace_is_not_part_of_the_declaration(self):
        c, _ = one([comp("golang.org/x/sys", "  BSD-Source-Code ")], policy=REVIEWED_POLICY, files=self.FILES)
        self.assertEqual(c["via"], "reviewed")

    def test_the_declaration_is_matched_case_sensitively(self):
        c, _ = one([comp("golang.org/x/sys", "bsd-source-code")], policy=REVIEWED_POLICY, files=self.FILES)
        self.assertEqual(c["status"], "fail")

    def test_an_id_a_name_and_an_expression_are_different_declarations(self):
        for c in (comp("golang.org/x/sys", lname="BSD-Source-Code"), comp("golang.org/x/sys", expr="BSD-Source-Code")):
            r, _ = one([c], policy=REVIEWED_POLICY, files=self.FILES)
            self.assertEqual(r["status"], "fail")

    def test_parentheses_are_part_of_an_expression_declaration(self):
        pol = dict(REVIEWED_POLICY, reviewed=[dict(REVIEWED_POLICY["reviewed"][0], declared="expression:(BSD-Source-Code)")])
        self.assertEqual(one([comp("golang.org/x/sys", expr="(BSD-Source-Code)")], policy=pol, files=self.FILES)[0]["via"], "reviewed")
        self.assertEqual(one([comp("golang.org/x/sys", expr="BSD-Source-Code")], policy=pol, files=self.FILES)[0]["status"], "fail")

    def test_a_licence_appearing_where_none_was_declared_makes_the_record_stale(self):
        files = {("khroma", "license"): b"The MIT License (MIT)\n"}
        c, _ = one([comp("khroma", "GPL-3.0-only")], policy=REVIEWED_POLICY, files=files)
        self.assertEqual(c["status"], "fail")

    def test_a_record_without_a_declaration_is_never_applied(self):
        rec = {k: v for k, v in REVIEWED_POLICY["reviewed"][0].items() if k != "declared"}
        c, r = one([comp("golang.org/x/sys", "BSD-Source-Code")], policy=dict(REVIEWED_POLICY, reviewed=[rec]),
                   files=self.FILES)
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("declared" in w for w in r["warnings"]))


class ElectionHardeningTests(unittest.TestCase):
    """An election is the owner's choice from a dual licence, so its inputs must be evidence, not free text."""

    TEXT = b"dual licence statement\n"
    FILES = {("p", "COPYING"): TEXT}

    def policy(self, **election):
        e = {"component": "p", "declared": "id:Weird-1.0", "offered": "Weird-1.0 OR MIT", "elected": "MIT",
             "evidence": [{"file": "COPYING", "sha256": sha(self.TEXT)}], "reason": "x"}
        e.update(election)
        return dict(POLICY, elections=[e])

    def check(self, comp_, **election):
        return one([comp_], policy=self.policy(**election), files=self.FILES)

    def test_a_well_formed_election_applies(self):
        c, _ = self.check(comp("p", "Weird-1.0"))
        self.assertEqual((c["status"], c["via"], c["elected"]), ("pass", "election", "MIT"))

    # (a) a denied operand cannot be laundered through an election
    def test_a_denied_operand_in_the_offered_expression_refuses_the_election(self):
        for denied in ("SSPL-1.0", "GPL-3.0-only"):
            with self.subTest(denied=denied):
                c, r = self.check(comp("p", "Weird-1.0"), offered=f"Weird-1.0 OR {denied} OR MIT")
                self.assertEqual(c["status"], "fail")
                self.assertTrue(any("denied" in w for w in r["warnings"]))

    def test_an_extra_operand_that_is_neither_allowed_nor_declared_refuses_the_election(self):
        # GPL-3.0-or-later is on no list here: not denied, but not a licence this election may carry either.
        c, r = self.check(comp("p", "Weird-1.0"), offered="Weird-1.0 OR GPL-3.0-or-later OR MIT")
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("GPL-3.0-or-later" in w and "neither allowed" in w for w in r["warnings"]))

    def test_a_denied_declared_side_refuses_the_election(self):
        c, r = self.check(comp("p", "SSPL-1.0"), declared="id:SSPL-1.0", offered="SSPL-1.0 OR MIT")
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("denied" in w for w in r["warnings"]))

    def test_a_denied_licence_cannot_be_the_elected_one(self):
        c, _ = self.check(comp("p", "Weird-1.0"), offered="Weird-1.0 OR SSPL-1.0", elected="SSPL-1.0")
        self.assertEqual(c["status"], "fail")

    # (b) the elected licence must be one the policy allows
    def test_the_elected_licence_must_be_allowed(self):
        c, r = self.check(comp("p", "Weird-1.0"), offered="Weird-1.0 OR Other-1.0", elected="Other-1.0")
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("not allowed" in w for w in r["warnings"]))

    def test_an_election_only_licence_may_be_elected(self):
        c, _ = self.check(comp("p", "Weird-1.0"), offered="Weird-1.0 OR FTL", elected="FTL")
        self.assertEqual(c["via"], "election")

    # (c) the offered expression is pinned to licence files that ship
    def test_a_changed_licence_file_makes_the_election_stale(self):
        c, r = one([comp("p", "Weird-1.0")], policy=self.policy(), files={("p", "COPYING"): self.TEXT + b"edit"})
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("stale" in w and "COPYING" in w for w in r["warnings"]))

    def test_a_missing_licence_file_fails_closed(self):
        c, r = one([comp("p", "Weird-1.0")], policy=self.policy(), files={})
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("not found" in w and "COPYING" in w for w in r["warnings"]))

    def test_every_pinned_file_must_match(self):
        pol = self.policy(evidence=[{"file": "COPYING", "sha256": sha(self.TEXT)}, {"file": "OTHER", "sha256": sha(b"x")}])
        c, _ = one([comp("p", "Weird-1.0")], policy=pol, files={("p", "COPYING"): self.TEXT})
        self.assertEqual(c["status"], "fail")

    def test_an_election_without_evidence_is_refused(self):
        for evidence in (None, [], [{"file": "COPYING"}], [{"file": "COPYING", "sha256": "abc"}]):
            with self.subTest(evidence=evidence):
                e = {"component": "p", "declared": "id:Weird-1.0", "offered": "Weird-1.0 OR MIT", "elected": "MIT", "reason": "x"}
                if evidence is not None:
                    e["evidence"] = evidence
                c, r = one([comp("p", "Weird-1.0")], policy=dict(POLICY, elections=[e]), files=self.FILES)
                self.assertEqual(c["status"], "fail")
                self.assertTrue(any("evidence" in w for w in r["warnings"]))

    def test_a_reader_that_refuses_the_package_makes_the_election_stale(self):
        def reader(component, name):
            raise licence_scan.LicenceFileError("installed version 9 is not 1")
        r = evaluate({"components": [comp("p", "Weird-1.0")]}, self.policy(), set(), reader)
        self.assertFalse(r["ok"])
        self.assertTrue(any("p" in w and "installed version" in w for w in r["warnings"]))

    # (d) offered is a pure top-level OR
    def test_an_and_in_the_offered_expression_refuses_the_election(self):
        for offered in ("Weird-1.0 AND MIT", "ISC AND (Weird-1.0 OR MIT)", "(Weird-1.0 OR MIT) AND ISC"):
            with self.subTest(offered=offered):
                c, r = self.check(comp("p", "Weird-1.0"), offered=offered)
                self.assertEqual(c["status"], "fail")
                self.assertTrue(any("top-level OR" in w for w in r["warnings"]))

    def test_a_nested_or_in_the_offered_expression_refuses_the_election(self):
        c, _ = self.check(comp("p", "Weird-1.0"), offered="Weird-1.0 OR (Other-1.0 OR MIT)")
        self.assertEqual(c["status"], "fail")

    def test_a_single_licence_is_not_a_choice(self):
        c, _ = self.check(comp("p", "MIT"), declared="id:MIT", offered="MIT", elected="MIT")
        self.assertNotEqual(c.get("via"), "election")

    def test_the_elected_licence_must_be_a_top_level_operand(self):
        c, r = self.check(comp("p", "Weird-1.0"), elected="ISC")
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("operand" in w for w in r["warnings"]))

    def test_an_id_declaration_must_be_another_operand(self):
        c, r = self.check(comp("p", "Else-1.0"), declared="id:Else-1.0")
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("operand" in w for w in r["warnings"]))

    def test_the_declaration_may_not_be_the_elected_licence_itself(self):
        c, _ = self.check(comp("p", "MIT"), declared="id:MIT")
        self.assertNotEqual(c.get("via"), "election")

    def test_an_expression_declaration_must_equal_the_whole_offered_expression(self):
        good = self.check(comp("p", expr="(Weird-1.0 OR MIT)"), declared="expression:(Weird-1.0 OR MIT)")[0]
        self.assertEqual(good["via"], "election")
        reordered = self.check(comp("p", expr="MIT OR Weird-1.0"), declared="expression:MIT OR Weird-1.0")[0]
        self.assertEqual(reordered["via"], "election")
        narrower = self.check(comp("p", expr="Weird-1.0 OR Else-1.0"),
                              declared="expression:Weird-1.0 OR Else-1.0")[0]
        self.assertEqual(narrower["status"], "fail")

    def test_the_declaration_is_matched_exactly_and_by_kind(self):
        c, r = self.check(comp("p", lname="Weird-1.0"))
        self.assertEqual(c["status"], "fail")
        self.assertTrue(any("stale election" in w for w in r["warnings"]))
        c, _ = self.check(comp("p", "weird-1.0"))
        self.assertEqual(c["status"], "fail")


class PackageRootTests(unittest.TestCase):
    """Where a component's licence file is read from, and what the reader refuses."""

    def tree(self, d, files):
        for rel, body in files.items():
            path = os.path.join(d, rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "wb") as f:
                f.write(body if isinstance(body, bytes) else body.encode())

    def npm(self, name, version="1.0.0", path=None):
        c = {"name": name, "version": version, "purl": f"pkg:npm/{name}@{version}"}
        if path is not None:
            c["properties"] = [{"name": "cdx:npm:package:path", "value": path}]
        return c

    def test_uses_the_install_path_the_sbom_gives(self):
        with tempfile.TemporaryDirectory() as d:
            self.tree(d, {"node_modules/a/node_modules/tslib/license": "NESTED",
                          "node_modules/a/node_modules/tslib/package.json": '{"version": "2.3.0"}',
                          "node_modules/tslib/license": "HOISTED",
                          "node_modules/tslib/package.json": '{"version": "2.8.1"}'})
            reader = licence_scan.make_reader(node_modules=os.path.join(d, "node_modules"))
            c = self.npm("tslib", "2.3.0", "node_modules/a/node_modules/tslib")
            self.assertEqual(reader(c, "license"), b"NESTED")

    def test_falls_back_to_the_top_level_package_without_a_path(self):
        with tempfile.TemporaryDirectory() as d:
            self.tree(d, {"node_modules/khroma/license": "MIT", "node_modules/khroma/package.json": '{"version": "1.0.0"}'})
            reader = licence_scan.make_reader(node_modules=os.path.join(d, "node_modules"))
            self.assertEqual(reader(self.npm("khroma"), "license"), b"MIT")

    def test_installed_version_must_equal_the_component_version(self):
        with tempfile.TemporaryDirectory() as d:
            self.tree(d, {"node_modules/khroma/license": "MIT", "node_modules/khroma/package.json": '{"version": "9.9.9"}'})
            reader = licence_scan.make_reader(node_modules=os.path.join(d, "node_modules"))
            with self.assertRaisesRegex(licence_scan.LicenceFileError, "version"):
                reader(self.npm("khroma", "1.0.0"), "license")

    def test_a_package_without_a_manifest_is_refused(self):
        with tempfile.TemporaryDirectory() as d:
            self.tree(d, {"node_modules/khroma/license": "MIT"})
            reader = licence_scan.make_reader(node_modules=os.path.join(d, "node_modules"))
            with self.assertRaisesRegex(licence_scan.LicenceFileError, "package.json"):
                reader(self.npm("khroma"), "license")

    def test_install_paths_that_escape_node_modules_are_refused(self):
        with tempfile.TemporaryDirectory() as d:
            self.tree(d, {"node_modules/x/license": "x", "outside/license": "SECRET",
                          "outside/package.json": '{"version": "1.0.0"}'})
            reader = licence_scan.make_reader(node_modules=os.path.join(d, "node_modules"))
            for bad in ("../outside", "node_modules/../outside", os.path.join(d, "outside"), "/etc"):
                with self.subTest(path=bad):
                    with self.assertRaisesRegex(licence_scan.LicenceFileError, "escape|outside"):
                        reader(self.npm("x", "1.0.0", bad), "license")

    def test_purl_names_that_escape_are_refused(self):
        with tempfile.TemporaryDirectory() as d:
            self.tree(d, {"outside/license": "SECRET", "outside/package.json": '{"version": "1.0.0"}',
                          "node_modules/x/package.json": "{}"})
            reader = licence_scan.make_reader(node_modules=os.path.join(d, "node_modules"))
            c = {"name": "outside", "version": "1.0.0", "purl": "pkg:npm/%2E%2E/outside@1.0.0"}
            with self.assertRaisesRegex(licence_scan.LicenceFileError, "escape|outside"):
                reader(c, "license")

    def test_go_purl_paths_that_escape_the_module_cache_are_refused(self):
        with tempfile.TemporaryDirectory() as d:
            os.makedirs(os.path.join(d, "cache"))
            self.tree(d, {"outside@v1.0.0/LICENSE": "SECRET"})
            reader = licence_scan.make_reader(go_mod_cache=os.path.join(d, "cache"))
            c = {"name": "x", "version": "v1.0.0", "purl": "pkg:golang/../outside@v1.0.0"}
            with self.assertRaisesRegex(licence_scan.LicenceFileError, "escape|outside"):
                reader(c, "LICENSE")

    def test_evaluate_turns_a_refused_read_into_a_stale_record(self):
        def reader(component, name):
            raise licence_scan.LicenceFileError("installed version 9.9.9 is not 2.1.0")
        r = evaluate({"components": [comp("khroma")]}, REVIEWED_POLICY, set(), reader)
        self.assertFalse(r["ok"])
        self.assertTrue(any("khroma" in w and "9.9.9" in w for w in r["warnings"]))


class OwnWorkspaceTests(unittest.TestCase):
    """Own status needs more than a matching name: an npm component must be a local workspace package."""

    POLICY = dict(POLICY, own={"licences": ["AGPL-3.0-only"], "components": ["portal-status-ui", "@portal-app/"]})

    def own(self, c, workspaces):
        return evaluate({"components": [c]}, self.POLICY, set(), None, workspaces)["components"][0]

    def local(self, name, group=None, path=None):
        c = comp(name, "AGPL-3.0-only", group=group)
        c["purl"] = "pkg:npm/" + (f"%40{group[1:]}/" if group else "") + name
        key = f"{group}/{name}" if group else name
        c["properties"] = [{"name": "cdx:npm:package:path", "value": path or f"node_modules/{key}"}]
        return c

    def test_a_workspace_package_is_own(self):
        r = self.own(self.local("portal-status-ui"), {"portal-status-ui"})
        self.assertEqual((r["status"], r["via"]), ("pass", "own"))

    def test_a_scoped_workspace_package_is_own(self):
        r = self.own(self.local("sdk", "@portal-app"), {"@portal-app/sdk"})
        self.assertEqual(r["via"], "own")

    def test_a_registry_package_with_the_same_name_is_not_own(self):
        c = self.local("portal-status-ui")
        c["externalReferences"] = [{"type": "distribution", "url": "https://registry.npmjs.org/portal-status-ui/-/x.tgz"}]
        r = self.own(c, {"portal-status-ui"})
        self.assertEqual(r["status"], "fail")

    def test_a_same_named_package_outside_the_workspaces_is_not_own(self):
        r = self.own(self.local("portal-status-ui"), {"something-else"})
        self.assertEqual(r["status"], "fail")

    def test_without_workspace_information_no_npm_package_is_own(self):
        r = self.own(self.local("portal-status-ui"), None)
        self.assertEqual(r["status"], "fail")

    def test_a_nested_package_of_the_same_name_is_not_own(self):
        c = self.local("portal-status-ui", path="node_modules/other/node_modules/portal-status-ui")
        self.assertEqual(self.own(c, {"portal-status-ui"})["status"], "fail")

    def test_the_install_path_must_be_exactly_node_modules_then_the_name(self):
        for path in ("packages/ui", "node_modules/portal-status-ui/", "./node_modules/portal-status-ui",
                     "node_modules/../node_modules/portal-status-ui", "/abs/node_modules/portal-status-ui"):
            with self.subTest(path=path):
                self.assertEqual(self.own(self.local("portal-status-ui", path=path), {"portal-status-ui"})["status"], "fail")

    def test_a_component_with_no_install_path_is_not_own(self):
        c = self.local("portal-status-ui")
        del c["properties"]
        self.assertEqual(self.own(c, {"portal-status-ui"})["status"], "fail")

    def test_a_component_with_no_purl_is_not_own(self):
        c = self.local("portal-status-ui")
        del c["purl"]
        self.assertEqual(self.own(c, {"portal-status-ui"})["status"], "fail")

    def test_go_modules_need_no_workspace(self):
        r = evaluate({"components": [golang("github.com/KazuhaHub/x")]}, POLICY, set())["components"][0]
        self.assertEqual(r["via"], "own")

    def test_cli_reads_workspaces_from_the_root_manifest(self):
        with tempfile.TemporaryDirectory() as d:
            for rel, body in {"package.json": '{"workspaces": ["packages/*"]}',
                              "packages/ui/package.json": '{"name": "portal-status-ui"}'}.items():
                os.makedirs(os.path.dirname(os.path.join(d, rel)), exist_ok=True)
                with open(os.path.join(d, rel), "w") as f:
                    f.write(body)
            sp, pp = os.path.join(d, "s.json"), os.path.join(d, "p.json")
            with open(sp, "w") as f:
                json.dump({"components": [self.local("portal-status-ui")]}, f)
            with open(pp, "w") as f:
                json.dump(self.POLICY, f)
            base = [sys.executable, "-I", os.path.join(HERE, "licence_scan.py"), "--policy", pp]
            self.assertEqual(subprocess.run(base + [sp], capture_output=True, text=True).returncode, 1)
            self.assertEqual(subprocess.run(base + ["--workspace-root", d, sp], capture_output=True, text=True).returncode, 0)

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


class AppsCopyNoticeTests(unittest.TestCase):
    """A change here must prompt the matching change in the Apps repository's copy."""

    def test_the_evaluator_names_the_apps_copy_and_how_to_update_it(self):
        doc = licence_scan.__doc__
        for needle in ("KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps", "scripts/licence_scan.py",
                       "check_licence_copy.py", "licence-scan.pin"):
            self.assertIn(needle, doc)

    def test_the_policy_names_the_sections_the_apps_copy_shares(self):
        with open(os.path.join(HERE, "licence-policy.json")) as f:
            source = json.load(f)["source"]
        self.assertIn("StockAnalysisPrediction-Report-Portal-Apps", source)
        for section in ("allow", "deny", "election_only", "own.licences", "reviewed"):
            self.assertIn(section, source)


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

    def test_reviewed_records_are_the_two_owner_decisions(self):
        got = {r["reviewed_as"]: (sorted(r["components"]), r["file"], r["sha256"]) for r in self.policy["reviewed"]}
        self.assertEqual(got, {
            "BSD-3-Clause": (sorted(f"golang.org/x/{m}" for m in ["crypto", "image", "net", "oauth2", "sync", "sys", "text"]),
                             "LICENSE", "911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad"),
            "MIT": (["khroma"], "license", "66b333b0f66759a0b710459e03f7029abe17f4358114a128d2c972e642961b49"),
        })

    def test_reviewed_records_pin_the_declaration_they_were_reviewed_against(self):
        got = {r["reviewed_as"]: r["declared"] for r in self.policy["reviewed"]}
        self.assertEqual(got, {"BSD-3-Clause": "id:BSD-Source-Code", "MIT": ""})

    def test_freetype_election_names_the_dual_licence_it_chooses_from(self):
        e = next(e for e in self.policy["elections"] if e["component"] == "github.com/golang/freetype")
        self.assertEqual((e["declared"], e["offered"], e["elected"]), ("id:GPL-2.0-or-later", "GPL-2.0-or-later OR FTL", "FTL"))
        self.assertIsNone(licence_scan._election_problem(e, self.policy))
        self.assertEqual([f["file"] for f in e["evidence"]], ["LICENSE"])

    def test_the_election_evidence_is_pinned_to_the_shipped_licence_files(self):
        got = {e["component"]: [(f["file"], f["sha256"]) for f in e["evidence"]] for e in self.policy["elections"]}
        self.assertEqual(got, {
            "dompurify": [("LICENSE", "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30"),
                          ("LICENSE-MPL", "fab3dd6bdab226f1c08630b1dd917e11fcb4ec5e1e020e2c16f83a0a13863e85")],
            "github.com/golang/freetype": [("LICENSE", "d3ba056adc2b7909e95681deaae397fb37c97ed491a920f491214f07b62c41d0")]})

    def test_every_election_stands(self):
        for e in self.policy["elections"]:
            self.assertIsNone(licence_scan._election_problem(e, self.policy), e["component"])

    def test_reviewed_records_are_well_formed(self):
        for r in self.policy["reviewed"]:
            self.assertIn(r["reviewed_as"], self.policy["allow"])
            self.assertRegex(r["sha256"], r"^[0-9a-f]{64}$")
            self.assertTrue(r["reason"])
            self.assertFalse(any("*" in c for c in r["components"]), "records name modules, not patterns")


if __name__ == "__main__":
    unittest.main()
