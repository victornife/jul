/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 *
 * One-time setup for the real-server Playwright project: testdata/console-e2e.toml
 * declares `config_authority = "managed"` (ADR 0019 §9), so a fresh boot starts
 * `managed_unadopted` and refuses every write the real-server/issue82-phase5
 * specs exercise until the managed baseline is explicitly established (ADR 0019
 * §11.2.1 — no implicit first-boot adoption). This performs that one adoption
 * call against the already-running fixture bytes before any spec runs.
 */

import { request } from "@playwright/test";

const REAL_SERVER_PORT = 9291;
const realServerURL = `http://127.0.0.1:${String(REAL_SERVER_PORT)}`;

export default async function globalSetup(): Promise<void> {
  const selectedProjects = process.argv
    .filter((arg) => arg.startsWith("--project="))
    .map((arg) => arg.slice("--project=".length));
  const runRealServer =
    selectedProjects.length === 0 || selectedProjects.includes("real-server");
  if (!runRealServer) {
    return;
  }

  const ctx = await request.newContext({
    baseURL: realServerURL,
    extraHTTPHeaders: { Authorization: "Bearer jul-e2e-test-token" },
  });
  try {
    // Adoption binds itself to the preview's observed_digest/base_version
    // (ADR 0019 §14 step 6); the apply step rejects a request that omits them.
    const previewRes = await ctx.get("/api/config/adopt-external/preview");
    if (!previewRes.ok()) {
      throw new Error(
        `real-server global setup: adopt-external preview failed: ${String(previewRes.status())} ${await previewRes.text()}`,
      );
    }
    const preview = (await previewRes.json()) as {
      observed_digest: string;
      base_version?: string;
    };

    const res = await ctx.post("/api/config/adopt-external", {
      data: {
        mode: "hot",
        confirm: true,
        observed_digest: preview.observed_digest,
        base_version: preview.base_version,
      },
    });
    if (!res.ok()) {
      throw new Error(
        `real-server global setup: adopt-external failed: ${String(res.status())} ${await res.text()}`,
      );
    }
  } finally {
    await ctx.dispose();
  }
}
