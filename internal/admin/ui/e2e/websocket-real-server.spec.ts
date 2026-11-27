/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtemp, readFile, realpath, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { expect, request, test, type Page } from "@playwright/test";
import { AppProjectionSchema, HistoryEntrySchema, RawConfigSchema } from "../src/api/client.ts";

test.use({ extraHTTPHeaders: {}, actionTimeout: 15_000 });

test("Apps keyboard flow previews and applies native H3, establishes a trusted tunnel, and rolls back", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const root = resolve(import.meta.dirname, "../../../..");
  const dir = await mkdtemp(resolve(await realpath(tmpdir()), "jul-websocket-e2e-"));
  const binary = resolve(dir, "jul");
  const backendBinary = resolve(dir, "backend.test");
  const readyPath = resolve(dir, "backend.json");
  const token = `jul-websocket-e2e-${randomUUID()}`;
  const env = { ...process.env, JUL_EFFECTIVE_VERSION_KEY_FILE: resolve(dir, "version.key") };
  const freePort = async () => {
    const listener = createServer();
    await new Promise<void>((ok) => listener.listen(0, "127.0.0.1", ok));
    const address = listener.address();
    if (!address || typeof address === "string") throw new Error("listener has no TCP address");
    await new Promise<void>((ok, fail) => listener.close((err) => (err ? fail(err) : ok())));
    return address.port;
  };
  const adminPort = await freePort();
  let servingPort = await freePort();
  while (servingPort === adminPort) servingPort = await freePort();
  const adminURL = `http://127.0.0.1:${adminPort}`;
  const api = await request.newContext({
    baseURL: adminURL,
    extraHTTPHeaders: { Authorization: `Bearer ${token}` },
  });
  let backend: ChildProcess | undefined;
  let jul: ChildProcess | undefined;
  let clientPage: Page | undefined;
  let output = "";
  const record = (child: ChildProcess) => {
    const append = (data: Buffer) => {
      output = (output + data.toString()).slice(-65_536);
    };
    child.stdout?.on("data", append);
    child.stderr?.on("data", append);
  };
  const stop = async (child: ChildProcess | undefined, stdin = false) => {
    if (!child || child.exitCode !== null || child.signalCode !== null) return;
    const exited = new Promise<number | null>((ok) => child.once("exit", ok));
    if (stdin) child.stdin?.end();
    else child.kill("SIGTERM");
    return exited;
  };
  let fixtureExit: number | null | undefined;
  try {
    execFileSync("go", ["build", "-tags", "console http3", "-o", binary, "./cmd/jul"], {
      cwd: root,
      env,
      timeout: 120_000,
      stdio: "pipe",
    });
    execFileSync(
      "go",
      ["test", "-c", "-tags", "http3", "-o", backendBinary, "./internal/handler"],
      {
        cwd: root,
        env,
        timeout: 120_000,
        stdio: "pipe",
      },
    );
    backend = spawn(backendBinary, ["-test.run=^TestWebSocketConsoleHTTP3Fixture$"], {
      cwd: root,
      env: { ...env, JUL_WEBSOCKET_E2E_READY: readyPath },
    });
    record(backend);
    await expect
      .poll(async () => {
        if (backend?.exitCode !== null || backend.signalCode !== null) throw new Error(output);
        try {
          await readFile(readyPath);
          return true;
        } catch (error) {
          if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
          return false;
        }
      })
      .toBe(true);
    const fixture = JSON.parse(await readFile(readyPath, "utf8")) as {
      address: string;
      ca: string;
      client_cert: string;
      client_key: string;
    };
    execFileSync(binary, ["version-key", "init"], { cwd: root, env, stdio: "pipe" });
    const configPath = resolve(dir, "jul.toml");
    await writeFile(
      configPath,
      `
[global]
config_authority = "managed"
shutdown_timeout = "3s"
[[upstreams]]
name = "inventory"
servers = [${JSON.stringify(fixture.address)}]
[upstreams.backend_tls]
ca_mode = "file_only"
ca_file = ${JSON.stringify(fixture.ca)}
server_name = "inventory.internal"
client_cert = ${JSON.stringify(fixture.client_cert)}
client_key = ${JSON.stringify(fixture.client_key)}
[[servers]]
listen = "127.0.0.1:${servingPort}"
[[servers.locations]]
route_id = "console-websocket"
match = { type = "prefix", path = "/ws" }
proxy_pass = "https://inventory"
[[servers.locations]]
match = { type = "exact", path = "/client" }
return = 200
[admin]
enabled = true
listen = "127.0.0.1:${adminPort}"
token = "${token}"
console = true
history_dir = ${JSON.stringify(resolve(dir, "history"))}
`,
    );
    jul = spawn(binary, ["serve", "-config", configPath], { cwd: root, env });
    record(jul);
    await expect
      .poll(
        async () => {
          if (jul?.exitCode !== null || jul.signalCode !== null) throw new Error(output);
          try {
            return (await api.get("/healthz", { timeout: 500 })).status();
          } catch {
            return 0;
          }
        },
        { timeout: 30_000 },
      )
      .toBe(200);
    const adoption = await api.get("/api/config/adopt-external/preview");
    expect(adoption.status(), await adoption.text()).toBe(200);
    const adopted = await api.post("/api/config/adopt-external", {
      data: { mode: "hot", confirm: true, ...(await adoption.json()) },
    });
    expect(adopted.status(), await adopted.text()).toBe(200);

    await page.goto(`${adminURL}/apps`);
    const login = page.getByRole("dialog", { name: "Admin token required" });
    await login.getByLabel("Token").fill(token);
    await login.getByRole("button", { name: "Save & retry" }).press("Enter");
    await page.getByRole("button", { name: "Open App inventory", exact: true }).press("Enter");
    const detail = page.getByRole("dialog", { name: "inventory", exact: true });
    await detail
      .getByRole("combobox", { name: "WebSocket upstream protocol" })
      .selectOption("http3");
    const previewResponse = page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/config/patch/preview") &&
        response.request().method() === "POST",
    );
    await detail.getByRole("button", { name: "Review WebSocket protocol" }).press("Enter");
    const preview = await previewResponse;
    expect(preview.status(), await preview.text()).toBe(200);
    expect(preview.request().postDataJSON()).toMatchObject({
      ops: [
        {
          op: "upstream_set_websocket_protocol",
          upstream: "inventory",
          websocket_upstream_protocol: "http3",
        },
      ],
    });
    await expect(page).toHaveURL(/\/config$/);
    const applyButton = page.getByRole("button", { name: "Apply live", exact: true });
    await expect(applyButton).toBeEnabled();
    await applyButton.press("Enter");
    const apply = page.getByRole("dialog", { name: "Apply live?" });
    await expect(apply).toBeVisible();
    const acknowledgment = apply.getByRole("checkbox");
    if (await acknowledgment.count()) await acknowledgment.check();
    const confirmApply = apply.getByRole("button", { name: "Apply live", exact: true });
    await expect(confirmApply).toBeEnabled();
    await confirmApply.press("Enter");
    await expect(apply).not.toBeVisible();
    const apps = async () => {
      const response = await api.get("/api/apps");
      expect(response.status(), await response.text()).toBe(200);
      return AppProjectionSchema.array().parse(await response.json());
    };
    await expect.poll(async () => (await apps())[0]?.websocket_upstream_protocol).toBe("http3");
    const trafficPage = await page.context().newPage();
    clientPage = trafficPage;
    await trafficPage.goto(`http://127.0.0.1:${servingPort}/client`);
    const exchange = (message: string) =>
      trafficPage.evaluate(
        ({ url, message }) =>
          new Promise<string>((ok, fail) => {
            const state = window as Window & { julWebSocket?: WebSocket };
            const socket = state.julWebSocket ?? new WebSocket(url, "chat");
            state.julWebSocket = socket;
            socket.onerror = () => fail(new Error("WebSocket failed"));
            socket.onmessage = (event: MessageEvent<string>) =>
              ok(`${socket.protocol}:${event.data}`);
            if (socket.readyState === WebSocket.OPEN) socket.send(message);
            else socket.onopen = () => socket.send(message);
          }),
        { url: `ws://127.0.0.1:${servingPort}/ws`, message },
      );
    expect(await exchange("before rollback")).toBe("chat:before rollback");

    const history = HistoryEntrySchema.array().parse(
      await (await api.get("/api/config/history")).json(),
    );
    const previous = history[0];
    if (!previous) throw new Error("apply created no rollback snapshot");
    const current = RawConfigSchema.parse(await (await api.get("/api/config")).json());
    const rolledBack = await api.post("/api/config/rollback", {
      data: { id: previous.id, base_version: current.base_version },
    });
    expect(rolledBack.status(), await rolledBack.text()).toBe(200);
    await expect
      .poll(async () => (await apps())[0]?.websocket_upstream_protocol ?? "http1")
      .toBe("http1");
    expect(await exchange("after rollback")).toBe("chat:after rollback");
    const newHandshake = await trafficPage.evaluate(
      (url) =>
        new Promise<string>((ok) => {
          const socket = new WebSocket(url, "chat");
          socket.onopen = () => {
            socket.close();
            ok("opened");
          };
          socket.onerror = () => {
            socket.close();
            ok("rejected");
          };
        }),
      `ws://127.0.0.1:${servingPort}/ws`,
    );
    expect(newHandshake).toBe("rejected");
    expect(await exchange("sibling after failed H1 handshake")).toBe(
      "chat:sibling after failed H1 handshake",
    );
    await clientPage.evaluate(() =>
      (window as Window & { julWebSocket?: WebSocket }).julWebSocket?.close(),
    );
  } finally {
    await clientPage?.close();
    await api.dispose();
    await stop(jul);
    try {
      fixtureExit = await stop(backend, true);
    } finally {
      await rm(dir, { recursive: true, force: true });
      await test.info().attach("fixture-output", { body: output, contentType: "text/plain" });
    }
  }
  expect(fixtureExit, output).toBe(0);
});
