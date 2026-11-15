#!/usr/bin/env python3
"""Evaluate CycloneDX SBOM licences against the checked-in policy (scripts/licence-policy.json).

The policy is the owner decision recorded in docs/app-platform/167-owner-decisions-remaining-acceptance.md
section 8. The gate fails closed: a licence that is on neither list is as much a failure as a denied one.

    licence_scan.py --policy scripts/licence-policy.json [--verified NAME ...] [--report out.json] SBOM...

Exit status: 0 every component passes, 1 at least one component or SBOM fails, 2 unusable input.

Rules, in the order they apply to one component:

1. An exclusion (a package that is not shipped) passes only while the check it names has been verified
   with --verified. The caller passes the flag only after that check succeeded, so an exclusion cannot
   outlive the evidence behind it.
2. An election records which side of a dual licence the project takes. It applies only while the
   component still declares exactly what the election was written against; if upstream changes its
   declaration the election is reported as stale and ignored, and the component is judged on its merits.
3. Otherwise the declared SPDX expression is evaluated: OR passes when any choice is allowed, AND needs
   every term allowed, and several licence entries on one component are ANDed.
4. A licence in "election_only" (the FreeType License) is never a free choice: it counts only through an
   election record naming the component.
5. A reviewed-as record is the owner's reading of one licence text: it names components, the licence they are
   reviewed as (which must be on the allow list), the file inside the package and the SHA-256 of that file. It
   applies only to a component that fails on its merits for a reason other than a denied licence, only while it
   still declares exactly what the record was reviewed against (an extra term makes it stale), and only while
   the file that ships in the Go module cache or node_modules still hashes to the pinned value. A changed or
   missing file makes the record stale: it is ignored, with a warning, and the gate fails again.
6. The project's own licence (AGPL-3.0-only) is allowed only on the own components: an entry ending in "/" is
   a namespace prefix, any other entry names one package exactly. An npm component must also be a local
   workspace package (--workspace-root) with no registry distribution, so a registry package cannot take the name.

7. An npm component that is installed (--node-modules) must declare what its installed package.json declares:
   the SBOM is built from package-lock.json, whose license fields integrity does not cover. A mismatch fails and
   cannot be covered by a record. A component that is not installed (an optional package of another platform) is
   judged on the lock's declaration alone and counted in a warning.

Every component is judged, nested ones included (cyclonedx-npm nests packages that are not hoisted).

This evaluator is duplicated in the Apps repository (scripts/licence_scan.py in
KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps), together with the allow, deny, election_only, own.licences and
reviewed sections of scripts/licence-policy.json. A change to either must be copied there: merge it here first, then
copy the files into the Apps repository, write the merge commit's SHA into its scripts/licence-scan.pin and run its
scripts/check_licence_copy.py, which fails on any difference from the live files on this repository's
integration/app-platform and main.
"""
import argparse
import glob
import hashlib
import json
import os
import re
import sys
import urllib.parse

OK, ELECTION_REQUIRED, UNKNOWN, DENIED = "ok", "election-required", "unknown", "denied"
UNIDENTIFIED = "(licence entry without an identifier)"
_BAD_ORDER = {ELECTION_REQUIRED: 1, UNKNOWN: 2, DENIED: 3}


class ExpressionError(ValueError):
    pass


def _tokenize(expr):
    tokens = re.findall(r"\(|\)|[^\s()]+", expr)
    if not tokens:
        raise ExpressionError("empty expression")
    return tokens


