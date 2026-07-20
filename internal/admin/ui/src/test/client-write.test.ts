/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Tests for the Console v2 write-path client: validate / apply / wizard, plus
 * the fetch-based SSE transport (bearer header, no token in URL) and the CSP
 * nonce reader.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  authToken,
  validateConfig,
  applyConfig,
  generateConfig,
  subscribeEvents,
  subscribeLogs,
  fetchLogs,
  cspNonce,
  ConfigRejectedError,
  ConfigConflictError,
  ConfigRestartRequiredError,
  ConfigAdminChangeError,
  ApiError,
  ValidationResultSchema,
  ApplyResultSchema,
  type SseEvent,
} from "@/api/client.ts";

type FetchMock = ReturnType<typeof vi.fn>;
const realFetch = globalThis.fetch;

function mockFetch(impl: (url: string, init?: RequestInit) => Response): FetchMock {
  const fn = vi.fn((url: string, init?: RequestInit) => Promise.resolve(impl(url, init)));
  globalThis.fetch = fn as unknown as typeof fetch;
  return fn;
}

function lastInit(fn: FetchMock): RequestInit {
  const call = fn.mock.calls.at(-1);
  return (call?.[1] ?? {}) as RequestInit;
}

function authHeader(init: RequestInit): string | null {
  return new Headers(init.headers).get("Authorization");
}

