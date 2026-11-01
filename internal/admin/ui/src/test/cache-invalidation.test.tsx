/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import {
  auditExportUrl,
  authToken,
  downloadAuditExport,
  executeCachePurge,
  fetchAudit,
  fetchCachePurgeOperation,
  fetchCertHistory,
  fetchConsoleHealth,
  fetchFailingRoutes,
  fetchRequestSamples,
  fetchTimeline,
  fetchUpstreamHistory,
  previewCachePurge,
  reportClientError,
  searchConfig,
  uploadPluginWasm,
  type CachePurgePreviewResponse,
  type CachePurgeResponse,
} from "@/api/client.ts";
import {
  CacheInvalidationPanel,
  buildCliCommandSnippet,
  buildSelectorFromDraft,
} from "@/features/traffic-controls/CacheInvalidationPanel.tsx";
import { TrafficControlEditor } from "@/features/traffic-controls/TrafficControlEditor.tsx";
import { seedCache } from "@/lib/trafficPatchBuilders.ts";
import {
  cacheWarnings,
  generateCacheToml,
  generateLimitsToml,
  isValidCacheTagHeaders,
  limitsWarnings,
} from "@/lib/trafficToml.ts";

const realFetch = globalThis.fetch;

const samplePreview: CachePurgePreviewResponse = {
  api_version: "v1",
  cache_enabled: true,
  tag_ingestion_enabled: true,
  selector: {
    kind: "tag",
    host: "portal.example.com",
    tags: ["article:42"],
    tag_match: "any",
  },
  selector_digest: "abc123digest",
  preview_token: "v1.signed.token",
  matched_resources: 2,
  matched_representations: 4,
  memory_entries: 4,
  disk_entries: 2,
  matched_bytes: 8192,
  memory_bytes: 4096,
  disk_bytes: 4096,
  inflight_fills_matched: 1,
  inflight_tag_unknown: true,
  count_accuracy: "exact",
  truncated: false,
  scan_limit: 10000,
  scanned_entries: 12,
  source: "live_cache_index",
  as_of: "2026-10-25T12:00:00Z",
  cache_sequence: 19,
  execution_mode_contract: "reevaluate_selector_with_fence",
  broad_scope: false,
  warnings: [
    {
      code: "JUL_LOCAL_SCOPE_ONLY",
      severity: "info",
      scope: "cache",
      summary: "Cache invalidation applies only to this Jul instance.",
      detail: "Downstream browser caches are not notified.",
    },
    {
      code: "INFLIGHT_TAG_UNKNOWN",
      severity: "warning",
      scope: "selector.tags",
      summary: "In-flight fills do not know response tags until origin headers arrive.",
    },
  ],
  samples: [
    {
      host: "portal.example.com",
      path: "/articles/42?[REDACTED]",
      has_query: true,
      methods: ["GET", "HEAD"],
      representations: 2,
      tiers: ["memory", "disk"],
      body_bytes: 2048,
      vary_fields: ["Accept-Encoding"],
      tags: ["article:42"],
      freshness_state: "fresh",
    },
  ],
  config_authority: "managed",
};

const samplePurgeSuccess: CachePurgeResponse = {
  api_version: "v1",
  operation_id: "cp_1_abcd1234",
  state: "succeeded",
  terminal: true,
  outcome: "succeeded",
  invalidation_effective: "effective",
  selector: samplePreview.selector,
  selector_digest: "abc123digest",
  fence_sequence: 20,
  state_changed_since_preview: true,
  invalidated_resources: 2,
  invalidated_representations: 4,
  memory_entries_removed: 4,
  disk_entries_removed: 2,
  active_readers_retired: 1,
  fenced_inflight_ops: 1,
  failed_entries: 0,
  remaining_entries: 0,
  started_at: "2026-10-25T12:00:01Z",
  completed_at: "2026-10-25T12:00:01Z",
  warnings: samplePreview.warnings,
  boot_id: "boot-123",
  config_authority: "managed",
};

