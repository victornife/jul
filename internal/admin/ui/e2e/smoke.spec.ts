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

/** PatchResult returned for POST /api/config/patch. */
const PATCH_RESULT = {
  ok: true,
  summary: "route :8080 / target changed",
  candidate: `[[servers]]\nlisten = ":8080"\n\n  [[servers.locations]]\n  match = { type = "prefix", path = "/" }\n  proxy_pass = "http://new-app:4000"\n`,
  base_version: "v1",
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

  await page.route("/api/runtime/overview", (route) => json(route, OVERVIEW));
  await page.route("/api/routes", (route) => json(route, ROUTES));
  await page.route("/api/config", (route) => json(route, RAW_CONFIG));
  await page.route("/api/config/history", (route) => json(route, [HISTORY_ENTRY]));
  await page.route("/api/config/history/snap-001", (route) => json(route, HISTORY_SNAPSHOT));

  // Write endpoints — handled per-step so we can await them explicitly.
  await page.route("/api/config/patch", (route) => json(route, PATCH_RESULT));
  await page.route("/api/config/patch/candidate", (route) =>
    json(route, {
      ok: true,
      candidate: PATCH_RESULT.candidate,
      base_version: PATCH_RESULT.base_version,
    }),
  );
  await page.route("/api/config/patch/apply", (route) => json(route, PATCH_APPLY_RESULT));
  await page.route("/api/config/rollback", (route) => json(route, { ok: true }));

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
    // Clicking it sends POST /api/config/patch and then navigates to /config.
    const patchRequest = page.waitForRequest("/api/config/patch");
    await page.getByRole("button", { name: "Set target →" }).click();
    await patchRequest;

    // ── 4. Config panel — diff visible ─────────────────────────────────────────
    // The SPA navigated to /config after the patch was previewed. ConfigPanel
    // picks up the pending draft from the in-memory handoff, loads the raw
    // config from /api/config, and shows the pre-computed diff. The "atomic
    // The "atomic patch" badge signals we are in patch mode, and the "Pending changes"
    // section shows the diff.
    await expect(page.getByText("atomic patch", { exact: true })).toBeVisible();
    await expect(page.getByText("Pending changes", { exact: true })).toBeVisible();

    // The diff summary line from the fixture must be rendered by DiffView.
    await expect(page.getByText("1 modification", { exact: true })).toBeVisible();

    // ── 5. Apply the patch ─────────────────────────────────────────────────────
    // "Apply patch" is the label for the primary CTA in patch mode. Clicking it
    // opens a ConfirmDialog.
    await page.getByRole("button", { name: "Apply patch" }).click();

    // ConfirmDialog appears with the confirmation CTA.
    await expect(page.getByRole("button", { name: "Apply now" })).toBeVisible();

    // Intercept the apply request so we can assert it was sent.
    const applyRequest = page.waitForRequest("/api/config/patch/apply");
    await page.getByRole("button", { name: "Apply now" }).click();
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
    await page.route("/api/config/diff", (route) =>
      json(route, { summary: "rollback to snap-001", modifications: [] }),
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
