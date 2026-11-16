/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { execFileSync, spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtemp, realpath, rm, writeFile } from "node:fs/promises";
import { createServer, type ServerResponse } from "node:http";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { test, expect, request } from "@playwright/test";
import { OverviewSchema, RawConfigSchema } from "../src/api/client.ts";

test.use({ extraHTTPHeaders: {} });

test("packaged WAF refusal, observe and draining appear in Overview", async ({ page }) => {
  test.setTimeout(180_000);
  const root = resolve(import.meta.dirname, "../../../..");
  const dir = await mkdtemp(resolve(await realpath(tmpdir()), "jul-inspection-e2e-"));
  const binary = resolve(dir, "jul");
  const token = `jul-inspection-e2e-${randomUUID()}`;
  const env = { ...process.env, JUL_EFFECTIVE_VERSION_KEY_FILE: resolve(dir, "version.key") };
  const held: ServerResponse[] = [];
  let originCalls = 0;
  const origin = createServer((_req, res) => {
    originCalls++;
    held.push(res);
  });
  await new Promise<void>((ok) => origin.listen(0, "127.0.0.1", ok));
  const originAddress = origin.address();
  if (!originAddress || typeof originAddress === "string") throw new Error("origin has no TCP address");
  const freePort = async () => {
    const listener = createServer();
    await new Promise<void>((ok) => listener.listen(0, "127.0.0.1", ok));
    const address = listener.address();
    if (!address || typeof address === "string") throw new Error("listener has no TCP address");
    await new Promise<void>((ok, fail) => listener.close((err) => err ? fail(err) : ok()));
    return address.port;
  };
  const adminPort = await freePort();
  let servingPort = await freePort();
  while (servingPort === adminPort) servingPort = await freePort();
  const adminURL = `http://127.0.0.1:${adminPort}`;
  const servingURL = `http://127.0.0.1:${servingPort}`;
  const api = await request.newContext({
    baseURL: adminURL,
    extraHTTPHeaders: { Authorization: `Bearer ${token}` },
    timeout: 150_000,
  });
  const traffic = await request.newContext({ timeout: 150_000 });
  let server: ReturnType<typeof spawn> | undefined;
  let output = "";
  const pending: Promise<{ status: number } | { error: unknown }>[] = [];
  const sendTraffic = () => {
    pending.push(traffic.get(servingURL).then(
      (response) => ({ status: response.status() }),
      (error: unknown) => ({ error }),
    ));
  };
  try {
    execFileSync("go", ["build", "-tags", "console waf", "-o", binary, "./cmd/jul"], {
      cwd: root, env, timeout: 120_000, stdio: "pipe",
    });
    execFileSync(binary, ["version-key", "init"], { cwd: root, env, stdio: "pipe" });
    const configPath = resolve(dir, "jul.toml");
    await writeFile(configPath, `
[global]
config_authority = "managed"
[global.inspection]
mode = "enforce"
max_buffered_bytes = "64m"
max_response_header_bytes = "8k"
[waf]
enabled = true
mode = "block"
request_body_limit = "1k"
inline_rules = """
SecArgumentsLimit 1
SecResponseBodyLimit 1024
"""
[[servers]]
listen = "127.0.0.1:${servingPort}"
server_names = ["localhost"]
max_header_bytes = "8k"
[[servers.locations]]
match = { type = "prefix", path = "/" }
proxy_pass = "http://127.0.0.1:${originAddress.port}"
[admin]
enabled = true
listen = "127.0.0.1:${adminPort}"
token = "${token}"
console = true
history_dir = ${JSON.stringify(resolve(dir, "history"))}
`);
    server = spawn(binary, ["serve", "-config", configPath], { cwd: root, env });
    const recordOutput = (chunk: Buffer) => { output = (output + chunk.toString()).slice(-65_536); };
    server.stdout?.on("data", recordOutput);
    server.stderr?.on("data", recordOutput);
    await expect.poll(async () => {
      if (server?.exitCode !== null || server.signalCode !== null) throw new Error(`jul exited: ${output}`);
      try {
        return (await api.get("/healthz", { timeout: 500 })).status();
      } catch {
        return 0;
      }
    }, { timeout: 30_000 }).toBe(200);
    const adoption = await api.get("/api/config/adopt-external/preview");
    expect(adoption.status(), await adoption.text()).toBe(200);
    const preview = await adoption.json() as { observed_digest: string; base_version: string };
    const adopted = await api.post("/api/config/adopt-external", {
      data: { mode: "hot", confirm: true, ...preview },
    });
    expect(adopted.status(), await adopted.text()).toBe(200);

    const snapshot = async () => {
      const response = await api.get("/api/runtime/overview");
      expect(response.status(), await response.text()).toBe(200);
      const inspection = OverviewSchema.parse(await response.json()).inspection;
      if (!inspection) throw new Error("inspection projection unavailable");
      return inspection;
    };
    const initial = await snapshot();
    expect(initial.minimum_request_bytes).toBeGreaterThan(0);
    let active = initial;
    for (let count = 1; count <= 64 && !active.draining; count++) {
      sendTraffic();
      await expect.poll(() => originCalls).toBe(count);
      active = await snapshot();
      const used = active.usage.base_bytes + active.usage.buffer_bytes +
        active.usage.waf_state_bytes + (active.usage.audit_bytes ?? 0);
      if (used > (initial.minimum_request_bytes ?? 0)) break;
    }
    const used = active.usage.base_bytes + active.usage.buffer_bytes +
      active.usage.waf_state_bytes + (active.usage.audit_bytes ?? 0);
    expect(used).toBeGreaterThan(initial.minimum_request_bytes ?? 0);
    const baselineResponse = await api.get("/api/config");
    const baseline = RawConfigSchema.parse(await baselineResponse.json());
    const apply = await api.post("/api/config/patch/apply", {
      data: {
        base_version: baseline.base_version,
        ops: [{ op: "global_set", global: { inspection: {
          max_buffered_bytes: String(used - 1),
        } } }],
      },
    });
    expect(apply.status(), await apply.text()).toBe(200);
    await expect.poll(async () => (await snapshot()).draining).toBe(true);

    await page.goto(`${adminURL}/?inspection=1`);
    const tokenDialog = page.getByRole("dialog", { name: "Admin token required" });
    await tokenDialog.getByLabel("Token").fill(token);
    await tokenDialog.getByRole("button", { name: "Save & retry" }).click();
    const card = page.getByRole("region", { name: "Inspection resources" });
    await expect(card.getByText(/Mode: enforce\..*Draining\./)).toBeVisible();
    const callsBefore = originCalls;
    const refused = await traffic.get(servingURL);
    expect(refused.status()).toBe(503);
    expect(refused.headers()["retry-after"]).toBe("1");
    expect((await refused.text()).trim()).toBe("inspection_overloaded");
    expect(originCalls).toBe(callsBefore);

    const currentResponse = await api.get("/api/config");
    const current = RawConfigSchema.parse(await currentResponse.json());
    const observe = await api.post("/api/config/patch/apply", {
      data: {
        base_version: current.base_version,
        ops: [{ op: "global_set", global: { inspection: { mode: "observe" } } }],
      },
    });
    expect(observe.status(), await observe.text()).toBe(200);
    await expect.poll(async () => (await snapshot()).effective.mode).toBe("observe");
    sendTraffic();
    await expect.poll(() => originCalls).toBe(callsBefore + 1);
    await expect(card.getByText(/Mode: observe\..*Draining\./)).toBeVisible();
    await expect(card.getByText(/without governor-driven request refusal/)).toBeVisible();
    for (const response of held) response.writeHead(204).end();
    expect(await Promise.all(pending)).toEqual(Array<{ status: number }>(pending.length).fill({ status: 204 }));
    await expect.poll(async () => {
      const state = await snapshot();
      return { draining: state.draining, leases: state.active_leases, usage: state.usage };
    }).toEqual({
      draining: false,
      leases: 0,
      usage: { base_bytes: 0, buffer_bytes: 0, waf_state_bytes: 0, audit_bytes: 0 },
    });
    await expect(card.getByText(/Mode: observe\./)).toBeVisible();
    await expect(card.getByText(/Draining\./)).toHaveCount(0);
  } finally {
    for (const response of held) {
      if (!response.writableEnded) response.end();
    }
    await traffic.dispose();
    await Promise.all(pending);
    await api.dispose();
    if (server && server.exitCode === null && server.signalCode === null) {
      const exited = new Promise<void>((ok) => server?.once("exit", () => ok()));
      server.kill("SIGTERM");
      await exited;
    }
    await new Promise<void>((ok, fail) => origin.close((err) => err ? fail(err) : ok()));
    await rm(dir, { recursive: true, force: true });
  }
});
