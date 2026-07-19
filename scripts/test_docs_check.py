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


def _run_in_tmp(root: Path, fn) -> tuple[int, int]:
    """Run a docs-check function against a temporary docs tree.

    Returns the (ok_count, fail_count) produced by the function.
    """
    original_root = docs_check.ROOT
    original_docs = docs_check.DOCS
    docs_check.ROOT = root
    docs_check.DOCS = root / "docs"
    docs_check.OK = 0
    docs_check.FAIL = 0
    try:
        fn()
        return docs_check.OK, docs_check.FAIL
    finally:
        docs_check.ROOT = original_root
        docs_check.DOCS = original_docs


def test_check_horizon_specs_detects_missing_banner():
    """Year 3–5 specs without the standard banner must fail."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        specs = root / "docs" / "specs"
        specs.mkdir(parents=True)
        for year in (3, 4, 5):
            (specs / f"year-{year}.md").write_text("# Year spec\n\nSome content.\n", encoding="utf-8")

        ok, fail = _run_in_tmp(root, docs_check.check_horizon_specs)
        assert fail == 3, f"expected 3 failures, got {fail}"
        assert ok == 0, f"expected zero OK, got {ok}"


def test_check_horizon_specs_passes_with_banner():
    """Year 3–5 specs with the standard banner must pass."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        specs = root / "docs" / "specs"
        specs.mkdir(parents=True)
        banner = docs_check.HORIZON_BANNER_TEXT
        for year in (3, 4, 5):
            (specs / f"year-{year}.md").write_text(
                f"# Year spec\n\n> {banner}. Revalidate before starting.\n", encoding="utf-8"
            )

        ok, fail = _run_in_tmp(root, docs_check.check_horizon_specs)
        assert fail == 0, f"expected zero failures, got {fail}"
        assert ok == 3, f"expected 3 OK, got {ok}"


def test_check_active_roadmap_links_detects_broken_link():
    """A broken link inside the Active operating roadmap section must fail."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        roadmap = root / "docs" / "roadmap"
        roadmap.mkdir(parents=True)
        (roadmap / "README.md").write_text(
            "# Roadmap\n\n"
            "## Active operating roadmap\n\n"
            "| Phase | Focus |\n"
            "|---|---|\n"
            "| Phase 2 | [broken](missing.md) |\n\n"
            "## Delivered\n",
            encoding="utf-8",
        )

        ok, fail = _run_in_tmp(root, docs_check.check_active_roadmap_links)
        assert fail == 1, f"expected one failure, got {fail}"


def test_check_active_roadmap_links_ignores_external_links():
    """External links in the active section do not need local resolution."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        roadmap = root / "docs" / "roadmap"
        roadmap.mkdir(parents=True)
        (roadmap / "README.md").write_text(
            "# Roadmap\n\n"
            "## Active operating roadmap\n\n"
            "| Phase | Focus |\n"
            "|---|---|\n"
            "| Phase 2 | [external](https://example.com) |\n\n"
            "## Delivered\n",
            encoding="utf-8",
        )

        ok, fail = _run_in_tmp(root, docs_check.check_active_roadmap_links)
        assert fail == 0, f"expected zero failures, got {fail}"


def test_check_roadmap_active_ids_detects_duplicate():
    """Duplicate active roadmap IDs must be flagged."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        roadmap = root / "docs" / "roadmap"
        roadmap.mkdir(parents=True)
        (roadmap / "README.md").write_text(
            "# Roadmap\n\n"
            "## Hardening & platform\n\n"
            "| ID | Item | Status |\n"
            "|---|---|---|\n"
            "| HP-01 | Reload observability | 🚧 Phase 2 active |\n"
            "| HP-01 | Duplicate row | 🚧 Phase 2 active |\n\n",
            encoding="utf-8",
        )

        ok, fail = _run_in_tmp(root, docs_check.check_roadmap_active_ids)
        assert fail == 1, f"expected one failure, got {fail}"


def test_check_roadmap_active_ids_detects_delivered_overlap():
    """An ID listed as both active and delivered must fail."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        roadmap = root / "docs" / "roadmap"
        roadmap.mkdir(parents=True)
        (roadmap / "README.md").write_text(
            "# Roadmap\n\n"
            "## Hardening & platform\n\n"
            "| ID | Item | Status |\n"
            "|---|---|---|\n"
            "| HP-03 | Metric cardinality | ✅ Delivered |\n"
            "| HP-03 | Metric cardinality | 🚧 Phase 2 active |\n\n",
            encoding="utf-8",
        )

        ok, fail = _run_in_tmp(root, docs_check.check_roadmap_active_ids)
        assert fail == 1, f"expected one failure, got {fail}"


def test_check_roadmap_active_ids_passes_valid():
    """A clean mix of delivered and active IDs passes."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        roadmap = root / "docs" / "roadmap"
        roadmap.mkdir(parents=True)
        (roadmap / "README.md").write_text(
            "# Roadmap\n\n"
            "## Active operating roadmap\n\n"
            "| Phase | Focus | Status |\n"
            "|---|---|---|\n"
            "| Phase 2 | Reload results | 🚧 next |\n"
            "| Phase 3 | RBAC | 🔒 queued |\n\n"
            "## Hardening & platform\n\n"
            "| ID | Item | Status |\n"
            "|---|---|---|\n"
            "| HP-03 | Metric cardinality | ✅ Delivered |\n"
            "| HP-01 | Reload observability | 🚧 Phase 2 active |\n\n",
            encoding="utf-8",
        )

        ok, fail = _run_in_tmp(root, docs_check.check_roadmap_active_ids)
        assert fail == 0, f"expected zero failures, got {fail}"
        assert ok >= 1, f"expected at least one OK, got {ok}"


if __name__ == "__main__":
    test_check_finding_uniqueness_detects_conflict()
    test_check_finding_uniqueness_allows_decimal_suffixes()
    test_check_horizon_specs_detects_missing_banner()
    test_check_horizon_specs_passes_with_banner()
    test_check_active_roadmap_links_detects_broken_link()
    test_check_active_roadmap_links_ignores_external_links()
    test_check_roadmap_active_ids_detects_duplicate()
    test_check_roadmap_active_ids_detects_delivered_overlap()
    test_check_roadmap_active_ids_passes_valid()
    print("OK")
