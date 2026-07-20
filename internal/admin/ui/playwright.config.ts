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
// the Zod schemas in client.ts without any mocking. The binary must be
// pre-built before running. Include the "grpc" tag so the gRPC passthrough
// tests can hot-reload:
//   go build -tags "console grpc" -o jul ./cmd/jul
const REAL_SERVER_PORT = 9291;
const realServerURL = `http://127.0.0.1:${String(REAL_SERVER_PORT)}`;

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? "github" : "list",
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
      // real-server project: API-level tests against a real jul binary.
      // Does not use a browser; Playwright's request fixture is sufficient.
      name: "real-server",
      testMatch: "e2e/real-server.spec.ts",
      use: {
        baseURL: realServerURL,
        extraHTTPHeaders: { Authorization: "Bearer jul-e2e-test-token" },
      },
    },
  ],
  // webServer entries: the mocked-API smoke test uses vite preview; the
  // real-server tests use a pre-built jul binary (built in CI before this job,
  // or locally via: go build -tags console -o jul ./cmd/jul from repo root).
  webServer: [
    {
      command: `node node_modules/vite/bin/vite.js preview --port ${String(PREVIEW_PORT)}`,
      url: baseURL,
      reuseExistingServer: !process.env.CI,
      timeout: 30_000,
    },
    {
      // Path to the pre-built jul binary is relative to the ui/ directory.
      // In CI, the console-e2e job builds it into the repo root first.
      command: "cd ../../../ && ./jul serve -config testdata/console-e2e.toml",
      url: `${realServerURL}/healthz`,
      reuseExistingServer: !process.env.CI,
      timeout: 30_000,
    },
  ],
});