def parse_expression(expr):
    """Parse an SPDX licence expression into ("id", s) / ("and", [..]) / ("or", [..]).

    `X WITH Y` is kept as one opaque identifier: an exception changes the grant, so the base licence
    alone must never be allowed to stand for it.
    """
    tokens = _tokenize(expr)
    pos = 0

    def peek():
        return tokens[pos] if pos < len(tokens) else None

    def take():
        nonlocal pos
        pos += 1
        return tokens[pos - 1]

    def is_op(tok, name):
        return tok is not None and tok.upper() == name

    def parse_or():
        terms = [parse_and()]
        while is_op(peek(), "OR"):
            take()
            terms.append(parse_and())
        return terms[0] if len(terms) == 1 else ("or", terms)

    def parse_and():
        terms = [parse_atom()]
        while is_op(peek(), "AND"):
            take()
            terms.append(parse_atom())
        return terms[0] if len(terms) == 1 else ("and", terms)

    def parse_atom():
        tok = peek()
        if tok is None:
            raise ExpressionError("unexpected end of expression")
        if tok == "(":
            take()
            node = parse_or()
            if peek() != ")":
                raise ExpressionError("missing closing parenthesis")
            take()
            return node
        if tok == ")" or tok.upper() in ("AND", "OR", "WITH"):
            raise ExpressionError(f"unexpected {tok!r}")
        take()
        ident = tok
        if is_op(peek(), "WITH"):
            take()
            exc = peek()
            if exc is None or exc in ("(", ")") or exc.upper() in ("AND", "OR", "WITH"):
                raise ExpressionError("WITH needs an exception identifier")
            ident = f"{ident} WITH {take()}"
        return ("id", ident)

    node = parse_or()
    if pos != len(tokens):
        raise ExpressionError(f"unexpected {tokens[pos]!r}")
    return node


def load_policy(path):
    with open(path, encoding="utf-8") as f:
        policy = json.load(f)
    for key, default in (("allow", []), ("deny", []), ("own", {"licences": [], "components": []}),
                         ("election_only", []), ("elections", []), ("exclusions", []), ("reviewed", [])):
        policy.setdefault(key, default)
    return policy


MAX_LICENCE_FILE = 1 << 20


class LicenceFileError(Exception):
    """A component's shipped package cannot be trusted as the source of its licence file."""


def _purl_path(comp, kind):
    purl = comp.get("purl") or ""
    prefix = f"pkg:{kind}/"
    if not purl.startswith(prefix):
        return None
    body = purl[len(prefix):].split("?", 1)[0].split("#", 1)[0]
    body = body.rsplit("@", 1)[0]
    return urllib.parse.unquote(body)


def _go_escape(path):
    return re.sub(r"[A-Z]", lambda m: "!" + m.group(0).lower(), path)


def _property(comp, name):
    for prop in comp.get("properties") or []:
        if prop.get("name") == name:
            return prop.get("value")
    return None


def _inside(base, path):
    return path != base and os.path.commonpath([base, path]) == base


def _locate(comp, go_mod_cache, node_modules):
    """(root, is_npm) of a component's shipped package, or None when it is not a kind this knows or the cache or
    node_modules was not given. A root that is not strictly inside the module cache or node_modules is refused."""
    go = _purl_path(comp, "golang")
    npm = _purl_path(comp, "npm")
    if go is not None and go_mod_cache and comp.get("version"):
        base = os.path.realpath(go_mod_cache)
        root = os.path.realpath(os.path.join(base, _go_escape(go) + "@" + comp["version"]))
        if not _inside(base, root):
            raise LicenceFileError(f"module path {go!r} escapes the module cache")
        return root, False
    if npm is not None and node_modules:
        base = os.path.realpath(node_modules)
        where = _property(comp, "cdx:npm:package:path")
        if where is not None:
            if os.path.isabs(where):
                raise LicenceFileError(f"install path {where!r} is absolute and escapes node_modules")
            root = os.path.realpath(os.path.join(os.path.dirname(base), where))
        else:
            root = os.path.realpath(os.path.join(base, npm))
        if not _inside(base, root):
            raise LicenceFileError(f"install path {where or npm!r} escapes node_modules")
        return root, True
    return None


def _manifest(root, comp):
    """The installed package.json, whose version must be the component's."""
    try:
        with open(os.path.join(root, "package.json"), encoding="utf-8") as f:
            manifest = json.load(f)
    except (OSError, ValueError):
        raise LicenceFileError("the installed package has no readable package.json")
    if not isinstance(manifest, dict):
        raise LicenceFileError("the installed package.json is not an object")
    if comp.get("version") and manifest.get("version") != comp["version"]:
        raise LicenceFileError(f"installed version {manifest.get('version')!r} is not the component version "
                               f"{comp['version']!r}")
    return manifest


