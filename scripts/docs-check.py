#!/usr/bin/env python3
"""
Docs validation script.

Checks performed:
1. All relative Markdown links resolve to existing files.
2. All fenced TOML blocks are syntactically valid.
3. No placeholder URLs (example/jul) remain in docs.
4. Version and date are consistent across roadmap and status docs.
5. No future "Updated" dates in living doc headers.
6. Top-level config keys from schema.go appear in configuration.md (warning only).
"""
import json
import os
import re
import sys
from datetime import datetime, timezone
from pathlib import Path

try:
    import tomllib  # Python 3.11+
except ModuleNotFoundError:
    import tomli as tomllib

ROOT = Path(__file__).resolve().parent.parent
DOCS = ROOT / "docs"
OK = 0
FAIL = 0


def error(path: Path, line: int, msg: str):
    global FAIL
    FAIL += 1
    print(f"FAIL {path}:{line} — {msg}")


def warn(path: Path, line: int, msg: str):
    # warnings do not fail the build but are printed
    print(f"WARN {path}:{line} — {msg}")


def ok(msg: str):
    global OK
    OK += 1
    # print(f"OK   {msg}")


def _slugify(text: str) -> list[str]:
    """Convert text to lowercase word list. First strip all non-alnum to space,
    then split letter-digit boundaries so 'http3' -> ['http','3']."""
    text = text.lower()
    # Replace all non-alphanumeric with space first
    text = re.sub(r"[^a-z0-9]", " ", text)
    # Then split letter-digit boundaries
    text = re.sub(r"([a-z])(\d)", r"\1 \2", text)
    text = re.sub(r"(\d)([a-z])", r"\1 \2", text)
    return [w for w in text.split() if w]


def check_markdown_links(path: Path, text: str):
    """Check that relative markdown links point to existing files AND anchors resolve."""
    for match in re.finditer(r"!?\[([^\]]+)\]\(([^)]+)\)", text):
        raw_url = match.group(2)
        line = text[:match.start()].count("\n") + 1

        # Skip external URLs and anchors-only
        if re.match(r"^(https?|mailto|ftp):", raw_url):
            continue
        if raw_url.startswith("#"):
            continue

        # Strip anchor
        parts = raw_url.split("#", 1)
        url = parts[0]
        anchor = parts[1] if len(parts) > 1 else None
        if not url:
            continue

        # Resolve relative to the markdown file
        target = (path.parent / url).resolve()
        if not target.exists():
            error(path, line, f"broken link: {raw_url}")
            continue

        # If there's an anchor, check the target file for a matching heading
        if anchor and target.exists() and target.suffix == ".md":
            target_text = target.read_text(encoding="utf-8")
            # Check for explicit anchor marker {:#anchor} or {#anchor}
            explicit_anchor = re.compile(rf"\{{#?{re.escape(anchor)}\}}", re.IGNORECASE)
            if explicit_anchor.search(target_text):
                ok(f"{path}:{line} anchor {raw_url}")
                continue
            # Check for heading text matching anchor slug.
            # Anchor slug is typically heading text lowercased with spaces -> dashes,
            # and special chars stripped. We match case-insensitively to be forgiving.
            # Match: ## Some Title {#optional-anchor}   or   ## Some Title
            # We need to match the heading text against the anchor.
            anchor_words = anchor.replace("-", " ").lower().split()
            found = False
            for heading_match in re.finditer(r"^#+\s+(.+?)(?:\s*\{#[^}]+\})?\s*$", target_text, re.MULTILINE):
                heading_words = _slugify(heading_match.group(1))
                # Check if all anchor words appear in order in heading words (subsequence)
                idx = 0
                for hw in heading_words:
                    if idx < len(anchor_words) and hw == anchor_words[idx]:
                        idx += 1
                if idx == len(anchor_words):
                    found = True
                    break
                # Fallback: merged anchor without hyphens, e.g. "serverslocationsauth"
                # should match heading "servers locations auth".
                if "".join(anchor_words) in "".join(heading_words):
                    found = True
                    break
            if not found:
                error(path, line, f"broken anchor: {raw_url}")
                continue

        ok(f"{path}:{line} link {raw_url}")


