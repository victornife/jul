#!/usr/bin/env python3
"""Enforce issue #151's >=90.0% statement coverage on AUTO-04 production code."""

from __future__ import annotations

import argparse
import re
import sys
from collections import defaultdict
from pathlib import Path

LINE_RE = re.compile(
    r"^(?P<path>.+):\d+\.\d+,\d+\.\d+ (?P<statements>\d+) (?P<count>\d+)$"
)

TARGETS = {
    "adminclient package": lambda p: p.startswith("jul/internal/adminclient/") and not p.endswith("_test.go"),
    "cmd/jul AUTO-04 files": lambda p: p in {
        "jul/cmd/jul/remote_cli.go",
        "jul/cmd/jul/remote_usage.go",
    },
}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--coverprofile", required=True, type=Path)
    parser.add_argument("--floor", type=float, default=90.0)
    args = parser.parse_args()
    if args.floor < 0 or args.floor > 100:
        parser.error("--floor must be between 0 and 100")

    try:
        lines = args.coverprofile.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        print(f"ERROR: cannot read {args.coverprofile}: {exc}", file=sys.stderr)
        return 2
    if not lines or not lines[0].startswith("mode: "):
        print("ERROR: invalid Go cover profile", file=sys.stderr)
        return 2

    totals: dict[str, list[int]] = defaultdict(lambda: [0, 0])
    for n, line in enumerate(lines[1:], start=2):
        if not line:
            continue
        m = LINE_RE.match(line)
        if not m:
            print(f"ERROR: malformed cover profile line {n}: {line!r}", file=sys.stderr)
            return 2
        source = m.group("path")
        statements = int(m.group("statements"))
        covered = int(m.group("count")) > 0
        for label, matches in TARGETS.items():
            if matches(source):
                totals[label][1] += statements
                if covered:
                    totals[label][0] += statements

    failed = False
    for label in TARGETS:
        covered, total = totals[label]
        if total == 0:
            print(f"ERROR {label}: absent from coverage profile", file=sys.stderr)
            return 2
        pct = covered * 100.0 / total
        text = f"{label}: {pct:.2f}% ({covered}/{total} statements), floor {args.floor:.1f}%"
        if pct + 1e-12 < args.floor:
            print("FAIL " + text, file=sys.stderr)
            failed = True
        else:
            print("PASS " + text)
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
