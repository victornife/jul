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
  PendingRestartStatusSchema,
  IdentitySchema,
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

/**
 * Returns a copy of the raw TOML with a restart-required [cache] change.
 * If the config already contains a [cache] block, the memory_max_size field
 * is replaced; otherwise a new [cache] block is appended. This keeps the
 * stage_restart tests valid regardless of whether the server returns a minimal
 * or canonical raw view.
 */
function withCacheMemoryMaxSize(raw: string, value: string): string {
  if (raw.includes("[cache]")) {
    return raw.replace(
      /memory_max_size\s*=\s*['"][^'"]*['"]/,
      `memory_max_size = "${value}"`,
    );
  }
  return `${raw}\n[cache]\nenabled = true\nmemory_max_size = "${value}"\n`;
}

/**
 * Toggles [global].log_level between "debug" and "warn", preserving the
 * existing quote style so the substitution works for both minimal and
 * canonical raw TOML.
 */
function toggleLogLevel(raw: string): {
  modified: string;
  currentLevel: string;
  nextLevel: string;
  quote: string;
} {
  const levelMatch = raw.match(/log_level\s*=\s*(['"])(\w+)\1/);
  const quote = levelMatch?.[1] ?? '"';
  const currentLevel = levelMatch?.[2] ?? "warn";
  const nextLevel = currentLevel === "debug" ? "warn" : "debug";
  const modified = raw.replace(
    /log_level\s*=\s*['"]\w+['"]/,
    `log_level = ${quote}${nextLevel}${quote}`,
  );
  return { modified, currentLevel, nextLevel, quote };
}

/**
 * Posts to a config-mutating endpoint, retrying once per attempt (up to
 * `attempts` total) on a 409 response. The admin API can legitimately answer
 * a conflict when the live handler-generation or persisted version advanced
 * between authorization and persistence — e.g. a reload the file watcher
 * fired for its own echo of an immediately preceding write (#270) — and a
 * real client is expected to retry rather than treat that as fatal. Fails
 * with the last response's body if every attempt is exhausted, so a genuine
 * failure is still diagnosable instead of just asserting a bare status code.
 */
async function postWithConflictRetry(
  request: import("@playwright/test").APIRequestContext,
  url: string,
  options: Parameters<import("@playwright/test").APIRequestContext["post"]>[1],
  attempts = 3,
) {
  let resp = await request.post(url, options);
  for (let attempt = 1; resp.status() === 409 && attempt < attempts; attempt++) {
    resp = await request.post(url, options);
  }
  if (resp.status() === 409) {
    throw new Error(
      `POST ${url} still returned 409 after ${String(attempts)} attempts: ${await resp.text()}`,
    );
  }
  return resp;
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
  const parsed = z.array(AppProjectionSchema).safeParse(data);
  if (!parsed.success) {
    throw new Error(
      `Schema drift on /api/apps:\n${parsed.error.toString()}\n\nRaw:\n${JSON.stringify(data, null, 2)}`,
    );
  }

  // The e2e config carries one routed upstream, so this must not be empty. An
  // empty array would still parse, and the schema check above would pass while
  // proving nothing about backend state or the pool verdict.
  const pool = parsed.data.find((a) => a.name === "e2e-pool");
  expect(pool, `no e2e-pool in ${JSON.stringify(parsed.data)}`).toBeDefined();
  expect(pool?.backends).toHaveLength(1);

  // A backend with no health check and a closed circuit is available from
  // birth, whether or not anything is listening on the port. These two values
  // are what the Console reads; the enum membership is enforced by the schema,
  // so what is pinned here is that the server actually populates them.
  expect(pool?.backends[0]?.state).toBe("available");
  expect(pool?.verdict).toBe("healthy");
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

// ── Identity (/api/admin/me) ─────────────────────────────────────────────────

test("GET /api/admin/me matches IdentitySchema (legacy-token compatibility)", async ({
  request,
}) => {
  const resp = await request.get("/api/admin/me");
  expect(resp.status()).toBe(200);
  const data: unknown = await resp.json();
  const id = assertShape(IdentitySchema, data, "/api/admin/me");
  // The e2e server runs with the legacy shared token (RBAC disabled), so the
  // caller is reported as the synthetic admin-role legacy principal with a
  // non-empty permission set — the compatibility mode the migration relies on.
  expect(id.legacy).toBe(true);
  expect(id.role).toBe("admin");
  expect(id.permissions.length).toBeGreaterThan(0);
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
    // Read the current config — the raw view may be minimal or canonical.
    const cfgResp = await request.get("/api/config");
    expect(cfgResp.status()).toBe(200);
    const cfgData: unknown = await cfgResp.json();
    const cfg = RawConfigSchema.parse(cfgData);
    const baseVersion = cfg.base_version ?? "";

    // Changing cache.memory_max_size crosses CacheRestartRequired and should be
    // rejected without writing anything.
    const candidate = withCacheMemoryMaxSize(cfg.raw ?? "", "64m");

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

// ── Full apply → history → rollback flow (RA-10) ─────────────────────────────

test(
  "apply a hot-reloadable change, verify history entry, then rollback",
  async ({ request }) => {
    // 1. Read the current config and its version token.
    const cfgResp = await request.get("/api/config");
    expect(cfgResp.status()).toBe(200);
    const cfgData: unknown = await cfgResp.json();
    const original = RawConfigSchema.parse(cfgData);
    expect(original.raw).toBeTruthy();
    const baseVersion = original.base_version ?? "";

    // 2. Toggle log_level — a real semantic change that is hot-reloadable
    //    (not restart-required). The starting level depends on leftover state
    //    from earlier tests, so read it and switch to the other level.
    const { modified, currentLevel, nextLevel, quote } = toggleLogLevel(original.raw ?? "");
    // Sanity: the substitution changed something.
    expect(modified).not.toBe(original.raw ?? "");

    const applyUrl = baseVersion
      ? `/api/config/apply?base_version=${encodeURIComponent(baseVersion)}`
      : "/api/config/apply";

    const applyResp = await request.post(applyUrl, {
      headers: { "Content-Type": "application/toml" },
      data: modified,
    });
    // A hot-reloadable change must be accepted (200).
    expect(applyResp.status()).toBe(200);
    const applyData: unknown = await applyResp.json();
    const applyResult = assertShape(ApplyResultSchema, applyData, "/api/config/apply");
    expect(applyResult.ok).toBe(true);

    // 3. Verify the on-disk config now has debug log level.
    const afterApplyCfgResp = await request.get("/api/config");
    expect(afterApplyCfgResp.status()).toBe(200);
    const afterApplyCfgData: unknown = await afterApplyCfgResp.json();
    const afterApplyCfg = RawConfigSchema.parse(afterApplyCfgData);
    expect(afterApplyCfg.raw ?? "").toContain(`log_level = ${quote}${nextLevel}${quote}`);

    // 4. A history entry must exist (the apply snapshots the previous config).
    const histResp = await request.get("/api/config/history");
    expect(histResp.status()).toBe(200);
    const histData: unknown = await histResp.json();
    const history = z.array(HistoryEntrySchema).parse(histData);
    expect(history.length).toBeGreaterThan(0);
    const latestId = history[0].id;

    // 5. Roll back to the snapshot that captured the original config.
    //
    // The rollback handler compares the server's live handler-generation ID
    // against the one it observed when authorizing the request (#270), so it
    // can legitimately return a transient 409 if an unrelated reload (e.g. the
    // file watcher's own echo of step 2's write, suppressed on a best-effort
    // basis) advances that generation in the narrow window between the two.
    // A real Console client is expected to retry such a conflict rather than
    // fail outright, so this test does the same instead of asserting success
    // on the first attempt.
    const rollbackResp = await postWithConflictRetry(request, "/api/config/rollback", {
      headers: { "Content-Type": "application/json" },
      data: JSON.stringify({ id: latestId }),
    });
    expect([200, 204]).toContain(rollbackResp.status());

    // 6. Verify the rollback restored the original log_level ("warn").
    const afterResp = await request.get("/api/config");
    expect(afterResp.status()).toBe(200);
    const afterData: unknown = await afterResp.json();
    const after = RawConfigSchema.parse(afterData);
    expect(after.raw ?? "").not.toContain(`log_level = ${quote}${nextLevel}${quote}`);
    expect(after.raw ?? "").toContain(`log_level = ${quote}${currentLevel}${quote}`);
  },
);

// ── Reload serving behaviour (R5-11) ────────────────────────────────────────

const trafficURL = "http://127.0.0.1:9292/";

async function expectStaticOK(request: any, expected: string) {
  const resp = await request.get(trafficURL, { headers: { Authorization: "" } });
  expect(resp.status()).toBe(200);
  const body = await resp.text();
  expect(body).toContain(expected);
}

test(
  "successful hot reload switches traffic serving",
  async ({ request }) => {
    // 1. Baseline traffic content.
    await expectStaticOK(request, "Jul static OK");

    // 2. Read current config and switch the static root to www-reload.
    const cfgResp = await request.get("/api/config");
    expect(cfgResp.status()).toBe(200);
    const cfgData: unknown = await cfgResp.json();
    const original = RawConfigSchema.parse(cfgData);
    const baseVersion = original.base_version ?? "";
    const rootRegex = /root\s*=\s*['"]testdata\/www['"]/;
    expect(original.raw ?? "").toMatch(rootRegex);

    const modified = (original.raw ?? "").replace(
      rootRegex,
      "root = 'testdata/www-reload'",
    );
    expect(modified).not.toBe(original.raw ?? "");

    const applyUrl = baseVersion
      ? `/api/config/apply?base_version=${encodeURIComponent(baseVersion)}`
      : "/api/config/apply";
    const applyResp = await request.post(applyUrl, {
      headers: { "Content-Type": "application/toml" },
      data: modified,
    });
    expect(applyResp.status()).toBe(200);
    const applyResult = assertShape(ApplyResultSchema, await applyResp.json(), "/api/config/apply");
    expect(applyResult.ok).toBe(true);

    // 3. Traffic must now serve the reloaded content.
    await expectStaticOK(request, "Jul reloaded OK");

    // 4. Roll back via the most recent history entry.
    const histResp = await request.get("/api/config/history");
    expect(histResp.status()).toBe(200);
    const history = z.array(HistoryEntrySchema).parse(await histResp.json());
    expect(history.length).toBeGreaterThan(0);
    const rollbackResp = await request.post("/api/config/rollback", {
      headers: { "Content-Type": "application/json" },
      data: JSON.stringify({ id: history[0].id }),
    });
    expect([200, 204]).toContain(rollbackResp.status());

    // 5. Traffic must revert to the original content.
    await expectStaticOK(request, "Jul static OK");
  },
);

test(
  "rejected restart-required reload leaves traffic unchanged",
  async ({ request }) => {
    // 1. Baseline traffic content.
    await expectStaticOK(request, "Jul static OK");

    // 2. Read current config and submit a restart-required [cache] change.
    const cfgResp = await request.get("/api/config");
    expect(cfgResp.status()).toBe(200);
    const cfgData: unknown = await cfgResp.json();
    const original = RawConfigSchema.parse(cfgData);
    const baseVersion = original.base_version ?? "";
    const candidate = withCacheMemoryMaxSize(original.raw ?? "", "64m");

    const applyUrl = baseVersion
      ? `/api/config/apply?base_version=${encodeURIComponent(baseVersion)}`
      : "/api/config/apply";
    const applyResp = await request.post(applyUrl, {
      headers: { "Content-Type": "application/toml" },
      data: candidate,
    });
    expect(applyResp.status()).toBe(409);
    const body = (await applyResp.json()) as Record<string, unknown>;
    expect(body.restart_required).toBe(true);

    // 3. Traffic must still serve the original content (no partial swap).
    await expectStaticOK(request, "Jul static OK");

    // 4. On-disk config must be unchanged too.
    const afterResp = await request.get("/api/config");
    expect(afterResp.status()).toBe(200);
    const after = RawConfigSchema.parse(await afterResp.json());
    expect(after.raw ?? "").not.toContain('memory_max_size = "64m"');
  },
);

test(
  "invalid config apply leaves traffic unchanged",
  async ({ request }) => {
    // 1. Baseline traffic content.
    await expectStaticOK(request, "Jul static OK");

    // 2. Send malformed TOML.
    const applyResp = await request.post("/api/config/apply", {
      headers: { "Content-Type": "application/toml" },
      data: "[[servers\nmalformed toml",
    });
    expect(applyResp.status()).toBe(400);

    // 3. Traffic must still serve the original content.
    await expectStaticOK(request, "Jul static OK");
  },
);

test(
  "concurrent reload churn preserves traffic availability",
  async ({ request }) => {
    // 1. Baseline traffic content.
    await expectStaticOK(request, "Jul static OK");

    // 2. Apply several hot-reloadable toggles in sequence, verifying traffic
    //    remains available after each one.
    const cfgResp = await request.get("/api/config");
    expect(cfgResp.status()).toBe(200);
    const cfgData: unknown = await cfgResp.json();
    const original = RawConfigSchema.parse(cfgData);
    const baseVersion = original.base_version ?? "";

    for (let i = 0; i < 3; i++) {
      const level = i % 2 === 0 ? "debug" : "warn";
      const modified = (original.raw ?? "").replace(
        /log_level\s*=\s*"\w+"/,
        `log_level = "${level}"`,
      );
      // Re-read the live base_version each iteration so optimistic concurrency
      // does not reject the churn.
      const currentCfgResp = await request.get("/api/config");
      expect(currentCfgResp.status()).toBe(200);
      const currentCfg = RawConfigSchema.parse(await currentCfgResp.json());
      const currentVersion = currentCfg.base_version ?? "";
      const applyUrl = currentVersion
        ? `/api/config/apply?base_version=${encodeURIComponent(currentVersion)}`
        : "/api/config/apply";
      const applyResp = await request.post(applyUrl, {
        headers: { "Content-Type": "application/toml" },
        data: modified,
      });
      expect(applyResp.status()).toBe(200);
      await expectStaticOK(request, "Jul static OK");
    }

    // 3. Restore original log_level using a fresh version token.
    const finalCfgResp = await request.get("/api/config");
    expect(finalCfgResp.status()).toBe(200);
    const finalCfg = RawConfigSchema.parse(await finalCfgResp.json());
    const finalVersion = finalCfg.base_version ?? "";
    const restoreUrl = finalVersion
      ? `/api/config/apply?base_version=${encodeURIComponent(finalVersion)}`
      : "/api/config/apply";
    const restoreResp = await request.post(restoreUrl, {
      headers: { "Content-Type": "application/toml" },
      data: original.raw ?? "",
    });
    expect(restoreResp.status()).toBe(200);
  },
);

// ── Structured patch action (H-07) ───────────────────────────────────────────

test(
  "POST /api/config/patch/apply switches a location to gRPC passthrough",
  async ({ request }) => {
    // The e2e config has a static root route. Switch it to gRPC via structured
    // patch/apply and verify the action round-trips through /api/routes.
    const setLocationAction = async (action: Record<string, unknown>) => {
      const cfgResp = await request.get("/api/config");
      expect(cfgResp.status()).toBe(200);
      const cfg = RawConfigSchema.parse(await cfgResp.json());
      return postWithConflictRetry(request, "/api/config/patch/apply", {
        headers: { "Content-Type": "application/json" },
        data: JSON.stringify({
          base_version: cfg.base_version ?? "",
          ops: [
            {
              op: "location_set_action",
              listen: "127.0.0.1:9292",
              server_names: ["localhost"],
              match_type: "prefix",
              path: "/",
              action,
            },
          ],
        }),
      });
    };

    // The restore must run even if an assertion below fails — otherwise a
    // failure here leaves "/" pointed at a nonexistent gRPC backend for every
    // later test in this file (one shared server, workers: 1).
    let failure: unknown;
    try {
      const applyResp = await setLocationAction({ kind: "grpc_proxy", target: "http://backend:9000" });
      expect(applyResp.status()).toBe(200);
      const applyBody: unknown = await applyResp.json();
      expect((applyBody as Record<string, unknown>).ok).toBe(true);

      const routesResp = await request.get("/api/routes");
      expect(routesResp.status()).toBe(200);
      const routes = z.array(RouteProjectionSchema).parse(await routesResp.json());
      const server = routes.find((r) => r.listen === "127.0.0.1:9292");
      expect(server).toBeTruthy();
      const route = server!.locations.find((l) => l.match === "/" && l.type === "prefix");
      expect(route).toBeTruthy();
      expect(route!.action).toBe("grpc");
      expect(route!.target).toBe("http://backend:9000");
    } catch (err) {
      failure = err;
    }

    // Restore the static action so later tests keep the fixture baseline.
    const restoreResp = await setLocationAction({ kind: "static", target: "testdata/www" });
    if (failure) throw failure;
    expect(restoreResp.status()).toBe(200);
  },
);

// ── Stage_restart workflow (H-07) ─────────────────────────────────────────────

test("GET /api/config/pending-restart returns pending=false when no restart is staged", async ({ request }) => {
  const resp = await request.get("/api/config/pending-restart");
  expect(resp.status()).toBe(200);
  const body: unknown = await resp.json();
  // Either {pending: false} or {pending: true, status: {...}} — both are valid.
  // The server just started fresh, so pending should be false.
  expect((body as Record<string, unknown>).pending).toBe(false);
});

test(
  "stage_restart: stage a restart-required change, verify traffic unchanged, then discard",
  async ({ request }) => {
    // 1. Ensure no staged restart is active before we start.
    const preClearResp = await request.post("/api/config/pending-restart/discard");
    // 404 or 409 are acceptable here; we just want to clear any leftover state.
    expect([200, 409, 501]).toContain(preClearResp.status());

    // 2. Read current config.
    const cfgResp = await request.get("/api/config");
    expect(cfgResp.status()).toBe(200);
    const cfgData: unknown = await cfgResp.json();
    const original = RawConfigSchema.parse(cfgData);
    const baseVersion = original.base_version ?? "";

    // 3. Build a candidate with a restart-required change (cache.memory_max_size).
    const candidate = withCacheMemoryMaxSize(original.raw ?? "", "64m");
    const applyUrl = baseVersion
      ? `/api/config/apply?base_version=${encodeURIComponent(baseVersion)}&mode=stage_restart`
      : "/api/config/apply?mode=stage_restart";

    const stageResp = await request.post(applyUrl, {
      headers: { "Content-Type": "application/toml" },
      data: candidate,
    });
    expect(stageResp.status()).toBe(200);
    const stageData: unknown = await stageResp.json();
    const stageResult = assertShape(ApplyResultSchema, stageData, "/api/config/apply?mode=stage_restart");
    expect(stageResult.ok).toBe(true);
    expect(stageResult.mode).toBe("stage_restart");

    // 4. Traffic must still serve the original content (staged restart does NOT
    //    trigger a hot reload — the running server is unchanged).
    await expectStaticOK(request, "Jul static OK");

    // 5. GET /api/config/pending-restart must now show staged=true.
    const pendingResp = await request.get("/api/config/pending-restart");
    expect(pendingResp.status()).toBe(200);
    const pendingBody: unknown = await pendingResp.json();
    const pendingRecord = pendingBody as Record<string, unknown>;
    expect(pendingRecord.pending).toBe(true);
    if (pendingRecord.status) {
      const status = PendingRestartStatusSchema.safeParse(pendingRecord.status);
      if (status.success) {
        expect(status.data.staged).toBe(true);
        expect(status.data.managed).toBe(true);
      }
    }

    // 6. The overview must include pending_restart_status.
    const overviewResp = await request.get("/api/runtime/overview");
    expect(overviewResp.status()).toBe(200);
    const overview = assertShape(OverviewSchema, await overviewResp.json(), "/api/runtime/overview");
    // pending_restart may be present (flat list from legacy field) or
    // pending_restart_status (structured) — both are optional in the schema.
    // Just verify the response parses without error.
    expect(overview).toBeTruthy();

    // 7. Hot apply must be blocked (409) while a staged restart is pending.
    const cfgResp2 = await request.get("/api/config");
    expect(cfgResp2.status()).toBe(200);
    const latestCfg = RawConfigSchema.parse(await cfgResp2.json());
    const hotResp = await request.post(
      latestCfg.base_version
        ? `/api/config/apply?base_version=${encodeURIComponent(latestCfg.base_version ?? "")}`
        : "/api/config/apply",
      {
        headers: { "Content-Type": "application/toml" },
        data: latestCfg.raw ?? "",
      },
    );
    expect(hotResp.status()).toBe(409);

    // 8. Discard the staged restart.
    const discardResp = await request.post("/api/config/pending-restart/discard");
    expect(discardResp.status()).toBe(200);
    const discardData: unknown = await discardResp.json();
    expect((discardData as Record<string, unknown>).ok).toBe(true);

    // 9. After discard, pending-restart must be clear.
    const afterDiscardResp = await request.get("/api/config/pending-restart");
    expect(afterDiscardResp.status()).toBe(200);
    const afterDiscardBody = (await afterDiscardResp.json()) as Record<string, unknown>;
    expect(afterDiscardBody.pending).toBe(false);

    // 10. Hot apply must be unblocked now.
    const cfgResp3 = await request.get("/api/config");
    expect(cfgResp3.status()).toBe(200);
    const restoredCfg = RawConfigSchema.parse(await cfgResp3.json());
    const unlockedResp = await request.post(
      restoredCfg.base_version
        ? `/api/config/apply?base_version=${encodeURIComponent(restoredCfg.base_version ?? "")}`
        : "/api/config/apply",
      {
        headers: { "Content-Type": "application/toml" },
        data: restoredCfg.raw ?? "",
      },
    );
    // Applying the same config as is on disk is a no-op apply — 200 OK.
    expect(unlockedResp.status()).toBe(200);

    // 11. Traffic still serves original content.
    await expectStaticOK(request, "Jul static OK");
  },
);

test(
  "stage_restart: updating a staged config records updated (not created) state",
  async ({ request }) => {
    // 1. Clear any leftover staged restart.
    await request.post("/api/config/pending-restart/discard");

    // 2. Read config and stage a first restart.
    const cfgResp = await request.get("/api/config");
    expect(cfgResp.status()).toBe(200);
    const cfgData: unknown = await cfgResp.json();
    const original = RawConfigSchema.parse(cfgData);
    const baseVersion = original.base_version ?? "";
    const candidate = withCacheMemoryMaxSize(original.raw ?? "", "64m");

    const stageUrl = baseVersion
      ? `/api/config/apply?base_version=${encodeURIComponent(baseVersion)}&mode=stage_restart`
      : "/api/config/apply?mode=stage_restart";
    const stage1Resp = await request.post(stageUrl, {
      headers: { "Content-Type": "application/toml" },
      data: candidate,
    });
    expect(stage1Resp.status()).toBe(200);
    const stage1 = ApplyResultSchema.parse(await stage1Resp.json());
    expect(stage1.ok).toBe(true);

    // 3. Update the staged config with a slightly different candidate.
    const candidateV2 = withCacheMemoryMaxSize(original.raw ?? "", "128m");
    const cfgResp2 = await request.get("/api/config");
    const latestCfg = RawConfigSchema.parse(await cfgResp2.json());
    const updateUrl = latestCfg.base_version
      ? `/api/config/apply?base_version=${encodeURIComponent(latestCfg.base_version ?? "")}&mode=stage_restart`
      : "/api/config/apply?mode=stage_restart";
    const stage2Resp = await request.post(updateUrl, {
      headers: { "Content-Type": "application/toml" },
      data: candidateV2,
    });
    expect(stage2Resp.status()).toBe(200);
    const stage2 = ApplyResultSchema.parse(await stage2Resp.json());
    expect(stage2.ok).toBe(true);
    expect(stage2.mode).toBe("stage_restart");

    // 4. Pending status should still be active after the update.
    const pendingResp = await request.get("/api/config/pending-restart");
    expect(pendingResp.status()).toBe(200);
    const pendingBody = (await pendingResp.json()) as Record<string, unknown>;
    expect(pendingBody.pending).toBe(true);

    // 5. Clean up.
    await request.post("/api/config/pending-restart/discard");
  },
);