def check_toml_blocks(path: Path, text: str):
    """Parse every ```toml fenced block and report syntax errors."""
    # Match ```toml ... ``` blocks
    for match in re.finditer(r"```toml\n(.*?)```", text, re.DOTALL):
        block = match.group(1)
        line = text[:match.start()].count("\n") + 1
        try:
            tomllib.loads(block)
            ok(f"{path}:{line} TOML block")
        except Exception as exc:
            error(path, line, f"TOML syntax error: {exc}")


def check_placeholders(path: Path, text: str):
    """Detect placeholder URLs/names that should have been replaced."""
    bad = [
        r"example/jul",
        r"example\.com/jul",
        r"<RUN_ID>",
        r"actions/runs/<RUN_ID>",
        r"actions/runs/PENDING",
        r"\bPENDING\b",
        r"\?\?\?",
    ]
    for pattern in bad:
        for match in re.finditer(pattern, text):
            line = text[:match.start()].count("\n") + 1
            error(path, line, f"placeholder found: {pattern}")


def check_version_consistency(md_files: list[Path]):
    """Ensure roadmap and status docs carry the same version and date."""
    versions: dict[str, tuple[str, str, int]] = {}  # file stem -> (version, date, line)

    version_re = re.compile(r"> Version (\d+\.\d+) · Updated (\d{4}-\d{2}-\d{2})")
    for md in md_files:
        text = md.read_text(encoding="utf-8")
        for match in version_re.finditer(text):
            line = text[:match.start()].count("\n") + 1
            key = md.relative_to(DOCS).as_posix()
            versions[key] = (match.group(1), match.group(2), line)

    roadmap = versions.get("roadmap/README.md")
    status = versions.get("status.md")
    if roadmap and status:
        if roadmap[0] != status[0]:
            error(
                DOCS / "roadmap" / "README.md",
                roadmap[2],
                f"version mismatch: roadmap={roadmap[0]} vs status={status[0]}",
            )
        if roadmap[1] != status[1]:
            error(
                DOCS / "roadmap" / "README.md",
                roadmap[2],
                f"date mismatch: roadmap={roadmap[1]} vs status={status[1]}",
            )
    # Check that status.md changelog top version doesn't exceed header version
    status_md = DOCS / "status.md"
    if status_md.exists():
        text = status_md.read_text(encoding="utf-8")
        header_match = version_re.search(text)
        # Find first changelog row with version
        changelog_re = re.compile(r"\|\s*(\d{4}-\d{2}-\d{2})\s*\|\s*(\d+\.\d+)")
        first_changelog = None
        for m in changelog_re.finditer(text):
            first_changelog = m.group(2)
            break
        if header_match and first_changelog:
            header_ver = header_match.group(1)
            if _version_key(first_changelog) > _version_key(header_ver):
                error(status_md, text[:changelog_re.search(text).start()].count("\n") + 1,
                      f"changelog version {first_changelog} exceeds header version {header_ver}")

def _version_key(v: str) -> tuple[int, int]:
    parts = v.split(".")
    return (int(parts[0]), int(parts[1]))


def check_future_dates(path: Path, text: str):
    """Warn about 'Updated YYYY-MM-DD' dates in the future."""
    today = datetime.now(timezone.utc).date()
    for match in re.finditer(r"Updated (\d{4}-\d{2}-\d{2})", text):
        line = text[:match.start()].count("\n") + 1
        d = datetime.strptime(match.group(1), "%Y-%m-%d").date()
        if d > today:
            warn(path, line, f"future date: {match.group(1)}")


