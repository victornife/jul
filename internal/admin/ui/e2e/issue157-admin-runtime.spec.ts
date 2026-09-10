/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 *
 * HR-06B real-server acceptance coverage. This deliberately exercises the
 * browser flow for Console self-disable rather than only the Go handler: the
 * loaded SPA must receive the server-side reachability warning, retry the exact
 * patch with explicit confirmation, observe a correlated result while the API
 * remains reachable, and recover through the documented API path.
 */

import { test, expect } from "@playwright/test";
import {
  AdminRuntimeSettingsProjectionSchema,
  RawConfigSchema,
} from "../src/api/client.ts";

const consoleOffOp = { op: "admin_console_set", enabled: false } as const;
const consoleOnOp = { op: "admin_console_set", enabled: true } as const;
const consoleMarker = "<title>Jul.IA Console</title>";

async function currentBaseVersion(request: import("@playwright/test").APIRequestContext) {
  const response = await request.get("/api/config");
  expect(response.status()).toBe(200);
  return RawConfigSchema.parse(await response.json()).base_version ?? "";
}

async function rootServesConsole(
  request: import("@playwright/test").APIRequestContext,
): Promise<boolean> {
  const response = await request.get("/");
  if (response.status() !== 200) return false;
  return (await response.text()).includes(consoleMarker);
}

async function restoreConsole(request: import("@playwright/test").APIRequestContext): Promise<void> {
  const baseVersion = await currentBaseVersion(request);
  const response = await request.post("/api/config/patch/apply?confirm_admin=true", {
    headers: { "Content-Type": "application/json" },
    data: JSON.stringify({ base_version: baseVersion, ops: [consoleOnOp] }),
  });
  if (![200, 202, 204].includes(response.status())) {
    throw new Error(`failed to restore Console: ${String(response.status())} ${await response.text()}`);
  }
  await expect.poll(() => rootServesConsole(request)).toBe(true);
}

test.describe("HR-06B admin runtime", () => {
  test("GET /api/config/settings exposes the bounded live projection", async ({ request }) => {
    const response = await request.get("/api/config/settings");
    expect(response.status()).toBe(200);
    const settings = AdminRuntimeSettingsProjectionSchema.parse(await response.json());

    expect(settings.console).toBe(true);
    expect(settings.console_compiled).toBe(true);
    expect(settings.console_effective).toBe(true);
    expect(settings.plugin_upload_enabled).toBe(false);
    expect(settings.plugin_upload_max_size_mb).toBe(0);
    expect(settings.lifecycle.console?.class).toBe("hot_reload");
    expect(settings.lifecycle.plugin_upload_enabled?.class).toBe("hot_reload");
    expect(settings.lifecycle.plugin_upload_max_size?.class).toBe("hot_reload");
    expect(settings.lifecycle.plugin_upload_dir?.class).toBe("hot_reload");
  });

  test("browser can self-disable Console, keep API reachability, and re-enable through API", async ({
    page,
    request,
  }) => {
    // Always restore the shared real-server fixture, including when an
    // assertion after the successful disable fails.
    let disabled = false;
    try {
      await page.goto("/plugins");
      await expect(page.getByRole("heading", { name: "Plugins" })).toBeVisible();

      await page.getByRole("button", { name: "Runtime settings" }).click();
      const settingsDialog = page.getByRole("dialog", { name: "Admin runtime settings" });
      await expect(settingsDialog).toBeVisible();

      const consoleToggle = settingsDialog.getByLabel(
        "Serve the embedded Console on the existing admin listener",
      );
      await consoleToggle.uncheck();
      await expect(
        settingsDialog.getByText("Disabling the Console removes this web UI after the apply is terminal."),
      ).toBeVisible();

      const review = settingsDialog.getByRole("button", { name: "Review changes" });
      await expect(review).toBeDisabled();
      await settingsDialog.getByLabel("I understand how to re-enable the Console.").check();
      await expect(review).toBeEnabled();

      const previewRequest = page.waitForRequest("/api/config/patch/preview");
      await review.click();
      const preview = await previewRequest;
      expect(preview.postDataJSON()).toMatchObject({ ops: [consoleOffOp] });

      await expect(page).toHaveURL(/\/config$/);
      await expect(page.getByText("atomic patch", { exact: true })).toBeVisible();
      await expect(page.locator("code").filter({ hasText: /^admin\.console$/ })).toBeVisible();
      await expect(page.getByText("Hot apply: available", { exact: true })).toBeVisible();

      // First submission intentionally omits confirm_admin. The server's
      // reachability guard must reject it and leave the same dialog open with a
      // second, explicit confirmation. Nothing has been saved at this point.
      await page.getByRole("button", { name: "Apply live" }).click();
      const applyDialog = page.getByRole("dialog", { name: "Apply live?" });
      await expect(applyDialog).toBeVisible();

      const firstApply = page.waitForResponse(
        (response) => response.url().includes("/api/config/patch/apply") && response.request().method() === "POST",
      );
      await applyDialog.getByRole("button", { name: "Apply live" }).click();
      expect((await firstApply).status()).toBe(409);
      await expect(
        applyDialog.getByText("This edit changes how you reach the admin console."),
      ).toBeVisible();
      await expect(applyDialog.getByText("Nothing has been saved yet.")).toBeVisible();

      // The second click is the explicit self-lockout acknowledgement. The
      // already-loaded SPA remains alive long enough to receive the confirmed
      // response even though subsequent root requests switch to the fallback.
      const confirmedApply = page.waitForResponse(
        (response) => response.url().includes("/api/config/patch/apply?confirm_admin=true"),
      );
      await applyDialog.getByRole("button", { name: "Apply live" }).click();
      const confirmed = await confirmedApply;
      expect([200, 202]).toContain(confirmed.status());
      disabled = true;

      // The confirmed response may precede the async Publish result. Poll the
      // observable cutover rather than assuming a 200/202 response means the
      // next request has already crossed the generation boundary.
      await expect.poll(() => rootServesConsole(request)).toBe(false);

      // API reachability is independent from Console rendering.
      const overview = await request.get("/api/runtime/overview");
      expect(overview.status()).toBe(200);

      await restoreConsole(request);
      disabled = false;
      expect(await rootServesConsole(request)).toBe(true);
    } finally {
      if (disabled) await restoreConsole(request);
    }
  });
});
