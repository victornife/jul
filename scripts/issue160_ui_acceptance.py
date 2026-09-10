from pathlib import Path


def patch(path: str, old: str, new: str) -> None:
    p = Path(path)
    s = p.read_text()
    if old not in s:
        raise SystemExit(f"missing anchor in {path}: {old[:120]!r}")
    p.write_text(s.replace(old, new, 1))

# Complete the existing Admin Runtime Settings fixture with the three HR-07C fields.
patch(
    "internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.test.tsx",
    '''  max_event_conns: 4,\n  lifecycle: {''',
    '''  max_event_conns: 4,\n  audit_log_file: "/tmp/audit-a.jsonl",\n  audit_log_rotate_max_mb: 100,\n  audit_log_rotate_keep: 14,\n  audit_sink: { configured: true, active: true, healthy: true, generation: 7 },\n  lifecycle: {''',
)
patch(
    "internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.test.tsx",
    '''    max_event_conns: hot,\n  },''',
    '''    max_event_conns: hot,\n    audit_log_file: hot,\n    audit_log_rotate_max_mb: hot,\n    audit_log_rotate_keep: hot,\n  },''',
)

p = Path("internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.test.tsx")
s = p.read_text()
s += r'''

describe("AdminRuntimeSettingsDrawer HR-07C audit sink", () => {
  beforeEach(() => {
    mocks.run.mockClear();
    mocks.fetchSettings.mockReset();
    mocks.fetchSettings.mockResolvedValue(baseSettings);
    mocks.runnerError = null;
    mocks.runnerBusy = false;
  });

  it("submits a sparse path switch and warns that old files are not migrated", async () => {
    renderDrawer();
    await screen.findByText("Durable audit sink");

    fireEvent.change(screen.getByLabelText("Audit file"), {
      target: { value: "/tmp/audit-b.jsonl" },
    });
    expect(screen.getByText(/not copied, moved, merged, or deleted/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    await waitFor(() => {
      expect(mocks.run).toHaveBeenCalledWith([
        { op: "admin_audit_sink_set", audit_sink: { file: "/tmp/audit-b.jsonl" } },
      ]);
    });
  });

  it("disables only durable persistence and explains ring/file retention", async () => {
    renderDrawer();
    await screen.findByText("Durable audit sink");

    fireEvent.change(screen.getByLabelText("Audit file"), { target: { value: "" } });
    expect(screen.getByText(/in-memory audit ring and event IDs continue/)).toBeInTheDocument();
    expect(screen.getByText(/existing audit files\/backups are retained/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    await waitFor(() => {
      expect(mocks.run).toHaveBeenCalledWith([
        { op: "admin_audit_sink_set", audit_sink: { file: "" } },
      ]);
    });
  });

  it("submits rotation-only changes without path churn and shows retention semantics", async () => {
    renderDrawer();
    await screen.findByText("Durable audit sink");

    fireEvent.change(screen.getByLabelText(/Rotate max MB/), { target: { value: "64" } });
    fireEvent.change(screen.getByLabelText(/Backups to keep/), { target: { value: "9" } });
    expect(screen.getByText(/Preview never prunes backups/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    await waitFor(() => {
      expect(mocks.run).toHaveBeenCalledWith([
        {
          op: "admin_audit_sink_set",
          audit_sink: { rotate_max_mb: 64, rotate_keep: 9 },
        },
      ]);
    });
  });

  it("renders bounded active health and never exposes filesystem detail", async () => {
    mocks.fetchSettings.mockResolvedValue({
      ...baseSettings,
      audit_sink: {
        configured: true,
        active: true,
        healthy: false,
        generation: 11,
        write_failures: 2,
        last_failure_category: "write",
        last_failure_at: "2026-09-10T16:00:00Z",
      },
    });
    renderDrawer();
    await screen.findByText("Durable audit sink");

    expect(screen.getByText(/Status: Degraded/)).toBeInTheDocument();
    expect(screen.getByText(/generation 11/)).toBeInTheDocument();
    expect(screen.getByText(/write/)).toBeInTheDocument();
    expect(screen.queryByText(/permission denied|\/tmp\/secret/i)).not.toBeInTheDocument();
  });

  it("validates rotation values and exposes lifecycle badges for all three fields", async () => {
    renderDrawer();
    await screen.findByText("Durable audit sink");

    expect(screen.getAllByText("Hot reload").length).toBeGreaterThanOrEqual(3);
    fireEvent.change(screen.getByLabelText(/Rotate max MB/), { target: { value: "1.5" } });
    expect(screen.getByText(/Rotation values must be non-negative whole numbers/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Review changes" })).toBeDisabled();
  });
});
'''
p.write_text(s)

