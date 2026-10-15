#!/usr/bin/env python3
"""Score the complete changed Go-production diff with the HR-07C block algorithm.

Consumes a zero-context diff after strip_moved_lines.py. No package allowlist:
every changed non-test Go statement must be measured; the Go instrumenter
distinguishes declarations from missing coverage. Standalone Go
tools report absolute filenames, normalized to the repository root here.
"""
import argparse
import json
import re
import subprocess
import tempfile
from pathlib import Path


def instrumented_spans(path):
    """Ask Go's coverage instrumenter which source spans contain statements.

    Struct fields and static configuration tables are code but are not Go
    statements. Checking the instrumenter's complete inventory distinguishes
    those edits from missing test profiles without a filename exclusion.
    """
    with tempfile.TemporaryDirectory() as directory:
        output = Path(directory) / "instrumented.go"
        subprocess.run(["go", "tool", "cover", "-mode=set", "-var=GateCoverage",
                        "-o", str(output), str(path)], check=True, capture_output=True, text=True)
        source = output.read_text()
    positions = re.search(r"Pos:\s*\[[^\]]+\]uint32\{(.*?)\}", source, re.S)
    statements = re.search(r"NumStmt:\s*\[[^\]]+\]uint16\{(.*?)\}", source, re.S)
    if positions is None or statements is None:
        raise ValueError(f"cannot read Go coverage statement inventory: {path}")
    spans = re.findall(r"(\d+),\s*(\d+),\s*0x[0-9a-f]+,", positions[1])
    counts = re.findall(r"(\d+),", re.sub(r"//[^\n]*", "", statements[1]))
    if len(spans) != len(counts):
        raise ValueError(f"inconsistent Go coverage statement inventory: {path}")
    return [(int(lo), int(hi)) for (lo, hi), count in zip(spans, counts) if int(count)]


def score(diff, profiles, root):
    added, current = {}, None
    for line in diff.splitlines():
        if line.startswith("+++ b/"):
            current = line[6:]
            if not current.endswith(".go") or current.endswith("_test.go"):
                current = None
            else:
                added.setdefault(current, set())
        elif current is not None and line.startswith("@@"):
            match = re.search(r"\+(\d+)(?:,(\d+))?", line)
            start, count = int(match[1]), int(match[2] or "1")
            added[current].update(range(start, start + count))
    executions = {}
    for profile in profiles:
        for line in profile.splitlines()[1:]:
            span, statements, count = line.rsplit(" ", 2)
            path, coords = span.rsplit(":", 1)
            if Path(path).is_absolute():
                path = str(Path(path).relative_to(root))
            elif path.startswith("jul/"):
                path = path[4:]
            if path not in added:
                continue
            match = re.match(r"(\d+)\.\d+,(\d+)\.\d+", coords)
            lo, hi = int(match[1]), int(match[2])
            if any(lo <= line <= hi for line in added[path]):
                key = (path, coords, int(statements))
                executions[key] = executions.get(key, 0) + int(count)
    measured = {key[0] for key in executions}
    for path, numbers in added.items():
        lines = (root / path).read_text().splitlines()
        code = any(lines[n - 1].strip() and not lines[n - 1].lstrip().startswith(("//", "/*", "*", "*/")) for n in numbers)
        if code and path not in measured:
            if any(lo <= n <= hi for lo, hi in instrumented_spans(root / path) for n in numbers):
                raise ValueError(f"changed production file has no measured statements: {path}")
    total = sum(key[2] for key in executions)
    covered = sum(key[2] for key, count in executions.items() if count > 0)
    return {"covered": covered, "total": total, "percent": covered * 100 / total if total else None,
            "files": sorted(measured), "uncovered": [list(key) for key, count in executions.items() if count == 0]}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--diff", required=True, type=Path)
    parser.add_argument("--profile", required=True, action="append", type=Path)
    parser.add_argument("--floor", type=float, default=90)
    args = parser.parse_args()
    result = score(args.diff.read_text(), [p.read_text() for p in args.profile], Path.cwd())
    print(json.dumps(result, indent=2))
    raise SystemExit(1 if result["percent"] is not None and result["percent"] < args.floor else 0)