def check_schema_doc_drift():
    """Warn when a top-level config key from schema.go is missing from configuration.md."""
    schema_path = ROOT / "internal" / "config" / "schema.go"
    if not schema_path.exists():
        warn(schema_path, 0, "schema.go not found, skipping schema-doc drift check")
        return

    schema_text = schema_path.read_text(encoding="utf-8")
    # Extract toml tags from fields of the root Config struct only.
    # We start brace_depth at 1 to account for the opening brace of
    # `type Config struct {`, and stop when it returns to 0.
    keys: set[str] = set()
    in_config_struct = False
    brace_depth = 0
    for line in schema_text.splitlines():
        stripped = line.strip()
        if stripped.startswith("type Config struct"):
            in_config_struct = True
            brace_depth = 1  # account for the opening brace on this line
            continue
        if in_config_struct:
            brace_depth += stripped.count("{")
            brace_depth -= stripped.count("}")
            if brace_depth <= 0:
                break
            m = re.search(r'`toml:"([^"]+)"`', stripped)
            if m:
                # Strip struct-tag options like ",omitempty" so the key is just
                # the field name (e.g. "upstreams,omitempty" -> "upstreams").
                key = m.group(1).split(",", 1)[0].strip()
                if key:
                    keys.add(key)

    if not keys:
        warn(schema_path, 0, "no toml keys found in Config struct")
        return

    config_doc = DOCS / "configuration.md"
    if not config_doc.exists():
        warn(config_doc, 0, "configuration.md not found, skipping schema-doc drift check")
        return
    doc_text = config_doc.read_text(encoding="utf-8")
    doc_lower = _slugify(doc_text)
    doc_joined = "".join(doc_lower)

    for key in sorted(keys):
        key_slug = _slugify(key)
        key_joined = "".join(key_slug)
        # A well-documented key appears as [key], [[key]], or in backticks: `key`,
        # or as heading text, or as table header matching the slug.
        if key_joined not in doc_joined:
            needle = re.compile(rf"(\\[\\[{re.escape(key)}\\]\\]|\\[{re.escape(key)}\\]|`{re.escape(key)}`)")
            if not needle.search(doc_text):
                error(config_doc, 0, f"schema key '{key}' not found in configuration.md")


def check_balanced_fences(path: Path, text: str):
    """Ensure every ``` opening fence has a matching closing fence."""
    count = text.count("```")
    if count % 2 != 0:
        # Rough line estimate: find the last fence and report near it
        last = text.rfind("```")
        line = text[:last].count("\n") + 1
        error(path, line, f"unbalanced markdown fences: {count} fence markers (odd number)")


def check_denylist(path: Path, text: str):
    """Detect raw prompt/template strings that should not be committed."""
    patterns = [
        r"<userPrompt>",
        r"</userPrompt>",
        r"Provide the fully rewritten file",
    ]
    for pattern in patterns:
        for match in re.finditer(pattern, text):
            line = text[:match.start()].count("\n") + 1
            error(path, line, f"denylist match: {pattern}")


