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

# Issue #81 adds one authoritative pending-restart read to every structured
# preview. Most pre-existing component tests intentionally mock only the request
# they exercise, so letting the new read hit those mocks either consumes a queued
# response or feeds a patch-preview payload into PendingRestartResponseSchema.
# Keep this compatibility strictly in Vitest: endpoint-aware mocks continue to
# call the real client, while legacy mocks receive the truthful default that no
# staged restart exists. Production code remains strict and unchanged.
setup = Path("internal/admin/ui/src/test/setup.ts")
replace_once(
    setup,
    'import "@testing-library/jest-dom";\n',
    '''import "@testing-library/jest-dom";\nimport { vi } from "vitest";\n\ntype FetchMockWithImplementation = typeof fetch & {\n  getMockImplementation?: () =>\n    | ((...args: Parameters<typeof fetch>) => ReturnType<typeof fetch>)\n    | undefined;\n};\n\nvi.mock("@/api/client.ts", async (importOriginal) => {\n  const actual = await importOriginal<typeof import("@/api/client.ts")>();\n  return {\n    ...actual,\n    fetchPendingRestart: async () => {\n      const currentFetch = globalThis.fetch as FetchMockWithImplementation;\n      const implementation = currentFetch.getMockImplementation?.();\n      if (\n        typeof implementation === "function" &&\n        implementation.toString().includes("pending-restart")\n      ) {\n        return actual.fetchPendingRestart();\n      }\n      return { pending: false };\n    },\n  };\n});\n''',
    "legacy pending-restart test compatibility",
)

# ConfigPanel's older tests build a legacy patch handoff because that was the
# contract before issue #81. Their purpose is to exercise apply/finalization,
# not the new stale-preview recovery path. Upgrade only this test helper to the
# complete, already-authoritative draft contract so production still refreshes
# genuinely legacy handoffs.
write_test = Path("internal/admin/ui/src/test/console-v2-write.test.tsx")
text = write_test.read_text(encoding="utf-8")
old_import = "  type LegacyPendingPatchDraftInput,\n"
new_import = "  type PendingPatchDraft,\n"
if old_import not in text:
    raise SystemExit(f"{write_test}: legacy draft import anchor missing")
text = text.replace(old_import, new_import, 1)
old_return = "}): LegacyPendingPatchDraftInput {"
new_return = "}): PendingPatchDraft {"
if old_return not in text:
    raise SystemExit(f"{write_test}: structured draft return anchor missing")
text = text.replace(old_return, new_return, 1)
old_tail = '''    previewDiff,
    lifecycle,
  };
}'''
new_tail = '''    previewDiff,
    lifecycle,
    recommendedAction: lifecycle.can_apply_hot
      ? "hot"
      : lifecycle.can_stage_restart
        ? "stage_restart"
        : "none",
    pendingRestart: { state: "none", subsystems: [] },
    candidateState: "not_requested",
    requiresFreshPreview: false,
  };
}'''
if text.count(old_tail) != 1:
    raise SystemExit(f"{write_test}: structured draft metadata anchor moved")
text = text.replace(old_tail, new_tail, 1)

# The migrated confirmation action is also named "Apply live". Keep legacy
# tests scoped to the confirmation action rather than letting Testing Library
# see both the page action and the modal action with the same accessible name.
helper_anchor = '''// confirmRollback opens the rollback confirmation for the single seeded snapshot
// and clicks Confirm only once its diff preview has loaded — Confirm is disabled
// until the rollback-scoped diff resolves (N-02).
'''
helper = '''async function confirmApplyLive(): Promise<void> {
  const buttons = await screen.findAllByRole("button", { name: "Apply live" });
  const confirm = buttons[buttons.length - 1];
  if (confirm === undefined) throw new Error("Apply live confirmation button was not rendered.");
  fireEvent.click(confirm);
}

''' + helper_anchor
if text.count(helper_anchor) != 1:
    raise SystemExit(f"{write_test}: confirm helper anchor moved")
text = text.replace(helper_anchor, helper, 1)
confirm_old = 'fireEvent.click(await screen.findByRole("button", { name: "Apply now" }));'
confirm_count = text.count(confirm_old)
if confirm_count == 0:
    raise SystemExit(f"{write_test}: no legacy apply confirmation selectors found")
text = text.replace(confirm_old, 'await confirmApplyLive();')
text = text.replace('"Apply configuration?"', '"Apply live?"')
write_test.write_text(text, encoding="utf-8")
