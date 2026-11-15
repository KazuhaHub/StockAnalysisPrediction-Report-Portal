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
   applies only to a component that fails on its merits for a reason other than a denied licence, and only while
   the file that ships in the Go module cache or node_modules still hashes to the pinned value. A changed or
   missing file makes the record stale: it is ignored, with a warning, and the gate fails again.
6. The project's own licence (AGPL-3.0-only) is allowed only on the own components: an entry ending in "/" is
   a namespace prefix, any other entry names one package exactly.

This evaluator is duplicated in the Apps repository (scripts/licence_scan.py in KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps); keep the two in sync.
"""
import argparse
import hashlib
import json
import os
import re
import sys
import urllib.parse

OK, ELECTION_REQUIRED, UNKNOWN, DENIED = "ok", "election-required", "unknown", "denied"
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


def make_reader(go_mod_cache=None, node_modules=None):
    """A reader of one file inside a component's shipped package: the Go module cache directory of the
    module version, or the package directory under node_modules. It never leaves that directory."""
    def read(comp, name):
        root = None
        go = _purl_path(comp, "golang")
        npm = _purl_path(comp, "npm")
        if go and go_mod_cache and comp.get("version"):
            root = os.path.join(go_mod_cache, _go_escape(go) + "@" + comp["version"])
        elif npm and node_modules:
            root = os.path.join(node_modules, npm)
        if root is None or not os.path.isdir(root):
            return None
        real_root = os.path.realpath(root)
        path = os.path.realpath(os.path.join(real_root, name))
        if os.path.commonpath([real_root, path]) != real_root or not os.path.isfile(path):
            return None
        if os.path.getsize(path) > MAX_LICENCE_FILE:
            return None
        with open(path, "rb") as f:
            return f.read()
    return read


def component_key(comp):
    group = comp.get("group")
    return f"{group}/{comp['name']}" if group else comp["name"]


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
                continue
            # A name is taken literally, never parsed: it is free text, and an id is one identifier.
            nodes.append(("id", text))
        texts.append(text)
    return nodes, " AND ".join(texts)


def _norm_decl(text):
    text = re.sub(r"\s+", " ", text.strip()).lower()
    while text.startswith("(") and text.endswith(")") and _wraps(text):
        text = text[1:-1].strip()
    return text


def _wraps(text):
    depth = 0
    for i, ch in enumerate(text):
        depth += (ch == "(") - (ch == ")")
        if depth == 0 and i < len(text) - 1:
            return False
    return True


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


def evaluate(sbom, policy, verified, read_licence=None):
    """Judge every component of `sbom` (a dict with "components") against `policy`.

    Returns {"ok", "components", "failures", "warnings", "problems"}. `verified` is the set of check
    names the caller has confirmed passed; exclusions that require a name outside it do not apply.
    `read_licence(component, filename)` returns the bytes of a file in the shipped package, or None; without
    one no reviewed-as record can be verified.
    """
    comps = sbom.get("components") or []
    problems = []
    if not comps:
        problems.append("SBOM has no components: the generator produced nothing to scan")
    exclusions = {e["component"]: e for e in policy["exclusions"]}
    elections = {e["component"]: e for e in policy["elections"]}
    reviewed = {name: rec for rec in policy.get("reviewed", []) for name in rec["components"]}
    allow_lower = {a.lower() for a in policy["allow"]}
    own_prefixes = policy["own"]["components"]
    seen = set()
    results, warnings = [], []
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
        if not nodes:
            rec = reviewed.get(key)
            if rec is not None and _apply_review(key, comp, rec, UNKNOWN, allow_lower, read_licence, warnings):
                results.append(dict(base, status="pass", via="reviewed", reason="reviewed",
                                    reviewed_as=rec["reviewed_as"], detail=rec["reason"]))
            else:
                results.append(_fail(base, "no-licence", "the SBOM records no licence for this component"))
            continue
        own = any(key == p or (p.endswith("/") and key.startswith(p)) for p in own_prefixes)
        judge = _Judge(policy, own)
        election = elections.get(key)
        if election is not None:
            if _norm_decl(election["declared"]) != _norm_decl(declared):
                warnings.append(f"stale election for {key}: it was written for {election['declared']!r} "
                                f"but the SBOM declares {declared!r}; ignored")
            else:
                verdict = judge.atom(election["elected"])
                if verdict == OK or election["elected"].lower() in judge.election_only:
                    results.append(dict(base, status="pass", via="election", reason="elected",
                                        elected=election["elected"], detail=election["reason"]))
                    continue
                warnings.append(f"election for {key} names {election['elected']!r}, which the policy does not "
                                f"permit; ignored")
        verdict = judge.node(("and", nodes) if len(nodes) > 1 else nodes[0])
        if verdict == OK:
            results.append(dict(base, status="pass", via="own" if judge.used_own else "allow", reason="allowed"))
        else:
            rec = reviewed.get(key)
            if rec is not None:
                outcome = _apply_review(key, comp, rec, verdict, allow_lower, read_licence, warnings)
                if outcome is not None:
                    results.append(dict(base, status="pass", via="reviewed", reason="reviewed",
                                        reviewed_as=rec["reviewed_as"], detail=rec["reason"]))
                    continue
            detail = {DENIED: "a denied licence", UNKNOWN: "a licence that is on neither list",
                      ELECTION_REQUIRED: "only allowed through an election record, and none applies"}[verdict]
            results.append(_fail(base, verdict, detail))
    for name, e in {**elections, **exclusions, **reviewed}.items():
        if name not in seen:
            warnings.append(f"policy entry for {name} is not present in this SBOM set")
    failures = [r for r in results if r["status"] == "fail"]
    return {"ok": not failures and not problems, "components": results, "failures": failures,
            "warnings": warnings, "problems": problems}


def _apply_review(key, comp, rec, verdict, allow_lower, read_licence, warnings):
    """True when the reviewed-as record holds for this component; otherwise None, with the reason as a warning."""
    if rec["reviewed_as"].lower() not in allow_lower:
        warnings.append(f"reviewed-as record for {key} reviews it as {rec['reviewed_as']!r}, which is not allowed "
                        f"by the policy; ignored")
        return None
    if verdict == DENIED:
        warnings.append(f"reviewed-as record for {key} cannot override a denied licence; ignored")
        return None
    data = read_licence(comp, rec["file"]) if read_licence else None
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
        out.append((entry.get("expression") or lic.get("id") or lic.get("name") or "").strip())
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
    ap.add_argument("--report", help="write the full result as JSON")
    ap.add_argument("sbom", nargs="+")
    args = ap.parse_args(argv)
    try:
        policy = load_policy(args.policy)
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
                      make_reader(args.go_mod_cache, args.node_modules))
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
