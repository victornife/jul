#!/usr/bin/env python3
"""Reject local or alternate replacements for the supported WASM/WAF engines."""

import json
import subprocess
import sys


PROTECTED_MODULES = {
    "github.com/corazawaf/coraza/v3",
    "github.com/tetratelabs/wazero",
}


def main() -> int:
    result = subprocess.run(
        ["go", "mod", "edit", "-json"],
        check=True,
        capture_output=True,
        text=True,
    )
    module = json.loads(result.stdout)
    violations = [
        replacement["Old"]["Path"]
        for replacement in module.get("Replace", [])
        if replacement.get("Old", {}).get("Path") in PROTECTED_MODULES
    ]
    if violations:
        for path in sorted(violations):
            print(f"go.mod replaces protected upstream module {path}", file=sys.stderr)
        return 1
    print("No protected upstream module replacements found.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