# Real-server Playwright acceptance. It uses the same managed fixture as HR-06B/07A,
# verifies browser preview + live apply, actual JSONL files, global ring continuity,
# path cutover, rotation-only update, disable, and restoration.
Path("internal/admin/ui/e2e/issue160-audit-sink.spec.ts").write_text(r'''/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 *
 * HR-07C real-process acceptance. The Playwright webServer is one Jul process;
 * all transitions below happen through its live admin API without restarting it.
 */

import { expect, test } from "@playwright/test";
import { readFile, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  AdminRuntimeSettingsProjectionSchema,
  RawConfigSchema,
} from "../src/api/client.ts";

type Settings = ReturnType<typeof AdminRuntimeSettingsProjectionSchema.parse>;

async function settings(request: import("@playwright/test").APIRequestContext): Promise<Settings> {
  const response = await request.get("/api/config/settings");
  expect(response.status()).toBe(200);
  return AdminRuntimeSettingsProjectionSchema.parse(await response.json());
}

async function baseVersion(request: import("@playwright/test").APIRequestContext): Promise<string> {
  const response = await request.get("/api/config");
  expect(response.status()).toBe(200);
  return RawConfigSchema.parse(await response.json()).base_version ?? "";
}

async function applyAudit(
  request: import("@playwright/test").APIRequestContext,
  audit: { file?: string; rotate_max_mb?: number; rotate_keep?: number },
  base = "",
) {
  const response = await request.post("/api/config/patch/apply", {
    headers: { "Content-Type": "application/json" },
    data: JSON.stringify({ base_version: base, ops: [{ op: "admin_audit_sink_set", audit_sink: audit }] }),
  });
  expect([200, 202, 204]).toContain(response.status());
  return response;
}

async function waitAudit(
  request: import("@playwright/test").APIRequestContext,
  expected: { file?: string; rotate_max_mb?: number; rotate_keep?: number },
) {
  await expect
    .poll(async () => {
      const current = await settings(request);
      return (
        (expected.file === undefined || current.audit_log_file === expected.file) &&
        (expected.rotate_max_mb === undefined || current.audit_log_rotate_max_mb === expected.rotate_max_mb) &&
        (expected.rotate_keep === undefined || current.audit_log_rotate_keep === expected.rotate_keep)
      );
    })
    .toBe(true);
}

async function auditIDs(path: string): Promise<number[]> {
  const text = await readFile(path, "utf8");
  return text
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line) as { id: number })
    .map((event) => event.id);
}

async function ringMaxID(request: import("@playwright/test").APIRequestContext): Promise<number> {
  const response = await request.get("/api/audit?limit=10000");
  expect(response.status()).toBe(200);
  const body = (await response.json()) as { events?: Array<{ id?: number }> } | Array<{ id?: number }>;
  const events = Array.isArray(body) ? body : (body.events ?? []);
  return Math.max(0, ...events.map((event) => event.id ?? 0));
}

async function fileSize(path: string): Promise<number> {
  try {
    return (await stat(path)).size;
  } catch {
    return 0;
  }
}

test.describe.serial("HR-07C durable audit sink", () => {
  test("Console previews all audit fields as hot and applies a prepared path", async ({ page, request }) => {
    const initial = await settings(request);
    const original = {
      file: initial.audit_log_file,
      rotate_max_mb: initial.audit_log_rotate_max_mb,
      rotate_keep: initial.audit_log_rotate_keep,
    };
    const pathA = join(tmpdir(), `jul-hr07c-ui-${process.pid}-${Date.now()}.jsonl`);
    await rm(pathA, { force: true });
    let changed = false;
    try {
      await page.goto("/plugins");
      await page.getByRole("button", { name: "Runtime settings" }).click();
      const dialog = page.getByRole("dialog", { name: "Admin runtime settings" });
      await expect(dialog.getByText("Durable audit sink")).toBeVisible();
      await dialog.getByLabel("Audit file").fill(pathA);
      await dialog.getByLabel(/Rotate max MB/).fill("8");
      await dialog.getByLabel(/Backups to keep/).fill("3");
      await expect(dialog.getByText(/not copied, moved, merged, or deleted/)).toBeVisible();

      const previewRequest = page.waitForRequest("/api/config/patch/preview");
      await dialog.getByRole("button", { name: "Review changes" }).click();
      const preview = await previewRequest;
      expect(preview.postDataJSON()).toMatchObject({
        ops: [
          {
            op: "admin_audit_sink_set",
            audit_sink: { file: pathA, rotate_max_mb: 8, rotate_keep: 3 },
          },
        ],
      });

      await expect(page).toHaveURL(/\/config$/);
      for (const field of [
        "admin.audit_log_file",
        "admin.audit_log_rotate_max_mb",
        "admin.audit_log_rotate_keep",
      ]) {
        await expect(page.locator("code").filter({ hasText: new RegExp(`^${field.replaceAll(".", "\\.")}$`) })).toBeVisible();
      }
      await expect(page.getByText("Hot apply: available", { exact: true })).toBeVisible();

      await page.getByRole("button", { name: "Apply live" }).click();
      const applyDialog = page.getByRole("dialog", { name: "Apply live?" });
      await expect(applyDialog).toBeVisible();
      const response = page.waitForResponse(
        (r) => r.url().includes("/api/config/patch/apply") && r.request().method() === "POST",
      );
      await applyDialog.getByRole("button", { name: "Apply live" }).click();
      expect([200, 202]).toContain((await response).status());
      changed = true;
      await waitAudit(request, { file: pathA, rotate_max_mb: 8, rotate_keep: 3 });

      const live = await settings(request);
      expect(live.lifecycle.audit_log_file?.class).toBe("hot_reload");
      expect(live.lifecycle.audit_log_rotate_max_mb?.class).toBe("hot_reload");
      expect(live.lifecycle.audit_log_rotate_keep?.class).toBe("hot_reload");
      expect(live.audit_sink?.configured).toBe(true);
      expect(live.audit_sink?.active).toBe(true);
      expect(live.audit_sink?.healthy).toBe(true);
    } finally {
      if (changed) {
        await applyAudit(request, original);
        await waitAudit(request, original);
      }
      await rm(pathA, { force: true });
    }
  });

  test("one live Jul process cuts A to B, rotates policy, disables durability, and keeps ring IDs moving", async ({ request }) => {
    const initial = await settings(request);
    const original = {
      file: initial.audit_log_file,
      rotate_max_mb: initial.audit_log_rotate_max_mb,
      rotate_keep: initial.audit_log_rotate_keep,
    };
    const nonce = `${process.pid}-${Date.now()}`;
    const pathA = join(tmpdir(), `jul-hr07c-a-${nonce}.jsonl`);
    const pathB = join(tmpdir(), `jul-hr07c-b-${nonce}.jsonl`);
    await Promise.all([rm(pathA, { force: true }), rm(pathB, { force: true })]);
    let changed = false;
    try {
      const before = await ringMaxID(request);
      await applyAudit(request, { file: pathA, rotate_max_mb: 1, rotate_keep: 2 }, await baseVersion(request));
      changed = true;
      await waitAudit(request, { file: pathA, rotate_max_mb: 1, rotate_keep: 2 });

      await applyAudit(request, { rotate_keep: 3 });
      await waitAudit(request, { rotate_keep: 3 });
      await expect.poll(() => fileSize(pathA)).toBeGreaterThan(0);

      await applyAudit(request, { file: pathB });
      await waitAudit(request, { file: pathB });
      await applyAudit(request, { rotate_keep: 4 });
      await waitAudit(request, { rotate_keep: 4 });
      await expect.poll(() => fileSize(pathB)).toBeGreaterThan(0);

      const idsA = await auditIDs(pathA);
      const idsB = await auditIDs(pathB);
      expect(idsA.length).toBeGreaterThan(0);
      expect(idsB.length).toBeGreaterThan(0);
      expect(new Set([...idsA, ...idsB]).size).toBe(idsA.length + idsB.length);
      expect([...idsA].sort((a, b) => a - b)).toEqual(idsA);
      expect([...idsB].sort((a, b) => a - b)).toEqual(idsB);

      await applyAudit(request, { file: "" });
      await waitAudit(request, { file: "" });
      const aSize = await fileSize(pathA);
      const bSize = await fileSize(pathB);
      const ringAtDisable = await ringMaxID(request);

      const rejected = await request.post("/api/config/patch/apply", {
        headers: { "Content-Type": "application/json" },
        data: JSON.stringify({
          base_version: "definitely-stale",
          ops: [{ op: "admin_audit_sink_set", audit_sink: { rotate_keep: 5 } }],
        }),
      });
      expect(rejected.status()).toBe(409);
      await expect.poll(() => ringMaxID(request)).toBeGreaterThan(ringAtDisable);
      expect(await fileSize(pathA)).toBe(aSize);
      expect(await fileSize(pathB)).toBe(bSize);
      expect(await ringMaxID(request)).toBeGreaterThan(before);

      const disabled = await settings(request);
      expect(disabled.audit_sink).toBeUndefined();
    } finally {
      if (changed) {
        await applyAudit(request, original);
        await waitAudit(request, original);
      }
      await Promise.all([rm(pathA, { force: true }), rm(pathB, { force: true })]);
    }
  });
});
''')

