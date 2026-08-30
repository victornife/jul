/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it } from "vitest";
import type { ConfigPatch, PatchResult } from "@/api/client.ts";
import { patchResultToPendingDraft } from "@/lib/useRunPatchBatch.ts";

function baseResult(overrides: Partial<PatchResult> = {}): PatchResult {
  return {
    ok: true,
    summary: "location added",
    operation_summaries: [{ op_index: 0, op: "location_add", summary: "location added" }],
    diff: { summary: "1 change" },
    base_version: "v1",
    valid: true,
    validation_errors: [],
    ...overrides,
  };
}

describe("patchResultToPendingDraft route_id round-trip", () => {
  it("hydrates a location_add op's route_id from the preview's resource_id", () => {
    const ops: ConfigPatch[] = [
      {
        op: "location_add",
        listen: ":8080",
        server_names: ["app.example"],
        match_set: { type: "prefix", path: "/api" },
        action: { kind: "deny" },
      },
    ];
    const result = baseResult({
      operation_summaries: [
        { op_index: 0, op: "location_add", summary: "location added", resource_id: "r-preview-id" },
      ],
    });

    const draft = patchResultToPendingDraft(ops, result, "v1");
    const hydrated = draft.ops[0];
    if (hydrated === undefined || hydrated.op !== "location_add") {
      throw new Error("expected location_add");
    }
    expect(hydrated.route_id).toBe("r-preview-id");
  });

  it("is idempotent: re-running the conversion on the already-hydrated ops does not change the id", () => {
    const ops: ConfigPatch[] = [
      {
        op: "location_add",
        listen: ":8080",
        server_names: ["app.example"],
        match_set: { type: "prefix", path: "/api" },
        action: { kind: "deny" },
      },
    ];
    const firstResult = baseResult({
      operation_summaries: [
        { op_index: 0, op: "location_add", summary: "location added", resource_id: "r-first" },
      ],
    });
    const firstDraft = patchResultToPendingDraft(ops, firstResult, "v1");

    // A re-preview of the SAME (now-hydrated) ops must echo the same id back,
    // simulating what the real backend does when route_id is already set,
    // and the conversion must not overwrite it with anything else.
    const secondResult = baseResult({
      operation_summaries: [
        { op_index: 0, op: "location_add", summary: "location added", resource_id: "r-first" },
      ],
    });
    const secondDraft = patchResultToPendingDraft(firstDraft.ops, secondResult, "v1");
    const hydrated = secondDraft.ops[0];
    if (hydrated === undefined || hydrated.op !== "location_add") {
      throw new Error("expected location_add");
    }
    expect(hydrated.route_id).toBe("r-first");
  });

  it("leaves a caller-supplied route_id untouched even if the summary carries a different resource_id", () => {
    const ops: ConfigPatch[] = [
      {
        op: "location_add",
        listen: ":8080",
        server_names: ["app.example"],
        match_set: { type: "prefix", path: "/api" },
        action: { kind: "deny" },
        route_id: "checkout-api",
      },
    ];
    const result = baseResult({
      operation_summaries: [
        { op_index: 0, op: "location_add", summary: "location added", resource_id: "checkout-api" },
      ],
    });

    const draft = patchResultToPendingDraft(ops, result, "v1");
    const hydrated = draft.ops[0];
    if (hydrated === undefined || hydrated.op !== "location_add") {
      throw new Error("expected location_add");
    }
    expect(hydrated.route_id).toBe("checkout-api");
  });

  it("leaves non-location_add ops and a location_add missing a resource_id unchanged", () => {
    const ops: ConfigPatch[] = [
      { op: "server_add", listen: ":9090", server_names: ["example.com"] },
      {
        op: "location_add",
        listen: ":9090",
        server_names: ["example.com"],
        match_set: { type: "prefix", path: "/" },
        action: { kind: "deny" },
      },
    ];
    const result = baseResult({
      summary: "server added; location added",
      operation_summaries: [
        { op_index: 0, op: "server_add", summary: "server added" },
        { op_index: 1, op: "location_add", summary: "location added" },
      ],
    });

    const draft = patchResultToPendingDraft(ops, result, "v1");
    expect(draft.ops).toEqual(ops);
  });
});
