from __future__ import annotations

from pathlib import Path
import subprocess

PREVIOUS_PAYLOAD_COMMIT = "f032df9649f72e03220d1ccd35cd6b5788d64973"
PREVIOUS_SCRIPT = ".issue81-bundle/integration-fixes.py"

# Reuse the complete audited repair chain from the previous payload commit.
previous = subprocess.check_output(
    ["git", "show", f"{PREVIOUS_PAYLOAD_COMMIT}:{PREVIOUS_SCRIPT}"],
    text=True,
)
exec(compile(previous, f"{PREVIOUS_PAYLOAD_COMMIT}:{PREVIOUS_SCRIPT}", "exec"), {"__name__": "__main__"})

# The docs-check unit tests import helper modules and may leave Python bytecode
# caches behind. They are runner-local build artifacts, not implementation
# output, so ignore them in this reconstructed worktree's private exclude file
# instead of letting the final cleanliness guard mistake them for source changes.
exclude = Path(".git/info/exclude")
text = exclude.read_text(encoding="utf-8") if exclude.exists() else ""
pattern = "__pycache__/"
if pattern not in text.splitlines():
    if text and not text.endswith("\n"):
        text += "\n"
    exclude.write_text(text + pattern + "\n", encoding="utf-8")