patch(
    "docs/reload-semantics.md",
    '''  admin read/write/apply limits plus the shared SSE cap) apply through the same\n  transaction.''',
    '''  admin read/write/apply limits plus the shared SSE cap, and the durable audit\n  sink path/rotation policy) apply through the same transaction.''',
)
p = Path("docs/reload-semantics.md")
s = p.read_text()
if "admin history/audit resources" not in s:
    raise SystemExit("restart-bound audit prose anchor missing")
p.write_text(s.replace("admin history/audit resources", "admin history resources", 1))

for path, marker, text in [
    (
        "docs/console.md",
        "##",
        "\n> **Durable audit sink hot reload (HR-07C).** The Admin Runtime Settings drawer edits the audit JSONL path and rotation policy through the typed `admin_audit_sink_set` operation. Path switches never migrate or delete old files; disabling durability keeps the in-memory audit ring and monotonic IDs. Active health is bounded and secret-safe. See [audit sink hot reload](audit-sink-hot-reload.md).\n\n",
    ),
    (
        "docs/security-posture.md",
        "##",
        "\n> **Audit sink path safety.** HR-07C prepares the candidate destination before Publish, rejects unsafe final symlinks/special files, identity-checks the opened regular file, and exposes only bounded failure categories outside the authenticated `config:read` settings surface. See [audit sink hot reload](audit-sink-hot-reload.md).\n\n",
    ),
    (
        "docs/known-limitations.md",
        "##",
        "\n> **Audit sink retention boundary.** Hot path switches do not migrate, merge, or delete files at the previous destination. Rotation retention is applied only by later active rotations; Preview/Prepare never prune historical backups. Disabling durability preserves existing files and the process-lifetime in-memory audit ring. See [audit sink hot reload](audit-sink-hot-reload.md).\n\n",
    ),
]:
    p = Path(path)
    s = p.read_text()
    idx = s.find(marker)
    if idx < 0:
        raise SystemExit(f"heading marker missing in {path}")
    p.write_text(s[:idx] + text + s[idx:])

p = Path("CHANGELOG.md")
s = p.read_text()
pos = s.find("## ")
if pos < 0:
    raise SystemExit("CHANGELOG release heading missing")
line_end = s.find("\n", pos)
entry = "\n- **HR-07C / #160 — hot-reloadable durable audit sink.** The audit ring and global event IDs are process-stable while path/rotation changes publish a prepared durable sink generation, with same-path single-owner rotation, bounded post-Publish retirement, secret-safe health, typed Console editing, and deterministic failure/concurrency coverage.\n"
s = s[: line_end + 1] + entry + s[line_end + 1 :]
p.write_text(s)

print("HR-07C UI/E2E/docs acceptance staged")
