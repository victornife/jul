/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import {
  patchConfig,
  applyPatchBatch,
  fetchPatchCandidate,
  ConfigRejectedError,
  ConfigConflictError,
  ConfigAdminChangeError,
  ApiError,
} from "@/api/client.ts";

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
    expect(res.validation_errors).toBeUndefined();
    expect(JSON.parse(seenBody)).toMatchObject({ op: "route_set_target", target: "http://new" });
  });

  it("serializes a per-location WAF set with the nested waf payload", async () => {
    let seenBody = "";
    mockFetch((url, init) => {
      expect(url).toBe("/api/config/patch");
      seenBody = typeof init?.body === "string" ? init.body : "";
      return json({
        ok: true,
        summary: "route :8080/admin WAF override set (enabled — detect, CRS)",
        candidate: 'listen = ":8080"\n',
        diff: { summary: "1 change" },
      });
    });
    const res = await patchConfig({
      op: "location_waf_set",
      listen: ":8080",
      server_names: [],
      match_type: "prefix",
      path: "/admin",
      waf: { enabled: true, mode: "detect", crs_enabled: true },
    });
    expect(res.summary).toContain("WAF override set");
    // The nested waf payload is what the backend reads (req.WAF), so it must be
    // sent verbatim — not flattened onto the top-level toggle `enabled` field.
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "location_waf_set",
      path: "/admin",
      waf: { enabled: true, mode: "detect", crs_enabled: true },
    });
  });

  it("serializes a per-location WAF clear without a payload", async () => {
    let seenBody = "";
    mockFetch((url, init) => {
      expect(url).toBe("/api/config/patch");
      seenBody = typeof init?.body === "string" ? init.body : "";
      return json({
        ok: true,
        summary: "route :8080/admin WAF override cleared (inherits the global [waf])",
        candidate: 'listen = ":8080"\n',
        diff: { summary: "1 change" },
      });
    });
    const res = await patchConfig({
      op: "location_waf_clear",
      listen: ":8080",
      server_names: [],
      match_type: "prefix",
      path: "/admin",
    });
    expect(res.summary).toContain("override cleared");
    expect(JSON.parse(seenBody)).toMatchObject({ op: "location_waf_clear", path: "/admin" });
  });

  it("serializes a per-location auth set with the nested auth payload", async () => {
    let seenBody = "";
    mockFetch((url, init) => {
      expect(url).toBe("/api/config/patch");
      seenBody = typeof init?.body === "string" ? init.body : "";
      return json({
        ok: true,
        summary: "route :8080/api auth set (JWT)",
        candidate: 'listen = ":8080"\n',
        diff: { summary: "1 change" },
      });
    });
    const res = await patchConfig({
      op: "location_set_auth",
      listen: ":8080",
      server_names: [],
      match_type: "prefix",
      path: "/api",
      auth: { method: "jwt", jwt_jwks_url: "https://issuer.example/jwks.json", jwt_issuer: "iss" },
    });
    expect(res.summary).toContain("auth set");
    // The nested auth payload is what the backend reads (req.Auth), so it must be
    // sent verbatim under `auth`, not flattened onto the top level.
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "location_set_auth",
      path: "/api",
      auth: { method: "jwt", jwt_jwks_url: "https://issuer.example/jwks.json", jwt_issuer: "iss" },
    });
  });

  it("serializes a per-location auth clear without a payload", async () => {
    let seenBody = "";
    mockFetch((url, init) => {
      expect(url).toBe("/api/config/patch");
      seenBody = typeof init?.body === "string" ? init.body : "";
      return json({
        ok: true,
        summary: "route :8080/api auth cleared",
        candidate: 'listen = ":8080"\n',
        diff: { summary: "1 change" },
      });
    });
    const res = await patchConfig({
      op: "location_clear_auth",
      listen: ":8080",
      server_names: [],
      match_type: "prefix",
      path: "/api",
    });
    expect(res.summary).toContain("auth cleared");
    expect(JSON.parse(seenBody)).toMatchObject({ op: "location_clear_auth", path: "/api" });
  });

  it("serializes a server HTTP/3 toggle by listen", async () => {
    let seenBody = "";
    mockFetch((url, init) => {
      expect(url).toBe("/api/config/patch");
      seenBody = typeof init?.body === "string" ? init.body : "";
      return json({
        ok: true,
        summary: "server :443 HTTP/3 on",
        candidate: 'listen = ":443"\n',
        diff: { summary: "1 change" },
      });
    });
    const res = await patchConfig({ op: "server_toggle_http3", listen: ":443", enabled: true });
    expect(res.summary).toContain("HTTP/3");
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "server_toggle_http3",
      listen: ":443",
      enabled: true,
    });
  });

  it("serializes a server h2c toggle by listen", async () => {
    let seenBody = "";
    mockFetch((url, init) => {
      expect(url).toBe("/api/config/patch");
      seenBody = typeof init?.body === "string" ? init.body : "";
      return json({
        ok: true,
        summary: "server :8080 h2c off",
        candidate: 'listen = ":8080"\n',
        diff: { summary: "1 change" },
      });
    });
    const res = await patchConfig({ op: "server_toggle_h2c", listen: ":8080", enabled: false });
    expect(res.summary).toContain("h2c");
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "server_toggle_h2c",
      listen: ":8080",
      enabled: false,
    });
  });

  it("surfaces validation_errors when the candidate would not build", async () => {
    mockFetch(() =>
      json({
        ok: true,
        summary: "route :8080/api proxy_pass set to http://ghost",
        candidate: 'listen = ":8080"\n',
        diff: { summary: "1 change" },
        validation_errors: [
          {
            code: "unknown",
            path: "",
            summary: 'proxy_pass references unknown upstream "ghost"',
            detail: "",
            severity: "error",
          },
        ],
      }),
    );
    const res = await patchConfig({
      op: "route_set_target",
      listen: ":8080",
      server_names: [],
      match_type: "prefix",
      path: "/api",
      target: "http://ghost",
    });
    expect(res.candidate).toContain('listen = ":8080"');
    expect(res.validation_errors).toHaveLength(1);
    expect(res.validation_errors?.[0]?.summary).toContain("unknown upstream");
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
      patchConfig({
        op: "route_toggle_cache",
        listen: ":80",
        server_names: [],
        match_type: "prefix",
        path: "/",
        enabled: true,
      }),
    ).rejects.toBeInstanceOf(ApiError);
  });
});