describe("cache tag TOML and patch helpers (#621)", () => {
  it("validates and emits tag_headers and strip_tag_headers in generateCacheToml", () => {
    const seeded = seedCache({
      cache: {
        enabled: true,
        memory_max_size: "64m",
        disk_path: "/var/cache/jul",
        disk_max_size: "1g",
        max_entry_size: "16m",
        default_ttl: "60s",
        stale_while_revalidate: "10s",
        stale_if_error: "30s",
        lock: false,
        lock_timeout: "",
        range_enabled: false,
        max_ranges: 0,
        tag_headers: ["Cache-Tag", "Surrogate-Key"],
        strip_tag_headers: true,
      },
    });
    expect(seeded.tagHeaders).toEqual(["Cache-Tag", "Surrogate-Key"]);
    expect(seeded.stripTagHeaders).toBe(true);
    expect(isValidCacheTagHeaders(seeded)).toBe(true);

    const toml = generateCacheToml(seeded);
    expect(toml).toContain('tag_headers = ["Cache-Tag", "Surrogate-Key"]');
    expect(toml).toContain("strip_tag_headers = true");

    // Reserved headers, duplicates, >8 headers, or invalid token chars are rejected.
    expect(isValidCacheTagHeaders({ ...seeded, tagHeaders: ["ETag"] })).toBe(false);
    expect(isValidCacheTagHeaders({ ...seeded, tagHeaders: ["Cache-Control"] })).toBe(false);
    expect(
      isValidCacheTagHeaders({ ...seeded, tagHeaders: ["Cache-Tag", "cache-tag"] }),
    ).toBe(false);
    expect(
      isValidCacheTagHeaders({
        ...seeded,
        tagHeaders: ["H1", "H2", "H3", "H4", "H5", "H6", "H7", "H8", "H9"],
      }),
    ).toBe(false);
    expect(isValidCacheTagHeaders({ ...seeded, tagHeaders: ["Bad Header"] })).toBe(false);
    expect(() => generateCacheToml({ ...seeded, tagHeaders: ["ETag"] })).toThrow(
      /cache\.tag_headers/,
    );
  });

  it("builds selectors and secret-safe CLI snippets for all selector kinds", () => {
    expect(
      buildSelectorFromDraft({
        kind: "exact_url",
        host: "",
        url: "https://portal.example.com/articles/42?lang=en",
        uri: "",
        prefix: "",
        pattern: "",
        tagsText: "",
        tagMatch: "any",
        routeId: "route-articles",
      }).selector,
    ).toEqual({
      kind: "exact_url",
      url: "https://portal.example.com/articles/42?lang=en",
      route_id: "route-articles",
    });

    expect(
      buildSelectorFromDraft({
        kind: "exact_url",
        host: "portal.example.com",
        url: "",
        uri: "/articles/42",
        prefix: "",
        pattern: "",
        tagsText: "",
        tagMatch: "any",
        routeId: "",
      }).selector,
    ).toEqual({
      kind: "exact_url",
      host: "portal.example.com",
      uri: "/articles/42",
    });

    expect(
      buildSelectorFromDraft({
        kind: "exact_url",
        host: "",
        url: "",
        uri: "",
        prefix: "",
        pattern: "",
        tagsText: "",
        tagMatch: "any",
        routeId: "",
      }).error,
    ).toMatch(/Enter either a full http/);

    expect(
      buildSelectorFromDraft({
        kind: "prefix",
        host: "portal.example.com",
        url: "",
        uri: "",
        prefix: "/articles/",
        pattern: "",
        tagsText: "",
        tagMatch: "any",
        routeId: "",
      }).selector,
    ).toEqual({
      kind: "prefix",
      host: "portal.example.com",
      prefix: "/articles/",
    });

    expect(
      buildSelectorFromDraft({
        kind: "glob",
        host: "portal.example.com",
        url: "",
        uri: "",
        prefix: "",
        pattern: "/articles/*/comments",
        tagsText: "",
        tagMatch: "any",
        routeId: "",
      }).selector,
    ).toEqual({
      kind: "glob",
      host: "portal.example.com",
      pattern: "/articles/*/comments",
    });

    expect(
      buildSelectorFromDraft({
        kind: "tag",
        host: "portal.example.com",
        url: "",
        uri: "",
        prefix: "",
        pattern: "",
        tagsText: "article:42, tenant:acme",
        tagMatch: "all",
        routeId: "",
      }).selector,
    ).toEqual({
      kind: "tag",
      host: "portal.example.com",
      tags: ["article:42", "tenant:acme"],
      tag_match: "all",
    });

    expect(
      buildSelectorFromDraft({
        kind: "host",
        host: "portal.example.com",
        url: "",
        uri: "",
        prefix: "",
        pattern: "",
        tagsText: "",
        tagMatch: "any",
        routeId: "",
      }).selector,
    ).toEqual({
      kind: "host",
      host: "portal.example.com",
    });

    expect(
      buildSelectorFromDraft({
        kind: "all",
        host: "",
        url: "",
        uri: "",
        prefix: "",
        pattern: "",
        tagsText: "",
        tagMatch: "any",
        routeId: "",
      }).selector,
    ).toEqual({
      kind: "all",
      all: true,
    });

    expect(
      buildSelectorFromDraft({
        kind: "key",
        host: "",
        url: "",
        uri: "",
        prefix: "",
        pattern: "",
        tagsText: "",
        tagMatch: "any",
        routeId: "",
      }).error,
    ).toMatch(/structured selectors/);

    // Verify CLI snippets for every selector kind use POSIX single quotes and never leak tokens (F15).
    expect(
      buildCliCommandSnippet(
        { kind: "exact_url", url: "https://portal.example.com/a" },
        false,
      ),
    ).toContain("--url 'https://portal.example.com/a'");
    expect(
      buildCliCommandSnippet(
        { kind: "exact_url", host: "portal.example.com", uri: "/a" },
        false,
      ),
    ).toContain("--uri '/a'");
    expect(
      buildCliCommandSnippet(
        { kind: "prefix", host: "portal.example.com", prefix: "/articles/" },
        false,
      ),
    ).toContain("--prefix '/articles/'");
    expect(
      buildCliCommandSnippet(
        { kind: "glob", host: "portal.example.com", pattern: "/articles/*" },
        false,
      ),
    ).toContain("--glob '/articles/*'");
    expect(
      buildCliCommandSnippet(
        {
          kind: "tag",
          host: "portal.example.com",
          tags: ["article:42", "tag-$(id)-`uname`-'quote'"],
          tag_match: "all",
        },
        false,
      ),
    ).toContain("--tag 'tag-$(id)-`uname`-'\\''quote'\\'''");
    expect(
      buildCliCommandSnippet(
        {
          kind: "tag",
          host: "portal.example.com",
          tags: ["article:42"],
          tag_match: "all",
        },
        false,
      ),
    ).toContain("--tag-match all");
    expect(
      buildCliCommandSnippet({ kind: "host", host: "portal.example.com" }, true),
    ).toContain("--confirm-broad");
    expect(buildCliCommandSnippet({ kind: "all", all: true }, true)).toContain("--all");
    expect(
      buildCliCommandSnippet({ kind: "key", key: "GET:example.com:/a" }, false),
    ).toContain("--key 'GET:example.com:/a'");
  });
});

describe("client.ts /api/v1/cache/purge* methods (#621)", () => {
  beforeEach(() => {
    authToken.set("test-token-not-shown-in-ui");
  });

  afterEach(() => {
    authToken.clear();
    globalThis.fetch = realFetch;
    vi.restoreAllMocks();
  });

  it("calls preview, execute (with Idempotency-Key), and operation status endpoints", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url === "/api/v1/cache/purge/preview") {
        return Promise.resolve(
          new Response(JSON.stringify(samplePreview), { status: 200 }),
        );
      }
      if (url === "/api/v1/cache/purge") {
        const headers = new Headers(init?.headers);
        expect(headers.get("Idempotency-Key")).toBe("idem-key-1");
        return Promise.resolve(
          new Response(JSON.stringify(samplePurgeSuccess), { status: 200 }),
        );
      }
      if (url === "/api/v1/cache/purges/cp_1_abcd1234") {
        return Promise.resolve(
          new Response(JSON.stringify(samplePurgeSuccess), { status: 200 }),
        );
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({
            error: { code: "INVALID_REQUEST", message: "selector.prefix must start with /" },
          }),
          { status: 400 },
        ),
      );
    });
    globalThis.fetch = fetchMock;

    const preview = await previewCachePurge(samplePreview.selector, true);
    expect(preview.matched_resources).toBe(2);
    expect(preview.preview_token).toBe("v1.signed.token");

    const exec = await executeCachePurge({
      selector: samplePreview.selector,
      previewToken: preview.preview_token,
      idempotencyKey: "idem-key-1",
    });
    expect(exec.operation_id).toBe("cp_1_abcd1234");
    expect(exec.invalidation_effective).toBe("effective");

    const status = await fetchCachePurgeOperation("cp_1_abcd1234");
    expect(status.operation_id).toBe("cp_1_abcd1234");
  });

  it("surfaces structured /api/v1 error code and message", async () => {
    globalThis.fetch = vi.fn(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            error: {
              code: "INVALID_REQUEST",
              message: "broad cache invalidation requires confirm_broad=true",
            },
          }),
          { status: 400 },
        ),
      ),
    );

    await expect(
      executeCachePurge({ selector: { kind: "all", all: true } }),
    ).rejects.toThrow(/INVALID_REQUEST: broad cache invalidation requires confirm_broad=true/);
  });
});

