/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { defineConfig, devices } from "@playwright/test";

// vite preview serves the already-built SPA from the dist directory at a stable
// URL so the Playwright test can intercept every API call via page.route() and
// exercise the full rendered SPA without starting the Go server.
const PREVIEW_PORT = 4173;
const baseURL = `http://localhost:${String(PREVIEW_PORT)}`;

// Real-server project: a real jul binary serves the admin API on loopback so
// real-server.spec.ts can validate that the Go admin API response shapes match
// the Zod schemas in client.ts without any mocking. Playwright rebuilds the
// console-enabled binary after the SPA build so Go embeds the exact frontend
// under test. Include the "grpc" tag so the gRPC passthrough tests can hot-reload.
const REAL_SERVER_PORT = 9291;
const realServerURL = `http://127.0.0.1:${String(REAL_SERVER_PORT)}`;

// Determine whether the real-server project is selected so the browser-smoke
// job does not need Go or a jul binary (exit 127).
const selectedProjects = process.argv
  .filter((arg) => arg.startsWith("--project="))
  .map((arg) => arg.slice("--project=".length));
const runRealServer =
  selectedProjects.length === 0 || selectedProjects.includes("real-server");

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? "github" : "list",
  // Establishes the managed baseline for the real-server project once, after
  // its webServer entry is confirmed healthy and before any spec runs; a
  // no-op for the mocked chromium project. See e2e/global-setup.ts.
  globalSetup: "./e2e/global-setup.ts",
  // The real-server files share one mutable jul process/config/history store.
  // Run them sequentially so stateful lifecycle tests cannot invalidate each
  // other's pending drafts, snapshots, or live configuration mid-assertion.
  workers: runRealServer ? 1 : undefined,
  use: {
    headless: true,
    // Suppress verbose browser logs in CI but keep them locally for debugging.
    video: process.env.CI ? "retain-on-failure" : "off",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "chromium",
      testMatch: "e2e/smoke.spec.ts",
      use: { ...devices["Desktop Chrome"], baseURL },
    },
    {
      // real-server project: API-level and browser tests against a real jul binary.
      name: "real-server",
      testMatch: ["e2e/real-server.spec.ts", "e2e/issue82-phase5.spec.ts"],
      use: {
        baseURL: realServerURL,
        extraHTTPHeaders: { Authorization: "Bearer jul-e2e-test-token" },
      },
    },
  ],
  // webServer entries: the mocked-API smoke test uses vite preview; the
  // real-server project rebuilds jul at launch so the binary embeds the SPA that
  // the job just produced, rather than an older committed dist snapshot.
  webServer: [
    {
      command: `node node_modules/vite/bin/vite.js preview --port ${String(PREVIEW_PORT)}`,
      url: baseURL,
      reuseExistingServer: !process.env.CI,
      timeout: 30_000,
    },
    ...(runRealServer
      ? [
          {
            command:
              'cd ../../../ && go build -tags "console grpc" -o jul ./cmd/jul && ./jul serve -config testdata/console-e2e.toml',
            url: `${realServerURL}/healthz`,
            reuseExistingServer: !process.env.CI,
            timeout: 60_000,
          },
        ]
      : []),
  ],
});