def make_reader(go_mod_cache=None, node_modules=None):
    """A reader of one file inside a component's shipped package.

    A Go module is read from its directory in the module cache. An npm package is read from the install path
    the SBOM records (cdx:npm:package:path, which tells a nested copy from the hoisted one), or from the
    top-level package of that name; its package.json must carry the component's version. A root derived from
    the SBOM that is not strictly inside the module cache or node_modules is refused, and the file read never
    leaves that root. Returns None when the package or file is not there; raises LicenceFileError when what is
    there cannot be trusted.
    """
    def read(comp, name):
        located = _locate(comp, go_mod_cache, node_modules)
        if located is None:
            return None
        root, is_npm = located
        if not os.path.isdir(root):
            return None
        if is_npm:
            _manifest(root, comp)
        path = os.path.realpath(os.path.join(root, name))
        if not _inside(root, path) or not os.path.isfile(path) or os.path.getsize(path) > MAX_LICENCE_FILE:
            return None
        with open(path, "rb") as f:
            return f.read()
    return read


def make_installed_reader(node_modules):
    """A reader of the installed package.json of an npm component (None when it is not installed or is not an
    npm component), for the cross-check against the lock file's declaration. Same root rules as make_reader."""
    def read(comp):
        located = _locate(comp, None, node_modules) if _purl_path(comp, "npm") is not None else None
        if located is None or not os.path.isdir(located[0]):
            return None
        return _manifest(located[0], comp)
    return read


def _installed_declaration(manifest):
    """The licences an installed package.json declares, as a sorted list of strings (license as a string or an
    object with a type, or the legacy licenses list); an empty list when it declares none."""
    found = []

    def one(value):
        if isinstance(value, str) and value.strip():
            found.append(value.strip())
        elif isinstance(value, dict) and isinstance(value.get("type"), str) and value["type"].strip():
            found.append(value["type"].strip())
    one(manifest.get("license"))
    licences = manifest.get("licenses")
    if isinstance(licences, list):
        for item in licences:
            one(item)
    return sorted(found)


def _lock_declaration(comp):
    entries = comp.get("licenses") or []
    out = []
    for entry in entries:
        if "expression" in entry:
            out.append(entry["expression"].strip())
        else:
            lic = entry.get("license") or {}
            text = (lic.get("id") or lic.get("name") or "").strip()
            if text:
                out.append(text)
    return sorted(out)


def read_workspaces(root):
    """The package names of the npm workspaces declared by root/package.json (empty when there are none)."""
    with open(os.path.join(root, "package.json"), encoding="utf-8") as f:
        spec = json.load(f).get("workspaces") or []
    patterns = spec.get("packages", []) if isinstance(spec, dict) else spec
    names = set()
    for pattern in patterns:
        for folder in sorted(glob.glob(os.path.join(root, pattern))):
            manifest = os.path.join(folder, "package.json")
            if os.path.isfile(manifest):
                with open(manifest, encoding="utf-8") as f:
                    name = json.load(f).get("name")
                if name:
                    names.add(name)
    return names


def flatten(components):
    """Every component, nested ones included.

    cyclonedx-npm nests a package that is not hoisted inside the components of its parent, so judging only
    the top level would pass anything a parent happens to hide. A repeated component is judged once only
    when it is really the same: the same bom-ref and the same version and declared licences. Copies that
    share a bom-ref but differ in what they declare are all kept and judged separately, so one cannot shadow
    another. Components without a bom-ref are all kept.
    """
    seen, out = set(), []

    def walk(items):
        for comp in items or []:
            ref = comp.get("bom-ref")
            identity = None if ref is None else (ref, comp.get("version"), declaration_key(comp))
            if identity is None or identity not in seen:
                if identity is not None:
                    seen.add(identity)
                out.append(comp)
            walk(comp.get("components"))
    walk(components)
    return out


def component_key(comp):
    group = comp.get("group")
    return f"{group}/{comp['name']}" if group else comp["name"]


def declaration_key(comp):
    """The licence declaration of a component, exactly and by kind: 'id:MIT', 'name:Some terms',
    'expression:(MIT OR ISC)', joined with ' | ' when there are several entries, '' when there is none.
    Policy records pin this string, so an id, a name and an expression never stand for one another, and
    case, order and parentheses all count."""
    entries = comp.get("licenses") or (comp.get("evidence") or {}).get("licenses") or []
    parts = []
    for entry in entries:
        if "expression" in entry:
            parts.append("expression:" + entry["expression"].strip())
            continue
        lic = entry.get("license") or {}
        if (lic.get("id") or "").strip():
            parts.append("id:" + lic["id"].strip())
        elif (lic.get("name") or "").strip():
            parts.append("name:" + lic["name"].strip())
        else:
            parts.append("unidentified:")
    return " | ".join(parts)


