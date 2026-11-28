/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { expect, test } from "@playwright/test";
import { RawConfigSchema } from "../src/api/client.ts";

const trafficURL = "http://127.0.0.1:9292/";

test("Console previews and publishes an application quota policy on a real Jul server", async ({
  page,
  request,
}) => {
  test.setTimeout(120_000);

  const configResponse = await request.get("/api/config");
  expect(configResponse.status()).toBe(200);
  const initialConfig = RawConfigSchema.parse(await configResponse.json());
  const originalRaw = initialConfig.raw ?? "";

  try {
    await page.goto("/traffic");
    await expect(page.getByRole("heading", { name: "Global & Traffic Controls" })).toBeVisible();
    const policyCard = page.getByText("Application quotas", { exact: true }).first().locator("..");
    const editButton = policyCard.getByRole("button", { name: "Edit" });
    await editButton.press("Enter");

    const editor = page.getByRole("dialog", { name: "Application quotas" });
    await expect(editor).toBeVisible();
    await editor.getByLabel("Policy name").fill("quota-e2e");
    await editor.getByLabel("Application requests per second").fill("1");
    await editor.getByLabel("Application burst").fill("1");
    await editor.getByLabel("Shared rate").fill("1");
    await editor.getByLabel("Shared burst").fill("1");
    await editor.getByLabel("Per-caller rate").fill("1");
    await editor.getByLabel("Per-caller burst").fill("1");

    await editor.getByLabel("Route").selectOption({
      label: "127.0.0.1:9292 — localhost — prefix /",
    });
    await editor.getByLabel("Application policy").selectOption("quota-e2e");

    const previewResponse = page.waitForResponse(
      (response) =>
        response.url().includes("/api/config/patch/preview") &&
        response.request().method() === "POST",
    );
    await editor.getByRole("button", { name: "Review changes" }).press("Enter");
    const preview = await previewResponse;
    expect(preview.status()).toBe(200);
    await expect(page).toHaveURL(/\/config$/);

    const applyButton = page.getByRole("button", { name: "Apply live", exact: true });
    await expect(applyButton).toBeEnabled();
    await applyButton.press("Enter");
    const confirm = page.getByRole("dialog", { name: "Apply live?" });
    await expect(confirm).toBeVisible();
    const acknowledge = confirm.getByRole("checkbox");
    if ((await acknowledge.count()) > 0) await acknowledge.check();

    const applyResponse = page.waitForResponse(
      (response) =>
        response.url().includes("/api/config/patch/apply") &&
        response.request().method() === "POST",
    );
    await confirm.getByRole("button", { name: "Apply live", exact: true }).press("Enter");
    const applied = await applyResponse;
    expect(applied.status()).toBe(200);

    const routesResponse = await request.get("/api/routes");
    expect(routesResponse.status()).toBe(200);
    const routes: Array<{ listen: string; locations: Array<Record<string, unknown>> }> =
      await routesResponse.json();
    const rootRoute = routes
      .find((route) => route.listen === "127.0.0.1:9292")
      ?.locations.find((location) => location.match === "/" && location.type === "prefix");
    expect(rootRoute?.quota_policy).toBe("quota-e2e");

    const first = await request.get(trafficURL, { headers: { Authorization: "", Host: "localhost" } });
    expect(first.status()).toBe(200);
    const second = await request.get(trafficURL, { headers: { Authorization: "", Host: "localhost" } });
    expect(second.status()).toBe(429);
    expect(second.headers()["retry-after"]).toMatch(/^[1-9]\d*$/);

    await page.goto("/traffic");
    await expect(page.getByText(/quota-e2e · enforce · 1\/s/)).toBeVisible();
  } finally {
    const currentResponse = await request.get("/api/config");
    expect(currentResponse.status()).toBe(200);
    const current = RawConfigSchema.parse(await currentResponse.json());
    if (current.raw !== originalRaw) {
      const version = current.base_version ?? "";
      const restore = await request.post(
        version ? `/api/config/apply?base_version=${encodeURIComponent(version)}` : "/api/config/apply",
        {
          headers: { "Content-Type": "application/toml" },
          data: originalRaw,
        },
      );
      expect(restore.status(), await restore.text()).toBe(200);
    }
  }
});