def check_feature_status_manifest():
    """Validate docs/feature-status.yaml: parseable YAML, each doc file exists."""
    try:
        import yaml
    except ModuleNotFoundError:
        error(ROOT / "docs" / "feature-status.yaml", 0,
              "pyyaml is required for YAML manifest checks — install with: pip install pyyaml")
        return

    manifest = ROOT / "docs" / "feature-status.yaml"
    if not manifest.exists():
        error(manifest, 0, "feature-status.yaml is missing from docs/")
        return

    try:
        data = yaml.safe_load(manifest.read_text(encoding="utf-8"))
    except yaml.YAMLError as exc:
        error(manifest, 0, f"feature-status.yaml is not valid YAML: {exc}")
        return
    ok("feature-status.yaml parses as valid YAML")

    if not isinstance(data, dict) or "features" not in data:
        error(manifest, 0, "feature-status.yaml missing required 'features' key")
        return

    for entry in data.get("features", []):
        name = entry.get("name", "?")
        doc = entry.get("doc", "")
        if doc:
            doc_path = DOCS / doc
            if not doc_path.exists():
                error(manifest, 0, f"feature '{name}' references missing doc: {doc}")
            else:
                ok(f"feature-status.yaml: doc {doc} exists")

    # Cross-check: every feature in the manifest must appear in docs/status.md
    # with matching row-level data (maturity, criteria, doc) (R6-11).
    status_doc = DOCS / "status.md"
    if not status_doc.exists():
        error(manifest, 0, "docs/status.md is missing for cross-check")
        return
    status_text = status_doc.read_text(encoding="utf-8")
    status_rows = _parse_status_md_rows(status_text)

    for entry in data.get("features", []):
        name = entry.get("name", "?")
        feat_id = entry.get("id", "")
        search_term = re.split(r"[\(\+]", name)[0].strip()
        if search_term and search_term not in status_text:
            error(manifest, 0,
                  f"feature '{name}' from feature-status.yaml not found in docs/status.md — "
                  f"keep both in sync")
            continue
        if feat_id:
            if feat_id not in status_text:
                error(manifest, 0,
                      f"feature ID '{feat_id}' ({name}) not found in docs/status.md — "
                      f"table row may be missing or ID mismatch")
                continue
            row = status_rows.get(feat_id)
            if row:
                _compare_feature_row(manifest, entry, row)
            else:
                # ID present somewhere but not in a parseable GA/Beta row;
                # still acceptable, but note it for visibility.
                ok(f"feature-status.yaml: ID '{feat_id}' present in status.md")


def _cell_to_criterion(cell: str):
    """Convert a status.md criterion cell to its canonical value."""
    text = cell.strip().lower()
    if "✅" in text or "true" in text:
        return True
    if "n/a" in text or "not applicable" in text or "-" == text:
        return None
    if "☐" in text or "open" in text or "false" in text:
        return False
    return None


def _parse_status_md_rows(text: str) -> dict[str, dict]:
    """Parse GA and Beta tables in status.md, returning id -> row data."""
    rows: dict[str, dict] = {}
    in_table = False
    header_count = 0
    for line in text.splitlines():
        if not line.startswith("|"):
            in_table = False
            header_count = 0
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        if all(re.match(r"^[:\-]+$", c) for c in cells if c):
            in_table = True
            header_count += 1
            continue
        if not in_table or header_count < 1:
            continue
        # Expect Feature, ID, Tag, 1..9, Doc
        if len(cells) < 13:
            continue
        feat_id = cells[1]
        if not feat_id or feat_id in ("—", "-", "ID"):
            continue
        rows[feat_id] = {
            "name": cells[0],
            "tag": cells[2],
            "criteria": {
                i: _cell_to_criterion(cells[2 + i])
                for i in range(1, 10)
            },
            "doc": cells[12] if len(cells) > 12 else "",
        }
    return rows


def _compare_feature_row(manifest: Path, entry: dict, row: dict):
    """Compare a feature-status.yaml entry against its status.md table row."""
    feat_id = entry.get("id", "?")
    name = entry.get("name", "?")
    yaml_criteria = entry.get("criteria", {})
    row_criteria = row.get("criteria", {})
    for i in range(1, 10):
        yaml_val = yaml_criteria.get(i)
        row_val = row_criteria.get(i)
        if yaml_val != row_val:
            error(
                manifest,
                0,
                f"feature {feat_id} ({name}) criterion {i} mismatch: "
                f"YAML={yaml_val}, status.md={row_val}",
            )
    yaml_doc = entry.get("doc", "")
    row_doc = row.get("doc", "")
    if yaml_doc and row_doc:
        # status.md doc cell is a markdown link like [doc](doc.md); extract target.
        link_match = re.search(r"\]\(([^)]+)\)", row_doc)
        if link_match:
            row_doc_target = link_match.group(1)
            if row_doc_target != yaml_doc:
                error(
                    manifest,
                    0,
                    f"feature {feat_id} ({name}) doc mismatch: "
                    f"YAML={yaml_doc}, status.md={row_doc_target}",
                )
    ok(f"feature-status.yaml: {feat_id} row data matches status.md")


