/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { expect, test } from "@playwright/test";
import { readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { AdminRuntimeSettingsProjectionSchema, RawConfigSchema } from "../src/api/client.ts";

async function baseVersion(request: import("@playwright/test").APIRequestContext): Promise<string> {
  const response = await request.get("/api/config");
  expect(response.status()).toBe(200);
  return RawConfigSchema.parse(await response.json()).base_version ?? "";
}

async function settings(request: import("@playwright/test").APIRequestContext) {
  const response = await request.get("/api/config/settings");
  expect(response.status()).toBe(200);
  return AdminRuntimeSettingsProjectionSchema.parse(await response.json());
}

async function applyAudit(
  request: import("@playwright/test").APIRequestContext,
  audit: { file?: string; rotate_max_mb?: number; rotate_keep?: number },
) {
  const response = await request.post("/api/config/patch/apply", {
    headers: { "Content-Type": "application/json" },
    data: JSON.stringify({
      base_version: await baseVersion(request),
      ops: [{ op: "admin_audit_sink_set", audit_sink: audit }],
    }),
  });
  expect([200, 202]).toContain(response.status());
}

test("HR-07C setting-change success event is durably written to the newly published sink", async ({
  request,
}) => {
  const initial = await settings(request);
  const original = {
    file: initial.audit_log_file,
    rotate_max_mb: initial.audit_log_rotate_max_mb,
    rotate_keep: initial.audit_log_rotate_keep,
  };
  const path = join(tmpdir(), `jul-hr07c-transition-${process.pid}-${Date.now()}.jsonl`);
  await rm(path, { force: true });
  let changed = false;
  try {
    await applyAudit(request, { file: path, rotate_max_mb: 7, rotate_keep: 3 });
    changed = true;

    await expect
      .poll(async () => {
        try {
          const events = (await readFile(path, "utf8"))
            .split("\n")
            .filter(Boolean)
            .map(
              (line) =>
                JSON.parse(line) as {
                  operation?: string;
                  resource?: string;
                  result?: string;
                },
            );
          return events.some(
            (event) =>
              event.operation === "config.patch" &&
              event.resource === "config" &&
              event.result === "success",
          );
        } catch {
          return false;
        }
      })
      .toBe(true);
  } finally {
    if (changed) {
      await applyAudit(request, original);
    }
    await rm(path, { force: true });
  }
});