describe("applyPatchBatch", () => {
  const op = {
    op: "route_set_target" as const,
    listen: ":8080",
    server_names: [],
    match_type: "prefix",
    path: "/",
    target: "http://127.0.0.1:9100",
  };

  it("posts ops + base_version and returns the new version on success", async () => {
    let seenUrl = "";
    let seenBody = "";
    mockFetch((url, init) => {
      seenUrl = url;
      seenBody = typeof init?.body === "string" ? init.body : "";
      return json({
        ok: true,
        pending_reload: true,
        version: "feedfacefeedface",
        summary: ["route :8080/ proxy_pass set to http://127.0.0.1:9100"],
        diff: { summary: "1 change" },
        message: "Structured patch validated and saved.",
      });
    });
    const res = await applyPatchBatch([op], "deadbeefdeadbeef");
    expect(seenUrl).toBe("/api/config/patch/apply");
    expect(JSON.parse(seenBody)).toMatchObject({
      base_version: "deadbeefdeadbeef",
      ops: [{ op: "route_set_target", target: "http://127.0.0.1:9100" }],
    });
    expect(res.version).toBe("feedfacefeedface");
    expect(res.pending_reload).toBe(true);
    expect(res.summary).toHaveLength(1);
  });

  it("throws ConfigConflictError with current_version on a 409", async () => {
    mockFetch(() =>
      json(
        {
          ok: false,
          conflict: true,
          message: "The configuration changed since this edit was prepared; reload and try again.",
          current_version: "abc123abc123abc1",
        },
        409,
      ),
    );
    try {
      await applyPatchBatch([op], "stalestalestale0");
      expect.unreachable("expected a conflict");
    } catch (err) {
      expect(err).toBeInstanceOf(ConfigConflictError);
      expect((err as ConfigConflictError).currentVersion).toBe("abc123abc123abc1");
    }
  });

  it("throws ConfigRejectedError on a 400 structured rejection", async () => {
    mockFetch(() =>
      json(
        { ok: false, message: "Operation 2 could not be applied; no change was made.", errors: [] },
        400,
      ),
    );
    await expect(applyPatchBatch([op])).rejects.toBeInstanceOf(ConfigRejectedError);
  });

  it("throws ApiError on a non-structured transport failure", async () => {
    mockFetch(() => new Response("boom", { status: 500 }));
    await expect(applyPatchBatch([op])).rejects.toBeInstanceOf(ApiError);
  });

  it("recognizes admin confirmation and retries with confirm_admin", async () => {
    let seen = "";
    mockFetch((url) => {
      seen = url;
      return json({ ok: true, mode: "stage_restart", summary: [], diff: { summary: "ok" } });
    });
    await applyPatchBatch([op], "base", "stage_restart", true);
    expect(seen).toBe("/api/config/patch/apply?mode=stage_restart&confirm_admin=true");

    mockFetch(() =>
      json(
        {
          ok: false,
          admin_change: true,
          message: "confirm patch",
          changes: ["admin token changes"],
        },
        409,
      ),
    );
    await expect(applyPatchBatch([op])).rejects.toBeInstanceOf(ConfigAdminChangeError);
  });
});

