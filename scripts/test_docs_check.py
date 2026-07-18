#!/usr/bin/env python3
"""Unit tests for docs-check.py helpers.

Run with: python3 scripts/test_docs_check.py
"""

import sys
import tempfile
from pathlib import Path

import importlib.util

docs_check_path = Path(__file__).resolve().parent / "docs-check.py"
spec = importlib.util.spec_from_file_location("docs_check", docs_check_path)
assert spec is not None and spec.loader is not None
sys.modules["docs_check"] = importlib.util.module_from_spec(spec)
docs_check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(docs_check)


def test_check_finding_uniqueness_detects_conflict():
    """A finding marked both implemented and deferred must be flagged."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        audit = root / "docs" / "audit-register.md"
        audit.parent.mkdir(parents=True)
        audit.write_text(
            "# Audit Register\n\n"
            "| Finding | Title | Fix location | Test | Evidence | Status | Commit |\n"
            "|---|---|---|---|---|---|---|\n"
            "| R9-01 | Example | internal/x | TestX | unit | ✅ Implemented | abc123 |\n"
            "| R9-01 | Example | internal/x | TestX | unit | ⏳ Deferred | — |\n",
            encoding="utf-8",
        )

        original_root = docs_check.ROOT
        original_ok = docs_check.OK
        original_fail = docs_check.FAIL
        docs_check.ROOT = root
        docs_check.OK = 0
        docs_check.FAIL = 0
        try:
            docs_check.check_finding_uniqueness()
            assert docs_check.FAIL == 1, f"expected one failure, got {docs_check.FAIL}"
            assert docs_check.OK == 0, f"expected zero OK, got {docs_check.OK}"
        finally:
            docs_check.ROOT = original_root
            docs_check.OK = original_ok
            docs_check.FAIL = original_fail


def test_check_finding_uniqueness_allows_decimal_suffixes():
    """Sub-finding IDs like R9-14.1 are parsed and may coexist."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        audit = root / "docs" / "audit-register.md"
        audit.parent.mkdir(parents=True)
        audit.write_text(
            "# Audit Register\n\n"
            "| Finding | Title | Fix location | Test | Evidence | Status | Commit |\n"
            "|---|---|---|---|---|---|---|\n"
            "| R9-14.1 | One | internal/x | TestX | unit | ✅ Implemented | abc123 |\n"
            "| R9-14.2 | Two | internal/x | TestX | unit | ⏳ Deferred | — |\n",
            encoding="utf-8",
        )

        original_root = docs_check.ROOT
        original_ok = docs_check.OK
        original_fail = docs_check.FAIL
        docs_check.ROOT = root
        docs_check.OK = 0
        docs_check.FAIL = 0
        try:
            docs_check.check_finding_uniqueness()
            assert docs_check.FAIL == 0, f"expected zero failures, got {docs_check.FAIL}"
            assert docs_check.OK == 1, f"expected one OK, got {docs_check.OK}"
        finally:
            docs_check.ROOT = original_root
            docs_check.OK = original_ok
            docs_check.FAIL = original_fail


if __name__ == "__main__":
    test_check_finding_uniqueness_detects_conflict()
    test_check_finding_uniqueness_allows_decimal_suffixes()
    print("OK")