beforeEach(() => {
  authToken.clear();
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("validateConfig", () => {
  it("posts TOML and returns the parsed result", async () => {
    const fn = mockFetch(
      () => new Response(JSON.stringify({ ok: true, message: "ok" }), { status: 200 }),
    );
    const res = await validateConfig("listen = \":8443\"");
    expect(res.ok).toBe(true);
    expect(fn.mock.calls[0]?.[0]).toBe("/api/config/validate");
    expect(new Headers(lastInit(fn).headers).get("Content-Type")).toBe("application/toml");
  });

  it("attaches the bearer token when configured", async () => {
    authToken.set("sek");
    const fn = mockFetch(() => new Response(JSON.stringify({ ok: true }), { status: 200 }));
    await validateConfig("x = 1");
    expect(authHeader(lastInit(fn))).toBe("Bearer sek");
  });

  it("surfaces structured validation errors", async () => {
    mockFetch(
      () =>
        new Response(
          JSON.stringify({
            ok: false,
            message: "bad",
            errors: [{ code: "unknown_upstream", summary: "no pool", severity: "error" }],
          }),
          { status: 200 },
        ),
    );
    const res = await validateConfig("bad");
    expect(res.ok).toBe(false);
    expect(res.errors?.[0]?.code).toBe("unknown_upstream");
  });
});

describe("applyConfig", () => {
  it("returns the post-apply status on success", async () => {
    const fn = mockFetch(
      () =>
        new Response(
          JSON.stringify({
            ok: true,
            status: [{ group: "Traffic", name: "TLS", active: true }],
          }),
          { status: 200 },
        ),
    );
    const res = await applyConfig("listen = \":8443\"");
    expect(res.status?.[0]?.name).toBe("TLS");
    expect(fn.mock.calls[0]?.[0]).toBe("/api/config/apply");
    expect(new Headers(lastInit(fn).headers).get("Content-Type")).toBe("application/toml");
  });

  it("throws ConfigRejectedError with issues on HTTP 400", async () => {
    mockFetch(
      () =>
        new Response(
          JSON.stringify({
            ok: false,
            message: "rejected",
            errors: [{ code: "bad", summary: "nope", severity: "error" }],
          }),
          { status: 400 },
        ),
    );
    await expect(applyConfig("bad")).rejects.toBeInstanceOf(ConfigRejectedError);
    try {
      await applyConfig("bad");
    } catch (err) {
      expect(err).toBeInstanceOf(ConfigRejectedError);
      if (err instanceof ConfigRejectedError) {
        expect(err.message).toBe("rejected");
        expect(err.issues[0]?.summary).toBe("nope");
      }
    }
  });

  it("throws ApiError on an unstructured failure", async () => {
    mockFetch(() => new Response("nope", { status: 500 }));
    await expect(applyConfig("x")).rejects.toBeInstanceOf(ApiError);
  });

  it("appends base_version as a query param when supplied", async () => {
    const fn = mockFetch(
      () => new Response(JSON.stringify({ ok: true, status: [], version: "abc123" }), { status: 200 }),
    );
    const res = await applyConfig("listen = \":8443\"", "feedface");
    expect(res.version).toBe("abc123");
    expect(fn.mock.calls[0]?.[0]).toBe("/api/config/apply?base_version=feedface");
  });

  it("omits the query param when no base_version is supplied", async () => {
    const fn = mockFetch(
      () => new Response(JSON.stringify({ ok: true, status: [] }), { status: 200 }),
    );
    await applyConfig("listen = \":8443\"");
    expect(fn.mock.calls[0]?.[0]).toBe("/api/config/apply");
  });

  it("throws ConfigConflictError with the current version on HTTP 409", async () => {
    mockFetch(
      () =>
        new Response(
          JSON.stringify({
            ok: false,
            conflict: true,
            message: "stale",
            current_version: "9999",
          }),
          { status: 409 },
        ),
    );
    await expect(applyConfig("x", "stalebase")).rejects.toBeInstanceOf(ConfigConflictError);
    try {
      await applyConfig("x", "stalebase");
    } catch (err) {
      expect(err).toBeInstanceOf(ConfigConflictError);
      if (err instanceof ConfigConflictError) {
        expect(err.message).toBe("stale");
        expect(err.currentVersion).toBe("9999");
      }
    }
  });

  it("throws ConfigRestartRequiredError (not a conflict) on HTTP 409 with restart_required", async () => {
    mockFetch(
      () =>
        new Response(
          JSON.stringify({
            ok: false,
            restart_required: true,
            message: "This change requires a server restart to take effect: ACME domains changed.",
          }),
          { status: 409 },
        ),
    );
    await expect(applyConfig("x")).rejects.toBeInstanceOf(ConfigRestartRequiredError);
    try {
      await applyConfig("x");
    } catch (err) {
      expect(err).toBeInstanceOf(ConfigRestartRequiredError);
      expect(err).not.toBeInstanceOf(ConfigConflictError);
      if (err instanceof ConfigRestartRequiredError) {
        expect(err.message).toContain("restart");
      }
    }
  });

  it("throws ConfigAdminChangeError (not a conflict) on HTTP 409 with admin_change", async () => {
    mockFetch(
      () =>
        new Response(
          JSON.stringify({
            ok: false,
            admin_change: true,
            message: "This change affects how you reach the admin console; re-apply with confirmation to proceed.",
            changes: ["the admin token would change (your current session would need to re-authenticate)"],
          }),
          { status: 409 },
        ),
    );
    await expect(applyConfig("x")).rejects.toBeInstanceOf(ConfigAdminChangeError);
    try {
      await applyConfig("x");
    } catch (err) {
      expect(err).toBeInstanceOf(ConfigAdminChangeError);
      expect(err).not.toBeInstanceOf(ConfigConflictError);
      if (err instanceof ConfigAdminChangeError) {
        expect(err.changes).toHaveLength(1);
        expect(err.changes[0]).toContain("token");
      }
    }
  });

  it("appends confirm_admin=true when confirmAdmin is set", async () => {
    const fn = mockFetch(
      () => new Response(JSON.stringify({ ok: true, status: [], version: "v2" }), { status: 200 }),
    );
    await applyConfig("listen = \":8443\"", "feedface", true);
    expect(fn.mock.calls[0]?.[0]).toBe("/api/config/apply?base_version=feedface&confirm_admin=true");
  });
});

describe("generateConfig", () => {
  it("posts JSON inputs and returns the generated TOML", async () => {
    const fn = mockFetch(
      () => new Response(JSON.stringify({ toml: "listen = \":80\"\n" }), { status: 200 }),
    );
    const toml = await generateConfig({ mode: "proxy", target: "http://x" });
    expect(toml).toContain("listen");
    expect(fn.mock.calls[0]?.[0]).toBe("/api/wizard/generate");
    expect(new Headers(lastInit(fn).headers).get("Content-Type")).toBe("application/json");
  });

  it("throws ApiError when the wizard rejects the input", async () => {
    mockFetch(
      () => new Response(JSON.stringify({ error: "serve mode requires a directory path" }), { status: 400 }),
    );
    await expect(generateConfig({ mode: "serve" })).rejects.toBeInstanceOf(ApiError);
  });
});

describe("subscribeEvents (fetch SSE)", () => {
  it("streams frames using the Authorization header and never the URL", async () => {
    authToken.set("sek");
    const encoder = new TextEncoder();
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode('data: {"type":"connected","time":"t1"}\n\n'));
        controller.enqueue(encoder.encode('data: {"type":"reload","time":"t2"}\n\n'));
        controller.close();
      },
    });
    const fn = mockFetch(() => new Response(stream, { status: 200 }));

    const events: SseEvent[] = [];
    const stop = subscribeEvents((ev) => events.push(ev));
    await vi.waitFor(() => {
      expect(events.length).toBe(2);
    });
    stop();

    expect(fn.mock.calls[0]?.[0]).toBe("/api/events");
    expect(authHeader(lastInit(fn))).toBe("Bearer sek");
    expect(events[0]?.type).toBe("connected");
    expect(events[1]?.type).toBe("reload");
  });

  it("reports a 401 without retrying", async () => {
    const fn = mockFetch(() => new Response("denied", { status: 401 }));
    const errors: unknown[] = [];
    const stop = subscribeEvents(
      () => undefined,
      (err) => errors.push(err),
    );
    await vi.waitFor(() => {
      expect(errors.length).toBe(1);
    });
    stop();
    expect(fn).toHaveBeenCalledTimes(1);
    expect(errors[0]).toBeInstanceOf(ApiError);
  });
});

