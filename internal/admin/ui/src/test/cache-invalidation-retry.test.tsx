/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import {
  authToken,
  FORBIDDEN_EVENT,
  UNAUTHORIZED_EVENT,
  type CachePurgePreviewResponse,
  type CachePurgeResponse,
  type CacheSelector,
} from "@/api/client.ts";
import { CacheInvalidationPanel } from "@/features/traffic-controls/CacheInvalidationPanel.tsx";

const realFetch = globalThis.fetch;
const operation: CachePurgeResponse = {
  api_version: "v1",
  operation_id: "cp_1_abcd1234",
  state: "canceled",
  terminal: true,
  outcome: "canceled",
  invalidation_effective: "incomplete",
  selector: { kind: "tag", host: "portal.example.com", tags: ["A"] },
  selector_digest: "original-selector",
  fence_sequence: 5,
  invalidated_resources: 0,
  invalidated_representations: 0,
  memory_entries_removed: 0,
  disk_entries_removed: 0,
  remaining_entries: 1,
  started_at: "2026-10-26T14:00:00Z",
  boot_id: "boot-1",
  warnings: [],
  config_authority: "file_owned",
};

interface PurgeBody {
  selector: CacheSelector;
  confirm_broad?: boolean;
  retry_operation_id?: string;
  preview_token?: string;
}

function installApi({
  operations = { [operation.operation_id]: operation },
  broadPreview = false,
  initialResult,
  retryResult,
  retryError = false,
  retryBroadScope = false,
}: {
  operations?: Record<string, CachePurgeResponse>;
  broadPreview?: boolean;
  initialResult?: CachePurgeResponse;
  retryResult?: CachePurgeResponse;
  retryError?: boolean;
  retryBroadScope?: boolean;
} = {}) {
  const posts: Array<PurgeBody & { key: string | null }> = [];
  const originalKeys = new Map<string, string | null>();
  const json = (body: unknown, status = 200) =>
    Promise.resolve(new Response(JSON.stringify(body), { status }));
  const parseBody = (init?: RequestInit): PurgeBody => {
    if (typeof init?.body !== "string") throw new Error("Expected a JSON request body");
    return JSON.parse(init.body) as PurgeBody;
  };
  globalThis.fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url =
      typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
    if (url.endsWith("/me")) {
      return json({
        authenticated: true,
        principal: "ops",
        role: "admin",
        permissions: ["stats:read", "cache:purge", "config:read"],
      });
    }
    const inspected = Object.values(operations).find((op) =>
      url.endsWith(`/cache/purges/${op.operation_id}`),
    );
    if (inspected) return json(inspected, inspected.terminal ? 200 : 202);
    if (url.endsWith("/cache/purge/preview")) {
      const body = parseBody(init);
      const preview: CachePurgePreviewResponse = {
        api_version: "v1",
        cache_enabled: true,
        tag_ingestion_enabled: true,
        selector: body.selector,
        selector_digest: "preview-selector",
        preview_token: "signed-preview-token",
        matched_resources: broadPreview ? 100 : 1,
        matched_representations: broadPreview ? 100 : 1,
        memory_entries: broadPreview ? 100 : 1,
        disk_entries: 0,
        matched_bytes: 1000,
        memory_bytes: 1000,
        disk_bytes: 0,
        inflight_fills_matched: 0,
        inflight_tag_unknown: false,
        count_accuracy: "exact",
        truncated: false,
        scan_limit: 10000,
        scanned_entries: broadPreview ? 100 : 1,
        source: "live_cache_index",
        as_of: "2026-10-26T14:00:00Z",
        cache_sequence: 4,
        execution_mode_contract: "reevaluate_selector_with_fence",
        broad_scope: broadPreview,
        warnings: [],
        samples: [],
        config_authority: "file_owned",
      };
      return json(preview);
    }
    if (url.endsWith("/cache/purge") && init?.method === "POST") {
      const body = parseBody(init);
      const key = new Headers(init.headers).get("Idempotency-Key");
      posts.push({ ...body, key });
      if (!body.retry_operation_id && initialResult) {
        originalKeys.set(initialResult.operation_id, key);
        return json(initialResult);
      }
      const prior = operations[body.retry_operation_id ?? ""];
      if (!prior) throw new Error(`Unexpected operation retry: ${JSON.stringify(body)}`);
      if (key && key !== originalKeys.get(prior.operation_id)) {
        return json(
          {
            error: {
              code: "invalid_request",
              message: "Idempotency-Key is not bound to retry_operation_id",
            },
          },
          400,
        );
      }
      if (retryBroadScope && body.confirm_broad !== true) {
        return json(
          {
            error: {
              code: "invalid_request",
              message: "broad cache invalidation requires confirm_broad = true",
            },
          },
          400,
        );
      }
      if (retryError) {
        return json({ error: { code: "unavailable", message: "simulated retry failure" } }, 503);
      }
      return json(
        retryResult ?? {
          ...prior,
          state: "succeeded",
          outcome: "succeeded",
          invalidation_effective: "effective",
          remaining_entries: 0,
        },
      );
    }
    throw new Error(`Unexpected request ${url}`);
  });
  return posts;
}