describe("CacheInvalidationPanel full operator workflow (#621)", () => {
  beforeEach(() => {
    authToken.set("operator-session-token");
  });

  afterEach(() => {
    cleanup();
    authToken.clear();
    globalThis.fetch = realFetch;
    vi.restoreAllMocks();
  });

  function renderWorkflow(props?: Partial<React.ComponentProps<typeof CacheInvalidationPanel>>) {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, refetchInterval: false } },
    });
    return render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <CacheInvalidationPanel
            cacheEnabled={true}
            tagHeaders={["Cache-Tag"]}
            serverNames={["portal.example.com", "api.example.com"]}
            routes={[
              {
                listen: ":443",
                http3: false,
                h2c: false,
                server_names: ["portal.example.com"],
                locations: [
                  {
                    index: 0,
                    match: "/articles/",
                    type: "prefix",
                    action: "proxy",
                    route_id: "route-articles",
                    auth: false,
                    cache: true,
                    compression: false,
                    rate_limit: false,
                    secure: true,
                    require_client_cert: false,
                  },
                ],
              },
            ]}
            authorityMode="file_owned"
            {...props}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );
  }

  it("executes select scope -> preview -> confirm -> execute and clears preview when selector changes", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/me")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              authenticated: true,
              principal: "ops",
              role: "admin",
              permissions: ["stats:read", "cache:purge", "config:read"],
            }),
            { status: 200 },
          ),
        );
      }
      if (url === "/api/v1/cache/purge/preview") {
        return Promise.resolve(
          new Response(JSON.stringify(samplePreview), { status: 200 }),
        );
      }
      if (url === "/api/v1/cache/purge") {
        return Promise.resolve(
          new Response(JSON.stringify(samplePurgeSuccess), { status: 200 }),
        );
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    globalThis.fetch = fetchMock;

    renderWorkflow();

    // Authority badge shows file_owned operational allowance.
    expect(
      screen.getByText(/file_owned \(operational purge allowed\)/),
    ).toBeInTheDocument();

    // Switch selector kind to tag and fill tag.
    fireEvent.change(screen.getByLabelText("Invalidation selector kind"), {
      target: { value: "tag" },
    });
    fireEvent.change(screen.getByLabelText("Origin grouping tags"), {
      target: { value: "article:42" },
    });

    // Click preview.
    fireEvent.click(screen.getByRole("button", { name: "Preview invalidation scope" }));

    expect(await screen.findByRole("region", { name: "Invalidation preview" })).toBeInTheDocument();
    expect(screen.getByText("4 entries (4.0 KiB)")).toBeInTheDocument();
    expect(screen.getByText("2 entries (4.0 KiB)")).toBeInTheDocument();
    expect(screen.getByText(/portal\.example\.com\/articles\/42\?\[REDACTED\]/)).toBeInTheDocument();

    // Editing selector input invalidates the preview so stale confirmation is impossible.
    fireEvent.change(screen.getByLabelText("Origin grouping tags"), {
      target: { value: "article:43" },
    });
    expect(screen.queryByRole("region", { name: "Invalidation preview" })).not.toBeInTheDocument();

    // Preview again and execute.
    fireEvent.click(screen.getByRole("button", { name: "Preview invalidation scope" }));
    expect(await screen.findByRole("region", { name: "Invalidation preview" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Confirm & invalidate" }));

    const resultRegion = await screen.findByLabelText("Invalidation execution result");
    expect(resultRegion).toBeInTheDocument();
    expect(resultRegion).toHaveTextContent("cp_1_abcd1234");
    expect(resultRegion).toHaveTextContent("effective");
  });

  it("enforces confirm_broad for broad-scope previews, handles partial failure retry, and supports operation lookup", async () => {
    const broadPreview: CachePurgePreviewResponse = {
      ...samplePreview,
      selector: { kind: "host", host: "portal.example.com" },
      broad_scope: true,
      truncated: true,
      count_accuracy: "lower_bound_truncated",
      matched_resources: 250,
      matched_representations: 500,
    };
    const partialResponse: CachePurgeResponse = {
      ...samplePurgeSuccess,
      operation_id: "cp_2_partial",
      state: "partial",
      outcome: "partial_failure",
      invalidation_effective: "incomplete",
      failed_entries: 2,
      remaining_entries: 2,
      recovery_guidance:
        "2 disk entries could not be removed. Retry the same selector to complete cleanup.",
    };

    let purgeCalls = 0;
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/me")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              authenticated: true,
              principal: "ops",
              role: "admin",
              permissions: ["stats:read", "cache:purge"],
            }),
            { status: 200 },
          ),
        );
      }
      if (url === "/api/v1/cache/purge/preview") {
        return Promise.resolve(new Response(JSON.stringify(broadPreview), { status: 200 }));
      }
      if (url === "/api/v1/cache/purge") {
        purgeCalls += 1;
        const body = purgeCalls === 1 ? partialResponse : { ...samplePurgeSuccess, operation_id: "cp_2_partial" };
        return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
      }
      if (url === "/api/v1/cache/purges/cp_99_lookup") {
        return Promise.resolve(
          new Response(
            JSON.stringify({ ...samplePurgeSuccess, operation_id: "cp_99_lookup" }),
            { status: 200 },
          ),
        );
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    });
    globalThis.fetch = fetchMock;

    renderWorkflow({ cacheEnabled: false, tagHeaders: [] });

    // Disabled cache info note is shown.
    expect(screen.getByText(/Response caching is currently disabled/)).toBeInTheDocument();

    // Use route helper with prefix kind.
    fireEvent.change(screen.getByLabelText("Invalidation selector kind"), {
      target: { value: "prefix" },
    });
    fireEvent.change(screen.getByLabelText("Configured route helper"), {
      target: { value: "route-articles" },
    });
    expect(screen.getByLabelText("Anchored path prefix")).toHaveValue("/articles/");

    // Switch to host scope and preview.
    fireEvent.change(screen.getByLabelText("Invalidation selector kind"), {
      target: { value: "host" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Preview invalidation scope" }));

    expect(await screen.findByText(/\[TRUNCATED PREVIEW\]/)).toBeInTheDocument();

    // Confirm & invalidate is disabled until confirm_broad checkbox is checked.
    const confirmBtn = screen.getByRole("button", { name: "Confirm & invalidate" });
    expect(confirmBtn).toBeDisabled();

    fireEvent.click(screen.getByLabelText(/Confirm broad invalidation/));
    expect(confirmBtn).not.toBeDisabled();

    // Execute -> returns partial failure with retry button.
    fireEvent.click(confirmBtn);
    expect(await screen.findByText(/Recovery guidance:/)).toBeInTheDocument();

    const retryBtn = screen.getByRole("button", { name: /Retry invalidation \(cp_2_partial\)/ });
    expect(retryBtn).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Confirm retry scope"));
    fireEvent.click(retryBtn);
    await waitFor(() => {
      expect(purgeCalls).toBe(2);
    });

    // Operation lookup by ID.
    fireEvent.change(screen.getByLabelText("Inspect purge operation ID"), {
      target: { value: "cp_99_lookup" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Check operation status" }));
    expect(await screen.findByText("cp_99_lookup")).toBeInTheDocument();
  });

  it("clears preview and alerts operator when credentials or permissions change while confirmation is open", async () => {
    globalThis.fetch = vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/me")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              authenticated: true,
              principal: "ops",
              role: "admin",
              permissions: ["stats:read", "cache:purge"],
            }),
            { status: 200 },
          ),
        );
      }
      if (url === "/api/v1/cache/purge/preview") {
        return Promise.resolve(
          new Response(
            JSON.stringify({ ...samplePreview, matched_representations: 0 }),
            { status: 200 },
          ),
        );
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    });

    renderWorkflow({ tagHeaders: [] });

    fireEvent.change(screen.getByLabelText("Invalidation selector kind"), {
      target: { value: "tag" },
    });
    expect(screen.getByText(/TAG_INGESTION_DISABLED/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Origin grouping tags"), {
      target: { value: "article:42" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Preview invalidation scope" }));

    expect(await screen.findByText(/\[NO STORED MATCHES\]/)).toBeInTheDocument();

    // Dispatch forbidden event while confirmation is open.
    fireEvent(window, new CustomEvent("jul:forbidden"));

    expect(await screen.findByText(/\[PERMISSION CHANGE\]/)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Invalidation preview" })).not.toBeInTheDocument();
  });

  it("renders tag header controls in TrafficControlEditor cache drawer", async () => {
    globalThis.fetch = vi.fn(() =>
      Promise.resolve(new Response("{}", { status: 200 })),
    );

    const current = {
      cache: {
        enabled: true,
        memory_max_size: "64MiB",
        disk_path: "/var/cache/jul",
        disk_max_size: "1GiB",
        max_entry_size: "32MiB",
        default_ttl: "1m0s",
        stale_while_revalidate: "30s",
        stale_if_error: "5m0s",
        lock: false,
        lock_timeout: "",
        range_enabled: false,
        max_ranges: 0,
        tag_headers: ["Cache-Tag"],
        strip_tag_headers: true,
      },
    };
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, refetchInterval: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <TrafficControlEditor kind="cache" current={current} onClose={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await screen.findByRole("region", { name: "Scoped cache invalidation" });
    const tagHeaderInput = screen.getByLabelText(/Trusted origin tag headers/);
    expect(tagHeaderInput).toHaveValue("Cache-Tag");
    expect(
      screen.getByLabelText("Strip configured tag headers from client responses"),
    ).toBeChecked();

    // Entering reserved ETag header shows validation error.
    fireEvent.change(tagHeaderInput, { target: { value: "ETag" } });
    expect(
      screen.getByText(/Tag headers must be 1 to 8 unique, non-reserved HTTP header token names/),
    ).toBeInTheDocument();

    fireEvent.change(tagHeaderInput, { target: { value: "Cache-Tag, Surrogate-Key" } });
    expect(
      screen.queryByText(/Tag headers must be 1 to 8 unique, non-reserved HTTP header token names/),
    ).not.toBeInTheDocument();
  });

  it("exercises exact_url, glob, all, error states, and byte formatting in CacheInvalidationPanel", async () => {
    const mibPreview: CachePurgePreviewResponse = {
      ...samplePreview,
      memory_bytes: 512,
      disk_bytes: 3 * 1024 * 1024,
    };
    let shouldFail = true;
    globalThis.fetch = vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/me")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              authenticated: true,
              principal: "ops",
              role: "admin",
              permissions: ["stats:read", "cache:purge"],
            }),
            { status: 200 },
          ),
        );
      }
      if (shouldFail) {
        return Promise.resolve(
          new Response(
            JSON.stringify({ error: { code: "INVALID_REQUEST", message: "bad selector" } }),
            { status: 400 },
          ),
        );
      }
      return Promise.resolve(new Response(JSON.stringify(mibPreview), { status: 200 }));
    });

    renderWorkflow();

    // Preview with empty exact_url inputs triggers client validation error.
    fireEvent.change(screen.getByLabelText("Target hostname"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview invalidation scope" }));
    expect(await screen.findByText(/Enter either a full http/)).toBeInTheDocument();

    // Use route helper when kind is exact_url and glob.
    fireEvent.change(screen.getByLabelText("Configured route helper"), {
      target: { value: "route-articles" },
    });
    expect(screen.getByLabelText("Exact request URI")).toHaveValue("/articles/");

    fireEvent.change(screen.getByLabelText("Invalidation selector kind"), {
      target: { value: "glob" },
    });
    fireEvent.change(screen.getByLabelText("Configured route helper"), {
      target: { value: "route-articles" },
    });
    expect(screen.getByLabelText("Anchored glob pattern")).toHaveValue("/articles/**");
    fireEvent.change(screen.getByLabelText("Anchored glob pattern"), {
      target: { value: "/articles/*/comments" },
    });

    // Switch to exact_url and fill URL and URI.
    fireEvent.change(screen.getByLabelText("Invalidation selector kind"), {
      target: { value: "exact_url" },
    });
    fireEvent.change(screen.getByLabelText("Exact public URL"), {
      target: { value: "https://portal.example.com/articles/42" },
    });
    fireEvent.change(screen.getByLabelText("Exact request URI"), {
      target: { value: "/articles/42" },
    });

    // Switch to tag and change tagMatch.
    fireEvent.change(screen.getByLabelText("Invalidation selector kind"), {
      target: { value: "tag" },
    });
    fireEvent.change(screen.getByLabelText("Multi-tag match mode"), {
      target: { value: "all" },
    });
    fireEvent.change(screen.getByLabelText("Origin grouping tags"), {
      target: { value: "article:42" },
    });

    // Preview failure shows API error.
    fireEvent.click(screen.getByRole("button", { name: "Preview invalidation scope" }));
    expect(await screen.findByText(/INVALID_REQUEST: bad selector/)).toBeInTheDocument();

    // Lookup failure shows API error.
    fireEvent.change(screen.getByLabelText("Inspect purge operation ID"), {
      target: { value: "cp_missing" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Check operation status" }));
    expect(await screen.findByText(/INVALID_REQUEST: bad selector/)).toBeInTheDocument();

    // Succeed preview and check byte formatting (<1024 B and MiB), then fail execute.
    shouldFail = false;
    fireEvent.click(screen.getByRole("button", { name: "Preview invalidation scope" }));
    expect(await screen.findByText("4 entries (512 B)")).toBeInTheDocument();
    expect(screen.getByText("2 entries (3.00 MiB)")).toBeInTheDocument();

    shouldFail = true;
    fireEvent.click(screen.getByRole("button", { name: "Confirm & invalidate" }));
    expect(await screen.findByText(/INVALID_REQUEST: bad selector/)).toBeInTheDocument();
  });

  it("preserves unredacted query selector on execute/retry, omits fabricated route_id, and handles omitted wire arrays (F07, F08)", async () => {
    const sentPurgeBodies: Array<Record<string, unknown>> = [];
    const sentPreviewBodies: Array<Record<string, unknown>> = [];
    let purgeCall = 0;

    globalThis.fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/me")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              authenticated: true,
              principal: "ops",
              role: "admin",
              permissions: ["stats:read", "cache:purge"],
            }),
            { status: 200 },
          ),
        );
      }
      if (url === "/api/v1/cache/purge/preview") {
        const rawBody = typeof init?.body === "string" ? init.body : "{}";
        const parsed = JSON.parse(rawBody) as Record<string, unknown>;
        sentPreviewBodies.push(parsed);
        // Simulate wire response with redacted display selector and omitted samples/warnings arrays (F07, F08).
        return Promise.resolve(
          new Response(
            JSON.stringify({
              api_version: "v1",
              cache_enabled: true,
              tag_ingestion_enabled: true,
              selector: {
                kind: "exact_url",
                host: "portal.example.com",
                uri: "/articles/42?<redacted>",
              },
              selector_digest: "deadbeef",
              preview_token: "cpt1.10.1790000000.0123456789abcdef0123456789abcdef",
              matched_resources: 0,
              matched_representations: 0,
              memory_entries: 0,
              disk_entries: 0,
              matched_bytes: 0,
              memory_bytes: 0,
              disk_bytes: 0,
              inflight_fills_matched: 0,
              count_accuracy: "exact",
              truncated: false,
              scan_limit: 10000,
              scanned_entries: 0,
              source: "live_cache_index",
              as_of: "2026-10-25T10:00:00Z",
              cache_sequence: 10,
              execution_mode_contract: "reevaluate_selector_with_fence",
              broad_scope: false,
            }),
            { status: 200 },
          ),
        );
      }
      if (url === "/api/v1/cache/purge") {
        purgeCall += 1;
        const rawBody = typeof init?.body === "string" ? init.body : "{}";
        const parsed = JSON.parse(rawBody) as Record<string, unknown>;
        sentPurgeBodies.push(parsed);
        // First call returns partial with redacted display selector and omitted warnings; retry succeeds.
        return Promise.resolve(
          new Response(
            JSON.stringify({
              api_version: "v1",
              operation_id: "cp_query_1",
              state: purgeCall === 1 ? "partial" : "succeeded",
              terminal: true,
              outcome: purgeCall === 1 ? "partial_failure" : "succeeded",
              invalidation_effective: purgeCall === 1 ? "incomplete" : "effective",
              selector: {
                kind: "exact_url",
                host: "portal.example.com",
                uri: "/articles/42?<redacted>",
              },
              selector_digest: "deadbeef",
              invalidated_resources: 1,
              invalidated_representations: 1,
              memory_entries_removed: 1,
              disk_entries_removed: 0,
              started_at: "2026-10-25T10:00:01Z",
              boot_id: "boot-1",
            }),
            { status: 200 },
          ),
        );
      }
      if (url === "/api/v1/cache/purges/cp_running_no_warnings") {
        // Running operation omitting warnings on the wire (F08).
        return Promise.resolve(
          new Response(
            JSON.stringify({
              api_version: "v1",
              operation_id: "cp_running_no_warnings",
              state: "running",
              terminal: false,
              outcome: "running",
              invalidation_effective: "incomplete",
              selector: { kind: "prefix", host: "portal.example.com", prefix: "/articles/" },
              selector_digest: "deadbeef",
              invalidated_resources: 0,
              invalidated_representations: 0,
              memory_entries_removed: 0,
              disk_entries_removed: 0,
              started_at: "2026-10-25T10:00:01Z",
              boot_id: "boot-1",
            }),
            { status: 202 },
          ),
        );
      }
      if (url === "/api/v1/cache/purges/cp_redacted_lookup") {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              api_version: "v1",
              operation_id: "cp_redacted_lookup",
              state: "partial",
              terminal: true,
              outcome: "partial_failure",
              invalidation_effective: "incomplete",
              selector: {
                kind: "exact_url",
                host: "portal.example.com",
                uri: "/articles/42?<redacted>",
              },
              selector_digest: "deadbeef",
              invalidated_resources: 1,
              invalidated_representations: 1,
              memory_entries_removed: 1,
              disk_entries_removed: 0,
              started_at: "2026-10-25T10:00:01Z",
              boot_id: "boot-1",
            }),
            { status: 200 },
          ),
        );
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    });

    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, refetchInterval: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <CacheInvalidationPanel
            cacheEnabled={true}
            tagHeaders={["Cache-Tag"]}
            serverNames={["portal.example.com", "second.example.com"]}
            routes={[
              {
                listen: ":443",
                http3: false,
                h2c: false,
                server_names: ["portal.example.com"],
                locations: [
                  {
                    index: 0,
                    match: "/no-id-route/",
                    type: "prefix",
                    action: "proxy",
                    auth: false,
                    cache: true,
                    compression: false,
                    rate_limit: false,
                    secure: true,
                    require_client_cert: false,
                  },
                ],
              },
              {
                listen: ":443",
                http3: false,
                h2c: false,
                server_names: ["second.example.com"],
                locations: [
                  {
                    index: 0,
                    match: "/no-id-route/",
                    type: "prefix",
                    action: "proxy",
                    auth: false,
                    cache: true,
                    compression: false,
                    rate_limit: false,
                    secure: true,
                    require_client_cert: false,
                  },
                ],
              },
            ]}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    // Selecting second vhost route updates host to second.example.com; selecting first restores portal.example.com without fabricating route_id (F07).
    fireEvent.change(screen.getByLabelText("Configured route helper"), {
      target: { value: "1:second.example.com:0:/no-id-route/" },
    });
    expect(screen.getByLabelText("Target hostname")).toHaveValue("second.example.com");
    fireEvent.change(screen.getByLabelText("Configured route helper"), {
      target: { value: "0:portal.example.com:0:/no-id-route/" },
    });
    expect(screen.getByLabelText("Target hostname")).toHaveValue("portal.example.com");
    expect(screen.getByLabelText("Exact request URI")).toHaveValue("/no-id-route/");

    // Enter a queried URL and preview.
    fireEvent.change(screen.getByLabelText("Exact public URL"), {
      target: { value: "https://portal.example.com/articles/42?lang=es" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Preview invalidation scope" }));
    expect(await screen.findByRole("region", { name: "Invalidation preview" })).toBeInTheDocument();

    // Preview request must have no route_id and must carry the full queried URL.
    expect(sentPreviewBodies[0]?.selector).toEqual({
      kind: "exact_url",
      host: "portal.example.com",
      url: "https://portal.example.com/articles/42?lang=es",
    });

    // Execute must send the original unredacted selector, NOT uri="/articles/42?<redacted>".
    fireEvent.click(screen.getByRole("button", { name: "Confirm & invalidate" }));
    expect(await screen.findByLabelText("Invalidation execution result")).toBeInTheDocument();
    expect(sentPurgeBodies[0]?.selector).toEqual({
      kind: "exact_url",
      host: "portal.example.com",
      url: "https://portal.example.com/articles/42?lang=es",
    });

    // Retry must also send the original unredacted selector, NOT result.selector.
    fireEvent.click(screen.getByLabelText("Confirm retry scope"));
    fireEvent.click(screen.getByRole("button", { name: /Retry invalidation/ }));
    await waitFor(() => {
      expect(sentPurgeBodies.length).toBe(2);
    });
    expect(sentPurgeBodies[1]?.selector).toEqual({
      kind: "exact_url",
      host: "portal.example.com",
      url: "https://portal.example.com/articles/42?lang=es",
    });

    // Lookup a running operation that omits warnings on the wire without throwing (F08).
    fireEvent.change(screen.getByLabelText("Inspect purge operation ID"), {
      target: { value: "cp_running_no_warnings" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Check operation status" }));
    expect(await screen.findByText(/RUNNING · running/)).toBeInTheDocument();

    // Lookup a partial operation with a redacted query selector when form inputs are cleared:
    // clicking Retry must NOT send ?<redacted> to the server, and instead prompt to re-enter the URL.
    fireEvent.change(screen.getByLabelText("Exact public URL"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Exact request URI"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Inspect purge operation ID"), {
      target: { value: "cp_redacted_lookup" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Check operation status" }));
    const retryRedactedBtn = await screen.findByRole("button", {
      name: "Retry invalidation (cp_redacted_lookup)",
    });
    fireEvent.click(screen.getByLabelText("Confirm retry scope"));
    fireEvent.click(retryRedactedBtn);
    expect(await screen.findByText(/re-enter the full URL above/)).toBeInTheDocument();
    expect(sentPurgeBodies.length).toBe(2);
  });

  it("ignores delayed preview responses when the operator edits the form while preview is pending (R03)", async () => {
    let resolvePreviewA: ((resp: Response) => void) | undefined;
    const sentPurgeBodies: Array<Record<string, unknown>> = [];
    let previewCallCount = 0;

    globalThis.fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/me")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              authenticated: true,
              principal: "ops",
              role: "admin",
              permissions: ["stats:read", "cache:purge"],
            }),
            { status: 200 },
          ),
        );
      }
      if (url === "/api/v1/cache/purge/preview") {
        previewCallCount += 1;
        if (previewCallCount === 1) {
          return new Promise<Response>((resolve) => {
            resolvePreviewA = resolve;
          });
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({
              ...samplePreview,
              selector: {
                kind: "exact_url",
                host: "portal.example.com",
                uri: "/article-b",
              },
              preview_token: "cpt1.11.1790000000.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            }),
            { status: 200 },
          ),
        );
      }
      if (url === "/api/v1/cache/purge") {
        const rawBody = typeof init?.body === "string" ? init.body : "{}";
        sentPurgeBodies.push(JSON.parse(rawBody) as Record<string, unknown>);
        return Promise.resolve(
          new Response(
            JSON.stringify({
              ...samplePurgeSuccess,
              selector: {
                kind: "exact_url",
                host: "portal.example.com",
                uri: "/article-b",
              },
            }),
            { status: 200 },
          ),
        );
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    });

    renderWorkflow();

    // 1. Start preview for /article-a.
    fireEvent.change(screen.getByLabelText("Exact request URI"), {
      target: { value: "/article-a" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Preview invalidation scope" }));
    expect(resolvePreviewA).toBeDefined();

    // 2. Change the URI input to /article-b while /article-a's preview is still pending.
    fireEvent.change(screen.getByLabelText("Exact request URI"), {
      target: { value: "/article-b" },
    });

    // 3. Resolve /article-a's delayed response: it must NOT restore /article-a's preview or confirmedSelector.
    resolvePreviewA?.(
      new Response(
        JSON.stringify({
          ...samplePreview,
          selector: {
            kind: "exact_url",
            host: "portal.example.com",
            uri: "/article-a",
          },
          preview_token: "cpt1.10.1790000000.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        }),
        { status: 200 },
      ),
    );
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: "Invalidation preview" })).not.toBeInTheDocument();
    });
    expect(screen.queryByRole("button", { name: "Confirm & invalidate" })).not.toBeInTheDocument();

    // 4. Preview /article-b and confirm: must submit /article-b, never /article-a.
    fireEvent.click(screen.getByRole("button", { name: "Preview invalidation scope" }));
    expect(await screen.findByRole("region", { name: "Invalidation preview" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Confirm & invalidate" }));
    expect(await screen.findByLabelText("Invalidation execution result")).toBeInTheDocument();
    expect(sentPurgeBodies).toHaveLength(1);
    expect(sentPurgeBodies[0]?.selector).toEqual({
      kind: "exact_url",
      host: "portal.example.com",
      uri: "/article-b",
    });
  });

  it("exercises limitsToml, limitsWarnings, and TrafficControlEditor compression/rate_limit/limits/cache flows", async () => {
    expect(
      generateLimitsToml({
        bodyLimit: "",
        readTimeout: "",
        writeTimeout: "",
        idleTimeout: "",
        proxyConnectTimeout: "",
        proxyReadTimeout: "",
        proxySendTimeout: "",
        maxFails: 0,
        failTimeout: "",
      }),
    ).toContain("No limits set");

    const populatedLimits = generateLimitsToml({
      bodyLimit: "2g",
      readTimeout: "0",
      writeTimeout: "10s",
      idleTimeout: "60s",
      proxyConnectTimeout: "0",
      proxyReadTimeout: "30s",
      proxySendTimeout: "30s",
      maxFails: 12,
      failTimeout: "10s",
    });
    expect(populatedLimits).toContain("client_max_body_size");
    expect(populatedLimits).toContain("proxy_connect_timeout");
    expect(populatedLimits).toContain("max_fails = 12");

    expect(
      limitsWarnings({
        bodyLimit: "2g",
        readTimeout: "0",
        writeTimeout: "10s",
        idleTimeout: "60s",
        proxyConnectTimeout: "0",
        proxyReadTimeout: "30s",
        proxySendTimeout: "30s",
        maxFails: 12,
        failTimeout: "10s",
      }),
    ).toHaveLength(4);

    expect(
      cacheWarnings({
        enabled: true,
        memoryMaxSize: "64m",
        diskPath: "/var/cache",
        diskMaxSize: "1g",
        defaultTTL: "24h",
        staleWhileRevalidate: "",
        staleIfError: "",
      }).length,
    ).toBeGreaterThan(0);

    globalThis.fetch = vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/me")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              authenticated: true,
              principal: "ops",
              role: "admin",
              permissions: ["config:write", "config:raw", "cache:purge", "stats:read"],
            }),
            { status: 200 },
          ),
        );
      }
      if (url === "/api/routes") {
        return Promise.resolve(
          new Response(
            JSON.stringify([
              {
                listen: ":443",
                http3: false,
                h2c: false,
                server_names: ["portal.example.com"],
                locations: [
                  {
                    index: 0,
                    match: "/articles/",
                    type: "prefix",
                    action: "proxy",
                    auth: false,
                    cache: true,
                    compression: true,
                    rate_limit: true,
                    secure: true,
                    require_client_cert: false,
                  },
                ],
              },
            ]),
            { status: 200 },
          ),
        );
      }

      if (url === "/api/config") {
        return Promise.resolve(
          new Response(
            JSON.stringify({ raw: "[cache]\nenabled = true\n", base_version: "v1" }),
            { status: 200 },
          ),
        );
      }
      if (url === "/api/config/pending-restart") {
        return Promise.resolve(
          new Response(JSON.stringify({ pending: false }), { status: 200 }),
        );
      }
      if (url === "/api/config/preview") {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              ok: true,
              base_version: "v1",
              valid: true,
              validation_errors: [],
              diff: { summary: "cache updated" },
              lifecycle: {
                can_apply_hot: true,
                can_stage_restart: false,
              },
            }),
            { status: 200 },
          ),
        );
      }
      return Promise.resolve(new Response("[]", { status: 200 }));
    });

    const traffic = {
      compression: {
        enabled: true,
        encoders: ["gzip"],
        level: 5,
        min_size: "1k",
        types: ["text/plain"],
        precompressed: false,
      },
      rate_limit: {
        enabled: true,
        key: "ip",
        rate: 50,
        burst: 100,
        max_conns: 200,
      },
      cache: {
        enabled: true,
        memory_max_size: "64m",
        disk_path: "/var/cache/jul",
        disk_max_size: "1g",
        max_entry_size: "16m",
        default_ttl: "60s",
        stale_while_revalidate: "10s",
        stale_if_error: "30s",
        lock: false,
        lock_timeout: "",
        range_enabled: false,
        max_ranges: 0,
        tag_headers: ["Cache-Tag"],
        strip_tag_headers: true,
      },
      servers: [
        {
          listen: ":8080",
          server_names: ["portal.example.com"],
          client_max_body_size: "10m",
          read_timeout: "15s",
          write_timeout: "15s",
          idle_timeout: "60s",
        },
        {
          listen: ":8443",
          server_names: ["api.example.com"],
          client_max_body_size: "20m",
          read_timeout: "30s",
          write_timeout: "30s",
          idle_timeout: "120s",
        },
      ],
    };

    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, refetchInterval: false } },
    });
    const { rerender } = render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <TrafficControlEditor kind="compression" current={traffic} onClose={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByLabelText("Enable compression"));
    fireEvent.click(screen.getByLabelText("br"));
    fireEvent.click(screen.getByLabelText("text/*"));
    fireEvent.change(screen.getByLabelText("Compression level"), { target: { value: "6" } });
    fireEvent.change(screen.getByLabelText("Minimum response size"), { target: { value: "2k" } });
    fireEvent.click(screen.getByLabelText("Serve precompressed sidecars"));
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Review changes" })).not.toBeDisabled();
    });
    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));

    rerender(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <TrafficControlEditor kind="rate_limit" current={traffic} onClose={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    fireEvent.click(screen.getByLabelText("Enable global rate limiting"));
    fireEvent.change(screen.getByLabelText(/Key/), { target: { value: "header:X-Key" } });
    fireEvent.change(screen.getByLabelText("Requests per second"), { target: { value: "100" } });
    fireEvent.change(screen.getByLabelText("Burst"), { target: { value: "200" } });
    fireEvent.change(screen.getByLabelText("Maximum concurrent connections"), {
      target: { value: "300" },
    });

    rerender(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <TrafficControlEditor kind="limits" current={traffic} onClose={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => {
      expect(screen.getByLabelText("Client maximum body size")).toHaveValue("10m");
    });
    fireEvent.change(screen.getByLabelText("Server"), {
      target: { value: JSON.stringify([":8443", ["api.example.com"]]) },
    });
    expect(screen.getByLabelText("Client maximum body size")).toHaveValue("20m");
    fireEvent.change(screen.getByLabelText("Client maximum body size"), {
      target: { value: "25m" },
    });
    fireEvent.change(screen.getByLabelText("Read timeout"), { target: { value: "20s" } });
    fireEvent.change(screen.getByLabelText("Write timeout"), { target: { value: "20s" } });
    fireEvent.change(screen.getByLabelText("Idle timeout"), { target: { value: "90s" } });
    fireEvent.change(screen.getByLabelText("Proxy connect timeout"), { target: { value: "6s" } });
    fireEvent.change(screen.getByLabelText("Proxy read timeout"), { target: { value: "35s" } });
    fireEvent.change(screen.getByLabelText("Proxy send timeout"), { target: { value: "35s" } });
    fireEvent.change(screen.getByLabelText("Maximum failures"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Failure timeout"), { target: { value: "15s" } });

    rerender(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <TrafficControlEditor kind="cache" current={traffic} onClose={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    fireEvent.change(screen.getByLabelText("Memory maximum size"), { target: { value: "128m" } });
    fireEvent.change(screen.getByLabelText("Disk path"), { target: { value: "/var/cache/jul2" } });
    fireEvent.change(screen.getByLabelText("Disk maximum size"), { target: { value: "2g" } });
    fireEvent.change(screen.getByLabelText("Default TTL"), { target: { value: "120s" } });
    fireEvent.change(screen.getByLabelText("Stale while revalidate"), {
      target: { value: "20s" },
    });
    fireEvent.change(screen.getByLabelText("Stale if error"), { target: { value: "60s" } });
    fireEvent.click(
      screen.getByLabelText("Strip configured tag headers from client responses"),
    );

    // All cache invalidation uses the structured preview/confirm panel.
    expect(await screen.findByRole("button", { name: "Preview invalidation scope" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Purge cache" })).not.toBeInTheDocument();


    const reviewBtn = screen.getByRole("button", { name: "Review staged change" });
    await waitFor(() => {
      expect(reviewBtn).not.toBeDisabled();
    });
    fireEvent.click(reviewBtn);
  });

  it("exercises observability, audit, plugin upload, methods in client.ts", async () => {
    globalThis.fetch = vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.includes("/api/search")) {
        return Promise.resolve(new Response("[]", { status: 200 }));
      }
      if (url.includes("/api/observability/requests")) {
        return Promise.resolve(
          new Response(
            JSON.stringify([{ time: "2026-10-25T00:00:00Z", method: "GET", path: "/", status: 200 }]),
            { status: 200 },
          ),
        );
      }
      if (url.includes("/api/observability/failing-routes")) {
        return Promise.resolve(new Response("[]", { status: 200 }));
      }
      if (url.includes("/api/observability/upstream-history")) {
        return Promise.resolve(new Response("[]", { status: 200 }));
      }
      if (url.includes("/api/observability/cert-history")) {
        return Promise.resolve(new Response("[]", { status: 200 }));
      }
      if (url.includes("/api/observability/timeline")) {
        return Promise.resolve(new Response("[]", { status: 200 }));
      }
      if (url.includes("/api/admin/health")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              status: "ok",
              requests: 10,
              errors: 0,
              latency_p50: 1,
              latency_p95: 2,
              latency_p99: 3,
              sse_conns: 1,
            }),
            { status: 200 },
          ),
        );
      }
      if (url.includes("/api/audit/export")) {
        return Promise.resolve(new Response("id,actor\n1,ops\n", { status: 200 }));
      }
      if (url.includes("/api/audit")) {
        return Promise.resolve(new Response("[]", { status: 200 }));
      }
      if (url.includes("/api/plugins/upload")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({ name: "test.wasm", path: "/etc/jul/plugins/test.wasm", size: 128 }),
            { status: 200 },
          ),
        );
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    });

    expect(await searchConfig("articles", "routes")).toEqual([]);
    expect((await fetchRequestSamples()).length).toBe(1);
    expect(await fetchFailingRoutes(10)).toEqual([]);
    expect(await fetchUpstreamHistory()).toEqual([]);
    expect(await fetchCertHistory()).toEqual([]);
    expect(await fetchTimeline()).toEqual([]);
    expect((await fetchConsoleHealth()).status).toBe("ok");
    reportClientError({ message: "test error" });
    expect(await fetchAudit({ op: "cache.purge", result: "success", limit: 10 })).toEqual([]);
    expect(auditExportUrl("csv", { op: "cache.purge", result: "success" })).toContain(
      "format=csv",
    );
    const uploaded = await uploadPluginWasm(new File(["wasm"], "test.wasm"));
    expect(uploaded.name).toBe("test.wasm");
    if (typeof URL.createObjectURL === "function") {
      await downloadAuditExport("csv", { op: "cache.purge" });
    }
  });
});