def _declared_nodes(comp):
    """The component's licences as parse trees, plus the text they were written as."""
    entries = comp.get("licenses") or (comp.get("evidence") or {}).get("licenses") or []
    nodes, texts = [], []
    for entry in entries:
        if "expression" in entry:
            text = entry["expression"].strip()
            nodes.append(parse_expression(text))
        else:
            lic = entry.get("license") or {}
            text = (lic.get("id") or lic.get("name") or "").strip()
            if not text:
                # Only a url or a text: nothing the policy can match, so it must fail rather than vanish.
                text = UNIDENTIFIED
            # A name is taken literally, never parsed: it is free text, and an id is one identifier.
            nodes.append(("id", text))
        texts.append(text)
    return nodes, " AND ".join(texts)


def _declared_atoms(declared):
    """The licence identifiers inside a pinned 'kind:text' declaration."""
    kind, _, text = declared.partition(":")
    if kind == "expression":
        found = []

        def walk(node):
            if node[0] == "id":
                found.append(node[1])
            else:
                for child in node[1]:
                    walk(child)
        walk(parse_expression(text))
        return found
    return [text]


def _election_problem(e, policy):
    """Why an election cannot stand, or None. The record is the owner's choice from a dual licence, so it
    must be a real choice and its inputs must be evidence, not free text:

    - the offered expression is a pure top-level OR of identifiers, and the elected licence is one of them;
    - no identifier of the declaration or of the offer is denied, and the elected one is allowed (on the
      allow list, or elected-only);
    - the declaration is another top-level operand, or the whole offer;
    - the offer is pinned to the SHA-256 of the licence files it was read from (checked against the shipped
      package by the caller).
    """
    deny = {d.lower() for d in policy["deny"]}
    allowed = {a.lower() for a in policy["allow"]} | {a.lower() for a in policy["election_only"]}
    try:
        offer = parse_expression(e["offered"])
        declared_atoms = _declared_atoms(e["declared"])
        declared_tree = parse_expression(e["declared"].partition(":")[2]) if e["declared"].startswith("expression:") else None
    except (ExpressionError, KeyError) as err:
        return f"its declaration or offer does not parse: {err}"
    if offer[0] != "or" or any(child[0] != "id" for child in offer[1]):
        return f"the offer {e['offered']!r} is not a pure top-level OR expression"
    operands = [child[1] for child in offer[1]]
    for atom in operands + declared_atoms:
        if atom.lower() in deny:
            return f"{atom!r} is a denied licence"
    if e["elected"] not in operands:
        return f"{e['elected']!r} is not a top-level operand of {e['offered']!r}"
    if e["elected"].lower() not in allowed:
        return f"{e['elected']!r} is not allowed by the policy"
    kind = e["declared"].partition(":")[0]
    if kind == "id":
        if declared_atoms[0] == e["elected"] or declared_atoms[0] not in operands:
            return f"the declared {declared_atoms[0]!r} is not another top-level operand of {e['offered']!r}"
        for atom in operands:
            if atom != declared_atoms[0] and atom.lower() not in allowed:
                return f"{atom!r} in the offer is neither allowed by the policy nor the declared side"
    elif kind == "expression":
        if declared_tree[0] != "or" or any(c[0] != "id" for c in declared_tree[1]) \
                or {c[1] for c in declared_tree[1]} != set(operands):
            return f"the declared expression is not the whole offer {e['offered']!r}"
    else:
        return f"the declaration {e['declared']!r} must be 'id:...' or 'expression:...'"
    evidence = e.get("evidence")
    if not evidence or any(not isinstance(f, dict) or not f.get("file")
                           or not re.fullmatch(r"[0-9a-fA-F]{64}", f.get("sha256", "")) for f in evidence):
        return "it has no licence-file evidence (a list of {file, sha256}) for the offered expression"
    return None


def _election_evidence(key, comp, e, read_licence, warnings):
    """True when every pinned licence file still ships with the hash the election was written against."""
    for item in e["evidence"]:
        try:
            data = read_licence(comp, item["file"]) if read_licence else None
        except LicenceFileError as err:
            warnings.append(f"election for {key}: {err}; ignored")
            return False
        if data is None:
            warnings.append(f"election for {key}: licence file {item['file']!r} not found in the shipped package; ignored")
            return False
        if hashlib.sha256(data).hexdigest() != item["sha256"].lower():
            warnings.append(f"stale election for {key}: {item['file']!r} no longer hashes to the pinned SHA-256; ignored")
            return False
    return True


