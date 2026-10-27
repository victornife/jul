/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { CacheInspectorPanel } from "@/features/traffic-controls/CacheInspectorPanel.tsx";
import { PermissionContext } from "@/auth/usePermission.ts";
import { authToken, FORBIDDEN_EVENT, type CacheInspectionPage } from "@/api/client.ts";

function page(): CacheInspectionPage {
  return {
    api_version: "v1",
    expires_at: new Date(Date.now() + 60000).toISOString(),
    next_cursor: "bound.cursor",
    consistency: "bounded_metadata_capture_not_global_snapshot",
    concurrent_change: "unknown_after_capture",
    policy_source: "global inherited",
    route_cache_state: "host scope",
    inspection: {
      state: "available",
      captured_at: "2026-10-27T13:00:00Z",
      count_accuracy: "exact",
      truncated: false,
      matched_resources: 1,
      matched_representations: 1,
      memory_entries: 1,
      disk_entries: 0,
      matched_body_bytes: 10,
      evidence_state: "observed",
      warnings: [],
      memory_occupancy: { bytes: 100, max_bytes: 1000, entries: 1, evictions: 0 },
      disk_occupancy: null,
      policy: {
        default_ttl_seconds: 60,
        swr_seconds: 10,
        sie_seconds: 5,
        max_entry_bytes: 1000,
        range_enabled: false,
        fill_lock_enabled: false,
        tag_ingestion_enabled: true,
      },
      resources: [
        {
          resource_id: "opaque-resource",
          host: "example.com",
          path: "/article",
          has_query: true,
          methods: ["GET"],
          representations: 1,
          tiers: ["memory"],
          body_bytes: 10,
          freshness_state: "fresh",
          vary_fields: ["Accept-Language"],
          tags: ["article:42"],
          details: [
            {
              id: "opaque-rep",
              method: "GET",
              tiers: ["memory"],
              body_bytes: 10,
              origin_age_seconds: 2,
              freshness_state: "fresh",
              vary_fields: ["Accept-Language"],
              evidence_state: "recorded",
              retained_policy: true,
              effective_sie_seconds: 0,
              evidence: {
                inserted_at: "2026-10-27T12:59:58Z",
                freshness_source: "origin_max_age",
                freshness_seconds: 60,
                swr_source: "gateway_fallback",
                swr_seconds: 10,
                sie_source: "gateway_fallback",
                sie_seconds: 5,
                requires_validation: false,
                must_revalidate: false,
              },
            },
          ],
        },
      ],
      decisions: [
        {
          resource_id: "opaque-resource",
          host: "example.com",
          path: "/article",
          reason: "fresh_hit",
          observed_at: "2026-10-27T13:00:00Z",
          expires_at: new Date(Date.now() + 60000).toISOString(),
        },
      ],
    },
  };
}
const realFetch = globalThis.fetch;
beforeEach(() => {
  authToken.set("inspection-test-token");
});
afterEach(() => {
  cleanup();
  globalThis.fetch = realFetch;
  authToken.clear();
  vi.restoreAllMocks();
});
function json(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("cache inspector", () => {
  it("shows server-owned metadata, policy, occupancy and repeats selector for paging", async () => {
    const requests: Array<{
      selector: { kind: string; host: string; uri?: string };
      cursor?: string;
    }> = [];
    globalThis.fetch = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      if (typeof init?.body !== "string") throw new Error("Expected request JSON body");
      requests.push(JSON.parse(init.body) as (typeof requests)[number]);
      return Promise.resolve(json(page()));
    });
    render(<CacheInspectorPanel serverNames={["example.com"]} />);
    fireEvent.change(screen.getByLabelText("Filter"), { target: { value: "exact_url" } });
    fireEvent.change(screen.getByLabelText("Filter value"), {
      target: { value: "/article?token=SECRET" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Inspect / refresh" }));
    await screen.findByText(/origin_max_age/);
    expect(screen.getByText(/Memory occupancy 100/)).toBeInTheDocument();
    expect(screen.getByText(/Current global fallback/)).toBeInTheDocument();
    expect(screen.getByText(/Concurrent-change evidence/)).toBeInTheDocument();
    expect(screen.getByText(/query omitted/)).toBeInTheDocument();
    expect(screen.queryByText(/SECRET/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Next metadata page" }));
    await waitFor(() => {
      expect(requests.length).toBe(2);
    });
    expect(requests[1]?.cursor).toBe("bound.cursor");
    expect(requests[1]?.selector).toEqual(requests[0]?.selector);
    expect(window.localStorage.getItem("cache-inspection")).toBeNull();
    expect(window.sessionStorage.getItem("cache-inspection")).toBeNull();
  });
  it.each(["empty", "disabled", "unavailable", "partial", "truncated"])(
    "renders %s without inventing history",
    async (state) => {
      const d = page();
      d.inspection.state = state;
      d.inspection.resources = [];
      d.inspection.evidence_state = "disabled";
      d.inspection.truncated = state === "truncated";
      globalThis.fetch = vi.fn(() => Promise.resolve(json(d)));
      render(<CacheInspectorPanel serverNames={["example.com"]} />);
      fireEvent.click(screen.getByRole("button", { name: "Inspect / refresh" }));
      await screen.findByText(/No resource metadata/);
      expect(screen.getByText(new RegExp(`Result: ${state}`))).toBeInTheDocument();
      if (state === "truncated")
        expect(screen.getByText(/Truncated observation/)).toBeInTheDocument();
    },
  );
  it("has an accessible denial state and does not request without a grant", () => {
    const f = vi.fn();
    globalThis.fetch = f;
    render(
      <PermissionContext.Provider
        value={{ identity: null, isLoading: false, ready: true, has: () => false }}
      >
        <CacheInspectorPanel serverNames={["example.com"]} />
      </PermissionContext.Provider>,
    );
    expect(screen.getByRole("status")).toHaveTextContent("cache:inspect");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(f).not.toHaveBeenCalled();
  });
  it("clears stale credential results and suppresses superseded responses", async () => {
    let resolve: (r: Response) => void = () => {};
    globalThis.fetch = vi.fn(
      () =>
        new Promise<Response>((r) => {
          resolve = r;
        }),
    );
    render(<CacheInspectorPanel serverNames={["example.com"]} />);
    fireEvent.click(screen.getByRole("button", { name: "Inspect / refresh" }));
    expect(screen.getByRole("status")).toHaveTextContent("Loading");
    fireEvent.change(screen.getByLabelText("Configured hostname"), {
      target: { value: "other.example.com" },
    });
    resolve(json(page()));
    await waitFor(() => {
      expect(screen.queryByText(/origin_max_age/)).not.toBeInTheDocument();
    });
    window.dispatchEvent(new Event(FORBIDDEN_EVENT));
    await screen.findByRole("alert");
    expect(screen.queryByText(/opaque-resource/)).not.toBeInTheDocument();
  });
  it("distinguishes recorded and current effective stale-if-error after reload", async () => {
    const d = page();
    const rep = d.inspection.resources[0]?.details[0];
    if (!rep) throw new Error("fixture detail missing");
    rep.evidence.sie_seconds = 60;
    rep.effective_sie_seconds = 0;
    rep.retained_policy = true;
    globalThis.fetch = vi.fn(() => Promise.resolve(json(d)));
    render(<CacheInspectorPanel serverNames={["example.com"]} />);
    fireEvent.click(screen.getByRole("button", { name: "Inspect / refresh" }));
    await screen.findByText(/recorded stale-if-error 60s/);
    expect(screen.getByText(/current effective stale-if-error 0s/)).toBeInTheDocument();
    expect(screen.getByText(/Entry retained from a different policy/)).toBeInTheDocument();
  });
  it("shows expired and legacy evidence, and disables stale pagination", async () => {
    const d = page();
    d.expires_at = new Date(Date.now() - 1000).toISOString();
    const row = d.inspection.resources[0];
    if (!row) throw new Error("fixture row missing");
    const rep = row.details[0];
    if (!rep) throw new Error("fixture detail missing");
    rep.evidence_state = "legacy_unavailable";
    row.details_truncated = true;
    globalThis.fetch = vi.fn(() => Promise.resolve(json(d)));
    render(<CacheInspectorPanel serverNames={["example.com"]} />);
    fireEvent.click(screen.getByRole("button", { name: "Inspect / refresh" }));
    await screen.findByText(/Stale evidence/);
    expect(screen.getByRole("button", { name: "Next metadata page" })).toBeDisabled();
    expect(screen.getByText(/legacy_unavailable/)).toBeInTheDocument();
    expect(screen.getByText(/Representation details truncated/)).toBeInTheDocument();
  });
  it("surfaces an API failure without reflecting request secrets", async () => {
    globalThis.fetch = vi.fn(() =>
      Promise.resolve(
        json({ error: { code: "storage_unavailable", message: "SECRET_ERROR" } }, 503),
      ),
    );
    render(<CacheInspectorPanel serverNames={["example.com"]} />);
    fireEvent.click(screen.getByRole("button", { name: "Inspect / refresh" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Inspection unavailable");
    expect(screen.queryByText(/SECRET_ERROR/)).not.toBeInTheDocument();
  });
});
