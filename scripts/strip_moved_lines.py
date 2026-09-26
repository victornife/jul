#!/usr/bin/env python3
"""Drop verbatim-moved lines from a `git diff --unified=0` for added-code gates.

The per-issue coverage gates measure statements on *added* lines. A pure move
(code cut from one file and pasted into another) shows up as added lines even
though no code changed, so moved, pre-existing statements would be charged to
the moving change. This filter removes every added line whose exact text
(ignoring surrounding whitespace) is also removed elsewhere in the same diff,
matched one-for-one, and rewrites the hunk headers so the remaining added
lines keep their real new-file line numbers. Genuinely new or edited lines are
kept, so a moved block that was also modified is still measured where it
changed.

usage: git diff --unified=0 BASE...HEAD -- '*.go' | scripts/strip_moved_lines.py
"""
import re
import sys
from collections import Counter

HUNK = re.compile(r"^@@ -\S+ \+(\d+)(?:,(\d+))? @@")


def strip_moved(diff: str) -> str:
    lines = diff.splitlines()
    removed = Counter(
        l[1:].strip() for l in lines if l.startswith("-") and not l.startswith("---") and l[1:].strip()
    )
    out = []
    run = []
    run_start = 0

    def flush():
        nonlocal run
        if run:
            out.append(f"@@ -0,0 +{run_start},{len(run)} @@")
            out.extend(run)
            run = []

    lineno = 0
    for line in lines:
        if line.startswith("diff --git") or line.startswith("--- ") or line.startswith("+++ ") or line.startswith("index "):
            flush()
            out.append(line)
            continue
        m = HUNK.match(line)
        if m:
            flush()
            lineno = int(m.group(1))
            continue
        if line.startswith("+"):
            text = line[1:].strip()
            if text and removed[text] > 0:
                removed[text] -= 1
                flush()
            else:
                if not run:
                    run_start = lineno
                run.append(line)
            lineno += 1
            continue
        flush()
    flush()
    return "\n".join(out) + ("\n" if out else "")


if __name__ == "__main__":
    sys.stdout.write(strip_moved(sys.stdin.read()))