def _run_lifecycle_generator_check():
    """Run the Go generator in check mode and report staleness.

    The Go lifecycle registry is the machine authority; this script does not
    reimplement lifecycle semantics. It only asks the generator whether the
    committed mirrors match what the registry renders, and surfaces the
    generator's own remediation message.
    """
    import subprocess

    gen = "internal/lifecycle/lifecyclegen"
    try:
        result = subprocess.run(
            ["go", "run", "./" + gen, "-out", "docs", "-check"],
            cwd=ROOT,
            capture_output=True,
            text=True,
            timeout=180,
        )
    except FileNotFoundError:
        error(ROOT / gen, 0, "'go' not found in PATH; cannot verify generated lifecycle artifacts")
        return False
    except subprocess.TimeoutExpired:
        error(ROOT / gen, 0, "timeout running the lifecycle generator in check mode")
        return False
    if result.returncode != 0:
        error(ROOT / "docs" / "config-lifecycle.yaml", 0, result.stderr.strip() or result.stdout.strip())
        return False
    ok("generated lifecycle artifacts match the Go registry")
    return True


def _load_lifecycle_metadata():
    """Load the generated machine-readable lifecycle metadata."""
    path = ROOT / "docs" / "generated" / "config-lifecycle.json"
    if not path.exists():
        error(path, 0, "generated lifecycle metadata is missing — run: make lifecycle-generate")
        return None
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        error(path, 0, f"generated lifecycle metadata is not valid JSON: {exc}")
        return None


def check_lifecycle_manifest():
    """Validate the generated lifecycle artifacts structurally.

    Semantic authority lives in internal/lifecycle. This function verifies the
    properties a reviewer relies on when reading the committed artifacts:

    1. The generator's check mode passes, so the mirrors are not stale.
    2. The YAML mirror parses and declares the same version as the metadata.
    3. The world is closed: every schema leaf is either classified or carries an
       explicit exemption.
    4. Every restart-required field is startup-consumed, so the reload gate
       actually compares it.
    5. Every field carries a bounded subsystem that the artifact documents.
    6. Every restart-required subsystem is described in reload-semantics.md.
    """
    try:
        import yaml
    except ModuleNotFoundError:
        error(ROOT / "docs" / "config-lifecycle.yaml", 0,
              "pyyaml is required for YAML manifest checks — install with: pip install pyyaml")
        return

    if not _run_lifecycle_generator_check():
        return

    meta = _load_lifecycle_metadata()
    if meta is None:
        return

    manifest = ROOT / "docs" / "config-lifecycle.yaml"
    try:
        data = yaml.safe_load(manifest.read_text(encoding="utf-8"))
    except yaml.YAMLError as exc:
        error(manifest, 0, f"config-lifecycle.yaml is not valid YAML: {exc}")
        return
    ok("config-lifecycle.yaml parses as valid YAML")

    if not isinstance(data, dict):
        error(manifest, 0, "config-lifecycle.yaml must be a mapping")
        return
    if data.get("version") != meta.get("version"):
        error(manifest, 0,
              f"YAML version {data.get('version')} does not match the metadata version {meta.get('version')}")

    counts = meta.get("counts", {})
    leaves = counts.get("schema_leaves", 0)
    entries = counts.get("registry_entries", 0)
    exemptions = len(meta.get("schema_exemptions", []))
    if leaves != entries + exemptions:
        error(
            ROOT / "internal" / "lifecycle" / "registry.go", 0,
            f"closed-world violation: {leaves} schema leaves, {entries} registry entries, "
            f"{exemptions} exemptions — every public leaf needs exactly one disposition",
        )
    else:
        ok(f"closed world: {entries} registry entries cover {leaves} schema leaves ({exemptions} exemptions)")

    documented_subsystems = {s["name"] for s in meta.get("subsystems", [])}
    yaml_fields = {f.get("path") for f in data.get("fields", [])}
    restart_subsystems = set()

    for field in meta.get("fields", []):
        path = field.get("path", "?")
        if field.get("class") == "restart_required":
            restart_subsystems.add(field.get("subsystem", ""))
            if not field.get("startup_consumed"):
                error(
                    ROOT / "internal" / "lifecycle" / "registry.go", 0,
                    f"restart_required path '{path}' is not startup-consumed; the reload gate would ignore it",
                )
        if field.get("subsystem") not in documented_subsystems:
            error(
                ROOT / "internal" / "lifecycle" / "registry.go", 0,
                f"path '{path}' uses undocumented subsystem '{field.get('subsystem')}'",
            )
        if not str(field.get("reason", "")).strip():
            error(ROOT / "internal" / "lifecycle" / "registry.go", 0, f"path '{path}' has no reason")
        if path not in yaml_fields:
            error(manifest, 0, f"path '{path}' is in the metadata but missing from config-lifecycle.yaml")

    generated_md = ROOT / "docs" / "generated" / "config-lifecycle.md"
    if not generated_md.exists():
        error(generated_md, 0, "generated lifecycle reference is missing — run: make lifecycle-generate")
    else:
        md_text = generated_md.read_text(encoding="utf-8")
        for field in meta.get("fields", []):
            if f"| `{field['path']}` |" not in md_text:
                error(generated_md, 0, f"path '{field['path']}' is missing from the generated reference")

    reload_doc = ROOT / "docs" / "reload-semantics.md"
    if not reload_doc.exists():
        error(reload_doc, 0, "docs/reload-semantics.md is missing")
        return
    reload_text = reload_doc.read_text(encoding="utf-8").lower()
    for subsystem in sorted(restart_subsystems):
        if not subsystem:
            continue
        keyword = subsystem.split("_")[0]
        if keyword not in reload_text:
            error(
                reload_doc, 0,
                f"restart-required subsystem '{subsystem}' is not mentioned in reload-semantics.md — "
                f"keep the conceptual doc in sync with the registry",
            )
        else:
            ok(f"reload-semantics.md covers restart-required subsystem '{subsystem}'")



