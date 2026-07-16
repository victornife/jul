/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 *
 * Real-server E2E tests for the Jul.IA admin API (Finding C2).
 *
 * Architecture
 * ────────────
 * These tests run against a real `jul` binary (built with `-tags console`)
 * serving testdata/console-e2e.toml. They use Playwright's `request` fixture
 * rather than a browser because the goal is schema-drift detection: if a Go
 * projection renames a JSON field, the Zod schema in client.ts will throw a
 * ZodError when parsing the real response — catching drift that the mocked
 * smoke.spec.ts cannot catch.
 *
 * Prerequisites (handled by the `real-server` webServer in playwright.config.ts)
 * ────────────
 * 1. `go build -tags console -o jul ./cmd/jul` from the repo root.
 * 2. The jul binary must be started with testdata/console-e2e.toml before
 *    this spec runs — the webServer entry in playwright.config.ts does this.
 *
 * Token
 * ────────────
 * The admin token is "jul-e2e-test-token" (from testdata/console-e2e.toml).
 * It is set as a default header on all requests via the `real-server` project's
 * `extraHTTPHeaders` in playwright.config.ts.
 */

import { test, expect } from "@playwright/test";
import { z } from "zod";
import {
  OverviewSchema,
  RouteProjectionSchema,
  AppProjectionSchema,
  CertProjectionSchema,
  RawConfigSchema,
  ApplyResultSchema,
  HistoryEntrySchema,
  PluginsProjectionSchema,
  ValidationResultSchema,
} from "../src/api/client.ts";

// ── Helpers ───────────────────────────────────────────────────────────────────

/**
 * Asserts that `data` parses cleanly against `schema`. Throws a descriptive
 * error (including the raw response body) if validation fails so schema drift
 * is immediately actionable.
 */
function assertShape<T>(schema: z.ZodType<T>, data: unknown, endpoint: string): T {
  const result = schema.safeParse(data);
  if (!result.success) {
    throw new Error(
      `Schema drift on ${endpoint}:\n${result.error.toString()}\n\nRaw response:\n${JSON.stringify(data, null, 2)}`,
    );
  }
  return result.data;
}

// ── Overview ─────────────────────────────────────────────────────────────────

test("GET /api/runtime/overview matches OverviewSchema", async ({ request }) => {
  const resp = await request.get("/api/runtime/overview");
  expect(resp.status()).toBe(200);
  const data: unknown = await resp.json();
  const overview = assertShape(OverviewSchema, data, "/api/runtime/overview");
  // Basic sanity: product name and at least one feature status row present.
  expect(overview.product).toBe("Jul.IA");
  expect(overview.status.length).toBeGreaterThan(0);
});

// ── Routes ───────────────────────────────────────────────────────────────────

test("GET /api/routes matches RouteProjectionSchema[]", async ({ request }) => {
  const resp = await request.get("/api/routes");
  expect(resp.status()).toBe(200);
  const data: unknown = await resp.json();
  const routes = z.array(RouteProjectionSchema).safeParse(data);
  if (!routes.success) {
    throw new Error(
      `Schema drift on /api/routes:\n${routes.error.toString()}\n\nRaw:\n${JSON.stringify(data, null, 2)}`,
    );
  }
  // The e2e test config has one server block → at least one route.
  expect(routes.data.length).toBeGreaterThan(0);
});

// ── Apps (upstreams) ──────────────────────────────────────────────────────────

test("GET /api/apps matches AppProjectionSchema[]", async ({ request }) => {
  const resp = await request.get("/api/apps");
  expect(resp.status()).toBe(200);
  const data: unknown = await resp.json();
  // No upstreams in the e2e config → empty array is valid.
  const parsed = z.array(AppProjectionSchema).safeParse(data);
  if (!parsed.success) {
    throw new Error(
      `Schema drift on /api/apps:\n${parsed.error.toString()}\n\nRaw:\n${JSON.stringify(data, null, 2)}`,
    );
  }
});

// ── TLS certs ────────────────────────────────────────────────────────────────

test("GET /api/tls matches CertProjectionSchema[]", async ({ request }) => {
  const resp = await request.get("/api/tls");
  expect(resp.status()).toBe(200);
  const data: unknown = await resp.json();
  // No TLS in the e2e config → empty array.
  const parsed = z.array(CertProjectionSchema).safeParse(data);
  if (!parsed.success) {
    throw new Error(
      `Schema drift on /api/tls:\n${parsed.error.toString()}\n\nRaw:\n${JSON.stringify(data, null, 2)}`,
    );
  }
});

// ── Auth gate ─────────────────────────────────────────────────────────────────

test("GET /api/runtime/overview without token returns 401", async ({ request }) => {
  const resp = await request.get("/api/runtime/overview", {
    headers: { Authorization: "" },
  });
  expect(resp.status()).toBe(401);
});

// ── Health endpoints (no auth required) ──────────────────────────────────────

