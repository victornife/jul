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


def check_markdown_links(path: Path, text: str):
    """Check that relative markdown links point to existing files."""
    # Match [text](url) and ![alt](url) but skip URLs with protocols
    for match in re.finditer(r"!?\[([^\]]+)\]\(([^)]+)\)", text):
        raw_url = match.group(2)
        line = text[:match.start()].count("\n") + 1

        # Skip external URLs and anchors-only
        if re.match(r"^(https?|mailto|ftp):", raw_url):
            continue
        if raw_url.startswith("#"):
            continue

        # Strip anchor
        url = raw_url.split("#")[0]
        if not url:
            continue

        # Resolve relative to the markdown file
        target = (path.parent / url).resolve()
        if not target.exists():
            error(path, line, f"broken link: {raw_url}")
        else:
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
    bad = [r"example/jul", r"example\.com/jul"]
    for pattern in bad:
        for match in re.finditer(pattern, text):
            line = text[:match.start()].count("\n") + 1
            error(path, line, f"placeholder found: {pattern}")


def check_version_consistency(md_files: list[Path]):
    """Ensure roadmap and status docs carry the same version/date."""
    versions: dict[str, tuple[str, int]] = {}  # file stem -> (version, line)

    version_re = re.compile(r"> Version (\d+\.\d+) · Updated (\d{4}-\d{2}-\d{2})")
    for md in md_files:
        text = md.read_text(encoding="utf-8")
        for match in version_re.finditer(text):
            line = text[:match.start()].count("\n") + 1
            key = md.relative_to(DOCS).as_posix()
            versions[key] = (match.group(1), line)

    roadmap = versions.get("roadmap/README.md")
    status = versions.get("status.md")
    if roadmap and status and roadmap[0] != status[0]:
        error(
            DOCS / "roadmap" / "README.md",
            roadmap[1],
            f"version mismatch: roadmap={roadmap[0]} vs status={status[0]}",
        )


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
    # Extract toml tags from fields of the root Config struct.
    # We look for lines like: `Foo FooConfig   `toml:"foo"` within the Config struct definition.
    keys: set[str] = set()
    in_config_struct = False
    brace_depth = 0
    for line in schema_text.splitlines():
        stripped = line.strip()
        if stripped.startswith("type Config struct"):
            in_config_struct = True
        if in_config_struct:
            brace_depth += stripped.count("{")
            brace_depth -= stripped.count("}")
            if brace_depth < 0:
                break
            m = re.search(r'`toml:"([^"]+)"`', stripped)
            if m:
                keys.add(m.group(1))

    if not keys:
        warn(schema_path, 0, "no toml keys found in Config struct")
        return

    config_doc = DOCS / "configuration.md"
    if not config_doc.exists():
        warn(config_doc, 0, "configuration.md not found, skipping schema-doc drift check")
        return
    doc_text = config_doc.read_text(encoding="utf-8")

    for key in sorted(keys):
        # A well-documented key appears as [key], [[key]], or in backticks: `key`
        needle = re.compile(rf"(\\[\\[{re.escape(key)}\\]\\]|\\[{re.escape(key)}\\]|`{re.escape(key)}`)")
        if not needle.search(doc_text):
            warn(config_doc, 0, f"schema key '{key}' not found in configuration.md")


def main():
    SKIP_DIRS = {"node_modules", "vendor", ".git", "__pycache__"}
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

    check_version_consistency(md_files)
    check_schema_doc_drift()

    print()
    print(f"Results: {OK} passed, {FAIL} failed")
    sys.exit(1 if FAIL else 0)


if __name__ == "__main__":
    main()
