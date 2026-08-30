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


def _write_adrs(root: Path, records, index_names=None):
    """Write an ADR tree; records is a list of (filename, heading_number)."""
    adr = root / "docs" / "adr"
    adr.mkdir(parents=True)
    for name, heading in records:
        (adr / name).write_text(f"# ADR {heading} — Title\n\n## Context\n", encoding="utf-8")
    listed = index_names if index_names is not None else [n for n, _ in records]
    body = "# ADRs\n\n" + "".join(f"- [x]({n})\n" for n in listed)
    (adr / "README.md").write_text(body, encoding="utf-8")
    return adr


def test_check_adr_numbering_passes_on_a_clean_tree():
    """Unique, contiguous, indexed ADRs whose headings match must pass."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_adrs(root, [("0001-first.md", "0001"), ("0002-second-one.md", "0002")])

        ok, fail = _run_in_tmp(root, docs_check.check_adr_numbering)
        assert fail == 0, f"expected zero failures, got {fail}"
        assert ok == 1, f"expected 1 OK, got {ok}"


def test_check_adr_numbering_detects_duplicate_number():
    """Two ADRs sharing a number must fail — the defect that motivated this guard."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_adrs(root, [("0001-first.md", "0001"), ("0001-also-first.md", "0001")])

        _, fail = _run_in_tmp(root, docs_check.check_adr_numbering)
        assert fail == 1, f"expected 1 failure, got {fail}"


def test_check_adr_numbering_detects_gap():
    """A missing number in the sequence must fail."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_adrs(root, [("0001-first.md", "0001"), ("0003-third.md", "0003")])

        _, fail = _run_in_tmp(root, docs_check.check_adr_numbering)
        assert fail == 1, f"expected 1 failure, got {fail}"


def test_check_adr_numbering_detects_heading_mismatch():
    """A heading number that disagrees with the filename must fail."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_adrs(root, [("0001-first.md", "0007")])

        _, fail = _run_in_tmp(root, docs_check.check_adr_numbering)
        assert fail == 1, f"expected 1 failure, got {fail}"


def test_check_adr_numbering_detects_unindexed_record():
    """An ADR absent from the index must fail."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_adrs(
            root,
            [("0001-first.md", "0001"), ("0002-second.md", "0002")],
            index_names=["0001-first.md"],
        )

        _, fail = _run_in_tmp(root, docs_check.check_adr_numbering)
        assert fail == 1, f"expected 1 failure, got {fail}"


def test_check_adr_numbering_detects_bad_filename():
    """A filename that is not NNNN-kebab-case.md must fail."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_adrs(root, [("0001-first.md", "0001"), ("2-Bad_Name.md", "0002")])

        _, fail = _run_in_tmp(root, docs_check.check_adr_numbering)
        assert fail == 1, f"expected 1 failure, got {fail}"


