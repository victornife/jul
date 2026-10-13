#!/usr/bin/env python3
"""Check D11's frozen denominator and source-backed disposition packet."""
from __future__ import annotations
import csv
import hashlib
import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]


def check(root: Path, complete: bool = False) -> list[str]:
    errors: list[str] = []
    base = root / "docs/audit"
    frozen = json.loads((base / "2026-10-13-d11-original.json").read_text())
    current = json.loads((base / "2026-10-13-d11-certification.json").read_text())
    complete = complete or current.get("state") in {"ready_for_review", "completed"}
    original = root / frozen["original_ledger"]
    if hashlib.sha256(original.read_bytes()).hexdigest() != frozen["sha256"]:
        errors.append("Original historical ledger changed")
    with original.open() as stream:
        rows = list(csv.DictReader(stream, delimiter="\t"))
    unreviewed = [r for r in rows if r["disposition"] == "not reviewed"]
    if len(rows) != frozen["original_total"] or len(unreviewed) != frozen["original_unreviewed"]:
        errors.append("Original denominator changed")
    expected = {r["id"]: r for r in frozen["frozen_rows"]}
    actual = current["rows"]
    if len(actual) != len(expected) or {r["id"] for r in actual} != set(expected):
        errors.append("Original row missing, duplicated or denominator replaced")
    new_ids = [r["id"] for r in current["new_rows"]]
    if len(set(new_ids)) != len(new_ids) or set(new_ids) & set(expected):
        errors.append("Supplemental row ID duplicated or overlaps original inventory")
    tests: set[str] = set()
    for directory in (root / "internal", root / "cmd"):
        for path in directory.rglob("*_test.go"):
            tests.update(re.findall(r"^func (Test\w+)\(", path.read_text(), re.M))
    verdicts = {"correct", "fixed in this PR", "accepted limitation", "defect filed/linked"}
    for row in actual + current["new_rows"]:
        name = row["id"]
        old = expected.get(name)
        if old and any(row.get(key) != old.get(key) for key in
                       ("original_path", "original_disposition", "original_reason_and_limit")):
            errors.append(f"{name}: original identity/reason was rewritten")
        if old and row.get("current_paths") != old.get("current_paths") and not row.get("path_reconciliation"):
            errors.append(f"{name}: unexplained moved/removed/wildcard boundary")
        if not row.get("responsible_owner") or not row.get("review_boundary"):
            errors.append(f"{name}: owner/boundary missing")
        if not row.get("current_paths") and not row.get("path_reconciliation"):
            errors.append(f"{name}: current paths missing without reconciliation")
        for path in row.get("current_paths", []):
            relative = Path(path)
            if relative.is_absolute() or ".." in relative.parts or not (root / relative).exists():
                errors.append(f"{name}: missing or invalid current path {path}")
        if row.get("verdict") is None:
            if complete:
                errors.append(f"{name}: no substantive final disposition")
            continue
        if row["verdict"] not in verdicts:
            errors.append(f"{name}: invalid verdict")
        if row.get("status") not in {"reviewed", "superseded-context"}:
            errors.append(f"{name}: disposition has a nonfinal row status")
        if not re.fullmatch(r"https://github.com/victornife/jul/pull/\d+", row.get("establishing_pr") or ""):
            errors.append(f"{name}: establishing PR is not a concrete reviewable PR")
        for key in ("claims", "implementation_and_authority", "execution_evidence", "limitations", "establishing_pr"):
            if not row.get(key):
                errors.append(f"{name}: {key} missing")
        for ref in row.get("implementation_and_authority", []):
            if not (root / ref).exists():
                errors.append(f"{name}: missing implementation/authority {ref}")
        for test in row.get("named_tests", []):
            if test.startswith("Test") and test not in tests:
                errors.append(f"{name}: nonexistent named test {test}")
        for evidence in row.get("execution_evidence", []):
            if not evidence.startswith("https://") and not (base / evidence).exists():
                errors.append(f"{name}: missing execution evidence {evidence}")
        if row["verdict"] == "defect filed/linked" and not row.get("open_defects"):
            errors.append(f"{name}: filed-defect verdict without an owner link")
    review = current["reviewer_spot_check"]
    if review["status"] == "completed":
        minimum = (len(expected) + 9) // 10
        sampled = set(review.get("sampled_rows", []))
        if not sampled <= set(expected) or len(sampled) < minimum or not all(review.get(k) for k in ("reviewer", "source", "result")):
            errors.append("Completed reviewer spot-check lacks reviewer/source/10% original sample/result")
        if review.get("review_kind") not in {"maintainer-human", "maintainer-assigned-human"}:
            errors.append("Completed reviewer spot-check must identify an actual human review; agent preparation does not qualify")
    elif current.get("state") == "completed":
        errors.append("Completed D11 state requires the actual human reviewer spot-check")
    return errors


if __name__ == "__main__":
    failures = check(ROOT, "--complete" in sys.argv)
    for error in failures:
        print(f"D11: {error}")
    print(f"D11 inventory/disposition check: {len(failures)} errors")
    raise SystemExit(bool(failures))