def _is_own(comp, key, prefixes, workspaces):
    """The project's own component: a matching name, and proof it is the local package.

    A name alone can be squatted by a registry package, and a Go module path is not proof either, so a
    component with no purl is never own. A Go module needs a golang purl. An npm component must have an
    install path of exactly node_modules/<its name> (a nested copy of the same name is not it), carry no
    registry distribution, and be a package of this repository's workspaces.
    """
    if not any(key == p or (p.endswith("/") and key.startswith(p)) for p in prefixes):
        return False
    purl = comp.get("purl") or ""
    if purl.startswith("pkg:golang/"):
        return True
    if not purl.startswith("pkg:npm/"):
        return False
    if any(r.get("type") == "distribution" for r in comp.get("externalReferences") or []):
        return False
    if _property(comp, "cdx:npm:package:path") != f"node_modules/{key}":
        return False
    return workspaces is not None and key in workspaces


class _Judge:
    def __init__(self, policy, own):
        self.allow = {s.lower() for s in policy["allow"]}
        self.deny = {s.lower() for s in policy["deny"]}
        self.election_only = {s.lower() for s in policy["election_only"]}
        self.own_licences = {s.lower() for s in policy["own"]["licences"]} if own else set()
        self.used_own = False

    def atom(self, ident):
        low = ident.lower()
        if low in self.deny:
            return DENIED
        if low in self.allow:
            return OK
        if low in self.own_licences:
            self.used_own = True
            return OK
        if low in self.election_only:
            return ELECTION_REQUIRED
        return UNKNOWN

    def node(self, node):
        kind = node[0]
        if kind == "id":
            return self.atom(node[1])
        results = [self.node(n) for n in node[1]]
        if kind == "or":
            if OK in results:
                return OK
            return min(results, key=lambda r: _BAD_ORDER[r])  # the most actionable reason
        if all(r == OK for r in results):
            return OK
        return max((r for r in results if r != OK), key=lambda r: _BAD_ORDER[r])


def _fail(base, reason, detail):
    return dict(base, status="fail", reason=reason, detail=detail)


