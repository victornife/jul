/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as client from "@/api/client.ts";

// Exercise the real client; only the browser network boundary is replaced.
vi.unmock("@/api/client.ts");
const json = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
const lifecycle = { changes: [], can_apply_hot: true, can_stage_restart: true };
type NetworkFetch = (url: string, init: RequestInit) => Promise<Response>;
const ops: client.ConfigPatch[] = [{ op: "global_set", global: { log_level: "debug" } }];

beforeEach(() => {
  sessionStorage.clear();
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  sessionStorage.clear();
});

describe("credential and read contracts", () => {
  it("replaces and clears tab credentials without putting them in URLs or local storage", async () => {
    const fetch = vi.fn<NetworkFetch>().mockImplementation(() => Promise.resolve(json([])));
    vi.stubGlobal("fetch", fetch);
    expect(client.authToken.get()).toBe("");
    await client.fetchHistory();
    client.authToken.set("synthetic-old-token");
    await client.fetchHistory();
    client.authToken.set("synthetic-new-token");
    await client.fetchHistory();
    client.authToken.clear();
    await client.fetchHistory();
    expect(
      fetch.mock.calls.map((call) => new Headers(call[1].headers).get("Authorization")),
    ).toEqual([null, "Bearer synthetic-old-token", "Bearer synthetic-new-token", null]);
    expect(fetch.mock.calls.map((call) => call[0])).toEqual(Array(4).fill("/api/config/history"));
    expect(localStorage.getItem("jul_admin_token")).toBeNull();
    expect(sessionStorage.getItem("jul_admin_token")).toBeNull();
  });

  it.each([401, 403])(
    "broadcasts %i once and never replays the read automatically",
    async (status) => {
      const event = vi.fn<(event: Event) => void>();
      const name = status === 401 ? client.UNAUTHORIZED_EVENT : client.FORBIDDEN_EVENT;
      window.addEventListener(name, event);
      try {
        client.authToken.set("synthetic-secret");
        const fetch = vi
          .fn<NetworkFetch>()
          .mockResolvedValue(json({ error: "request denied" }, status));
        vi.stubGlobal("fetch", fetch);
        await expect(client.fetchHistory()).rejects.toMatchObject({
          name: "ApiError",
          path: "/config/history",
          status,
          message: "request denied",
        });
        expect(event).toHaveBeenCalledTimes(1);
        expect(fetch).toHaveBeenCalledTimes(1);
        expect(fetch.mock.calls[0]?.[0]).not.toContain("synthetic-secret");
        expect((event.mock.calls[0]?.[0] as CustomEvent).detail).toBeNull();
      } finally {
        window.removeEventListener(name, event);
      }
    },
  );

  it.each(["not json", "", "null"])("rejects malformed or empty history: %j", async (body) => {
    vi.stubGlobal("fetch", vi.fn<NetworkFetch>().mockResolvedValue(new Response(body)));
    await expect(client.fetchHistory()).rejects.toBeInstanceOf(Error);
  });

  it.each([
    ["3", 3],
    ["1.6", 2],
    ["invalid", undefined],
    ["-3", 0],
    ["Tue, 13 Oct 2026 12:00:04 GMT", 4],
    ["Tue, 13 Oct 2026 11:59:59 GMT", 0],
  ])("retains bounded Retry-After %s in typed diagnostics", async (header, expected) => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-13T12:00:00Z"));
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response("bad gateway", {
          status: 503,
          headers: { "Retry-After": header },
        }),
      ),
    );
    await expect(client.fetchHistory()).rejects.toMatchObject({
      status: 503,
      retryAfter: expected,
    });
  });

  it("encodes snapshot identities as one path segment and validates the result", async () => {
    const fetch = vi.fn<NetworkFetch>().mockResolvedValue(json({ id: "a/b ?", raw: "[global]" }));
    vi.stubGlobal("fetch", fetch);
    await expect(client.fetchHistorySnapshot("a/b ?")).resolves.toEqual({
      id: "a/b ?",
      raw: "[global]",
    });
    expect(fetch.mock.calls[0]?.[0]).toBe("/api/config/history/a%2Fb%20%3F");
  });

  it.each(["missing header", "403", "network"])(
    "cannot certify a current version after %s",
    async (kind) => {
      const fetch =
        kind === "network"
          ? vi.fn<NetworkFetch>().mockRejectedValue(new TypeError("offline"))
          : vi.fn<NetworkFetch>().mockResolvedValue(json([], kind === "403" ? 403 : 200));
      vi.stubGlobal("fetch", fetch);
      await expect(client.fetchCurrentConfigVersion()).resolves.toBeUndefined();
      expect(fetch).toHaveBeenCalledTimes(1);
    },
  );

  it("reads the authoritative version header with the current bearer", async () => {
    client.authToken.set("synthetic-revision-token");
    const fetch = vi
      .fn<NetworkFetch>()
      .mockResolvedValue(json([], 200, { "X-Jul-Config-Version": "v-current" }));
    vi.stubGlobal("fetch", fetch);
    await expect(client.fetchCurrentConfigVersion()).resolves.toBe("v-current");
    expect(new Headers(fetch.mock.calls[0]?.[1].headers).get("Authorization")).toBe(
      "Bearer synthetic-revision-token",
    );
  });
});

