#!/usr/bin/env python3
"""Verify that FULL_TAGS in Makefile and .github/workflows/ci.yml stay in sync."""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def extract_from_makefile() -> str | None:
    text = (ROOT / "Makefile").read_text(encoding="utf-8")
    m = re.search(r'^FULL_TAGS\s*:=\s*(.+)$', text, re.MULTILINE)
    if not m:
        return None
    # Strip optional quotes
    return m.group(1).strip().strip('"\'')


def extract_from_ci() -> str | None:
    text = (ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
    m = re.search(r'^\s*FULL_TAGS:\s*"(.+)"$', text, re.MULTILINE)
    if not m:
        return None
    return m.group(1).strip()


def main() -> int:
    mk = extract_from_makefile()
    ci = extract_from_ci()

    if mk is None:
        print("ERROR: FULL_TAGS not found in Makefile", file=sys.stderr)
        return 1
    if ci is None:
        print("ERROR: FULL_TAGS not found in .github/workflows/ci.yml", file=sys.stderr)
        return 1

    if mk != ci:
        print(f"ERROR: FULL_TAGS mismatch", file=sys.stderr)
        print(f"  Makefile: {mk}", file=sys.stderr)
        print(f"  ci.yml:   {ci}", file=sys.stderr)
        return 1

    print(f"OK: FULL_TAGS in sync ({len(mk.split())} tags)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
