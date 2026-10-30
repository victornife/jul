/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 *
 * Browser-level smoke test for the Jul.IA Console SPA (Finding UI-1).
 *
 * Architecture
 * ────────────
 * The test mounts the built SPA via `vite preview` (see playwright.config.ts)
 * and intercepts every `/api/*` call with `page.route()` so no Go server is
 * needed. This lets the test validate:
 *   • the SPA boots and renders correctly against valid API fixture data,
 *   • every Zod schema in client.ts accepts the exact field shapes the mocked
 *     responses carry (schema-vs-projection drift would throw a ZodError and
 *     land in a panel-error boundary, failing the assertions below), and
 *   • the core operator flow — overview → edit route → diff → apply → rollback
 *     — completes end-to-end in the real browser engine.
 *
 * Mock contract
 * ─────────────
 * Fixture objects are typed inline to match the Zod schemas exported from
 * client.ts; an intentional field rename in either the schema or the fixture
 * will surface as a parse error (ZodError thrown inside the SPA) that the test
 * catches via the panel-error assertion fallback.
 */

import { test, expect, type Page, type Route } from "@playwright/test";

// ── Shared fixture data ───────────────────────────────────────────────────────

/** Minimal valid Overview payload (mirrors OverviewSchema). */
const OVERVIEW = {
  product: "Jul.IA",
  version: "0.0.0-smoke",
  status: [
    { group: "core", name: "HTTP server", active: true },
    { group: "core", name: "Config watcher", active: true },
  ],
};

/** Single proxy route used throughout the flow. */
const ROUTES = [
  {
    listen: ":8080",
    server_names: [],
    http3: false,
    h2c: false,
    locations: [
      {
        index: 0,
        match: "/",
        type: "prefix",
        action: "proxy",
        target: "http://app:3000",
        auth: false,
        cache: false,
        compression: false,
        rate_limit: false,
        secure: false,
        require_client_cert: false,
      },
    ],
  },
];

const RAW_CONFIG = {
  raw: `[[servers]]\nlisten = ":8080"\n\n  [[servers.locations]]\n  match = { type = "prefix", path = "/" }\n  proxy_pass = "http://app:3000"\n`,
  base_version: "v1",
};

const TRAFFIC_CONTROLS = {
  access_log: {
    enabled: true,
    sinks: ["stdout"],
    format: "text",
    rotate_max_mb: 100,
    rotate_keep: 7,
  },
  cache: {
    enabled: true,
    memory_max_size: "64m",
    disk_path: "/var/cache/jul",
    disk_max_size: "1g",
    max_entry_size: "16m",
    default_ttl: "60s",
    stale_while_revalidate: "10s",
    stale_if_error: "30s",
    lock: false,
    lock_timeout: "",
    range_enabled: false,
    max_ranges: 0,
    tag_headers: ["Cache-Tag"],
    strip_tag_headers: true,
  },
  servers: [
    {
      listen: ":8080",
      server_names: ["portal.example.com"],
      client_max_body_size: "10m",
      read_timeout: "15s",
      write_timeout: "15s",
      idle_timeout: "60s",
    },
  ],
};

const PATCH_CANDIDATE = `[[servers]]\nlisten = ":8080"\n\n  [[servers.locations]]\n  match = { type = "prefix", path = "/" }\n  proxy_pass = "http://new-app:4000"\n`;

/** PatchResult returned for POST /api/config/patch/preview. */
const PATCH_RESULT = {
  ok: true,
  summary: "route :8080 / target changed",
  operation_summaries: [],
  base_version: "v1",
  valid: true,
  validation_errors: [],
  lifecycle: {
    changes: [],
    can_apply_hot: true,
    can_stage_restart: true,
    hot_paths: ["servers.locations"],
    restart_required_paths: [],
    new_listener_only_paths: [],
    ignored_deprecated_paths: [],
    validation_rejected_paths: [],
    pending_subsystems: [],
  },
  diff: {
    summary: "1 modification",
    modifications: [
      {
        kind: "route",
        name: ":8080 /",
        before: 'proxy_pass = "http://app:3000"',
        after: 'proxy_pass = "http://new-app:4000"',
      },
    ],
  },
};

/** PatchApplyResult returned for POST /api/config/patch/apply. */
const PATCH_APPLY_RESULT = {
  ok: true,
  pending_reload: false,
  version: "v2",
  summary: ["target changed from http://app:3000 to http://new-app:4000"],
  diff: {
    summary: "1 modification",
    modifications: [
      {
        kind: "route",
        name: ":8080 /",
        before: 'proxy_pass = "http://app:3000"',
        after: 'proxy_pass = "http://new-app:4000"',
      },
    ],
  },
  status: [{ group: "core", name: "HTTP server", active: true }],
};

/** History entry — represents the snapshot taken just before our apply. */
const HISTORY_ENTRY = {
  id: "snap-001",
  time: "2026-07-13T20:00:00Z",
  size: 256,
};

