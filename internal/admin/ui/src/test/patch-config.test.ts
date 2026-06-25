import { describe, it, expect, vi, afterEach } from "vitest";
import { patchConfig, ConfigRejectedError, ApiError } from "@/api/client.ts";

const realFetch = globalThis.fetch;

function mockFetch(fn: (url: string, init?: RequestInit) => Response): void {
  globalThis.fetch = vi.fn((input: string, init?: RequestInit) =>
    Promise.resolve(fn(input, init)),
  ) as unknown as typeof fetch;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("patchConfig", () => {
  it("posts the patch and returns candidate + diff on success", async () => {
    let seenBody = "";
    mockFetch((url, init) => {
      expect(url).toBe("/api/config/patch");
      seenBody = typeof init?.body === "string" ? init.body : "";
      return json({
        ok: true,
        summary: "route :8080/api proxy_pass set to http://new",
        candidate: 'listen = ":8080"\n',
        diff: { summary: "1 change" },
      });
    });
    const res = await patchConfig({
      op: "route_set_target",
      listen: ":8080",
      server_names: [],
      match_type: "prefix",
      path: "/api",
      target: "http://new",
    });
    expect(res.summary).toContain("proxy_pass set to http://new");
    expect(res.candidate).toContain('listen = ":8080"');
    expect(res.diff.summary).toBe("1 change");
    expect(JSON.parse(seenBody)).toMatchObject({ op: "route_set_target", target: "http://new" });
  });

  it("throws ConfigRejectedError on a 400 structured rejection", async () => {
    mockFetch(() =>
      json({ ok: false, message: "The edit could not be applied.", errors: [] }, 400),
    );
    await expect(
      patchConfig({ op: "upstream_remove_backend", upstream: "pool", address: "x" }),
    ).rejects.toBeInstanceOf(ConfigRejectedError);
  });

  it("throws ApiError on a non-structured transport failure", async () => {
    mockFetch(() => new Response("boom", { status: 500 }));
    await expect(
      patchConfig({ op: "route_toggle_cache", listen: ":80", server_names: [], match_type: "prefix", path: "/", enabled: true }),
    ).rejects.toBeInstanceOf(ApiError);
  });
});
