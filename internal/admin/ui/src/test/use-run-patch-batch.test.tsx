/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { act, renderHook, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ConfigPatch } from "@/api/client.ts";
import { describePatchBatchError, useRunPatchBatch } from "@/lib/useRunPatchBatch.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import {
  pendingDraftStorageKey,
  takePendingDraft,
  type PendingPatchDraft,
} from "@/lib/configDraftHandoff.ts";

const realFetch = globalThis.fetch;

function Wrapper({ children }: { readonly children: ReactNode }) {
  return <MemoryRouter initialEntries={["/routes"]}>{children}</MemoryRouter>;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function successfulPreview() {
  return {
    ok: true,
    summary: "server added; location added",
    operation_summaries: [
      { op_index: 0, op: "server_add", summary: "server added" },
      { op_index: 1, op: "location_add", summary: "location added" },
    ],
    diff: { summary: "2 changes" },
    base_version: "v1",
    valid: true,
    validation_errors: [],
    lifecycle: {
      changes: [],
      can_apply_hot: true,
      can_stage_restart: true,
      hot_paths: ["servers"],
      restart_required_paths: [],
      new_listener_only_paths: [],
      ignored_deprecated_paths: [],
      validation_rejected_paths: [],
      pending_subsystems: [],
    },
  };
}

beforeEach(() => {
  takePendingDraft();
  sessionStorage.removeItem(pendingDraftStorageKey);
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
  takePendingDraft();
  sessionStorage.removeItem(pendingDraftStorageKey);
});

describe("useRunPatchBatch", () => {
  it("previews the exact ordered ops and preserves the complete secret-safe assessment", async () => {
    const ops: ConfigPatch[] = [
      { op: "server_add", listen: ":8443", server_names: ["b.example", "a.example"] },
      {
        op: "location_add",
        listen: ":8443",
        server_names: ["b.example", "a.example"],
        match_set: { type: "prefix", path: "/api" },
        action: { kind: "proxy", target: "http://app" },
      },
    ];
    let requestBody: unknown = null;
    globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
      expect(input).toBe("/api/config/patch/preview");
      requestBody = JSON.parse(typeof init?.body === "string" ? init.body : "null");
      return Promise.resolve(json(successfulPreview()));
    }) as unknown as typeof fetch;

    const { result } = renderHook(() => useRunPatchBatch(), { wrapper: Wrapper });
    let previewed: Awaited<ReturnType<typeof result.current.preview>> = null;
    await act(async () => {
      previewed = await result.current.preview(ops);
    });

    expect(requestBody).toEqual({ ops });
    expect(previewed).toMatchObject({
      kind: "patch",
      ops,
      baseVersion: "v1",
      summary: "server added; location added",
      operationSummaries: successfulPreview().operation_summaries,
      valid: true,
      validationErrors: [],
      previewDiff: { summary: "2 changes" },
      lifecycle: successfulPreview().lifecycle,
    });
    expect(previewed).not.toHaveProperty("candidate");
    expect(takePendingDraft()).toBeNull();

    act(() => {
      if (previewed === null) throw new Error("preview missing");
      result.current.handoff(previewed);
    });
    const handedOff = takePendingDraft();
    expect(handedOff).toMatchObject({ kind: "patch", ops, baseVersion: "v1" });
    expect(handedOff).not.toHaveProperty("candidate");
  });

  it("pins an explicitly supplied base version through preview and compatibility parsing", async () => {
    let requestBody: unknown = null;
    globalThis.fetch = vi.fn((_input: string, init?: RequestInit) => {
      requestBody = JSON.parse(typeof init?.body === "string" ? init.body : "null");
      const response = successfulPreview();
      delete (response as { base_version?: string }).base_version;
      return Promise.resolve(json(response));
    }) as unknown as typeof fetch;

    const op: ConfigPatch = { op: "server_toggle_h2c", listen: ":8080", enabled: true };
    const { result } = renderHook(() => useRunPatchBatch(), { wrapper: Wrapper });
    const previewed: PendingPatchDraft[] = [];
    await act(async () => {
      const draft = await result.current.preview([op], "reviewed-base");
      if (draft !== null) previewed.push(draft);
    });

    expect(requestBody).toEqual({ base_version: "reviewed-base", ops: [op] });
    expect(previewed[0]?.baseVersion).toBe("reviewed-base");
  });

  it("fails closed when a preview echoes a different requested base version", async () => {
    globalThis.fetch = vi.fn(() =>
      Promise.resolve(json({ ...successfulPreview(), base_version: "unexpected-base" })),
    );
    const { result } = renderHook(() => useRunPatchBatch(), { wrapper: Wrapper });
    await act(async () => {
      expect(
        await result.current.preview(
          [{ op: "server_toggle_h2c", listen: ":8080", enabled: true }],
          "reviewed-base",
        ),
      ).toBeNull();
    });
    expect(result.current.error?.message).toMatch(/did not match the requested base version/i);
    expect(takePendingDraft()).toBeNull();
  });

  it("rejects an empty batch without issuing a request", async () => {
    globalThis.fetch = vi.fn();
    const { result } = renderHook(() => useRunPatchBatch(), { wrapper: Wrapper });
    await act(async () => {
      expect(await result.current.preview([])).toBeNull();
    });
    expect(result.current.error?.message).toMatch(/at least one operation/i);
    expect(globalThis.fetch).not.toHaveBeenCalled();
    expect(result.current.busy).toBe(false);
  });

  it("preserves a structured zero-based failed-operation error", async () => {
    globalThis.fetch = vi.fn(() =>
      Promise.resolve(
        json(
          {
            ok: false,
            message: "target already exists",
            errors: [],
            op_index: 1,
            op: "location_add",
          },
          400,
        ),
      ),
    );

    const { result } = renderHook(() => useRunPatchBatch(), { wrapper: Wrapper });
    await act(async () => {
      await result.current.preview([
        { op: "server_add", listen: ":8443" },
        {
          op: "location_add",
          listen: ":8443",
          match_set: { type: "prefix", path: "/" },
          action: { kind: "deny" },
        },
      ]);
    });

    expect(describePatchBatchError(result.current.error)).toContain(
      "Operation 1 (location_add) was rejected",
    );
    expect(takePendingDraft()).toBeNull();
  });

  it("keeps busy true until the preview request settles", async () => {
    let resolveResponse: ((response: Response) => void) | undefined;
    globalThis.fetch = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          resolveResponse = resolve;
        }),
    );

    const { result } = renderHook(() => useRunPatchBatch(), { wrapper: Wrapper });
    let promise: Promise<unknown> | undefined;
    act(() => {
      promise = result.current.preview([{ op: "server_add", listen: ":8080" }]);
    });
    await waitFor(() => {
      expect(result.current.busy).toBe(true);
    });
    await act(async () => {
      resolveResponse?.(json(successfulPreview()));
      await promise;
    });
    expect(result.current.busy).toBe(false);
  });
});

describe("useRunPatch", () => {
  it("delegates one operation as a one-element ordered batch", async () => {
    let requestBody: unknown = null;
    globalThis.fetch = vi.fn((_input: string, init?: RequestInit) => {
      requestBody = JSON.parse(typeof init?.body === "string" ? init.body : "null");
      return Promise.resolve(
        json({
          ...successfulPreview(),
          summary: "HTTP/3 enabled",
          operation_summaries: [
            { op_index: 0, op: "server_toggle_http3", summary: "HTTP/3 enabled" },
          ],
        }),
      );
    }) as unknown as typeof fetch;

    const op: ConfigPatch = { op: "server_toggle_http3", listen: ":443", enabled: true };
    const { result } = renderHook(() => useRunPatch(), { wrapper: Wrapper });
    act(() => {
      result.current.run(op);
    });

    await waitFor(() => {
      expect(requestBody).toEqual({ ops: [op] });
    });
    await waitFor(() => {
      expect(takePendingDraft()).toMatchObject({ kind: "patch", ops: [op] });
    });
  });
});