describe("subscribeLogs (fetch SSE log tail)", () => {
  it("fires onOpen on connect and delivers parsed LogEntry payloads", async () => {
    authToken.set("sek");
    const encoder = new TextEncoder();
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode('data: {"type":"connected","time":"t1"}\n\n'));
        controller.enqueue(
          encoder.encode(
            'data: {"type":"log","time":"t2","data":{"time":"t2","method":"GET","host":"h","path":"/a","status":200,"bytes":3,"duration_ms":1.5}}\n\n',
          ),
        );
        controller.enqueue(encoder.encode('data: {"type":"ping","time":"t3"}\n\n'));
        controller.close();
      },
    });
    const fn = mockFetch(() => new Response(stream, { status: 200 }));

    let opened = false;
    const entries: Array<{ path: string }> = [];
    const stop = subscribeLogs(
      (e) => entries.push(e),
      {
        onOpen: () => {
          opened = true;
        },
      },
    );
    await vi.waitFor(() => {
      expect(entries.length).toBe(1);
    });
    stop();

    expect(fn.mock.calls[0]?.[0]).toBe("/api/observability/logs/stream");
    expect(authHeader(lastInit(fn))).toBe("Bearer sek");
    expect(opened).toBe(true);
    expect(entries[0]?.path).toBe("/a");
  });
});

describe("fetchLogs", () => {
  it("requests the bounded snapshot with a limit and parses entries", async () => {
    const fn = mockFetch(
      () =>
        new Response(
          JSON.stringify([
            { time: "t", method: "GET", host: "h", path: "/x", status: 200, bytes: 0, duration_ms: 2 },
          ]),
          { status: 200 },
        ),
    );
    const rows = await fetchLogs(50);
    expect(rows[0]?.path).toBe("/x");
    expect(fn.mock.calls[0]?.[0]).toBe("/api/observability/logs?limit=50");
  });
});

describe("cspNonce", () => {
  it("reads the injected meta nonce", () => {
    const meta = document.createElement("meta");
    meta.setAttribute("name", "csp-nonce");
    meta.setAttribute("content", "abc123");
    document.head.appendChild(meta);
    expect(cspNonce()).toBe("abc123");
    meta.remove();
  });

  it("returns empty string when absent", () => {
    expect(cspNonce()).toBe("");
  });
});

describe("schemas", () => {
  it("ValidationResultSchema rejects a non-boolean ok", () => {
    expect(() => ValidationResultSchema.parse({ ok: "yes" })).toThrow();
  });

  it("ApplyResultSchema requires ok:true and a status array", () => {
    expect(() => ApplyResultSchema.parse({ ok: false, status: [] })).toThrow();
    const ok = ApplyResultSchema.parse({ ok: true, status: [] });
    expect(ok.status).toHaveLength(0);
  });
});
