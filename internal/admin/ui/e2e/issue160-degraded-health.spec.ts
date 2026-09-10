/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { expect, test, type Route } from "@playwright/test";

function json(route: Route, body: unknown, status = 200): Promise<void> {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
}

const hot = {
  class: "hot_reload",
  subsystem: "admin",
  reason: "runtime resource",
  conditional: false,
};

test("HR-07C degraded durable-sink health is understandable without raw filesystem detail", async ({
  page,
}) => {
  await page.route("/api/**", (route) => json(route, { error: "not found" }, 404));
  await page.route("/api/plugins", (route) => json(route, []));
  await page.route("/api/admin/health", (route) =>
    json(route, { healthy: false, reason: "audit_sink", detail: "durable audit sink is degraded" }),
  );
  await page.route("/api/config/settings", (route) =>
    json(route, {
      console: true,
      console_compiled: true,
      console_effective: true,
      plugin_upload_enabled: false,
      plugin_upload_max_size_mb: 32,
      plugin_upload_dir: "/operator/plugin-dir",
      plugin_upload_effective: false,
      upload_directory_health: "disabled",
      rate_limit_read_per_min: 240,
      rate_limit_write_per_min: 60,
      rate_limit_apply_per_min: 30,
      max_event_conns: 4,
      audit_log_file: "/operator/audit.jsonl",
      audit_log_rotate_max_mb: 100,
      audit_log_rotate_keep: 14,
      audit_sink: {
        configured: true,
        active: true,
        healthy: false,
        generation: 9,
        write_failures: 3,
        rotate_failures: 1,
        cleanup_failures: 0,
        retirement_failures: 0,
        last_failure_category: "write",
        last_failure_at: "2026-09-10T18:00:00Z",
      },
      lifecycle: {
        console: hot,
        plugin_upload_enabled: hot,
        plugin_upload_max_size: hot,
        plugin_upload_dir: hot,
        rate_limit_read_per_min: hot,
        rate_limit_write_per_min: hot,
        rate_limit_apply_per_min: hot,
        max_event_conns: hot,
        audit_log_file: hot,
        audit_log_rotate_max_mb: hot,
        audit_log_rotate_keep: hot,
      },
    }),
  );

  await page.goto("/plugins");
  await page.getByRole("button", { name: "Runtime settings" }).click();
  const dialog = page.getByRole("dialog", { name: "Admin runtime settings" });
  await expect(dialog.getByText(/Status: Degraded/)).toBeVisible();
  await expect(dialog.getByText(/generation 9/)).toBeVisible();
  await expect(dialog.getByText(/write/)).toBeVisible();
  await expect(dialog.getByText(/permission denied|no space left|errno|EACCES/i)).toHaveCount(0);
});