# Standard banner required on Year 3–5 horizon specs so they cannot be read as
# committed delivery schedules.
HORIZON_BANNER_TEXT = "Concept horizon — not committed"


def check_horizon_specs():
    """Year 3–5 specs must contain the standard horizon banner."""
    for year in (3, 4, 5):
        path = DOCS / "specs" / f"year-{year}.md"
        if not path.exists():
            error(path, 0, f"year-{year}.md is missing")
            continue
        text = path.read_text(encoding="utf-8")
        if HORIZON_BANNER_TEXT not in text:
            error(path, 0, f"missing horizon banner: {HORIZON_BANNER_TEXT!r}")
        else:
            ok(f"year-{year}.md has horizon banner")


def check_active_roadmap_links():
    """Links inside the Active operating roadmap section must resolve."""
    roadmap = DOCS / "roadmap" / "README.md"
    if not roadmap.exists():
        error(roadmap, 0, "roadmap README is missing")
        return

    text = roadmap.read_text(encoding="utf-8")
    lines = text.splitlines()
    in_active = False

    for line_no, line in enumerate(lines, 1):
        if line.startswith("## Active operating roadmap"):
            in_active = True
            continue
        if in_active and line.startswith("## "):
            break
        if not in_active:
            continue

        for match in re.finditer(r"!?\[([^\]]+)\]\(([^)]+)\)", line):
            raw_url = match.group(2)
            # Skip external URLs, anchors-only, and mailto.
            if re.match(r"^(https?|mailto|ftp):", raw_url):
                continue
            if raw_url.startswith("#"):
                continue

            parts = raw_url.split("#", 1)
            url = parts[0]
            anchor = parts[1] if len(parts) > 1 else None
            if not url:
                continue

            target = (roadmap.parent / url).resolve()
            if not target.exists():
                error(roadmap, line_no, f"active roadmap broken link: {raw_url}")
                continue

            if anchor and target.suffix == ".md":
                target_text = target.read_text(encoding="utf-8")
                explicit = re.compile(rf"\{{#?{re.escape(anchor)}\}}", re.IGNORECASE)
                if explicit.search(target_text):
                    ok(f"active roadmap link {raw_url}")
                    continue

                anchor_words = anchor.replace("-", " ").lower().split()
                found = False
                for hm in re.finditer(
                    r"^#+\s+(.+?)(?:\s*\{#[^}]+\})?\s*$", target_text, re.MULTILINE
                ):
                    heading_words = _slugify(hm.group(1))
                    idx = 0
                    for hw in heading_words:
                        if idx < len(anchor_words) and hw == anchor_words[idx]:
                            idx += 1
                    if idx == len(anchor_words):
                        found = True
                        break
                    if "".join(anchor_words) in "".join(heading_words):
                        found = True
                        break
                if not found:
                    error(roadmap, line_no, f"active roadmap broken anchor: {raw_url}")
                    continue

            ok(f"active roadmap link {raw_url}")