test("GET /healthz returns {status: ok}", async ({ request }) => {
  const resp = await request.get("/healthz", { headers: { Authorization: "" } });
  expect(resp.status()).toBe(200);
  const body: unknown = await resp.json();
  expect(body).toMatchObject({ status: "ok" });
});

// ── Raw config read-write path (mutation flows) ───────────────────────────────

test("GET /api/config matches RawConfigSchema and carries base_version", async ({ request }) => {
  const resp = await request.get("/api/config");
  expect(resp.status()).toBe(200);
  const data: unknown = await resp.json();
  const cfg = assertShape(RawConfigSchema, data, "/api/config");
  // base_version fingerprints the live config for optimistic concurrency.
  expect(typeof cfg.base_version).toBe("string");
  expect((cfg.base_version ?? "").length).toBeGreaterThan(0);
  // raw contains valid TOML starting with the global or server block.
  expect(typeof cfg.raw).toBe("string");
  expect(cfg.raw).toContain("servers");
});

test("POST /api/config/validate with current config returns ok=true", async ({ request }) => {
  // First read the current config.
  const cfgResp = await request.get("/api/config");
  expect(cfgResp.status()).toBe(200);
  const cfgData: unknown = await cfgResp.json();
  const cfg = RawConfigSchema.parse(cfgData);
  expect(cfg.raw).toBeTruthy();

  // Validate it — must be side-effect-free.
  const validateResp = await request.post("/api/config/validate", {
    headers: { "Content-Type": "application/toml" },
    data: cfg.raw,
  });
  expect(validateResp.status()).toBe(200);
  const vData: unknown = await validateResp.json();
  const result = assertShape(ValidationResultSchema, vData, "/api/config/validate");
  expect(result.ok).toBe(true);
});

test("POST /api/config/apply with stale base_version returns 409 conflict", async ({ request }) => {
  // Read the current raw config.
  const cfgResp = await request.get("/api/config");
  expect(cfgResp.status()).toBe(200);
  const cfgData: unknown = await cfgResp.json();
  const cfg = RawConfigSchema.parse(cfgData);

  // Apply with an obviously wrong base_version — server must reject with 409.
  const applyResp = await request.post("/api/config/apply?base_version=stale-intentionally-wrong", {
    headers: { "Content-Type": "application/toml" },
    data: cfg.raw,
  });
  expect(applyResp.status()).toBe(409);
  const body: unknown = await applyResp.json();
  // The 409 body carries restart_required or admin_change or a conflict message.
  expect(body).toBeTruthy();
});

test(
  "POST /api/config/apply adding [cache] block returns 409 restart_required",
  async ({ request }) => {
    // Read the current config — e2e config has no [cache] section.
    const cfgResp = await request.get("/api/config");
    expect(cfgResp.status()).toBe(200);
    const cfgData: unknown = await cfgResp.json();
    const cfg = RawConfigSchema.parse(cfgData);
    const baseVersion = cfg.base_version ?? "";

    // Append a cache block — this crosses CacheRestartRequired and should be
    // rejected without writing anything.
    const candidate = `${cfg.raw ?? ""}\n[cache]\nenabled = true\nmemory_max_size = "64MiB"\n`;

    const applyResp = await request.post(
      `${baseVersion ? `/api/config/apply?base_version=${encodeURIComponent(baseVersion)}` : "/api/config/apply"}`,
      {
        headers: { "Content-Type": "application/toml" },
        data: candidate,
      },
    );
    // The preflight must reject this with 409 restart_required.
    expect(applyResp.status()).toBe(409);
    const body = (await applyResp.json()) as Record<string, unknown>;
    expect(body.restart_required).toBe(true);
  },
);

test("GET /api/config/history returns HistoryEntrySchema[]", async ({ request }) => {
  const resp = await request.get("/api/config/history");
  expect(resp.status()).toBe(200);
  const data: unknown = await resp.json();
  // History may be empty on a fresh server start — that is valid.
  const result = z.array(HistoryEntrySchema).safeParse(data);
  if (!result.success) {
    throw new Error(
      `Schema drift on /api/config/history:\n${result.error.toString()}\n\nRaw:\n${JSON.stringify(data, null, 2)}`,
    );
  }
  // Each entry, if present, must carry an id and a time string.
  for (const entry of result.data) {
    expect(typeof entry.id).toBe("string");
    expect(typeof entry.time).toBe("string");
  }
});

test("GET /api/plugins shows upload_enabled=false for e2e config", async ({ request }) => {
  const resp = await request.get("/api/plugins");
  expect(resp.status()).toBe(200);
  const data: unknown = await resp.json();
  const plugins = assertShape(PluginsProjectionSchema, data, "/api/plugins");
  // The e2e config does not set plugin_upload_enabled = true, so the
  // secure-by-default parser sets it to false and the projection confirms it.
  expect(plugins.upload_enabled).toBe(false);
});
