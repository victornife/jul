from __future__ import annotations

from pathlib import Path
import subprocess

PREVIOUS_PAYLOAD_COMMIT = "aa17e662cb57ff56d8d90e96827875a2378b2762"
PREVIOUS_SCRIPT = ".issue81-bundle/integration-fixes.py"

# Reuse the already-audited integration repair script from the immutable parent
# commit, then layer the compatibility fixes discovered by the one-shot test run.
previous = subprocess.check_output(
    ["git", "show", f"{PREVIOUS_PAYLOAD_COMMIT}:{PREVIOUS_SCRIPT}"],
    text=True,
)
exec(compile(previous, f"{PREVIOUS_PAYLOAD_COMMIT}:{PREVIOUS_SCRIPT}", "exec"), {"__name__": "__main__"})


def replace_once(path: Path, old: str, new: str, label: str) -> None:
    text = path.read_text(encoding="utf-8")
    if new in text:
        return
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: {label}: expected one anchor, found {count}")
    path.write_text(text.replace(old, new, 1), encoding="utf-8")


# The issue-81 projection is additive: older fixtures may omit any of the new
# top-level sections. Keep the runtime parser aligned with the Partial public
# type while production responses continue to supply the complete projection.
client = Path("internal/admin/ui/src/api/client.ts")
for field, schema in (
    ("compression", "CompressionProjectionSchema"),
    ("rate_limit", "GlobalRateLimitProjectionSchema"),
    ("cache", "CacheProjectionSchema"),
):
    replace_once(
        client,
        f"  {field}: {schema},\n",
        f"  {field}: {schema}.optional(),\n",
        f"optional {field} projection",
    )

# Legacy parser assertions intentionally exercise partial traffic projections.
# Keep their property access consistent with the additive schema above.
console_test = Path("internal/admin/ui/src/test/consolev2.test.tsx")
replace_once(
    console_test,
    "expect(t.compression.encoders).toContain(\"gzip\");",
    "expect(t.compression?.encoders).toContain(\"gzip\");",
    "optional compression assertion",
)
replace_once(
    console_test,
    "expect(t.rate_limit.rate).toBe(100);",
    "expect(t.rate_limit?.rate).toBe(100);",
    "optional rate-limit assertion",
)

# op_index is an API index, not a human ordinal. Preserve the server's zero-based
# discriminator in errors so operators can correlate it to the submitted batch.
hook = Path("internal/admin/ui/src/lib/useRunPatchBatch.ts")
replace_once(
    hook,
    "String(error.opIndex + 1)",
    "String(error.opIndex)",
    "zero-based failed operation index",
)