const HISTORY_SNAPSHOT = {
  id: "snap-001",
  raw: RAW_CONFIG.raw,
};

// ── API mock helpers ──────────────────────────────────────────────────────────

function json(route: Route, body: unknown, status = 200): Promise<void> {
  return route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

/**
 * Registers the baseline API mocks for every test. Only endpoints that the
 * smoke touches need a handler; any unmatched request that the SPA happens to
 * fire (e.g. /api/admin/health) is fulfilled with an empty 404 that the client
 * treats as "feature unavailable" without crashing.
 */
async function setupApiMocks(page: Page): Promise<void> {
  // Catch-all: fulfil with 404 so optional panels degrade gracefully instead
  // of turning into a failed network request (which would cause the test to
  // see unexpected error banners).
  await page.route("/api/**", (route) => json(route, { error: "not found" }, 404));

  // Identity is verified before panels mount; these default smoke journeys
  // exercise the privileged comparison workflow. Role tests override it below.
  await page.route("/api/admin/me", (route) => json(route, {
    principal: "smoke-admin", role: "admin", token_id: "synthetic-smoke",
    legacy: false,
    permissions: ["status:read", "metrics:read", "config:read", "config:raw", "config:write", "config:apply", "config:trust", "config:adopt", "history:read", "history:raw", "history:rollback", "plugins:upload", "observability:read", "audit:read", "audit:export", "cache:purge", "reload:trigger", "admin:manage"],
  }));
  await page.route("/api/runtime/overview", (route) => json(route, OVERVIEW));
  await page.route("/api/routes", (route) => json(route, ROUTES));
  await page.route("/api/config", (route) => json(route, RAW_CONFIG));
  await page.route("/api/config/pending-restart", (route) => json(route, { pending: false }));
  await page.route("/api/config/validate", (route) =>
    json(route, { ok: true, message: "Configuration is valid." }),
  );
  await page.route("/api/config/diff", (route) =>
    json(route, {
      summary: "1 modification",
      modifications: [
        {
          kind: "access_log",
          name: "observability.access_log",
          before: "enabled = true",
          after: "enabled = false",
        },
      ],
    }),
  );
  await page.route("/api/traffic-controls", (route) => json(route, TRAFFIC_CONTROLS));
  await page.route("/api/config/history", (route) => json(route, [HISTORY_ENTRY]));
  await page.route("/api/config/history/snap-001", (route) => json(route, HISTORY_SNAPSHOT));

  // Write endpoints — handled per-step so we can await them explicitly.
  await page.route("/api/config/patch/preview", (route) => {
    const body = route.request().postDataJSON() as {
      ops?: Array<{ readonly op: string }>;
    };
    const ops = body.ops ?? [];
    return json(route, {
      ...PATCH_RESULT,
      operation_summaries: ops.map((operation, opIndex) => ({
        op_index: opIndex,
        op: operation.op,
        summary: `${operation.op} previewed`,
      })),
    });
  });
  await page.route("/api/config/patch/candidate", (route) =>
    json(route, {
      ok: true,
      candidate: PATCH_CANDIDATE,
      base_version: PATCH_RESULT.base_version,
    }),
  );
  await page.route("/api/config/patch/apply", (route) => json(route, PATCH_APPLY_RESULT));
  await page.route("/api/config/rollback", (route) => json(route, { ok: true }));
  await page.route("/api/v1/cache/purge/preview", (route) =>
    json(route, {
      api_version: "v1",
      cache_enabled: true,
      tag_ingestion_enabled: true,
      selector: {
        kind: "tag",
        host: "portal.example.com",
        tags: ["article:42"],
        tag_match: "any",
      },
      selector_digest: "smoke-digest-42",
      preview_token: "v1.smoke.token",
      matched_resources: 2,
      matched_representations: 4,
      memory_entries: 4,
      disk_entries: 2,
      matched_bytes: 8192,
      memory_bytes: 4096,
      disk_bytes: 4096,
      inflight_fills_matched: 0,
      count_accuracy: "exact",
      truncated: false,
      scan_limit: 10000,
      scanned_entries: 8,
      source: "live_cache_index",
      as_of: "2026-10-25T12:00:00Z",
      cache_sequence: 11,
      execution_mode_contract: "reevaluate_selector_with_fence",
      broad_scope: false,
      warnings: [
        {
          code: "JUL_LOCAL_SCOPE_ONLY",
          severity: "info",
          scope: "cache",
          summary: "Cache invalidation applies only to this Jul instance.",
        },
      ],
      samples: [
        {
          host: "portal.example.com",
          path: "/articles/42",
          methods: ["GET", "HEAD"],
          representations: 2,
          tiers: ["memory", "disk"],
          body_bytes: 2048,
          tags: ["article:42"],
          freshness_state: "fresh",
        },
      ],
      config_authority: "managed",
    }),
  );
  await page.route("/api/v1/cache/purge", (route) =>
    json(route, {
      api_version: "v1",
      operation_id: "cp_1_smoke42",
      state: "succeeded",
      terminal: true,
      outcome: "succeeded",
      invalidation_effective: "effective",
      selector: {
        kind: "tag",
        host: "portal.example.com",
        tags: ["article:42"],
        tag_match: "any",
      },
      selector_digest: "smoke-digest-42",
      fence_sequence: 12,
      invalidated_resources: 2,
      invalidated_representations: 4,
      memory_entries_removed: 4,
      disk_entries_removed: 2,
      active_readers_retired: 0,
      fenced_inflight_ops: 0,
      failed_entries: 0,
      remaining_entries: 0,
      started_at: "2026-10-25T12:00:01Z",
      completed_at: "2026-10-25T12:00:01Z",
      warnings: [],
      boot_id: "boot-smoke",
      config_authority: "managed",
    }),
  );

  // Silence the console-health poller that fires on every page mount.
  await page.route("/api/admin/health", (route) => json(route, { status: "ok", latency_ms: 1 }));
}

// ── Test ──────────────────────────────────────────────────────────────────────

test.describe("Console SPA smoke (UI-1)", () => {
  test("load overview → edit route → diff → apply → rollback", async ({ page }) => {
    await setupApiMocks(page);

    // ── 1. Overview ────────────────────────────────────────────────────────────
    // Navigate to the root; the SPA mounts, queries /api/runtime/overview, and
    // renders the OverviewPanel. Assert the heading is visible — this confirms
    // the SPA bootstrapped, the Zod parse of OverviewSchema succeeded, and the
    // component rendered without hitting an error boundary.
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();

    // The product name comes from the Overview fixture; seeing it confirms the
    // API response was parsed and threaded into the rendered output.
    await expect(page.getByText(OVERVIEW.product).first()).toBeVisible();

    // ── 2. Routes panel ────────────────────────────────────────────────────────
    await page.getByRole("link", { name: "Routes" }).click();
    await expect(page.getByRole("heading", { name: "Routes" })).toBeVisible();

    // The listen address from the ROUTES fixture must appear in the table,
    // confirming that RouteProjectionSchema was satisfied by the mock data and
    // the table row rendered.
    await expect(page.getByText(":8080")).toBeVisible();

    // ── 3. Open route detail and edit the proxy target ─────────────────────────
    // Click the location row to open the RouteDetail drawer. The RoutesPanel
    // renders rows as <tr> elements with onClick; select the one that contains
    // the current proxy target to be specific.
    await page.getByText("http://app:3000").click();

    // RouteDetail opens in a Drawer. The "Quick edits" section is always visible
    // for a proxy location and contains the "Proxy target" input.
    await expect(page.getByPlaceholder("http://app")).toBeVisible();

    // Change the proxy target to a new value.
    const targetInput = page.getByPlaceholder("http://app");
    await targetInput.fill("http://new-app:4000");

    // The "Set target →" button is now enabled (value differs from current).
    // Clicking it sends POST /api/config/patch/preview and then navigates to /config.
    const patchRequest = page.waitForRequest("/api/config/patch/preview");
    await page.getByRole("button", { name: "Set target →" }).click();
    await patchRequest;

    // ── 4. Config panel — diff visible ─────────────────────────────────────────
    // The SPA navigated to /config after the patch was previewed. ConfigPanel
    // picks up the pending draft from the in-memory handoff, loads the raw
    // config from /api/config, and shows the pre-computed diff. The "atomic
    // patch" badge signals we are in patch mode, and the "Pending changes"
    // section shows the diff.
    await expect(page.getByText("atomic patch", { exact: true })).toBeVisible();
    await expect(page.getByText("Pending changes", { exact: true })).toBeVisible();

    // The diff summary line from the fixture must be rendered by DiffView.
    await expect(page.getByText("1 modification", { exact: true })).toBeVisible();

    // ── 5. Apply the patch ─────────────────────────────────────────────────────
    // #81 standardizes the hot-path primary action on "Apply live". Clicking it
    // opens a ConfirmDialog with the same action label.
    await page.getByRole("button", { name: "Apply live" }).click();

    const applyDialog = page.getByRole("dialog", { name: "Apply live?" });
    await expect(applyDialog).toBeVisible();

    // Intercept the apply request so we can assert it was sent.
    const applyRequest = page.waitForRequest("/api/config/patch/apply");
    await applyDialog.getByRole("button", { name: "Apply live" }).click();
    await applyRequest;

    // After a successful apply, ConfigPanel shows the ApplyOutcomeBanner. The
    // outcome for a non-pending-reload apply with runtimeObserved=false is
    // "reload-pending" ("Applied — runtime reloading"). The banner has
    // role=status per ApplyOutcomeBanner's implementation.
    await expect(page.getByRole("status")).toBeVisible();

    // ── 6. History panel — rollback ────────────────────────────────────────────
    await page.getByRole("link", { name: "History" }).click();
    await expect(page.getByRole("heading", { name: "History" })).toBeVisible();

    // The single history entry from our fixture appears in the table.
    await expect(page.getByText("snap-001")).toBeVisible();

    // "Rollback" initiates a rollback for this snapshot. Clicking it opens a
    // ConfirmDialog that fetches the snapshot and shows a diff.
    await page.route("/api/config/history/snap-001/diff", (route) =>
      json(route, {
        summary: "rollback to snap-001",
        modifications: [],
        base_version: "v1",
      }),
    );
    await page.getByRole("button", { name: "Rollback" }).click();

    // The rollback ConfirmDialog appears.
    await expect(page.getByText("Roll back to this snapshot?")).toBeVisible();

    // Intercept the rollback request.
    const rollbackRequest = page.waitForRequest("/api/config/rollback");
    await page.getByRole("button", { name: "Roll back" }).click();
    await rollbackRequest;

    // The dialog is dismissed after the rollback POST returns.
    await expect(page.getByText("Roll back to this snapshot?")).not.toBeVisible();
  });

  test("creates routes with deterministic existing-server and new-server batches", async ({
    page,
  }) => {
    await setupApiMocks(page);
    await page.goto("/routes");

    await page.getByRole("button", { name: "New route" }).first().click();
    await expect(page.getByRole("heading", { name: "Create route" })).toBeVisible();
    await page.getByLabel("Path").fill("/api");
    await page.getByLabel("HTTP upstream target").fill("http://api:3000");

    const existingPreview = page.waitForRequest("/api/config/patch/preview");
    await page.getByRole("button", { name: "Review lifecycle and diff →" }).click();
    const existingBody = (await existingPreview).postDataJSON() as {
      ops: Array<Record<string, unknown>>;
    };
    expect(existingBody.ops.map((operation) => operation.op)).toEqual(["location_add"]);
    expect(existingBody.ops[0]).toMatchObject({
      op: "location_add",
      listen: ":8080",
      server_names: [],
      match_set: { type: "prefix", path: "/api" },
      action: { kind: "proxy", target: "http://api:3000" },
    });
    await expect(page).toHaveURL(/\/config$/);

    await page.getByRole("link", { name: "Routes" }).click();
    await page.getByRole("button", { name: "New route" }).first().click();
    await page.getByRole("radio", { name: /Create a new server/ }).check();
    await page.getByLabel("New listener").fill(":9090");
    await page.getByLabel("New server names (optional)").fill("b.example, a.example");
    await page.getByLabel("Path").fill("/v2");
    await page.getByLabel("HTTP upstream target").fill("http://v2:3000");

    const newServerPreview = page.waitForRequest("/api/config/patch/preview");
    await page.getByRole("button", { name: "Review lifecycle and diff →" }).click();
    const newServerBody = (await newServerPreview).postDataJSON() as {
      ops: Array<Record<string, unknown>>;
    };
    expect(newServerBody.ops.map((operation) => operation.op)).toEqual([
      "server_add",
      "location_add",
    ]);
    expect(newServerBody.ops[0]).toMatchObject({
      op: "server_add",
      listen: ":9090",
      server_names: ["a.example", "b.example"],
    });
    expect(newServerBody.ops[1]).toMatchObject({
      op: "location_add",
      listen: ":9090",
      server_names: ["a.example", "b.example"],
      match_set: { type: "prefix", path: "/v2" },
    });
    await expect(page).toHaveURL(/\/config$/);
  });

  test("previews exact route deletion and cancel performs no handoff or apply", async ({ page }) => {
    await setupApiMocks(page);
    let applyCalls = 0;
    await page.route("/api/config/patch/apply", (route) => {
      applyCalls += 1;
      return json(route, PATCH_APPLY_RESULT);
    });

    await page.goto("/routes");
    await page.getByText("http://app:3000").click();

    const deletePreview = page.waitForRequest("/api/config/patch/preview");
    await page.getByRole("button", { name: /Delete route prefix \/ from/ }).click();
    const deletionBody = (await deletePreview).postDataJSON() as {
      ops: Array<Record<string, unknown>>;
    };
    expect(deletionBody.ops).toEqual([
      {
        op: "location_remove",
        listen: ":8080",
        server_names: [],
        match_type: "prefix",
        path: "/",
      },
    ]);

    await expect(page.getByRole("dialog", { name: "Remove this exact route?" })).toBeVisible();
    await expect(page.getByText(/does not cascade to upstreams/)).toBeVisible();
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByRole("dialog", { name: "Remove this exact route?" })).not.toBeVisible();
    await expect(page).toHaveURL(/\/routes$/);
    expect(applyCalls).toBe(0);
  });
});

