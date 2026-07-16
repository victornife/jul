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


def check_lifecycle_manifest():
    """Validate docs/config-lifecycle.yaml and cross-check reload-semantics.md.

    Two checks:
    1. The manifest file parses as valid YAML.
    2. Every restart-required subsystem named in the manifest appears in
       docs/reload-semantics.md, so the two sources of truth stay in sync.
    """
    try:
        import yaml  # pyyaml; optional — skip gracefully if absent
    except ModuleNotFoundError:
        # PyYAML is not installed in this environment; skip the YAML checks
        # but do not fail the build (it is an optional semantic gate).
        return

    manifest = ROOT / "docs" / "config-lifecycle.yaml"
    if not manifest.exists():
        error(manifest, 0, "config-lifecycle.yaml is missing from docs/")
        return

    try:
        data = yaml.safe_load(manifest.read_text(encoding="utf-8"))
    except yaml.YAMLError as exc:
        error(manifest, 0, f"config-lifecycle.yaml is not valid YAML: {exc}")
        return
    ok("config-lifecycle.yaml parses as valid YAML")

    if not isinstance(data, dict) or "restart_required" not in data:
        error(manifest, 0, "config-lifecycle.yaml missing required 'restart_required' key")
        return

    # Cross-check: each subsystem in the manifest must appear in reload-semantics.md.
    reload_doc = ROOT / "docs" / "reload-semantics.md"
    if not reload_doc.exists():
        error(reload_doc, 0, "docs/reload-semantics.md is missing")
        return

    reload_text = reload_doc.read_text(encoding="utf-8").lower()
    for entry in data.get("restart_required", []):
        subsystem = entry.get("subsystem", "")
        if not subsystem:
            continue
        # Use the first keyword from the subsystem name as the search term.
        keyword = subsystem.split("_")[0]
        if keyword not in reload_text:
            error(
                reload_doc,
                0,
                f"restart-required subsystem '{subsystem}' from config-lifecycle.yaml "
                f"not mentioned in reload-semantics.md — keep both in sync",
            )
        else:
            ok(f"reload-semantics.md covers restart-required subsystem '{subsystem}'")


def check_finding_uniqueness():
    """Verify that no finding ID appears in the current audit doc with two
    conflicting status values (one resolved, one open) in the same table.

    This catches the append-and-preserve editing pattern that previously
    resulted in UI-1 being marked both Resolved and Still-open in the same
    document.
    """
    current_audit = ROOT / "docs" / "reviews" / "jul_full_repository_audit_2026-07-09.md"
    if not current_audit.exists():
        return  # no current audit to check

    text = current_audit.read_text(encoding="utf-8")
    # Extract all table rows that look like finding status rows:
    # | **ID** | ... | ✅ Resolved | or | ... | ⏳ Still open |
    resolved_ids: set[str] = set()
    open_ids: set[str] = set()

    for line_no, line in enumerate(text.splitlines(), 1):
        # Only inspect pipe-delimited table rows (lines starting with |) to
        # avoid false positives from narrative text that mentions a finding ID
        # and the word "open" in an unrelated clause.
        if not line.startswith("|"):
            continue
        id_match = re.search(r"\*\*([A-Z]+-\d+[a-z]?)\*\*", line)
        if not id_match:
            continue
        finding_id = id_match.group(1)
        low = line.lower()
        if "✅" in line or "resolved" in low:
            resolved_ids.add(finding_id)
        if "⏳" in line or "still open" in low or "open —" in low:
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
    check_lifecycle_manifest()
    check_finding_uniqueness()

    print()
    print(f"Results: {OK} passed, {FAIL} failed")
    sys.exit(1 if FAIL else 0)


if __name__ == "__main__":
    main()