def evaluate(sbom, policy, verified, read_licence=None, workspaces=None, installed=None):
    """Judge every component of `sbom` (a dict with "components") against `policy`.

    Returns {"ok", "components", "failures", "warnings", "problems"}. `verified` is the set of check
    names the caller has confirmed passed; exclusions that require a name outside it do not apply.
    `read_licence(component, filename)` returns the bytes of a file in the shipped package, or None; without
    one no reviewed-as record can be verified. `workspaces` is the set of local npm workspace package names an
    own npm component must belong to. Nested components are judged like top-level ones. `installed(component)` returns
    the installed package.json of an npm component (None when it is not installed): the SBOM of an npm project is
    built from package-lock.json, whose license fields its integrity hashes do not cover, so every installed
    component's declaration must agree with its installed manifest.
    """
    top = sbom.get("components") or []
    comps = flatten(top)
    problems = []
    if not top:
        problems.append("SBOM has no components: the generator produced nothing to scan")
    exclusions = {e["component"]: e for e in policy["exclusions"]}
    elections = {e["component"]: e for e in policy["elections"]}
    reviewed = {name: rec for rec in policy.get("reviewed", []) for name in rec["components"]}
    allow_lower = {a.lower() for a in policy["allow"]}
    own_prefixes = policy["own"]["components"]
    seen = set()
    results, warnings = [], []
    not_installed = []
    for comp in comps:
        key = component_key(comp)
        seen.add(key)
        base = {"name": key, "version": comp.get("version", ""), "declared": ""}
        if key in exclusions:
            ex = exclusions[key]
            base["declared"] = _declared_nodes_text(comp)
            if ex["requires"] in verified:
                results.append(dict(base, status="pass", via="exclusion", reason="excluded", detail=ex["reason"]))
            else:
                results.append(_fail(base, "exclusion-unverified",
                                     f"excluded as not shipped, but {ex['requires']!r} did not pass in this run"))
            continue
        try:
            nodes, declared = _declared_nodes(comp)
        except ExpressionError as e:
            results.append(_fail(dict(base, declared=_declared_nodes_text(comp)), "unparseable", str(e)))
            continue
        base["declared"] = declared
        own = _is_own(comp, key, own_prefixes, workspaces)
        if installed is not None and not own and (comp.get("purl") or "").startswith("pkg:npm/"):
            try:
                manifest = installed(comp)
            except LicenceFileError as err:
                results.append(_fail(base, "lock-unverifiable", f"the installed package cannot be trusted: {err}"))
                continue
            if manifest is None:
                not_installed.append(key)
            elif _installed_declaration(manifest) != _lock_declaration(comp):
                results.append(_fail(base, "lock-mismatch",
                                     f"the lock file declares {_lock_declaration(comp) or 'nothing'} but the installed "
                                     f"package.json declares {_installed_declaration(manifest) or 'nothing'}"))
                continue
        if not nodes:
            rec = reviewed.get(key)
            if rec is not None and _apply_review(key, comp, rec, UNKNOWN, declared, allow_lower, read_licence, warnings):
                results.append(dict(base, status="pass", via="reviewed", reason="reviewed",
                                    reviewed_as=rec["reviewed_as"], detail=rec["reason"]))
            else:
                results.append(_fail(base, "no-licence", "the SBOM records no licence for this component"))
            continue
        judge = _Judge(policy, own)
        election = elections.get(key)
        if election is not None:
            if election["declared"] != declaration_key(comp):
                warnings.append(f"stale election for {key}: it was written for the declaration "
                                f"{election['declared']!r} but the SBOM declares {declaration_key(comp)!r}; ignored")
            elif _election_problem(election, policy):
                warnings.append(f"election for {key} refused: {_election_problem(election, policy)}")
            elif _election_evidence(key, comp, election, read_licence, warnings):
                results.append(dict(base, status="pass", via="election", reason="elected",
                                    elected=election["elected"], detail=election["reason"]))
                continue
        verdict = judge.node(("and", nodes) if len(nodes) > 1 else nodes[0])
        if verdict == OK:
            results.append(dict(base, status="pass", via="own" if judge.used_own else "allow", reason="allowed"))
        else:
            rec = reviewed.get(key)
            if rec is not None:
                outcome = _apply_review(key, comp, rec, verdict, declared, allow_lower, read_licence, warnings)
                if outcome is not None:
                    results.append(dict(base, status="pass", via="reviewed", reason="reviewed",
                                        reviewed_as=rec["reviewed_as"], detail=rec["reason"]))
                    continue
            detail = {DENIED: "a denied licence", UNKNOWN: "a licence that is on neither list",
                      ELECTION_REQUIRED: "only allowed through an election record, and none applies"}[verdict]
            results.append(_fail(base, verdict, detail))
    if not_installed:
        warnings.append(f"{len(not_installed)} npm components are not installed and were judged on the lock file's "
                        f"declaration alone (optional packages of other platforms): "
                        f"{', '.join(sorted(not_installed)[:5])}{', ...' if len(not_installed) > 5 else ''}")
    for name, e in {**elections, **exclusions, **reviewed}.items():
        if name not in seen:
            warnings.append(f"policy entry for {name} is not present in this SBOM set")
    failures = [r for r in results if r["status"] == "fail"]
    return {"ok": not failures and not problems, "components": results, "failures": failures,
            "warnings": warnings, "problems": problems}


def _apply_review(key, comp, rec, verdict, declared, allow_lower, read_licence, warnings):
    """True when the reviewed-as record holds for this component; otherwise None, with the reason as a warning."""
    if rec["reviewed_as"].lower() not in allow_lower:
        warnings.append(f"reviewed-as record for {key} reviews it as {rec['reviewed_as']!r}, which is not allowed "
                        f"by the policy; ignored")
        return None
    if "declared" not in rec:
        warnings.append(f"reviewed-as record for {key} does not pin the declaration it was reviewed against "
                        f"(no 'declared'); ignored")
        return None
    if rec["declared"] != declaration_key(comp):
        warnings.append(f"stale reviewed-as record for {key}: it was reviewed against the declaration "
                        f"{rec['declared']!r} but the SBOM declares {declaration_key(comp)!r}; ignored")
        return None
    if verdict == DENIED:
        warnings.append(f"reviewed-as record for {key} cannot override a denied licence; ignored")
        return None
    try:
        data = read_licence(comp, rec["file"]) if read_licence else None
    except LicenceFileError as err:
        warnings.append(f"reviewed-as record for {key}: {err}; ignored")
        return None
    if data is None:
        warnings.append(f"reviewed-as record for {key}: licence file {rec['file']!r} not found in the shipped "
                        f"package; ignored")
        return None
    if hashlib.sha256(data).hexdigest() != rec["sha256"].lower():
        warnings.append(f"stale reviewed-as record for {key}: {rec['file']!r} no longer hashes to the pinned "
                        f"SHA-256; ignored")
        return None
    return True


