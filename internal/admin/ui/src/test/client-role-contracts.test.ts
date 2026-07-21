/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Wave-D schema and role-contract tests (D2/D4/D5).
 *
 * These tests verify:
 *  1. PatchApplyResultSchema accepts the new correlated reload, restoration,
 *     and mode fields without Zod parse errors (D2/H-06).
 *  2. OverviewSchema accepts admin_health and last_managed_apply without
 *     errors, and both fields are optional so existing responses still parse
 *     (D4/M-05).
 *  3. applyPatchBatch sends the mode as a URL query param, not in the body,
 *     so the backend URL-based routing (r.URL.Query().Get("mode")) sees it
 *     correctly (D1).
 *
 * Role-contract notes:
 *  - operator (config:write, not config:raw): patch apply succeeds; raw config
 *    fetch returns 403 which ConfigPanel must not treat as fatal.
 *  - viewer (no write perms): apply endpoint returns 403; the apply button
 *    must be disabled or the mutation must surface the error.
 *
 * The last two role scenarios are integration-level and require a full
 * browser harness; they are exercised via console-v2-write.test.tsx which
 * can observe DOM buttons. This file focuses on the schema/network contract.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import {
  PatchApplyResultSchema,
  OverviewSchema,
  applyPatchBatch,
  type ApplyMode,
} from "@/api/client.ts";

afterEach(() => {
  vi.restoreAllMocks();
});

// ── D2: PatchApplyResultSchema correlated result fields ──────────────────────

describe("PatchApplyResultSchema — correlated reload fields (D2/H-06)", () => {
  it("accepts a minimal legacy response (no new fields)", () => {
    const minimal = {
      ok: true,
      summary: ["route_set_target: :8080 /"],
      diff: { summary: "1 change" },
    };
    const result = PatchApplyResultSchema.safeParse(minimal);
    expect(result.success).toBe(true);
  });

  it("accepts a full correlated response with reload + restoration fields", () => {
    const full = {
      ok: true,
      mode: "hot" as const,
      version: "abc123",
      pending_reload: false,
      summary: ["route_set_target: :8080 /"],
      diff: { summary: "1 change" },
      reload: {
        id: "rl_7",
        source: "admin",
        outcome: "applied_live",
        serving_version: "abc123",
        published: true,
        timed_out: false,
        duration_ms: 85,
      },
      restored: false,
      restore_error: "",
      final_disk_version: "abc123",
    };
    const result = PatchApplyResultSchema.safeParse(full);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.reload?.outcome).toBe("applied_live");
      expect(result.data.mode).toBe("hot");
    }
  });

  it("accepts a failed-and-restored response", () => {
    const failedRestored = {
      ok: true,
      mode: "hot" as const,
      summary: ["upstream_add_backend: pool1 127.0.0.1:9001"],
      diff: { summary: "0 changes" },
      reload: {
        id: "rl_8",
        outcome: "not_applied",
        published: false,
        timed_out: false,
      },
      restored: true,
      final_disk_version: "prev-version",
    };
    const result = PatchApplyResultSchema.safeParse(failedRestored);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.restored).toBe(true);
      expect(result.data.reload?.outcome).toBe("not_applied");
    }
  });

  it("accepts stage_restart mode", () => {
    const staged = {
      ok: true,
      mode: "stage_restart" as const,
      version: "def456",
      summary: ["global.log_format changed"],
      diff: { summary: "1 change" },
    };
    const result = PatchApplyResultSchema.safeParse(staged);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.mode).toBe("stage_restart");
    }
  });
});

// ── D4: OverviewSchema admin_health + last_managed_apply ────────────────────

describe("OverviewSchema — admin_health and last_managed_apply (D4/M-05)", () => {
  const base = {
    product: "Jul.IA",
    version: "1.0.0",
    status: [],
  };

  it("parses a minimal overview without the new fields (backwards compat)", () => {
    const result = OverviewSchema.safeParse(base);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.admin_health).toBeUndefined();
      expect(result.data.last_managed_apply).toBeUndefined();
    }
  });

  it("accepts admin_health when the admin subsystem is healthy", () => {
    const result = OverviewSchema.safeParse({
      ...base,
      admin_health: { healthy: true },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.admin_health?.healthy).toBe(true);
    }
  });

  it("accepts admin_health when the admin subsystem is degraded", () => {
    const result = OverviewSchema.safeParse({
      ...base,
      admin_health: {
        healthy: false,
        reason: "admin_reload",
        detail: "admin subsystem reload failed: rbac policy build failed",
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.admin_health?.healthy).toBe(false);
      expect(result.data.admin_health?.reason).toBe("admin_reload");
    }
  });

  it("accepts last_managed_apply with a successful outcome", () => {
    const result = OverviewSchema.safeParse({
      ...base,
      last_managed_apply: {
        id: "rl_42",
        mode: "hot",
        ok: true,
        outcome: "applied_live",
        completed_at: "2026-07-21T12:00:00Z",
        final_disk_version: "abc",
        final_serving_version: "abc",
        actor: "admin",
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.last_managed_apply?.ok).toBe(true);
      expect(result.data.last_managed_apply?.outcome).toBe("applied_live");
    }
  });

  it("accepts last_managed_apply with a failed-and-restored outcome", () => {
    const result = OverviewSchema.safeParse({
      ...base,
      last_managed_apply: {
        id: "rl_43",
        mode: "hot",
        ok: false,
        outcome: "not_applied",
        restored: true,
        final_disk_version: "prev",
        completed_at: "2026-07-21T13:00:00Z",
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.last_managed_apply?.ok).toBe(false);
      expect(result.data.last_managed_apply?.restored).toBe(true);
    }
  });
});

// ── D1: applyPatchBatch mode query param ─────────────────────────────────────

describe("applyPatchBatch — mode query param (D1)", () => {
  function successResponse() {
    return Promise.resolve(
      new Response(
        JSON.stringify({
          ok: true,
          mode: "hot",
          summary: ["upstream_add: pool2 127.0.0.1:9002"],
          diff: { summary: "1 change" },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
  }

  it("does NOT append mode=hot to the URL (default omitted for brevity)", async () => {
    const spy = vi.spyOn(globalThis, "fetch").mockImplementation(() => successResponse());
    await applyPatchBatch([{ op: "upstream_add", upstream: "pool2", address: "127.0.0.1:9002" }]);
    const url = (spy.mock.calls[0] as [string])[0];
    expect(url).toBe("/api/config/patch/apply");
    expect(url).not.toContain("mode=hot");
  });

  it("appends mode=stage_restart to the URL when mode is stage_restart", async () => {
    const spy = vi.spyOn(globalThis, "fetch").mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            ok: true,
            mode: "stage_restart",
            summary: ["global.log_format changed"],
            diff: { summary: "1 change" },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );
    await applyPatchBatch(
      [{ op: "upstream_add", upstream: "pool3", address: "127.0.0.1:9003" }],
      undefined,
      "stage_restart" as ApplyMode,
    );
    const url = (spy.mock.calls[0] as [string])[0];
    expect(url).toBe("/api/config/patch/apply?mode=stage_restart");
  });

  it("includes baseVersion in the request body, not in the URL", async () => {
    const spy = vi.spyOn(globalThis, "fetch").mockImplementation(() => successResponse());
    await applyPatchBatch(
      [{ op: "upstream_add", upstream: "pool4", address: "127.0.0.1:9004" }],
      "base-ver-xyz",
    );
    const call = spy.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(call[1].body as string) as { base_version: string };
    expect(body.base_version).toBe("base-ver-xyz");
    expect(call[0]).not.toContain("base_version");
  });
});
