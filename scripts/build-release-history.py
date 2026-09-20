#!/usr/bin/env python3
"""Build the offline release-note archive embedded in published binaries."""

from __future__ import annotations

import argparse
import json
import re
from pathlib import Path


CALVER = re.compile(r"^v(\d{4})\.(\d{1,2})(?:\.(\d+))?$")
DEFAULT_LIMIT = 10


def version_key(tag: str) -> tuple[int, int, int]:
    match = CALVER.fullmatch(tag)
    if not match:
        raise ValueError(f"invalid CalVer tag: {tag}")
    return int(match.group(1)), int(match.group(2)), int(match.group(3) or 0)


def load_maturities(path: Path) -> dict[str, str]:
    records = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(records, list):
        raise ValueError("release maturity metadata must be a JSON array")
    maturities: dict[str, str] = {}
    for record in records:
        if not isinstance(record, dict) or record.get("isDraft"):
            continue
        tag = record.get("tagName")
        if isinstance(tag, str) and CALVER.fullmatch(tag):
            maturities[tag] = "beta" if record.get("isPrerelease") else "release"
    return maturities


def normalize_target_maturity(value: str) -> str:
    if value == "release":
        return "release"
    if value in {"prerelease", "draft"}:
        return "beta"
    raise ValueError(f"unknown target maturity: {value}")


def build(
    root: Path,
    through: str,
    maturities: dict[str, str],
    target_maturity: str,
    limit: int = DEFAULT_LIMIT,
) -> list[dict[str, str]]:
    if limit < 1:
        raise ValueError("release history limit must be positive")
    ceiling = version_key(through)
    items: list[dict[str, str]] = []
    for path in root.glob("docs/releases/[0-9][0-9][0-9][0-9]/v*.md"):
        tag = path.stem
        try:
            key = version_key(tag)
        except ValueError:
            continue
        if key > ceiling:
            continue
        # A committed note can precede publication, and a draft may never become a public release.
        # The target is included because this archive is built before its Release is published;
        # every older entry must already have public GitHub maturity metadata.
        if tag != through and tag not in maturities:
            continue
        markdown = path.read_text(encoding="utf-8").strip()
        if not markdown:
            raise ValueError(f"empty release note: {path}")
        first = markdown.splitlines()[0].removeprefix("# ").strip()
        title = first.split(" — ", 1)[1].strip() if " — " in first else ""
        items.append({"tag": tag, "title": title, "markdown": markdown})
    items.sort(key=lambda item: version_key(item["tag"]), reverse=True)
    if not any(item["tag"] == through for item in items):
        raise ValueError(f"release history does not contain target {through}")
    selected = items[:limit]
    for item in selected:
        tag = item["tag"]
        maturity = target_maturity if tag == through else maturities.get(tag)
        if maturity not in {"release", "beta"}:
            raise ValueError(f"no release maturity for {tag}")
        item["maturity"] = maturity
    return selected


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path.cwd())
    parser.add_argument("--through", required=True)
    parser.add_argument("--limit", type=int, default=DEFAULT_LIMIT)
    parser.add_argument("--maturities", type=Path, required=True)
    parser.add_argument("--target-maturity", choices=("prerelease", "release", "draft"), required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    maturities = load_maturities(args.maturities.resolve())
    items = build(
        args.root.resolve(),
        args.through,
        maturities,
        normalize_target_maturity(args.target_maturity),
        args.limit,
    )
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(items, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