def test_check_adr_numbering_requires_an_index():
    """A missing docs/adr/README.md must fail."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        adr = root / "docs" / "adr"
        adr.mkdir(parents=True)
        (adr / "0001-first.md").write_text("# ADR 0001 — Title\n", encoding="utf-8")

        _, fail = _run_in_tmp(root, docs_check.check_adr_numbering)
        assert fail == 1, f"expected 1 failure, got {fail}"


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


# ── Lifecycle artifact checks ────────────────────────────────────────────────
#
# docs-check.py no longer reimplements lifecycle semantics: the Go generator is
# the authority and this script only verifies structural properties of the
# committed mirrors. These tests pin those properties against synthetic
# metadata so a regression in the checker itself is caught.

import json


def _write_lifecycle_tree(root: Path, fields, subsystems=None, counts=None, version=2,
                          reload_semantics="# Reload semantics\n\nadmin cache tls\n"):
    """Write a minimal docs/ tree carrying generated lifecycle artifacts."""
    subsystems = subsystems if subsystems is not None else [{"name": "admin", "description": "Admin listener."}]
    counts = counts if counts is not None else {
        "schema_paths": len(fields),
        "schema_leaves": len(fields),
        "registry_entries": len(fields),
        "startup_consumed": sum(1 for f in fields if f.get("startup_consumed")),
        "by_class": {},
    }
    meta = {
        "version": version,
        "generated_by": "internal/lifecycle",
        "regenerate_command": "make lifecycle-generate",
        "classes": [],
        "subsystems": subsystems,
        "counts": counts,
        "conditions": [],
        "fields": fields,
        "schema_exemptions": [],
    }
    docs = root / "docs"
    (docs / "generated").mkdir(parents=True, exist_ok=True)
    (docs / "generated" / "config-lifecycle.json").write_text(json.dumps(meta), encoding="utf-8")
    (docs / "generated" / "config-lifecycle.md").write_text(
        "# Configuration lifecycle reference\n\n"
        + "".join(f"| `{f['path']}` | `{f['class']}` |\n" for f in fields),
        encoding="utf-8",
    )
    (docs / "config-lifecycle.yaml").write_text(
        f"version: {version}\nfields:\n"
        + "".join(f'  - path: "{f["path"]}"\n' for f in fields),
        encoding="utf-8",
    )
    (docs / "reload-semantics.md").write_text(reload_semantics, encoding="utf-8")
    return meta


def _field(path, cls="hot_reload", subsystem="admin", reason="because", startup=False):
    return {
        "path": path,
        "class": cls,
        "subsystem": subsystem,
        "reason": reason,
        "startup_consumed": startup,
        "address_keyed": False,
        "conditional": False,
        "deprecated": False,
        "ignored": False,
        "reserved": False,
        "secret_digested": False,
    }


def _run_lifecycle_checks(root: Path):
    """Run check_lifecycle_manifest with the generator step stubbed out."""
    original = docs_check._run_lifecycle_generator_check
    docs_check._run_lifecycle_generator_check = lambda: True
    try:
        return _run_in_tmp(root, docs_check.check_lifecycle_manifest)
    finally:
        docs_check._run_lifecycle_generator_check = original


def test_lifecycle_detects_missing_disposition():
    """A schema leaf without a disposition breaks the closed world."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        fields = [_field("admin.listen", "restart_required", startup=True)]
        _write_lifecycle_tree(root, fields, counts={
            "schema_paths": 2, "schema_leaves": 2, "registry_entries": 1,
            "startup_consumed": 1, "by_class": {},
        })
        _, fail = _run_lifecycle_checks(root)
        assert fail >= 1, "an unclassified schema leaf must fail the closed-world check"


def test_lifecycle_detects_restart_entry_without_extractor():
    """A restart-required path that is not startup-consumed must be flagged."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_lifecycle_tree(root, [_field("admin.listen", "restart_required", startup=False)])
        _, fail = _run_lifecycle_checks(root)
        assert fail >= 1, "a restart-required path outside the fingerprint must fail"


def test_lifecycle_detects_undocumented_subsystem():
    """A bounded subsystem set means every used subsystem is described."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_lifecycle_tree(root, [_field("admin.listen", subsystem="mystery")])
        _, fail = _run_lifecycle_checks(root)
        assert fail >= 1, "an undocumented subsystem must fail"


def test_lifecycle_detects_missing_reason():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_lifecycle_tree(root, [_field("admin.listen", reason="   ")])
        _, fail = _run_lifecycle_checks(root)
        assert fail >= 1, "a path without a reason must fail"


def test_lifecycle_detects_yaml_version_drift():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_lifecycle_tree(root, [_field("admin.listen")])
        yaml_path = root / "docs" / "config-lifecycle.yaml"
        yaml_path.write_text(yaml_path.read_text(encoding="utf-8").replace("version: 2", "version: 1"), encoding="utf-8")
        _, fail = _run_lifecycle_checks(root)
        assert fail >= 1, "a YAML version that disagrees with the metadata must fail"


def test_lifecycle_detects_missing_reload_semantics_coverage():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_lifecycle_tree(
            root,
            [_field("cache.enabled", "restart_required", subsystem="cache", startup=True)],
            subsystems=[{"name": "cache", "description": "Cache."}],
            reload_semantics="# Reload semantics\n\nnothing relevant here\n",
        )
        _, fail = _run_lifecycle_checks(root)
        assert fail >= 1, "a restart-required subsystem missing from reload-semantics.md must fail"