test("traffic controls exposes the access-log editor", async ({ page }) => {
  await setupApiMocks(page);
  await page.goto("/traffic");

  await expect(page.getByRole("heading", { name: "Traffic Controls" })).toBeVisible();
  const accessCard = page
    .getByText("Access Logging", { exact: true })
    .locator("..", { hasText: "enabled" });
  await expect(accessCard).toBeVisible();
  await accessCard.getByRole("button", { name: "Edit" }).click();

  await expect(page.getByText("Edit access logging")).toBeVisible();
  await expect(page.getByRole("checkbox", { name: "Enable request access logging" })).toBeChecked();

  await page.getByRole("checkbox", { name: "Enable request access logging" }).uncheck();
  await page.getByRole("button", { name: "Review in editor →" }).click();

  await expect(page).toHaveURL(/\/config$/);
  await expect(page.getByText("Pending changes", { exact: true })).toBeVisible();
});

// ── Role-based gating (P3-04) ─────────────────────────────────────────────────
//
// These scenarios mock GET /api/admin/me with a concrete identity so the
// PermissionProvider resolves a role and the Console gates controls proactively.
// The server remains authoritative (proven by the Go role-matrix E2E); here we
// assert only the browser-visible half: the identity is shown and mutating
// controls the caller cannot use are disabled, with the reason surfaced.