function renderPanel() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchInterval: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <CacheInvalidationPanel
          cacheEnabled
          tagHeaders={["Cache-Tag"]}
          serverNames={["portal.example.com"]}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function inspectOperation(id = operation.operation_id) {
  fireEvent.change(screen.getByLabelText("Inspect purge operation ID"), { target: { value: id } });
  const lookup = screen.getByRole("button", { name: "Check operation status" });
  await waitFor(() => {
    expect(lookup).toBeEnabled();
  });
  fireEvent.click(lookup);
  await waitFor(() => {
    expect(lookup).toBeEnabled();
  });
  return screen.findByRole("button", { name: `Retry invalidation (${id})` });
}

async function previewPrefix(broad: boolean) {
  fireEvent.change(screen.getByLabelText("Invalidation selector kind"), {
    target: { value: "prefix" },
  });
  fireEvent.change(screen.getByLabelText("Anchored path prefix"), { target: { value: "/many" } });
  const preview = screen.getByRole("button", { name: "Preview invalidation scope" });
  await waitFor(() => {
    expect(preview).toBeEnabled();
  });
  fireEvent.click(preview);
  await screen.findByRole("region", { name: "Invalidation preview" });
  if (broad) fireEvent.click(screen.getByLabelText(/Confirm broad invalidation/));
}

