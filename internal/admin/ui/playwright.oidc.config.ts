/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl */
import { defineConfig, devices } from "@playwright/test";
export default defineConfig({
  testDir: "./e2e",
  testMatch: "oidc.spec.ts",
  workers: 1,
  retries: 0,
  timeout: 60_000,
  use: { ...devices["Desktop Chrome"], baseURL: "http://127.0.0.1:9296", headless: true },
  webServer: {
    command:
      "cd ../../.. && JUL_OIDC_BROWSER=1 go test -tags console ./internal/admin -run ^TestOIDCBrowserHarness$ -count=1 -timeout=6m",
    url: "http://127.0.0.1:9296/healthz",
    timeout: 90_000,
    reuseExistingServer: false,
  },
});