/** Identity fixtures mirror IdentitySchema; permission sets match the predefined roles. */
const VIEWER_IDENTITY = {
  principal: "auditbot",
  role: "viewer",
  token_id: "0f1e2d3c",
  permissions: [
    "status:read",
    "metrics:read",
    "config:read",
    "history:read",
    "observability:read",
    "audit:read",
  ],
  legacy: false,
};

const OPERATOR_IDENTITY = {
  principal: "op",
  role: "operator",
  token_id: "a1b2c3d4",
  permissions: [
    "status:read",
    "metrics:read",
    "config:read",
    "config:write",
    "config:apply",
    "history:read",
    "history:rollback",
    "plugins:upload",
    "observability:read",
    "audit:read",
    "audit:export",
    "cache:purge",
    "reload:trigger",
  ],
  legacy: false,
};

/** mockIdentity overrides GET /api/admin/me with a concrete identity fixture. */
async function mockIdentity(page: Page, identity: unknown): Promise<void> {
  await page.route("/api/admin/me", (route) => json(route, identity));
}

test.describe("Console RBAC gating (P3-04)", () => {
  test("viewer sees its identity and cannot roll back", async ({ page }) => {
    await setupApiMocks(page);
    await mockIdentity(page, VIEWER_IDENTITY);

    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();

    // The identity badge reflects the authenticated principal and role.
    await expect(page.locator('[title*="role: viewer"]').first()).toBeVisible();

    // History: a viewer lacks history:rollback, so the control is disabled and
    // the required permission is surfaced as its title. The server would reject
    // the call too — this only avoids leading the operator into a certain 403.
    await page.getByRole("link", { name: "History" }).click();
    await expect(page.getByRole("heading", { name: "History" })).toBeVisible();
    await expect(page.getByText("snap-001")).toBeVisible();
    const rollback = page.getByRole("button", { name: "Rollback" });
    await expect(rollback).toBeDisabled();
    await expect(rollback).toHaveAttribute("title", /history:rollback permission/);

    // Route authoring is independently gated by config:write.
    await page.getByRole("link", { name: "Routes" }).click();
    const newRoute = page.getByRole("button", { name: "New route" }).first();
    await expect(newRoute).toBeDisabled();
    await expect(newRoute).toHaveAttribute("title", /config:write/);
  });

  test("operator identity enables the rollback control", async ({ page }) => {
    await setupApiMocks(page);
    await mockIdentity(page, OPERATOR_IDENTITY);

    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
    await expect(page.locator('[title*="role: operator"]').first()).toBeVisible();

    // An operator holds history:rollback, so the same control is enabled.
    await page.getByRole("link", { name: "History" }).click();
    await expect(page.getByRole("heading", { name: "History" })).toBeVisible();
    await expect(page.getByText("snap-001")).toBeVisible();
    await expect(page.getByRole("button", { name: "Rollback" })).toBeEnabled();
  });
});