describe("fetchPatchCandidate", () => {
  const op = {
    op: "route_set_target" as const,
    listen: ":8080",
    server_names: [],
    match_type: "prefix",
    path: "/api",
    target: "http://new",
  };

  it("posts ops + base_version and returns the candidate TOML on success", async () => {
    let seenUrl = "";
    let seenBody = "";
    mockFetch((url, init) => {
      seenUrl = url;
      seenBody = typeof init?.body === "string" ? init.body : "";
      return json({
        ok: true,
        candidate: 'listen = ":8080"\n',
        base_version: "deadbeefdeadbeef",
      });
    });
    const res = await fetchPatchCandidate([op], "deadbeefdeadbeef");
    expect(seenUrl).toBe("/api/config/patch/candidate");
    expect(JSON.parse(seenBody)).toMatchObject({
      base_version: "deadbeefdeadbeef",
      ops: [{ op: "route_set_target", target: "http://new" }],
    });
    expect(res.candidate).toContain('listen = ":8080"');
    expect(res.base_version).toBe("deadbeefdeadbeef");
  });

  it("throws ApiError(403) when the token lacks config:raw", async () => {
    mockFetch(() => json({ error: "forbidden" }, 403));
    try {
      await fetchPatchCandidate([op]);
      expect.unreachable("expected a forbidden error");
    } catch (err) {
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).status).toBe(403);
    }
  });

  it("throws ConfigConflictError with current_version on a 409 stale preview", async () => {
    mockFetch(() =>
      json(
        {
          ok: false,
          conflict: true,
          message: "The configuration changed since this edit was prepared; reload and try again.",
          current_version: "abc123abc123abc1",
        },
        409,
      ),
    );
    try {
      await fetchPatchCandidate([op], "stalestalestale0");
      expect.unreachable("expected a conflict");
    } catch (err) {
      expect(err).toBeInstanceOf(ConfigConflictError);
      expect((err as ConfigConflictError).currentVersion).toBe("abc123abc123abc1");
    }
  });

  it("throws ConfigRejectedError on a 400 invalid operation", async () => {
    mockFetch(() =>
      json(
        { ok: false, message: "Operation 1 could not be applied; no change was made.", errors: [] },
        400,
      ),
    );
    await expect(fetchPatchCandidate([op])).rejects.toBeInstanceOf(ConfigRejectedError);
  });
});