describe("Console canonical operation retries", () => {
  beforeEach(() => {
    authToken.set("retry-operator-token");
  });

  afterEach(() => {
    cleanup();
    authToken.clear();
    globalThis.fetch = realFetch;
  });

  it.each([false, true])(
    "omits unknown keys and uses the looked-up selector, with an unrelated preview=%s",
    async (unrelatedPreview) => {
      const posts = installApi({ broadPreview: true });
      renderPanel();
      if (unrelatedPreview) await previewPrefix(true);
      const retry = await inspectOperation();
      expect(retry).toBeDisabled();
      const confirmation = screen.getByLabelText("Confirm retry scope");
      expect(confirmation).not.toBeChecked();
      fireEvent.click(retry);
      expect(posts).toHaveLength(0);
      fireEvent.click(confirmation);
      fireEvent.click(retry);
      await screen.findByText(/SUCCEEDED · succeeded/);
      expect(posts).toEqual([
        {
          selector: operation.selector,
          retry_operation_id: operation.operation_id,
          confirm_broad: true,
          key: null,
        },
      ]);
    },
  );

  it.each([false, true])(
    "explicitly confirms broad retry after clearing a preview, original broad scope=%s",
    async (originalBroad) => {
      const canceled: CachePurgeResponse = {
        ...operation,
        selector: { kind: "prefix", host: "portal.example.com", prefix: "/many" },
        fence_sequence: originalBroad ? 5 : 0,
        remaining_entries: originalBroad ? 100 : 0,
      };
      const posts = installApi({
        operations: { [canceled.operation_id]: canceled },
        broadPreview: originalBroad,
        initialResult: canceled,
        retryBroadScope: true,
      });
      renderPanel();
      await previewPrefix(originalBroad);
      fireEvent.click(screen.getByRole("button", { name: "Confirm & invalidate" }));
      const retry = await screen.findByRole("button", {
        name: `Retry invalidation (${canceled.operation_id})`,
      });
      expect(posts[0]?.key).not.toBeNull();
      expect(posts[0]?.confirm_broad).toBe(originalBroad ? true : undefined);
      fireEvent.click(screen.getByLabelText("Confirm retry scope"));
      expect(retry).toBeEnabled();
      fireEvent.click(screen.getByRole("button", { name: "Cancel preview" }));
      expect(screen.getByLabelText("Confirm retry scope")).not.toBeChecked();
      expect(retry).toBeDisabled();
      fireEvent.click(retry);
      expect(posts).toHaveLength(1);
      fireEvent.click(screen.getByLabelText("Confirm retry scope"));
      fireEvent.click(retry);
      await screen.findByText(/SUCCEEDED · succeeded/);
      expect(posts[1]).toEqual({
        selector: canceled.selector,
        retry_operation_id: canceled.operation_id,
        confirm_broad: true,
        key: null,
      });
    },
  );

  it.each([false, true])(
    "clears consent when checking operation status, same operation=%s",
    async (sameOperation) => {
      const next = { ...operation, operation_id: "cp_2_abcd1234" };
      const posts = installApi({
        operations: { [operation.operation_id]: operation, [next.operation_id]: next },
      });
      renderPanel();
      const originalRetry = await inspectOperation();
      fireEvent.click(screen.getByLabelText("Confirm retry scope"));
      expect(originalRetry).toBeEnabled();
      const retry = await inspectOperation(
        sameOperation ? operation.operation_id : next.operation_id,
      );
      expect(screen.getByLabelText("Confirm retry scope")).not.toBeChecked();
      expect(retry).toBeDisabled();
      fireEvent.click(retry);
      expect(posts).toHaveLength(0);
    },
  );

  it.each(["jul:credential-changed", UNAUTHORIZED_EVENT, FORBIDDEN_EVENT])(
    "clears retry consent on %s",
    async (event) => {
      const posts = installApi();
      renderPanel();
      const retry = await inspectOperation();
      fireEvent.click(screen.getByLabelText("Confirm retry scope"));
      expect(retry).toBeEnabled();
      fireEvent(window, new Event(event));
      expect(screen.getByLabelText("Confirm retry scope")).not.toBeChecked();
      expect(retry).toBeDisabled();
      fireEvent.click(retry);
      expect(posts).toHaveLength(0);
    },
  );

  it.each([false, true])(
    "consumes consent even when a retry stays incomplete, request error=%s",
    async (retryError) => {
      const posts = installApi({ retryResult: operation, retryError });
      renderPanel();
      const retry = await inspectOperation();
      fireEvent.click(screen.getByLabelText("Confirm retry scope"));
      fireEvent.click(retry);
      await waitFor(() => {
        expect(posts).toHaveLength(1);
      });
      if (retryError) await screen.findByText(/simulated retry failure/);
      await waitFor(() => {
        expect(screen.getByLabelText("Confirm retry scope")).toBeEnabled();
      });
      expect(screen.getByLabelText("Confirm retry scope")).not.toBeChecked();
      const nextRetry = screen.getByRole("button", {
        name: `Retry invalidation (${operation.operation_id})`,
      });
      expect(nextRetry).toBeDisabled();
      fireEvent.click(nextRetry);
      expect(posts).toHaveLength(1);
    },
  );

  it("requires a terminal status before retry consent can be given", async () => {
    const operations = {
      [operation.operation_id]: {
        ...operation,
        state: "running" as const,
        outcome: "running" as const,
        terminal: false,
      },
    };
    const posts = installApi({ operations });
    renderPanel();
    const retry = await inspectOperation();
    expect(screen.getByText(/This operation is still running/)).toBeInTheDocument();
    expect(screen.getByLabelText("Confirm retry scope")).toBeDisabled();
    expect(retry).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Confirm retry scope"));
    fireEvent.click(retry);
    expect(posts).toHaveLength(0);
  });
});