// #645: run against the shipped SPA, including identity and browser storage.
test.describe("Configuration comparison confidentiality", () => {
  test("writer without raw cannot restore a stored privileged comparison", async ({ page }) => {
    await setupApiMocks(page);
    await mockIdentity(page, OPERATOR_IDENTITY);
    let rawRequests = 0;
    await page.route("/api/config", (route) => {
      rawRequests += 1;
      return json(route, { raw: "SYNTHETIC_PRIVILEGED_645", base_version: "private" });
    });
    await page.addInitScript(() => {
      sessionStorage.setItem("__jul_config_pending_draft_v4", JSON.stringify({
        kind: "patch", ops: [{ op: "compression_set" }],
        previewDiff: { summary: "SYNTHETIC_PRIVILEGED_645" },
      }));
    });
    await page.goto("/config");
    await expect(page.getByRole("note").filter({ hasText: "Configuration previews require config:raw" })).toBeVisible();
    await expect(page.locator("body")).not.toContainText("SYNTHETIC_PRIVILEGED_645");
    expect(rawRequests).toBe(0);
    expect(await page.evaluate(() => sessionStorage.getItem("__jul_config_pending_draft_v4"))).toBeNull();
  });

  test("verified permission loss removes an already rendered raw configuration", async ({ page }) => {
    await setupApiMocks(page);
    await page.route("/api/config", (route) => json(route, { raw: "# SYNTHETIC_PRIVILEGED_645", base_version: "v1" }));
    await page.goto("/config");
    await expect(page.locator(".cm-content")).toContainText("SYNTHETIC_PRIVILEGED_645");
    await mockIdentity(page, OPERATOR_IDENTITY);
    await page.evaluate(() => { window.dispatchEvent(new CustomEvent("jul:forbidden")); });
    await expect(page.getByRole("note").filter({ hasText: "Configuration previews require config:raw" })).toBeVisible();
    await expect(page.locator("body")).not.toContainText("SYNTHETIC_PRIVILEGED_645");
  });

  test("an initial rejected identity keeps token recovery operable", async ({ page }) => {
    await setupApiMocks(page);
    await page.route("/api/admin/me", (route) => json(route, { error: "unauthorized" }, 401));
    await page.goto("/");
    await expect(page.getByRole("dialog", { name: "Admin token required" })).toBeVisible();
    await expect(page.getByLabel("Token", { exact: true })).toBeEditable();
    await expect(page.getByRole("heading", { name: "Overview" })).not.toBeVisible();
  });

  test("scoped cache invalidation workflow: select scope -> preview -> confirm -> execute and viewer RBAC gating (#621)", async ({
    page,
  }) => {
    await setupApiMocks(page);
    await page.goto("/traffic");
    await expect(
      page.getByRole("heading", { name: "Global & Traffic Controls" }),
    ).toBeVisible();

    const cacheHeading = page.getByText("Cache", { exact: true }).first();
    await cacheHeading.locator("..").getByRole("button", { name: "Edit" }).click();

    const dialog = page.getByRole("dialog", { name: "Edit cache" });
    await expect(dialog).toBeVisible();

    await dialog.getByLabel("Invalidation selector kind").selectOption("tag");
    await dialog.getByLabel("Origin grouping tags").fill("article:42");
    await dialog.getByRole("button", { name: "Preview invalidation scope" }).click();

    const previewRegion = dialog.getByRole("region", { name: "Invalidation preview" });
    await expect(previewRegion).toBeVisible();
    await expect(previewRegion).toContainText("portal.example.com/articles/42");

    await previewRegion.getByRole("button", { name: "Confirm & invalidate" }).click();

    const resultBanner = dialog.getByLabel("Invalidation execution result");
    await expect(resultBanner).toBeVisible();
    await expect(resultBanner).toContainText("cp_1_smoke42");
    await expect(resultBanner).toContainText("effective");

    // Verify viewer identity cannot preview or execute purge (requires cache:purge).
    await mockIdentity(page, VIEWER_IDENTITY);
    await page.goto("/traffic");
    await page
      .getByText("Cache", { exact: true })
      .first()
      .locator("..")
      .getByRole("button", { name: "Edit" })
      .click();
    const viewerDialog = page.getByRole("dialog", { name: "Edit cache" });
    await viewerDialog.getByLabel("Invalidation selector kind").selectOption("tag");
    await viewerDialog.getByLabel("Origin grouping tags").fill("article:42");
    await expect(
      viewerDialog.getByRole("button", { name: "Preview invalidation scope" }),
    ).toBeDisabled();
  });
});

