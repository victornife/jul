#!/usr/bin/env python3
from pathlib import Path


def replace_once(text: str, old: str, new: str, label: str) -> str:
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{label}: expected one match, found {count}")
    return text.replace(old, new, 1)


status = Path("docs/status.md")
text = status.read_text(encoding="utf-8")
pointer_start = "> For the current evidence-based assessment, release blockers and\n"
pointer_end = "\n\n**Keep this current.**"
start_at = text.find(pointer_start)
end_at = text.find(pointer_end, start_at + len(pointer_start))
if start_at < 0 or end_at < 0:
    raise SystemExit("status audit-pointer markers not found")
new_pointer = (
    "> Current maturity and delivery live in this page and\n"
    "> [`feature-status.yaml`](feature-status.yaml). Volatile issue sequencing lives\n"
    "> in [#62](https://github.com/victornife/jul/issues/62), and dated audit\n"
    "> disposition lives in the [audit register](audit-register.md). The\n"
    "> [2026-08-03 combined audit](audit/combined-audit-2026-08-03.md) remains a\n"
    "> preserved programme-opening baseline, not a second current-status source."
)
text = text[:start_at] + new_pointer + text[end_at:]
legacy_start = "## Recently shipped continuous panels\n"
changelog = "## Changelog\n"
start_at = text.find(legacy_start)
end_at = text.find(changelog, start_at + len(legacy_start))
if start_at < 0 or end_at < 0:
    raise SystemExit("legacy status block markers not found")
text = text[:start_at] + text[end_at:]
status.write_text(text.rstrip() + "\n", encoding="utf-8", newline="\n")

checker = Path("scripts/docs-check.py")
text = checker.read_text(encoding="utf-8")
old = '    if len(headings) == len(set(headings)):\n        ok("status.md headings are unique")'
new = (
    "    required = {\n"
    '        "GA", "GA — soak pending", "Beta", "Alpha", "Deprecated",\n'
    '        "Soak tracking (post-GA gate)", "Changelog",\n'
    "    }\n"
    "    declared = {title for level, title in headings if level == 2}\n"
    "    for title in sorted(required - declared):\n"
    '        error(path, 0, f"missing canonical status section: {title}")\n'
    "    forbidden = {\n"
    '        "Beta (shipped; remaining GA gaps)",\n'
    '        "Recently shipped continuous panels",\n'
    "    }\n"
    "    for title in sorted(forbidden & declared):\n"
    '        error(path, 0, f"legacy status section remains: {title}")\n'
    "    if required <= declared and not (forbidden & declared):\n"
    '        ok("status.md canonical sections are complete")'
)
text = replace_once(text, old, new + "\n\n", "status checker block")
checker.write_text(text, encoding="utf-8", newline="\n")

tests = Path("scripts/test_docs_check.py")
text = tests.read_text(encoding="utf-8")
old_fixture = (
    '            "# Status\\n\\n## Repeated\\n\\nText.\\n\\n## Repeated\\n",\n'
)
new_fixture = (
    '            "# Status\\n\\n## GA\\n\\n### Repeated\\n\\nText.\\n\\n### Repeated\\n\\n"\n'
    '            "## GA — soak pending\\n\\n## Beta\\n\\n## Alpha\\n\\n## Deprecated\\n\\n"\n'
    '            "## Soak tracking (post-GA gate)\\n\\n## Changelog\\n",\n'
)
text = replace_once(text, old_fixture, new_fixture, "duplicate-heading fixture")

test_name = "test_status_heading_contract_rejects_legacy_beta_section"
addition = "\n".join([
    "",
    "",
    f"def {test_name}():",
    "    with tempfile.TemporaryDirectory() as tmpdir:",
    "        root = Path(tmpdir)",
    '        docs = root / "docs"',
    "        docs.mkdir(parents=True)",
    '        (docs / "status.md").write_text(',
    '            "# Status\\n\\n## GA\\n\\n## GA — soak pending\\n\\n## Beta\\n\\n"',
    '            "## Alpha\\n\\n## Deprecated\\n\\n## Soak tracking (post-GA gate)\\n\\n"',
    '            "## Beta (shipped; remaining GA gaps)\\n\\n## Changelog\\n",',
    '            encoding="utf-8",',
    "        )",
    "        _, fail = _run_in_tmp(root, docs_check.check_status_heading_uniqueness)",
    '        assert fail == 1, f"expected one legacy-section failure, got {fail}"',
    "",
])
if f"def {test_name}():" not in text:
    main_marker = '\nif __name__ == "__main__":\n'
    main_at = text.rfind(main_marker)
    if main_at < 0:
        raise SystemExit("final test runner marker not found")
    text = text[:main_at] + addition + text[main_at:]
    runner = '    test_status_heading_uniqueness_rejects_duplicate_anchor()\n    print("OK")'
    replacement = (
        '    test_status_heading_uniqueness_rejects_duplicate_anchor()\n'
        f"    {test_name}()\n"
        '    print("OK")'
    )
    text = replace_once(text, runner, replacement, "test runner tail")
tests.write_text(text, encoding="utf-8", newline="\n")

importer = Path("docs/nginx-importer.md")
text = importer.read_text(encoding="utf-8")
marker = "\n## GA status\n"
if text.count(marker) != 1:
    raise SystemExit(f"importer GA section: expected one match, found {text.count(marker)}")
prefix = text.split(marker, 1)[0].rstrip()
replacement = "\n".join([
    "",
    "",
    "## Maturity and delivery",
    "",
    "This guide documents the current `main` surface, which is broader than",
    "the released base importer. The two contracts are deliberately separate:",
    "",
    "### Base importer — Y1-09 (`GA` / `soaked`)",
    "",
    "The released GA record covers the single-file conversion contract and the",
    "evidence that existed in the released line. The current support matrix above",
    "also contains later additive mappings; those do **not** retroactively widen",
    "the released GA contract.",
    "",
    "| Criterion | Released base evidence |",
    "| --- | --- |",
    "| Translation behavior | Deterministic single-file parse/translate path and its released golden output. |",
    "| Performance | Published parse and translate benchmark baselines. |",
    "| Limitations | Explicit unsupported-directive and semantic-difference list; no full NGINX emulation claim. |",
    "| Compatibility | The documented conversion CLI and generated Jul configuration behavior are governed by [compatibility.md](compatibility.md). |",
    "| Soak / validation | [Released importer validation evidence](soak-evidence.md#2026-07-06--phase-2b-soak-preparation-local-windows-5-min-smoke--validation-scripts). |",
    "| Runnable example | `examples/migrate/nginx.conf` through ordinary conversion mode. |",
    "| Security | Parser failure containment, secret-safe diagnostics, and use on a trusted migration host. |",
    "| Fuzzing | `FuzzTranslate` covers parse, translate, and marshal round trip. |",
    "| Operable surface | `jul import nginx -o <file> <nginx.conf>`. |",
    "",
    "### Assessment, provenance, and includes — MIG-ASSESS (`Beta` / `merged`)",
    "",
    "Schema-v2 human/JSON assessment, stable findings and guidance, source spans,",
    "target mappings, and bounded root-confined include traversal are merged on",
    "current `main`. They are not contained in the older released GA record and",
    "have not completed a separate stable-release and long-running-soak promotion.",
    "Their machine contract and operating boundary are documented in",
    "[nginx-assessment.md](nginx-assessment.md) and tracked explicitly in",
    "[status.md](status.md).",
    "",
])
importer.write_text(prefix + replacement, encoding="utf-8", newline="\n")
