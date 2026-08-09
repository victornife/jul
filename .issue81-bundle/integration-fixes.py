from __future__ import annotations

from pathlib import Path
import subprocess

PREVIOUS_PAYLOAD_COMMIT = "f18f2d72f5384033009292dcc638d5ee931fedc2"
PREVIOUS_SCRIPT = ".issue81-bundle/integration-fixes.py"

# Reuse the already-audited integration repair chain from the previous payload
# commit, then fix the final frontend lint issue discovered by the one-shot run.
previous = subprocess.check_output(
    ["git", "show", f"{PREVIOUS_PAYLOAD_COMMIT}:{PREVIOUS_SCRIPT}"],
    text=True,
)
exec(compile(previous, f"{PREVIOUS_PAYLOAD_COMMIT}:{PREVIOUS_SCRIPT}", "exec"), {"__name__": "__main__"})

# The request-body guard above each callback already narrows requestBody to a
# string. Keeping an explicit `as string` assertion is redundant and rejected by
# @typescript-eslint/no-unnecessary-type-assertion.
for rel, expected in (
    ("internal/admin/ui/src/test/consolev2.test.tsx", 2),
    ("internal/admin/ui/src/test/mtls.test.tsx", 1),
):
    path = Path(rel)
    text = path.read_text(encoding="utf-8")
    old = "onPatch(requestBody as string);"
    count = text.count(old)
    if count != expected:
        raise SystemExit(f"{rel}: expected {expected} redundant request-body assertions, found {count}")
    path.write_text(text.replace(old, "onPatch(requestBody);"), encoding="utf-8")