for (const inspectionOnly of [false, true]) {
  test(`bounded cache inspection is keyboard-operable with ${inspectionOnly ? "cache:inspect only" : "configuration read"} (#622)`, async ({
    page,
  }) => {
    await setupApiMocks(page);
    await page.route("/api/admin/me", (route) =>
      json(route, {
        principal: "cache-reader",
        role: "inspect",
        legacy: false,
        permissions: inspectionOnly
          ? ["cache:inspect"]
          : ["status:read", "config:read", "cache:inspect"],
      }),
    );
    const privilegedReads: string[] = [];
    page.on("request", (request) => {
      if (
        /\/api\/(runtime\/overview|admin\/health|routes|stats|config)(?:\?|$)/.test(
          new URL(request.url()).pathname,
        )
      )
        privilegedReads.push(request.url());
    });
    if (inspectionOnly)
      await page.route("/api/traffic-controls", (route) => json(route, { inspection_only: true }));
    let purges = 0;
    let reads = 0;
    await page.route("/api/v1/cache/purge", (route) => {
      purges++;
      return json(route, {}, 403);
    });
    await page.route("/api/v1/cache/inspect", (route) => {
      reads++;
      return json(route, {
        api_version: "v1",
        expires_at: new Date(Date.now() + 60000).toISOString(),
        consistency: "bounded_metadata_capture_not_global_snapshot",
        concurrent_change: "unknown_after_capture",
        policy_source: "global inherited",
        route_cache_state: "host scope",
        inspection: {
          state: "empty",
          captured_at: new Date().toISOString(),
          count_accuracy: "exact",
          truncated: false,
          matched_resources: 0,
          matched_representations: 0,
          memory_entries: 0,
          disk_entries: 0,
          matched_body_bytes: 0,
          resources: [],
          decisions: [],
          evidence_state: "disabled",
          warnings: [],
          memory_occupancy: { bytes: 0, max_bytes: 64000000, entries: 0, evictions: 0 },
          disk_occupancy: null,
          policy: {
            default_ttl_seconds: 60,
            swr_seconds: 10,
            sie_seconds: 5,
            max_entry_bytes: 64000000,
            range_enabled: false,
            fill_lock_enabled: false,
            tag_ingestion_enabled: false,
          },
        },
      });
    });
    await page.goto("/traffic");
    if (!inspectionOnly) {
      await page
        .getByText("Cache", { exact: true })
        .first()
        .locator("..")
        .getByRole("button", { name: "Edit" })
        .click();
    }
    const inspector = page.getByRole("region", { name: "Cache inspector" });
    await expect(inspector).toBeVisible();
    const host = inspector.getByLabel("Configured hostname");
    await host.focus();
    await expect(host).toBeFocused();
    await host.fill("portal.example.com");
    const inspect = inspector.getByRole("button", { name: "Inspect / refresh" });
    await inspect.focus();
    await page.keyboard.press("Enter");
    await expect(inspector.getByText(/No resource metadata/)).toBeVisible();
    await expect(inspector.getByText(/Current global fallback/)).toBeVisible();
    await expect(inspector.getByText(/Memory occupancy/)).toBeVisible();
    await expect(inspector.getByText(/live entries may have changed/)).toBeVisible();
    expect(reads).toBe(1);
    expect(purges).toBe(0);
    if (inspectionOnly) {
      expect(privilegedReads).toEqual([]);
      await expect(page.getByRole("button", { name: "Preview invalidation scope" })).toHaveCount(0);
      await expect(page.getByRole("button", { name: "Review changes" })).toHaveCount(0);
    } else {
      await expect(
        page
          .getByRole("dialog", { name: "Edit cache" })
          .getByRole("button", { name: "Preview invalidation scope" }),
      ).toBeDisabled();
    }
  });
}