def _declared_nodes_text(comp):
    entries = comp.get("licenses") or (comp.get("evidence") or {}).get("licenses") or []
    out = []
    for entry in entries:
        lic = entry.get("license") or {}
        out.append((entry.get("expression") or lic.get("id") or lic.get("name") or UNIDENTIFIED).strip())
    return " AND ".join(t for t in out if t)


def _render(result, sources):
    lines = []
    comps = result["components"]
    by_via = {}
    for c in comps:
        if c["status"] == "pass":
            by_via[c["via"]] = by_via.get(c["via"], 0) + 1
    lines.append(f"licence scan: {len(comps)} components from {', '.join(sources)}")
    lines.append("  passed: " + (", ".join(f"{n} {v}" for v, n in sorted(by_via.items())) or "none"))
    for c in comps:
        if c["status"] == "pass" and c["via"] in ("election", "exclusion", "reviewed"):
            extra = {"election": f" -> {c.get('elected')}", "reviewed": f" reviewed as {c.get('reviewed_as')}"}.get(c["via"], "")
            note = "" if c["via"] == "reviewed" else f": {c['detail']}"
            lines.append(f"  {c['via']}: {c['name']} {c['version']} [{c['declared']}]{extra}{note}")
    for w in result["warnings"]:
        lines.append(f"  warning: {w}")
    for p in result["problems"]:
        lines.append(f"  PROBLEM: {p}")
    if result["failures"]:
        lines.append(f"  FAILED: {len(result['failures'])} component(s)")
        for c in result["failures"]:
            lines.append(f"    {c['name']} {c['version']}  licence: {c['declared'] or '(none)'}  "
                         f"[{c['reason']}] {c['detail']}")
    lines.append("licence scan: " + ("PASS" if result["ok"] else "FAIL"))
    return "\n".join(lines)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--policy", required=True)
    ap.add_argument("--verified", action="append", default=[], metavar="NAME",
                    help="a check that passed in this run; exclusions requiring it become valid")
    ap.add_argument("--go-mod-cache", metavar="DIR", help="Go module cache (go env GOMODCACHE), where reviewed-as "
                    "records find a module's licence file")
    ap.add_argument("--node-modules", metavar="DIR", help="node_modules directory holding the packages of the SBOM")
    ap.add_argument("--workspace-root", metavar="DIR", help="repository root whose package.json workspaces "
                    "name the local packages an own npm component must belong to")
    ap.add_argument("--report", help="write the full result as JSON")
    ap.add_argument("sbom", nargs="+")
    args = ap.parse_args(argv)
    try:
        policy = load_policy(args.policy)
        workspaces = read_workspaces(args.workspace_root) if args.workspace_root else None
        comps, empty = [], []
        for path in args.sbom:
            with open(path, encoding="utf-8") as f:
                doc = json.load(f)
            got = doc.get("components") or []
            if not got:
                empty.append(path)
            comps.extend(got)
    except (OSError, ValueError, KeyError) as e:
        print(f"licence scan: cannot read input: {e}", file=sys.stderr)
        return 2
    result = evaluate({"components": comps}, policy, set(args.verified),
                      make_reader(args.go_mod_cache, args.node_modules), workspaces,
                      make_installed_reader(args.node_modules) if args.node_modules else None)
    for path in empty:
        result["problems"].append(f"{path} has no components")
    result["problems"] = sorted(set(result["problems"]))
    result["ok"] = result["ok"] and not empty
    print(_render(result, args.sbom))
    if args.report:
        with open(args.report, "w", encoding="utf-8") as f:
            json.dump(result, f, indent=2)
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    sys.exit(main())