describe("preview, discard and terminal lookup", () => {
  it("pins raw preview bytes and base in the body/header, preserving the server lifecycle", async () => {
    client.authToken.set("synthetic-preview-token");
    const fetch = vi.fn<NetworkFetch>().mockResolvedValue(
      json({
        ok: true,
        base_version: "v1",
        valid: true,
        diff: { summary: "one change" },
        lifecycle,
      }),
    );
    vi.stubGlobal("fetch", fetch);
    const result = await client.previewRawConfig("[global]\nlog_level = 'debug'", "v1");
    expect(result.lifecycle.can_apply_hot).toBe(true);
    expect(result.validation_errors).toEqual([]);
    const [url, init] = fetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/config/preview");
    expect(init.method).toBe("POST");
    expect(init.body).toBe("[global]\nlog_level = 'debug'");
    expect(new Headers(init.headers).get("X-Jul-Base-Version")).toBe("v1");
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer synthetic-preview-token");
  });

  it.each([401, 403, 500])(
    "raw preview rejects %i with malformed response and no replay",
    async (status) => {
      const fetch = vi.fn<NetworkFetch>().mockResolvedValue(new Response("invalid", { status }));
      vi.stubGlobal("fetch", fetch);
      await expect(client.previewRawConfig("[global]", "v1")).rejects.toMatchObject({
        name: "ApiError",
        status,
        path: "/config/preview",
      });
      expect(fetch).toHaveBeenCalledTimes(1);
    },
  );

  it.each([401, 409, 503])("discard rejects %i and never reports success", async (status) => {
    client.authToken.set("synthetic-discard-token");
    const fetch = vi
      .fn<NetworkFetch>()
      .mockResolvedValue(json({ error: "unsafe discard" }, status));
    vi.stubGlobal("fetch", fetch);
    await expect(client.discardPendingRestart()).rejects.toMatchObject({
      status,
      message: "unsafe discard",
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0]?.[0]).toBe("/api/config/pending-restart/discard");
    expect(fetch.mock.calls[0]?.[1].method).toBe("POST");
  });

  it("discards with no credential, validates the result, and distinguishes returned messages", async () => {
    const fetch = vi
      .fn<NetworkFetch>()
      .mockResolvedValueOnce(json({ ok: true }))
      .mockResolvedValueOnce(json({ message: "disk changed" }, 409))
      .mockResolvedValueOnce(new Response("invalid", { status: 500 }));
    vi.stubGlobal("fetch", fetch);
    await expect(client.discardPendingRestart()).resolves.toMatchObject({ ok: true });
    await expect(client.discardPendingRestart()).rejects.toMatchObject({ message: "disk changed" });
    await expect(client.discardPendingRestart()).rejects.toMatchObject({ status: 500 });
    expect(new Headers(fetch.mock.calls[0]?.[1].headers).has("Authorization")).toBe(false);
  });

  it.each([401, 403, 500])(
    "exact-ID lookup rejects %i without interpreting it as terminal",
    async (status) => {
      client.authToken.set("synthetic-status-token");
      const fetch = vi.fn<NetworkFetch>().mockResolvedValue(new Response("invalid", { status }));
      vi.stubGlobal("fetch", fetch);
      await expect(client.fetchManagedApply("apply/a")).rejects.toMatchObject({ status });
      expect(fetch.mock.calls[0]?.[0]).toBe("/api/config/applies/apply%2Fa");
      expect(new Headers(fetch.mock.calls[0]?.[1].headers).get("Cache-Control")).toBe("no-store");
      expect(fetch).toHaveBeenCalledTimes(1);
    },
  );

  it.each([400, 401, 403, 409, 500])(
    "protected candidate rejects %i without credential or retry",
    async (status) => {
      const fetch = vi.fn<NetworkFetch>().mockResolvedValue(new Response("invalid", { status }));
      vi.stubGlobal("fetch", fetch);
      await expect(client.fetchPatchCandidate(ops, "v1")).rejects.toBeInstanceOf(client.ApiError);
      expect(fetch).toHaveBeenCalledTimes(1);
      expect(JSON.parse(fetch.mock.calls[0]?.[1].body as string)).toEqual({
        base_version: "v1",
        ops,
      });
    },
  );
});

describe("mutations do not replay after auth or transport failures", () => {
  const mutations = [
    { name: "raw apply", path: "/api/config/apply", run: () => client.applyConfig("[global]") },
    {
      name: "patch apply",
      path: "/api/config/patch/apply",
      run: () => client.applyPatchBatch(ops, "v1"),
    },
    { name: "rollback", path: "/api/config/rollback", run: () => client.rollback("snap/a") },
  ];
  for (const mutation of mutations) {
    it.each([401, 403])(
      `${mutation.name}: rejection %i broadcasts and never retries`,
      async (status) => {
        const event = vi.fn<(event: Event) => void>();
        const name = status === 401 ? client.UNAUTHORIZED_EVENT : client.FORBIDDEN_EVENT;
        window.addEventListener(name, event);
        try {
          const fetch = vi
            .fn<NetworkFetch>()
            .mockResolvedValue(new Response("invalid", { status }));
          vi.stubGlobal("fetch", fetch);
          await expect(mutation.run()).rejects.toMatchObject({ status });
          expect(fetch).toHaveBeenCalledTimes(1);
          expect(fetch.mock.calls[0]?.[0]).toBe(mutation.path);
          expect(event).toHaveBeenCalledTimes(1);
        } finally {
          window.removeEventListener(name, event);
        }
      },
    );
    it(`${mutation.name}: connection failure is preserved, never silently resent`, async () => {
      const error = new TypeError("synthetic network failure");
      const fetch = vi.fn<NetworkFetch>().mockRejectedValue(error);
      vi.stubGlobal("fetch", fetch);
      await expect(mutation.run()).rejects.toBe(error);
      expect(fetch).toHaveBeenCalledTimes(1);
    });
  }
});
