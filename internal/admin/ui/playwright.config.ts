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

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL,
    headless: true,
    // Suppress verbose browser logs in CI but keep them locally for debugging.
    video: process.env.CI ? "retain-on-failure" : "off",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  // The SPA must be pre-built before `pnpm e2e` runs. In CI, the build step
  // precedes this job; locally, run `pnpm build` first or set
  // SKIP_BUILD=1 if dist is already current.
  webServer: {
    command: `node node_modules/vite/bin/vite.js preview --port ${String(PREVIEW_PORT)}`,
    url: baseURL,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
});
