/**
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
  HistoryEntrySchema,
  RawConfigSchema,
} from "../src/api/client.ts";
import { z } from "zod";

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
    data: JSON.stringify({
      base_version: base,
      ops: [{ op: "admin_audit_sink_set", audit_sink: audit }],
    }),
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
        (expected.rotate_max_mb === undefined ||
          current.audit_log_rotate_max_mb === expected.rotate_max_mb) &&
        (expected.rotate_keep === undefined ||
          current.audit_log_rotate_keep === expected.rotate_keep)
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
  const body = (await response.json()) as
    | { events?: Array<{ id?: number }> }
    | Array<{ id?: number }>;
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

function withAuditSink(raw: string, file: string, maxMB: number, keep: number): string {
  const lines = raw.split("\n");
  const admin = lines.findIndex((line) => line.trim() === "[admin]");
  if (admin < 0) throw new Error("fixture has no [admin] section");
  let end = lines.length;
  for (let i = admin + 1; i < lines.length; i += 1) {
    if (/^\s*\[/.test(lines[i] ?? "")) {
      end = i;
      break;
    }
  }
  const keys = new Set(["audit_log_file", "audit_log_rotate_max_mb", "audit_log_rotate_keep"]);
  const body = lines
    .slice(admin + 1, end)
    .filter((line) => !keys.has((line.split("=", 1)[0] ?? "").trim()));
  body.push(`audit_log_file = ${JSON.stringify(file)}`);
  body.push(`audit_log_rotate_max_mb = ${String(maxMB)}`);
  body.push(`audit_log_rotate_keep = ${String(keep)}`);
  return [...lines.slice(0, admin + 1), ...body, ...lines.slice(end)].join("\n");
}

async function postRollbackWithConflictRetry(
  request: import("@playwright/test").APIRequestContext,
  id: string,
) {
  let response = await request.post("/api/config/rollback", {
    headers: { "Content-Type": "application/json" },
    data: JSON.stringify({ id }),
  });
  for (let i = 0; i < 3 && response.status() === 409; i += 1) {
    await new Promise((resolve) => setTimeout(resolve, 100));
    response = await request.post("/api/config/rollback", {
      headers: { "Content-Type": "application/json" },
      data: JSON.stringify({ id }),
    });
  }
  return response;
}

test.describe.serial("HR-07C durable audit sink", () => {
  test("Console previews all audit fields as hot and applies a prepared path", async ({
    page,
    request,
  }) => {
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
        await expect(
          page.locator("code").filter({ hasText: new RegExp(`^${field.replaceAll(".", "\\.")}$`) }),
        ).toBeVisible();
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

  test("one live Jul process cuts A to B, rotates policy, disables durability, and keeps ring IDs moving", async ({
    request,
  }) => {
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
      await applyAudit(
        request,
        { file: pathA, rotate_max_mb: 1, rotate_keep: 2 },
        await baseVersion(request),
      );
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

  test("managed raw apply and history rollback use the same hot audit transition without losing ring continuity", async ({
    request,
  }) => {
    const initialResp = await request.get("/api/config");
    expect(initialResp.status()).toBe(200);
    const initial = RawConfigSchema.parse(await initialResp.json());
    const originalRaw = initial.raw ?? "";
    const originalSettings = await settings(request);
    const path = join(tmpdir(), `jul-hr07c-raw-${process.pid}-${Date.now()}.jsonl`);
    await rm(path, { force: true });
    const beforeRing = await ringMaxID(request);
    let applied = false;
    try {
      const candidate = withAuditSink(originalRaw, path, 9, 6);
      expect(candidate).not.toBe(originalRaw);
      const applyUrl = initial.base_version
        ? `/api/config/apply?base_version=${encodeURIComponent(initial.base_version)}`
        : "/api/config/apply";
      const applyResp = await request.post(applyUrl, {
        headers: { "Content-Type": "application/toml" },
        data: candidate,
      });
      expect(applyResp.status()).toBe(200);
      applied = true;
      await waitAudit(request, { file: path, rotate_max_mb: 9, rotate_keep: 6 });

      const preview = await request.post("/api/config/patch/preview", {
        headers: { "Content-Type": "application/json" },
        data: JSON.stringify({
          base_version: await baseVersion(request),
          ops: [{ op: "admin_audit_sink_set", audit_sink: { rotate_keep: 7 } }],
        }),
      });
      expect(preview.status()).toBe(200);
      await expect.poll(() => fileSize(path)).toBeGreaterThan(0);
      const afterApplyRing = await ringMaxID(request);
      expect(afterApplyRing).toBeGreaterThan(beforeRing);

      const historyResp = await request.get("/api/config/history");
      expect(historyResp.status()).toBe(200);
      const history = z.array(HistoryEntrySchema).parse(await historyResp.json());
      expect(history.length).toBeGreaterThan(0);
      const rollbackResp = await postRollbackWithConflictRetry(request, history[0].id);
      expect([200, 204]).toContain(rollbackResp.status());

      await waitAudit(request, {
        file: originalSettings.audit_log_file,
        rotate_max_mb: originalSettings.audit_log_rotate_max_mb,
        rotate_keep: originalSettings.audit_log_rotate_keep,
      });
      const afterRollbackRing = await ringMaxID(request);
      expect(afterRollbackRing).toBeGreaterThan(afterApplyRing);
      const durableIDs = await auditIDs(path);
      expect(durableIDs.length).toBeGreaterThan(0);
      expect([...durableIDs].sort((a, b) => a - b)).toEqual(durableIDs);
      applied = false;
    } finally {
      if (applied) {
        const current = await request.get("/api/config");
        if (current.status() === 200) {
          const parsed = RawConfigSchema.parse(await current.json());
          const restoreUrl = parsed.base_version
            ? `/api/config/apply?base_version=${encodeURIComponent(parsed.base_version)}`
            : "/api/config/apply";
          await request.post(restoreUrl, {
            headers: { "Content-Type": "application/toml" },
            data: originalRaw,
          });
        }
      }
      await rm(path, { force: true });
    }
  });
});