def check_roadmap_active_ids():
    """Active roadmap IDs must be unique and must not overlap delivered items."""
    roadmap = DOCS / "roadmap" / "README.md"
    if not roadmap.exists():
        error(roadmap, 0, "roadmap README is missing")
        return

    text = roadmap.read_text(encoding="utf-8")
    lines = text.splitlines()

    # Feature IDs used in roadmap tables. Phase rows in the active operating
    # roadmap use "Phase N" and are checked for uniqueness separately.
    feat_id_re = re.compile(r"^(Y\d+-\d+|HP-\d+[A-Z]?|AI-MVP|SEC-\d+|R\d+-\d+(?:\.\d+)?)$")
    phase_id_re = re.compile(r"^Phase\s+(\d+)$")

    active_ids: dict[str, int] = {}      # id -> first line where seen as active
    delivered_ids: set[str] = set()
    in_active_section = False
    in_hardening_section = False
    in_delivered_section = False

    for line_no, line in enumerate(lines, 1):
        # Section detection: top-level headings only.
        if line.startswith("## Active operating roadmap"):
            in_active_section = True
            in_hardening_section = False
            in_delivered_section = False
            continue
        if line.startswith("## Hardening & platform"):
            in_active_section = False
            in_hardening_section = True
            in_delivered_section = False
            continue
        if re.match(r"^## Delivered", line):
            in_active_section = False
            in_hardening_section = False
            in_delivered_section = True
            continue
        if line.startswith("## ") and not line.startswith("### "):
            in_active_section = False
            in_hardening_section = False
            in_delivered_section = False
            continue

        if not line.startswith("|"):
            continue

        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) < 2:
            continue

        first_cell = cells[0]
        row_text = " ".join(cells)

        # Phase rows in the active operating roadmap.
        phase_match = phase_id_re.match(first_cell)
        if phase_match and in_active_section:
            phase_id = first_cell
            if phase_id in active_ids:
                error(roadmap, line_no, f"duplicate active roadmap ID: {phase_id}")
            else:
                active_ids[phase_id] = line_no
            continue

        # Feature IDs.
        id_match = feat_id_re.match(first_cell)
        if not id_match:
            continue
        feat_id = id_match.group(1)

        is_delivered = "✅" in row_text or "Delivered" in row_text
        is_active = (
            "🚧" in row_text
            or "⏳" in row_text
            or "🔒" in row_text
            or "active" in row_text.lower()
            or "in progress" in row_text.lower()
            or "Partially delivered" in row_text
            or "Design complete" in row_text
        )

        if in_delivered_section and is_delivered:
            delivered_ids.add(feat_id)
        elif in_hardening_section or in_active_section:
            if is_delivered:
                delivered_ids.add(feat_id)
            elif is_active:
                if feat_id in active_ids:
                    error(roadmap, line_no, f"duplicate active roadmap ID: {feat_id}")
                else:
                    active_ids[feat_id] = line_no

    # Delivered items must not also be listed under active work.
    for feat_id in sorted(active_ids):
        if feat_id in delivered_ids:
            error(
                roadmap,
                active_ids[feat_id],
                f"active roadmap ID {feat_id} also appears as delivered",
            )

    if active_ids:
        ok(f"roadmap active IDs are unique and do not overlap delivered items")


