#!/usr/bin/env python3
"""Validate the bilingual reader-note contract used by the release workflow."""

from __future__ import annotations

import re
import sys


def validate_bilingual_notes(notes: str) -> None:
    for locale in ("zh-CN", "en-US"):
        sections = re.findall(rf"<{locale}>(.*?)</{locale}>", notes, flags=re.DOTALL)
        if len(sections) != 1:
            raise ValueError(f"release notes must contain exactly one <{locale}> section")
        if not sections[0].strip():
            raise ValueError(f"the <{locale}> release-note section cannot be empty")


def main() -> int:
    notes = sys.stdin.read()
    try:
        validate_bilingual_notes(notes)
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
