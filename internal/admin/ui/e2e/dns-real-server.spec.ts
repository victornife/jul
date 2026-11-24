/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtemp, readFile, realpath, rm, writeFile } from "node:fs/promises";
import { createServer as createHTTPServer } from "node:http";
import { createServer as createHTTPSServer } from "node:https";
import { createServer as createTCPServer } from "node:net";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { test, expect, request } from "@playwright/test";

test("DNS-01 guided creation stages safely and reports only the serving state", async ({
  page,
}) => {
  test.setTimeout(180_000);
  page.setDefaultTimeout(15_000);
  const root = resolve(import.meta.dirname, "../../../..");
  const dir = await mkdtemp(resolve(await realpath(tmpdir()), "jul-dns-e2e-"));
  const binary = resolve(dir, "jul");
  const configPath = resolve(dir, "jul.toml");
  const token = `jul-dns-e2e-${randomUUID()}`;
  const secret = "c3ludGhldGljLXRzaWcta2V5LWZpeHR1cmU=";
  const env = {
    ...process.env,
    JUL_EFFECTIVE_VERSION_KEY_FILE: resolve(dir, "version.key"),
    JUL_DNS_E2E_TSIG: secret,
  };
  let caConnections = 0;
  let caRequests = 0;
  let dnsConnections = 0;
  const ca = createHTTPSServer(
    {
      cert: await readFile(resolve(root, "testdata/tls/localhost.crt")),
      key: await readFile(resolve(root, "testdata/tls/localhost.key")),
    },
    (_req, res) => {
      caRequests++;
      res.writeHead(500).end("test CA does not issue");
    },
  );
  ca.on("connection", () => {
    caConnections++;
  });
  const dns = createTCPServer((socket) => {
    dnsConnections++;
    socket.end();
  });
  await Promise.all([
    new Promise<void>((ok) => ca.listen(0, "127.0.0.1", ok)),
    new Promise<void>((ok) => dns.listen(0, "127.0.0.1", ok)),
  ]);
  const caAddress = ca.address();
  const dnsAddress = dns.address();
  if (
    !caAddress ||
    typeof caAddress === "string" ||
    !dnsAddress ||
    typeof dnsAddress === "string"
  ) {
    throw new Error("local test endpoints lack TCP addresses");
  }
  const freePort = async () => {
    const listener = createHTTPServer();
    await new Promise<void>((ok) => listener.listen(0, "127.0.0.1", ok));
    const address = listener.address();
    if (!address || typeof address === "string") throw new Error("listener has no TCP address");
    await new Promise<void>((ok, fail) => listener.close((err) => (err ? fail(err) : ok())));
    return address.port;
  };
  const ports = new Set<number>([caAddress.port, dnsAddress.port]);
  const allocatePort = async () => {
    let port = await freePort();
    while (ports.has(port)) port = await freePort();
    ports.add(port);
    return port;
  };
  const adminPort = await allocatePort();
  const servingPort = await allocatePort();
  const tlsPort = await allocatePort();
  const adminURL = `http://127.0.0.1:${adminPort}`;
  const api = await request.newContext({
    baseURL: adminURL,
    extraHTTPHeaders: { Authorization: `Bearer ${token}` },
  });
  let server: ChildProcess | undefined;
  let output = "";
  const start = () => {
    server = spawn(binary, ["serve", "-config", configPath], { cwd: root, env });
    const record = (chunk: Buffer) => {
      output = (output + chunk.toString()).slice(-65_536);
    };
    server.stdout?.on("data", record);
    server.stderr?.on("data", record);
  };
  const stop = async () => {
    if (!server || server.exitCode !== null || server.signalCode !== null) return;
    const exited = new Promise<void>((ok) => server?.once("exit", () => ok()));
    server.kill("SIGTERM");
    await exited;
  };
  const waitReady = async () => {
    await expect
      .poll(
        async () => {
          if (server?.exitCode !== null || server.signalCode !== null)
            throw new Error(`jul exited: ${output}`);
          try {
            return (await api.get("/healthz", { timeout: 500 })).status();
          } catch {
            return 0;
          }
        },
        { timeout: 30_000 },
      )
      .toBe(200);
  };
  try {
    execFileSync("go", ["build", "-tags", "console acme", "-o", binary, "./cmd/jul"], {
      cwd: root,
      env,
      timeout: 120_000,
      stdio: "pipe",
    });
    execFileSync(binary, ["version-key", "init"], { cwd: root, env, stdio: "pipe" });
    const original = `
[global]
config_authority = "managed"
[[servers]]
listen = "127.0.0.1:${servingPort}"
server_names = ["localhost"]
[[servers.locations]]
match = { type = "prefix", path = "/" }
proxy_pass = "http://127.0.0.1:9000"
[admin]
enabled = true
listen = "127.0.0.1:${adminPort}"
token = "${token}"
console = true
history_dir = ${JSON.stringify(resolve(dir, "history"))}
`;
    await writeFile(configPath, original);
    start();
    await waitReady();
    const adoption = await api.get("/api/config/adopt-external/preview");
    expect(adoption.status(), await adoption.text()).toBe(200);
    const adoptData = (await adoption.json()) as { observed_digest: string; base_version: string };
    const adopted = await api.post("/api/config/adopt-external", {
      data: { mode: "hot", confirm: true, ...adoptData },
    });
    expect(adopted.status(), await adopted.text()).toBe(200);

    await page.addInitScript((value) => sessionStorage.setItem("jul_admin_token", value), token);
    await page.goto(`${adminURL}/tls`);
    await page.getByRole("button", { name: "New TLS server" }).click();
    const drawer = page.getByRole("dialog", { name: "New TLS server" });
    await drawer.getByLabel("Listener").fill(`127.0.0.1:${tlsPort}`);
    await drawer.getByRole("textbox", { name: /^Host names/ }).fill("example.test, *.example.test");
    await drawer.getByLabel("Account email").fill("ops@example.test");
    await drawer.getByLabel("Challenge").selectOption("dns-01");
    await drawer.getByLabel("DNS server and port").fill(`127.0.0.1:${dnsAddress.port}`);
    await drawer.getByLabel("DNS update zone").fill("example.test");
    await drawer.getByLabel("TSIG key name").fill("jul.example.test");
    await drawer.getByLabel("TSIG secret reference").fill("${env:JUL_DNS_E2E_TSIG}");
    await drawer.getByLabel("Upstream target").fill("http://127.0.0.1:9000");
    await expect(drawer.getByText(/Draft block only:.*does not enable HTTP\/3/)).toBeVisible();
    await expect(drawer.getByText(/On a shared listener the strongest TLS floor/)).toBeVisible();
    await expect(drawer.getByText(/automatically selects ECDSA P-256 or RSA-2048/)).toBeVisible();
    await expect(drawer.locator("pre")).toContainText('tsig_secret = "${env:JUL_DNS_E2E_TSIG}"');
    await expect(drawer.locator("pre")).not.toContainText(secret);
    await drawer.getByRole("button", { name: /Review in editor/ }).click();
    await expect(page).toHaveURL(/\/config$/);
    const save = page.getByRole("button", { name: "Save for next restart", exact: true });
    await expect(save).toBeEnabled({ timeout: 30_000 });
    expect(await readFile(configPath, "utf8")).toBe(original);
    expect({ caConnections, caRequests, dnsConnections }).toEqual({
      caConnections: 0,
      caRequests: 0,
      dnsConnections: 0,
    });
    expect(await page.locator("body").innerText()).not.toContain(secret);
    await save.click();
    const confirm = page.getByRole("dialog", { name: "Save for next restart?" });
    const acknowledgement = confirm.getByRole("checkbox");
    if (await acknowledgement.count()) await acknowledgement.check();
    await confirm.getByRole("button", { name: "Save for next restart", exact: true }).click();
    await expect(confirm).not.toBeVisible();
    await expect(page.getByText("Restart required — configuration staged")).toBeVisible();
    const staged = await readFile(configPath, "utf8");
    expect(staged).toContain('tsig_secret = "${env:JUL_DNS_E2E_TSIG}"');
    expect(staged).not.toContain(secret);
    expect({ caConnections, caRequests, dnsConnections }).toEqual({
      caConnections: 0,
      caRequests: 0,
      dnsConnections: 0,
    });
    const tlsBeforeRestart = await api.get("/api/tls");
    expect(await tlsBeforeRestart.json()).toEqual([]);
    const beforeStatus = await api.get("/api/status");
    const beforeRows = (await beforeStatus.json()) as { name: string; active: boolean }[];
    expect(beforeRows.find((row) => row.name === "ACME DNS-01 (RFC 2136)")?.active).toBe(false);
    const exported = await api.get("/api/v1/config/export");
    expect(exported.status()).toBe(200);
    expect(await exported.text()).not.toContain(secret);

    await stop();
    const localDirectory = `https://127.0.0.1:${caAddress.port}/directory`;
    expect(staged).toContain('ca = "letsencrypt-staging"');
    await writeFile(
      configPath,
      staged.replace('ca = "letsencrypt-staging"', `ca = "${localDirectory}"`),
    );
    start();
    await waitReady();
    await expect
      .poll(
        async () => {
          const response = await api.get("/api/tls");
          const entries = (await response.json()) as { state?: string }[];
          return entries[0]?.state;
        },
        { timeout: 30_000 },
      )
      .toBe("failed");
    const afterStatus = await api.get("/api/status");
    const afterRows = (await afterStatus.json()) as { name: string; active: boolean }[];
    expect(afterRows.find((row) => row.name === "ACME DNS-01 (RFC 2136)")?.active).toBe(true);
    expect(caConnections).toBeGreaterThan(0);
    expect(dnsConnections).toBe(0);
    await page.goto(`${adminURL}/tls`);
    await expect(page.getByText("example.test", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("*.example.test", { exact: true }).first()).toBeVisible();
    await expect(page.locator("dd").filter({ hasText: `127.0.0.1:${tlsPort}` })).toBeVisible();
    await expect(page.getByText("failed", { exact: true })).toBeVisible();
    expect(await page.locator("body").innerText()).not.toContain(secret);
    expect(output).not.toContain(secret);
  } finally {
    await api.dispose();
    await stop();
    await Promise.all([
      new Promise<void>((ok, fail) => ca.close((err) => (err ? fail(err) : ok()))),
      new Promise<void>((ok, fail) => dns.close((err) => (err ? fail(err) : ok()))),
    ]);
    await rm(dir, { recursive: true, force: true });
  }
});
