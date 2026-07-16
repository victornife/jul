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
