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

# The new lifecycle-aware preview always reads pending-restart state. Existing
# component tests use fetch mocks written before that endpoint existed, and many
# of them intentionally ignore the request URL. Provide a test-harness default
# only when a mock does not explicitly mention the pending endpoint; tests that
# exercise pending=true/failed pending requests retain full control. Keep the
# original mock itself as the Proxy target so intercepted pending requests do
# not consume queued mockResolvedValueOnce values or alter its call history.
setup = Path("internal/admin/ui/src/test/setup.ts")
text = setup.read_text(encoding="utf-8")
marker = "function wrapIssue81Fetch"
if marker not in text:
    if 'import "@testing-library/jest-dom";\n' not in text:
        raise SystemExit(f"{setup}: jest-dom setup anchor missing")
    text += r'''

type Issue81FetchMock = typeof fetch & {
  getMockImplementation?: () => ((...args: Parameters<typeof fetch>) => ReturnType<typeof fetch>) | undefined;
};

function issue81RequestPath(input: Parameters<typeof fetch>[0]): string {
  if (typeof input === "string") return new URL(input, "http://localhost").pathname;
  if (input instanceof URL) return input.pathname;
  return new URL(input.url, "http://localhost").pathname;
}

function issue81HandlesPendingRestart(next: typeof fetch): boolean {
  const implementation = (next as Issue81FetchMock).getMockImplementation?.();
  return typeof implementation === "function" && implementation.toString().includes("pending-restart");
}

function wrapIssue81Fetch(next: typeof fetch): typeof fetch {
  return new Proxy(next, {
    apply(target, thisArg, args) {
      const [input] = args as Parameters<typeof fetch>;
      if (
        issue81RequestPath(input) === "/api/config/pending-restart" &&
        !issue81HandlesPendingRestart(target)
      ) {
        return Promise.resolve(
          new Response(JSON.stringify({ pending: false }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return Reflect.apply(target, thisArg, args);
    },
  });
}

let issue81Fetch = wrapIssue81Fetch(globalThis.fetch);
Object.defineProperty(globalThis, "fetch", {
  configurable: true,
  get(): typeof fetch {
    return issue81Fetch;
  },
  set(next: typeof fetch) {
    issue81Fetch = wrapIssue81Fetch(next);
  },
});
'''
    setup.write_text(text, encoding="utf-8")