def check_finding_uniqueness():
    """Verify that no finding ID appears in the current audit doc with two
    conflicting status values (one resolved, one open) in the same table.

    This catches the append-and-preserve editing pattern that previously
    resulted in UI-1 being marked both Resolved and Still-open in the same
    document.
    """
    current_audit = ROOT / "docs" / "audit-register.md"
    if not current_audit.exists():
        # The audit register is the authoritative document for closed and
        # deferred findings. If it is missing, the docs-check suite itself is
        # incomplete, so fail loudly rather than silently skipping validation.
        error(
            current_audit,
            0,
            "audit register docs/audit-register.md is missing; cannot validate finding uniqueness",
        )
        return

    text = current_audit.read_text(encoding="utf-8")
    # Extract all table rows that look like finding status rows:
    # | R9-01  | ... | ✅ Implemented | 2026-07-17 |
    # IDs look like R9-01, R10-01, R9-14.1 (decimal suffix for sub-findings).
    finding_re = re.compile(r"R\d+-\d+(?:\.\d+)?")
    resolved_ids: set[str] = set()
    open_ids: set[str] = set()

    for line_no, line in enumerate(text.splitlines(), 1):
        # Only inspect pipe-delimited table rows (lines starting with |) to
        # avoid false positives from narrative text that mentions a finding ID
        # and the word "open" in an unrelated clause.
        if not line.startswith("|"):
            continue
        # Strip leading/trailing pipes so the first and last cells are data,
        # not empty strings.
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        # Skip header rows and rows without enough cells.
        if len(cells) < 2:
            continue
        first_cell = cells[0]
        id_match = finding_re.search(first_cell)
        if not id_match:
            continue
        finding_id = id_match.group(0)
        # Determine status from the last cell (status) only, so narrative
        # mentions of words like "open" in other columns do not confuse us.
        status_cell = cells[-2].lower()
        if "✅" in status_cell or "implemented" in status_cell or "resolved" in status_cell or "closed" in status_cell:
            resolved_ids.add(finding_id)
        if "⏳" in status_cell or "still open" in status_cell or "open" in status_cell or "deferred" in status_cell:
            open_ids.add(finding_id)

    conflicts = resolved_ids & open_ids
    for fid in sorted(conflicts):
        error(
            current_audit,
            0,
            f"finding {fid} appears as both Resolved and Open in "
            f"{current_audit.name} — remove or archive the stale entry",
        )
    if not conflicts:
        ok(f"no conflicting finding statuses in {current_audit.name}")


def main():
    SKIP_DIRS = {"node_modules", "vendor", ".git", "__pycache__", "reviews"}
    md_files = [
        p for p in ROOT.rglob("*.md")
        if not any(part in SKIP_DIRS for part in p.parts)
    ]
    if not md_files:
        print("No markdown files found.")
        sys.exit(1)

    for md in sorted(md_files):
        text = md.read_text(encoding="utf-8")
        check_markdown_links(md, text)
        check_toml_blocks(md, text)
        check_placeholders(md, text)
        check_future_dates(md, text)
        check_balanced_fences(md, text)
        check_denylist(md, text)

    check_version_consistency(md_files)
    check_schema_doc_drift()
    check_feature_status_manifest()
    check_lifecycle_manifest()
    check_finding_uniqueness()
    check_horizon_specs()
    check_active_roadmap_links()
    check_roadmap_active_ids()

    print()
    print(f"Results: {OK} passed, {FAIL} failed")
    sys.exit(1 if FAIL else 0)


if __name__ == "__main__":
    main()