def test_lifecycle_passes_on_a_consistent_tree():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_lifecycle_tree(
            root,
            [_field("admin.listen", "restart_required", startup=True)],
            reload_semantics="# Reload semantics\n\nThe admin listener is startup-owned.\n",
        )
        ok, fail = _run_lifecycle_checks(root)
        assert fail == 0, f"expected zero failures, got {fail}"
        assert ok >= 1, f"expected at least one OK, got {ok}"


def test_lifecycle_generator_check_is_non_mutating_and_names_the_remedy():
    """Check mode must leave the tree byte-identical and print the remedy."""
    repo_root = Path(__file__).resolve().parent.parent
    generated = repo_root / "docs" / "generated" / "config-lifecycle.json"
    if not generated.exists():
        print("skip: generated lifecycle metadata is absent")
        return

    import subprocess

    before = {p: p.read_bytes() for p in sorted((repo_root / "docs" / "generated").iterdir())}
    before[repo_root / "docs" / "config-lifecycle.yaml"] = (repo_root / "docs" / "config-lifecycle.yaml").read_bytes()

    result = subprocess.run(
        ["go", "run", "./internal/lifecycle/lifecyclegen", "-out", "docs", "-check"],
        cwd=repo_root, capture_output=True, text=True, timeout=300,
    )
    for path, content in before.items():
        assert path.read_bytes() == content, f"check mode modified {path}"
    if result.returncode != 0:
        assert "make lifecycle-generate" in result.stderr, \
            f"stale-artifact failure must name the regeneration command: {result.stderr}"


def _run_existing_tests():
    test_check_finding_uniqueness_detects_conflict()
    test_check_finding_uniqueness_allows_decimal_suffixes()
    test_check_horizon_specs_detects_missing_banner()
    test_check_horizon_specs_passes_with_banner()
    test_check_active_roadmap_links_detects_broken_link()
    test_check_active_roadmap_links_ignores_external_links()
    test_check_roadmap_active_ids_detects_duplicate()
    test_check_roadmap_active_ids_detects_delivered_overlap()
    test_check_roadmap_active_ids_passes_valid()
    test_lifecycle_detects_missing_disposition()
    test_lifecycle_detects_restart_entry_without_extractor()
    test_lifecycle_detects_undocumented_subsystem()
    test_lifecycle_detects_missing_reason()
    test_lifecycle_detects_yaml_version_drift()
    test_lifecycle_detects_missing_reload_semantics_coverage()
    test_lifecycle_passes_on_a_consistent_tree()
    test_lifecycle_generator_check_is_non_mutating_and_names_the_remedy()


# ── Product-truth drift guards (issue #353) ─────────────────────────────────


def _write_feature_truth_tree(root: Path, *, readme_claim=False, index_link=True, delivery="merged"):
    docs = root / "docs"
    docs.mkdir(parents=True)
    (docs / "feature.md").write_text("# Feature\n", encoding="utf-8")
    (docs / "feature-status.yaml").write_text(
        "version: 2\nupdated: 2026-08-30\nfeatures:\n"
        "  - id: F-1\n"
        "    name: Feature one\n"
        "    tags: [core]\n"
        "    maturity: Beta\n"
        f"    delivery: {delivery}\n"
        "    doc: feature.md\n"
        "    criteria: {1: true, 2: null, 3: true, 4: false, 5: false, 6: true, 7: true, 8: null, 9: null}\n",
        encoding="utf-8",
    )
    link = "[feature](feature.md)" if index_link else "No feature link"
    (docs / "index.md").write_text(f"# Index\n\n{link}\n", encoding="utf-8")
    (docs / "status.md").write_text(
        "# Status\n\n## Beta\n\n"
        "| Feature | ID | Tag | Delivery | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | Doc |\n"
        "| --- | --- | --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | --- |\n"
        f"| Feature one | F-1 | core | `{delivery}` | ✅ | n/a | ✅ | ☐ | ☐ | ✅ | ✅ | n/a | n/a | [feature.md](feature.md) |\n",
        encoding="utf-8",
    )
    claim = "All shipped features are GA.\n" if readme_claim else "Maturity is in the manifest.\n"
    (root / "README.md").write_text(f"# Repo\n\n{claim}", encoding="utf-8")


