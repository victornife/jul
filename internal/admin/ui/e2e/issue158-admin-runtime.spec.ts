/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 *
 * HR-07A real-server acceptance coverage. These tests exercise the actual Jul
 * admin listener and the rendered Console. They deliberately change only the
 * four #158-owned fields and restore the shared state after every mutation.
 */

import { test, expect } from "@playwright/test";
import {
  AdminRuntimeSettingsProjectionSchema,
  RawConfigSchema,
} from "../src/api/client.ts";

interface AdminLimits {
  read_per_min: number;
  write_per_min: number;
  apply_per_min: number;
  max_event_conns: number;
}

async function currentBaseVersion(request: import("@playwright/test").APIRequestContext) {
  const response = await request.get("/api/config");
  expect(response.status()).toBe(200);
  return RawConfigSchema.parse(await response.json()).base_version ?? "";
}

async function currentSettings(request: import("@playwright/test").APIRequestContext) {
  const response = await request.get("/api/config/settings");
  if (response.status() !== 200) return null;
  return AdminRuntimeSettingsProjectionSchema.parse(await response.json());
}

function limitsFromSettings(
  settings: NonNullable<Awaited<ReturnType<typeof currentSettings>>>,
): AdminLimits {
  return {
    read_per_min: settings.rate_limit_read_per_min,
    write_per_min: settings.rate_limit_write_per_min,
    apply_per_min: settings.rate_limit_apply_per_min,
    max_event_conns: settings.max_event_conns,
  };
}

async function applyLimits(
  request: import("@playwright/test").APIRequestContext,
  limits: Partial<AdminLimits>,
  baseVersion = "",
) {
  const response = await request.post("/api/config/patch/apply", {
    headers: { "Content-Type": "application/json" },
    data: JSON.stringify({
      base_version: baseVersion,
      ops: [{ op: "admin_limits_set", admin_limits: limits }],
    }),
  });
  if (![200, 202, 204].includes(response.status())) {
    throw new Error(`failed to apply admin limits: ${String(response.status())} ${await response.text()}`);
  }
  return response;
}

async function waitForLimits(
  request: import("@playwright/test").APIRequestContext,
  expected: Partial<AdminLimits>,
): Promise<void> {
  await expect
    .poll(async () => {
      const settings = await currentSettings(request);
      if (!settings) return false;
      return (
        (expected.read_per_min === undefined || settings.rate_limit_read_per_min === expected.read_per_min) &&
        (expected.write_per_min === undefined || settings.rate_limit_write_per_min === expected.write_per_min) &&
        (expected.apply_per_min === undefined || settings.rate_limit_apply_per_min === expected.apply_per_min) &&
        (expected.max_event_conns === undefined || settings.max_event_conns === expected.max_event_conns)
      );
    })
    .toBe(true);
}

async function restoreLimits(
  request: import("@playwright/test").APIRequestContext,
  original: AdminLimits,
): Promise<void> {
  // Empty base_version is the server's explicit force mode. That makes cleanup
  // robust against the managed file-watch echo advancing the persisted version
  // after a successful live apply.
  await applyLimits(request, original);
  await waitForLimits(request, original);
}

test.describe("HR-07A admin admission runtime", () => {
  test("real admin API publishes a tighter read budget and returns the typed external 429", async ({
    request,
  }) => {
    const initial = await currentSettings(request);
    expect(initial).not.toBeNull();
    if (!initial) return;
    const original = limitsFromSettings(initial);

    // The shared fixture intentionally starts with high budgets. Preserve them
    // exactly so this stateful suite cannot couple later tests through quotas.
    expect(original.read_per_min).toBeGreaterThan(2);
    const baseVersion = await currentBaseVersion(request);
    let changed = false;
    try {
      await applyLimits(request, { read_per_min: 2 }, baseVersion);
      changed = true;

      // This is the first successful read observed under the new generation and
      // therefore consumes one of the new two-token burst. Polling attempts made
      // before Publish remain under the old captured generation.
      await waitForLimits(request, { read_per_min: 2 });

      const first = await request.get("/api/v1/status");
      expect(first.status()).toBe(200);

      const denied = await request.get("/api/v1/status");
      expect(denied.status()).toBe(429);
      const retryAfter = Number(denied.headers()["retry-after"] ?? "0");
      expect(retryAfter).toBeGreaterThanOrEqual(1);
      const body = (await denied.json()) as {
        error?: {
          code?: string;
          request_id?: string;
          details?: { retry_after_seconds?: number };
        };
      };
      expect(body.error?.code).toBe("rate_limited");
      expect(body.error?.request_id).toBeTruthy();
      expect(body.error?.details?.retry_after_seconds).toBe(retryAfter);
    } finally {
      if (changed) await restoreLimits(request, original);
    }
  });

  test("browser edits the shared SSE cap through lifecycle preview and live apply", async ({
    page,
    request,
  }) => {
    const initial = await currentSettings(request);
    expect(initial).not.toBeNull();
    if (!initial) return;
    const original = limitsFromSettings(initial);
    expect(original.max_event_conns).toBe(4);
    let changed = false;

    try {
      await page.goto("/plugins");
      await expect(page.getByRole("heading", { name: "Plugins" })).toBeVisible();
      await page.getByRole("button", { name: "Runtime settings" }).click();

      const dialog = page.getByRole("dialog", { name: "Admin runtime settings" });
      await expect(dialog).toBeVisible();
      await expect(dialog.getByText("Admin request admission")).toBeVisible();
      await expect(dialog.getByText(/Zero selects the canonical default \(4\)/)).toBeVisible();

      // The fourth number control is max_event_conns (read/write/apply precede
      // it). Lowering 4 -> 3 must show the no-forced-drain warning.
      const eventCap = dialog.getByRole("spinbutton").nth(3);
      await eventCap.fill("3");
      await expect(dialog.getByText(/Existing event\/log streams remain connected/)).toBeVisible();

      const previewRequest = page.waitForRequest("/api/config/patch/preview");
      await dialog.getByRole("button", { name: "Review changes" }).click();
      const preview = await previewRequest;
      expect(preview.postDataJSON()).toMatchObject({
        ops: [{ op: "admin_limits_set", admin_limits: { max_event_conns: 3 } }],
      });

      await expect(page).toHaveURL(/\/config$/);
      await expect(page.getByText("atomic patch", { exact: true })).toBeVisible();
      await expect(page.locator("code").filter({ hasText: /^admin\.max_event_conns$/ })).toBeVisible();
      await expect(page.getByText("Hot apply: available", { exact: true })).toBeVisible();

      await page.getByRole("button", { name: "Apply live" }).click();
      const applyDialog = page.getByRole("dialog", { name: "Apply live?" });
      await expect(applyDialog).toBeVisible();
      const applyResponse = page.waitForResponse(
        (response) =>
          response.url().includes("/api/config/patch/apply") && response.request().method() === "POST",
      );
      await applyDialog.getByRole("button", { name: "Apply live" }).click();
      expect([200, 202]).toContain((await applyResponse).status());
      changed = true;

      await waitForLimits(request, { max_event_conns: 3 });
    } finally {
      if (changed) await restoreLimits(request, original);
    }
  });
});
