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


def build(root: Path, through: str, limit: int = DEFAULT_LIMIT) -> list[dict[str, str]]:
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
        markdown = path.read_text(encoding="utf-8").strip()
        if not markdown:
            raise ValueError(f"empty release note: {path}")
        first = markdown.splitlines()[0].removeprefix("# ").strip()
        title = first.split(" — ", 1)[1].strip() if " — " in first else ""
        items.append({"tag": tag, "title": title, "markdown": markdown})
    items.sort(key=lambda item: version_key(item["tag"]), reverse=True)
    if not any(item["tag"] == through for item in items):
        raise ValueError(f"release history does not contain target {through}")
    return items[:limit]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path.cwd())
    parser.add_argument("--through", required=True)
    parser.add_argument("--limit", type=int, default=DEFAULT_LIMIT)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    items = build(args.root.resolve(), args.through, args.limit)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(items, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
