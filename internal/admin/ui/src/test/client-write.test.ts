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
  cspNonce,
  ConfigRejectedError,
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
    expect(res.status[0]?.name).toBe("TLS");
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
