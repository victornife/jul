#!/usr/bin/env python3
"""
Docs validation script.

Checks performed:
1. All relative Markdown links resolve to existing files.
2. All fenced TOML blocks are syntactically valid.
"""
import os
import re
import sys
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

    print()
    print(f"Results: {OK} passed, {FAIL} failed")
    sys.exit(1 if FAIL else 0)


if __name__ == "__main__":
    main()