def test_feature_manifest_rejects_readme_all_ga_claim():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_feature_truth_tree(root, readme_claim=True)
        _, fail = _run_in_tmp(root, docs_check.check_feature_status_manifest)
        assert fail == 1, f"expected one failure, got {fail}"


def test_feature_manifest_requires_index_discoverability():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_feature_truth_tree(root, index_link=False)
        _, fail = _run_in_tmp(root, docs_check.check_feature_status_manifest)
        assert fail == 1, f"expected one failure, got {fail}"


def test_feature_manifest_compares_delivery_state():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        _write_feature_truth_tree(root, delivery="merged")
        status = root / "docs" / "status.md"
        status.write_text(status.read_text(encoding="utf-8").replace("`merged`", "`candidate`"), encoding="utf-8")
        _, fail = _run_in_tmp(root, docs_check.check_feature_status_manifest)
        assert fail == 1, f"expected one failure, got {fail}"


def test_readme_go_version_accepts_major_minor_and_rejects_stale_patch():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        (root / "go.mod").write_text("module example\n\ngo 1.26.6\n", encoding="utf-8")
        (root / "README.md").write_text("- **Language:** Go 1.26\n", encoding="utf-8")
        _, fail = _run_in_tmp(root, docs_check.check_readme_go_version)
        assert fail == 0, f"expected coarse major.minor to pass, got {fail}"
        (root / "README.md").write_text("- **Language:** Go 1.26.5\n", encoding="utf-8")
        _, fail = _run_in_tmp(root, docs_check.check_readme_go_version)
        assert fail == 1, f"expected stale patch to fail, got {fail}"


def test_living_doc_header_detects_newer_changelog():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        docs = root / "docs"
        docs.mkdir(parents=True)
        (docs / "compatibility.md").write_text(
            "# Compatibility\n\n> Version 1.1 · Updated 2026-08-04\n\n"
            "| Date | Ver | Change |\n| --- | --- | --- |\n"
            "| 2026-08-19 | 1.6 | Newer |\n",
            encoding="utf-8",
        )
        _, fail = _run_in_tmp(root, docs_check.check_living_doc_headers)
        assert fail == 1, f"expected stale header failure, got {fail}"

def test_status_heading_uniqueness_rejects_duplicate_anchor():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        docs = root / "docs"
        docs.mkdir(parents=True)
        (docs / "status.md").write_text(
            "# Status\n\n## GA\n\n### Repeated\n\nText.\n\n### Repeated\n\n"
            "## GA — soak pending\n\n## Beta\n\n## Alpha\n\n## Deprecated\n\n"
            "## Soak tracking (post-GA gate)\n\n## Changelog\n",
            encoding="utf-8",
        )
        _, fail = _run_in_tmp(root, docs_check.check_status_heading_uniqueness)
        assert fail == 1, f"expected one duplicate-heading failure, got {fail}"


def test_status_heading_contract_rejects_legacy_beta_section():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        docs = root / "docs"
        docs.mkdir(parents=True)
        (docs / "status.md").write_text(
            "# Status\n\n## GA\n\n## GA — soak pending\n\n## Beta\n\n"
            "## Alpha\n\n## Deprecated\n\n## Soak tracking (post-GA gate)\n\n"
            "## Beta (shipped; remaining GA gaps)\n\n## Changelog\n",
            encoding="utf-8",
        )
        _, fail = _run_in_tmp(root, docs_check.check_status_heading_uniqueness)
        assert fail == 1, f"expected one legacy-section failure, got {fail}"

if __name__ == "__main__":
    _run_existing_tests()
    test_feature_manifest_rejects_readme_all_ga_claim()
    test_feature_manifest_requires_index_discoverability()
    test_feature_manifest_compares_delivery_state()
    test_readme_go_version_accepts_major_minor_and_rejects_stale_patch()
    test_living_doc_header_detects_newer_changelog()
    test_status_heading_uniqueness_rejects_duplicate_anchor()
    test_status_heading_contract_rejects_legacy_beta_section()
    print("OK")