test.describe("Route cache policy starters, failed/staged/denied flows, and accessibility (#623)", () => {
  test("keyboard-activates starters, surfaces validation/server alerts, hands off staged preview, and enforces RBAC", async ({
    page,
  }) => {
    await setupApiMocks(page);
    await page.route("/api/routes", (route) =>
      json(route, [
        {
          base_version: "v1",
          listen: ":8080",
          server_names: ["portal.example.com"],
          http3: false,
          h2c: false,
          locations: [
            {
              index: 0,
              route_id: "portal-api",
              match: "/api",
              type: "prefix",
              action: "proxy",
              target: "http://app:3000",
              auth: false,
              cache: true,
              compression: false,
              rate_limit: false,
              secure: false,
              require_client_cert: false,
              cache_policy: {
                default_ttl: "5m0s",
                stale_while_revalidate: "30s",
                stale_if_error: "5m0s",
                bypass_cookies: ["session_id"],
                bypass_any_cookie: false,
              },
              cache_effective: {
                global_enabled: true,
                default_ttl: "5m0s",
                default_ttl_source: "route_override",
                max_ttl_source: "none",
                stale_while_revalidate: "30s",
                swr_source: "route_override",
                stale_if_error: "5m0s",
                sie_source: "route_override",
                bypass_mode: "selected_cookies",
                bypass_cookies: ["session_id"],
                lock_enabled: true,
                range_enabled: true,
                memory_max_size: "64m",
                disk_enabled: true,
                max_entry_size: "16m",
              },
            },
          ],
        },
      ]),
    );

    await page.goto("/routes");
    await page.getByText("http://app:3000").click();
    await expect(page.getByText("Cache policy", { exact: true })).toBeVisible();

    // Open the Route cache policy editor via keyboard
    const editPolicyBtn = page.getByRole("button", { name: "Edit cache policy →" });
    await editPolicyBtn.focus();
    await expect(editPolicyBtn).toBeFocused();
    await page.keyboard.press("Enter");

    const editorDialog = page.getByRole("dialog", { name: "Route cache policy" });
    await expect(editorDialog).toBeVisible();
    await expect(editorDialog.getByLabel("Effective route cache posture")).toContainText(
      "selected_cookies (session_id)",
    );

    // 1. Validation error & dormant warning accessibility regions + aria-invalid state
    const defaultTTLInput = editorDialog.getByLabel("Default TTL override");
    const maxTTLInput = editorDialog.getByLabel("Max TTL cap");
    const cookieInput = editorDialog.getByLabel(/Cookie names/);
    await defaultTTLInput.fill("10m");
    await maxTTLInput.fill("1m");
    await cookieInput.fill("bad name=1");
    await expect(defaultTTLInput).toHaveAttribute("aria-invalid", "true");
    await expect(maxTTLInput).toHaveAttribute("aria-invalid", "true");
    await expect(cookieInput).toHaveAttribute("aria-invalid", "true");
    await expect(editorDialog.getByRole("alert", { name: "Cache policy errors" })).toContainText(
      "default_ttl (10m) must not exceed max_ttl (1m).",
    );
    await expect(editorDialog.getByRole("button", { name: "Preview change →" })).toBeDisabled();

    await editorDialog.getByLabel("Enable caching on this route (cache = true)").uncheck();
    await expect(editorDialog.getByRole("status", { name: "Cache policy warnings" })).toContainText(
      "Route cache is disabled",
    );

    // 2. Keyboard activation of the Conservative dynamic / API starter + failed preview flow
    let failPreview = true;
    await page.route("/api/config/patch/preview", (route) => {
      const body = route.request().postDataJSON() as {
        ops?: Array<{ readonly op: string }>;
      };
      if (failPreview) {
        return json(
          route,
          {
            ok: false,
            message: "Preview rejected by policy guard",
            errors: [
              {
                code: "cache_policy_conflict",
                path: "servers[0].locations[0].cache_policy",
                summary: "Simulated server-side preview failure",
                severity: "error",
              },
            ],
            op_index: 0,
            op: body.ops?.[0]?.op ?? "location_cache_policy_set",
          },
          400,
        );
      }
      return json(route, {
        ...PATCH_RESULT,
        summary: "route :8080/api cache policy set",
        operation_summaries: (body.ops ?? []).map((op, idx) => ({
          op_index: idx,
          op: op.op,
          summary: `${op.op} previewed`,
        })),
        lifecycle: {
          changes: [
            {
              path: "servers.0.locations.0.cache_policy.max_ttl",
              declared: "hot_reload",
              effective: "restart_required",
              subsystem: "cache",
              reason: "staged for restart in test fixture",
              ignored: false,
              reserved: false,
            },
          ],
          can_apply_hot: false,
          can_stage_restart: true,
          hot_paths: [],
          restart_required_paths: ["servers.0.locations.0.cache_policy.max_ttl"],
          new_listener_only_paths: [],
          ignored_deprecated_paths: [],
          validation_rejected_paths: [],
          pending_subsystems: ["cache"],
        },
        diff: {
          summary: "1 modification",
          modifications: [
            {
              kind: "cache",
              name: "portal.example.com :8080 /api",
              before: "max_ttl = none",
              after: "max_ttl = 1m0s",
              detail: "Change cache_policy.max_ttl on route /api",
            },
          ],
        },
      });
    });

    const conservativeStarter = editorDialog.getByRole("button", {
      name: /Conservative dynamic \/ API/i,
    });
    await conservativeStarter.focus();
    await page.keyboard.press("Enter");
    await expect(editorDialog.getByLabel("Default TTL override")).toHaveValue("0s");
    await expect(editorDialog.getByLabel("Max TTL cap")).toHaveValue("1m");

    const previewBtn = editorDialog.getByRole("button", { name: "Preview change →" });
    await expect(previewBtn).toBeEnabled();
    await previewBtn.click();
    await expect(editorDialog.getByRole("alert")).toContainText(
      /Simulated server-side preview failure|Preview rejected/i,
    );

    // 3. Staged restart handoff flow
    failPreview = false;
    await previewBtn.click();
    await expect(page.getByText("atomic patch", { exact: true })).toBeVisible();
    await expect(page.getByText("Change cache_policy.max_ttl on route /api")).toBeVisible();
    await expect(page.getByRole("button", { name: "Save for next restart" })).toBeVisible();

    // 4. Denied / viewer RBAC flow
    await mockIdentity(page, VIEWER_IDENTITY);
    await page.goto("/routes");
    await page.getByText("http://app:3000").click();
    await expect(page.getByRole("button", { name: "Edit cache policy →" })).toBeDisabled();
    await expect(page.getByRole("button", { name: "Disable cache →" })).toBeDisabled();
  });
});
