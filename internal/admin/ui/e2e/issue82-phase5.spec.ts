/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { expect, test } from "@playwright/test";
import { z } from "zod";
import { HistoryEntrySchema, RawConfigSchema } from "../src/api/client.ts";

const trafficURL = "http://127.0.0.1:9292/";

async function expectStaticOK(request: any, expected: string): Promise<void> {
  const response = await request.get(trafficURL, { headers: { Authorization: "" } });
  expect(response.status()).toBe(200);
  expect(await response.text()).toContain(expected);
}

test(
  "Phase 5 typed Console closure: create, prove data plane, stage/update/discard, delete, rollback",
  async ({ page, request }) => {
    test.setTimeout(180_000);

    const appName = "issue82-phase5-e2e";
    const newListener = "127.0.0.1:9294";
    const newTrafficURL = "http://127.0.0.1:9294/";

    const rawBeforeResp = await request.get("/api/config");
    expect(rawBeforeResp.status()).toBe(200);
    const rawBefore = RawConfigSchema.parse(await rawBeforeResp.json());
    const initialRaw = rawBefore.raw ?? "";

    const historyBeforeResp = await request.get("/api/config/history");
    expect(historyBeforeResp.status()).toBe(200);
    const historyBefore = z.array(HistoryEntrySchema).parse(await historyBeforeResp.json());
    const previousHistoryIDs = new Set(historyBefore.map((entry) => entry.id));

    async function applyConfigAction(label: string): Promise<void> {
      const action = page.getByRole("button", { name: label, exact: true });
      await expect(action).toBeVisible();
      await expect(action).toBeEnabled();
      await action.click();
      const dialog = page.getByRole("dialog", { name: `${label}?` });
      await expect(dialog).toBeVisible();
      const responsePromise = page.waitForResponse(
        (response) =>
          response.url().includes("/api/config/patch/apply") &&
          response.request().method() === "POST",
      );
      await dialog.getByRole("button", { name: label, exact: true }).click();
      const response = await responsePromise;
      expect(response.status()).toBe(200);
      await expect(dialog).not.toBeVisible({ timeout: 20_000 });
    }

    async function openTrafficEditor(cardTitle: string, dialogTitle: string) {
      await page.goto("/traffic");
      await expect(page.getByRole("heading", { name: "Global & Traffic Controls" })).toBeVisible();
      const heading = page.getByText(cardTitle, { exact: true }).first();
      const header = heading.locator("..");
      await header.getByRole("button", { name: "Edit" }).click();
      const dialog = page.getByRole("dialog", { name: dialogTitle });
      await expect(dialog).toBeVisible();
      return dialog;
    }

    async function expectNewListenerServing(): Promise<void> {
      await expect
        .poll(
          async () => {
            try {
              const response = await request.get(newTrafficURL, {
                headers: { Authorization: "", Host: "localhost" },
              });
              return response.status();
            } catch {
              return 0;
            }
          },
          { timeout: 20_000 },
        )
        .toBe(200);
      const response = await request.get(newTrafficURL, {
        headers: { Authorization: "", Host: "localhost" },
      });
      expect(await response.text()).toContain("Jul static OK");
    }

    await page.goto("/apps");
    await page.getByRole("button", { name: "New app", exact: true }).first().click();
    const appEditor = page.getByRole("dialog", { name: "New app / upstream" });
    await appEditor.getByLabel("App / upstream name").fill(appName);
    await appEditor.getByLabel("Address 1").fill("127.0.0.1:9292");
    await appEditor.getByRole("radio", { name: /New exact server/ }).check();
    await appEditor.getByLabel("Listener").fill(newListener);
    await appEditor.getByLabel("Server names").fill("localhost");
    await appEditor.getByLabel("Route path").fill("/");
    await appEditor.getByRole("button", { name: /Review batch in editor/ }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Apply live");
    await expectNewListenerServing();

    let baselineSnapshotID = "";
    await expect
      .poll(
        async () => {
          const response = await request.get("/api/config/history");
          if (response.status() !== 200) return "";
          const entries = z.array(HistoryEntrySchema).parse(await response.json());
          baselineSnapshotID = entries.find((entry) => !previousHistoryIDs.has(entry.id))?.id ?? "";
          return baselineSnapshotID;
        },
        { timeout: 20_000 },
      )
      .not.toBe("");

    const compression = await openTrafficEditor("Compression", "Edit compression");
    const compressionEnabled = compression.getByRole("checkbox", { name: "Enable compression" });
    if (!(await compressionEnabled.isChecked())) await compressionEnabled.check();
    // A zero Size is the configuration sentinel for the 1 KiB default after
    // authoritative serialize/reparse. Use an explicit 1-byte threshold so
    // this small fixture actually exercises the gzip data-plane path.
    await compression.getByLabel("Minimum response size").fill("1");
    await compression.getByRole("button", { name: "Review changes" }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Apply live");
    const compressed = await request.get(trafficURL, {
      headers: { Authorization: "", Host: "localhost", "Accept-Encoding": "gzip" },
    });
    expect(compressed.status()).toBe(200);
    expect(compressed.headers()["content-encoding"]).toBe("gzip");

    const limiter = await openTrafficEditor("Rate limiting", "Edit rate limiting");
    const limiterEnabled = limiter.getByRole("checkbox", { name: "Enable global rate limiting" });
    if (!(await limiterEnabled.isChecked())) await limiterEnabled.check();
    await limiter.getByLabel("Key").fill("ip");
    await limiter.getByLabel("Requests per second").fill("1");
    await limiter.getByLabel("Burst").fill("1");
    await limiter.getByRole("button", { name: "Review changes" }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Apply live");

    const statuses: number[] = [];
    for (let index = 0; index < 4; index += 1) {
      const limited = await request.get(trafficURL, {
        headers: { Authorization: "", Host: "localhost" },
      });
      statuses.push(limited.status());
    }
    expect(statuses).toContain(429);

    const limiterOff = await openTrafficEditor("Rate limiting", "Edit rate limiting");
    const limiterOffToggle = limiterOff.getByRole("checkbox", { name: "Enable global rate limiting" });
    if (await limiterOffToggle.isChecked()) await limiterOffToggle.uncheck();
    await limiterOff.getByRole("button", { name: "Review changes" }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Apply live");

    const preStageResp = await request.get("/api/config");
    expect(preStageResp.status()).toBe(200);
    const preStage = RawConfigSchema.parse(await preStageResp.json());
    const preStageRaw = preStage.raw ?? "";

    const globalEditor = await openTrafficEditor("Global settings", "Edit global settings");
    const logFormat = globalEditor.getByLabel("Log format");
    const currentFormat = await logFormat.inputValue();
    await logFormat.selectOption(currentFormat === "json" ? "text" : "json");
    await globalEditor.getByRole("button", { name: "Review changes" }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Save for next restart");
    await expect(page.getByText("Restart required — configuration staged")).toBeVisible();
    await expectStaticOK(request, "Jul static OK");

    const stagedLimiter = await openTrafficEditor("Rate limiting", "Edit rate limiting");
    const maxConns = stagedLimiter.getByLabel("Maximum concurrent connections");
    const currentMax = Number(await maxConns.inputValue()) || 0;
    await maxConns.fill(String(currentMax + 17));
    await stagedLimiter.getByRole("button", { name: "Review changes" }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Update staged configuration");
    await expect(page.getByText("Restart required — configuration staged")).toBeVisible();

    await page.getByRole("button", { name: "Discard staged configuration" }).click();
    await expect(page.getByText("Restart required — configuration staged")).not.toBeVisible({ timeout: 20_000 });
    await expect
      .poll(
        async () => {
          const response = await request.get("/api/config");
          if (response.status() !== 200) return "";
          return RawConfigSchema.parse(await response.json()).raw ?? "";
        },
        { timeout: 20_000 },
      )
      .toBe(preStageRaw);
    await expectStaticOK(request, "Jul static OK");

    // App creation intentionally stores a one-shot reopen selection so the
    // operator lands back on the exact created App after apply. Reuse that
    // authoritative restored drawer instead of trying to click through it.
    await page.goto("/apps");
    const appDetail = page.getByRole("dialog", { name: appName });
    await expect(appDetail).toBeVisible();
    await expect(appDetail).toContainText("Delete is blocked because 1 projected route still references this App");
    await expect(appDetail.getByRole("button", { name: "Delete App / upstream…" })).toBeDisabled();

    await page.goto("/routes");
    const listenerLabel = page.getByText(newListener, { exact: true }).first();
    const routeCard = listenerLabel.locator("xpath=ancestor::div[contains(@class,'rounded-lg')][1]");
    await routeCard.locator("tbody tr").first().click();
    const routeDetail = page.getByRole("dialog", { name: "prefix /" });
    await routeDetail.getByRole("button", { name: /Delete route prefix \/ from/ }).click();
    const routeDelete = page.getByRole("dialog", { name: "Remove this exact route?" });
    await routeDelete.getByRole("button", { name: "Hand off deletion for apply review" }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Apply live");

    await page.goto("/apps");
    await page.getByRole("button", { name: `Open App ${appName}` }).click();
    const deletableApp = page.getByRole("dialog", { name: appName });
    await deletableApp.getByRole("button", { name: "Delete App / upstream…" }).click();
    const appDelete = page.getByRole("dialog", { name: `Remove App/upstream ${appName}?` });
    await appDelete.getByRole("button", { name: "Hand off deletion for apply review" }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Apply live");

    await page.goto("/history");
    const historyRow = page.getByText(baselineSnapshotID, { exact: true }).locator("xpath=ancestor::tr");
    await historyRow.getByRole("button", { name: "Rollback" }).click();
    const rollbackDialog = page.getByRole("dialog", { name: "Roll back to this snapshot?" });
    await expect(rollbackDialog).toBeVisible();
    const rollbackButton = rollbackDialog.getByRole("button", { name: "Roll back", exact: true });
    // Rollback confirmation is intentionally gated on the async server-side
    // diff preview. Wait for that reviewed baseline, and prove the action
    // remains physically reachable even when the diff is taller than the viewport.
    await expect(rollbackButton).toBeEnabled({ timeout: 20_000 });
    await expect(rollbackButton).toBeInViewport();
    const rollbackResponse = page.waitForResponse(
      (response) =>
        response.url().includes("/api/config/rollback") && response.request().method() === "POST",
    );
    await rollbackButton.click();
    const rollback = await rollbackResponse;
    expect([200, 204]).toContain(rollback.status());

    await expect
      .poll(
        async () => {
          const response = await request.get("/api/config");
          if (response.status() !== 200) return "";
          return RawConfigSchema.parse(await response.json()).raw ?? "";
        },
        { timeout: 30_000 },
      )
      .toBe(initialRaw);
    await expectStaticOK(request, "Jul static OK");
  },
);
