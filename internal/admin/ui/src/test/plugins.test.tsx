/**
 * Vitest tests for the Console v2 Plugins panel (Phase 4h): the schema, the
 * draft → patch lib helpers, and the panel/editor rendering + interactions.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

import { PluginsProjectionSchema, type PluginProjection } from "@/api/client.ts";
import {
  emptyPluginDraft,
  seedPluginDraft,
  parseList,
  parseConfigMap,
  pluginDraftToPatch,
  pluginDraftWarnings,
} from "@/lib/plugins.ts";
import { PluginsPanel } from "@/features/plugins/PluginsPanel.tsx";

// ── schema ────────────────────────────────────────────────────────────────────

describe("PluginsProjectionSchema", () => {
  it("parses a plugin set with attachments", () => {
    const parsed = PluginsProjectionSchema.parse({
      compiled: true,
      plugins: [
        {
          name: "inject",
          source: "path",
          path: "header-inject.wasm",
          type: "middleware",
          kv: false,
          fetch: false,
          attachments: [{ scope: "location", role: "middleware", listen: ":8080", path: "/api" }],
        },
      ],
    });
    expect(parsed.compiled).toBe(true);
    expect(parsed.plugins[0]?.attachments?.[0]?.role).toBe("middleware");
  });

  it("rejects a plugin missing the required type", () => {
    expect(() =>
      PluginsProjectionSchema.parse({
        compiled: false,
        plugins: [{ name: "x", source: "path", kv: false, fetch: false }],
      }),
    ).toThrow();
  });
});

// ── lib helpers ───────────────────────────────────────────────────────────────

describe("plugins lib", () => {
  it("parseList splits comma and newline separated values", () => {
    expect(parseList("a, b\n c ,,")).toEqual(["a", "b", "c"]);
  });

  it("parseConfigMap parses key = value lines", () => {
    expect(parseConfigMap("header = X-Trace\n empty \n k=v")).toEqual({
      header: "X-Trace",
      empty: "",
      k: "v",
    });
  });

  it("pluginDraftToPatch omits empty optional fields", () => {
    const patch = pluginDraftToPatch(emptyPluginDraft());
    expect(patch).toEqual({ source: "path", type: "middleware" });
  });

  it("pluginDraftToPatch includes path, caps, hosts and config", () => {
    const draft = {
      ...emptyPluginDraft(),
      path: "x.wasm",
      fetch: true,
      allowedHosts: "api.example, auth.example",
      config: "k = v",
    };
    const patch = pluginDraftToPatch(draft);
    expect(patch.path).toBe("x.wasm");
    expect(patch.fetch).toBe(true);
    expect(patch.allowed_hosts).toEqual(["api.example", "auth.example"]);
    expect(patch.config).toEqual({ k: "v" });
  });

  it("seedPluginDraft keeps inline source for inline plugins", () => {
    const p: PluginProjection = {
      name: "embedded",
      source: "inline",
      type: "middleware",
      kv: true,
      fetch: false,
      config: { a: "1" },
    };
    const draft = seedPluginDraft(p);
    expect(draft.source).toBe("inline");
    expect(draft.kv).toBe(true);
    expect(draft.config).toBe("a = 1");
  });

  it("pluginDraftWarnings flags a missing name, path, and fetch allowlist", () => {
    const w = pluginDraftWarnings({ ...emptyPluginDraft(), fetch: true }, true);
    expect(w.some((m) => /name is required/i.test(m))).toBe(true);
    expect(w.some((m) => /module path is required/i.test(m))).toBe(true);
    expect(w.some((m) => /allowed host/i.test(m))).toBe(true);
  });
});

// ── panel ─────────────────────────────────────────────────────────────────────

const projection = {
  compiled: true,
  plugins: [
    {
      name: "inject",
      source: "path",
      path: "header-inject.wasm",
      type: "middleware",
      kv: false,
      fetch: false,
      attachments: [{ scope: "location", role: "middleware", listen: ":8080", path: "/api" }],
    },
    {
      name: "block",
      source: "path",
      path: "request-block.wasm",
      type: "handler",
      kv: false,
      fetch: false,
    },
    {
      name: "enrich",
      source: "path",
      path: "enrich.wasm",
      type: "middleware",
      kv: false,
      fetch: true,
      allowed_hosts: ["api.example.com", "auth.example.com"],
    },
  ],
};

describe("PluginsPanel", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(projection) }),
    );
  });
  afterEach(() => vi.restoreAllMocks());

  it("lists declared plugins with type badges", async () => {
    render(<PluginsPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("inject")).toBeInTheDocument();
    expect(screen.getByText("block")).toBeInTheDocument();
    expect(screen.getAllByText("middleware").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("handler")).toBeInTheDocument();
  });

  it("marks the panel Beta per the maturity model", async () => {
    render(<PluginsPanel />, { wrapper: Wrapper });
    await screen.findByText("inject");
    expect(screen.getByText("beta")).toBeInTheDocument();
  });

  it("shows the attachment and a detach control for a middleware plugin", async () => {
    render(<PluginsPanel />, { wrapper: Wrapper });
    expect(await screen.findByText(/:8080 \/api/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Detach" })).toBeInTheDocument();
  });

  it("disables Remove while a plugin is attached", async () => {
    render(<PluginsPanel />, { wrapper: Wrapper });
    await screen.findByText("inject");
    // inject (attached) → disabled; block (unattached) → enabled.
    const removeButtons = screen.getAllByRole("button", { name: "Remove" });
    expect(removeButtons[0]).toBeDisabled();
    expect(removeButtons[1]).toBeEnabled();
  });

  it("opens the editor drawer from New plugin", async () => {
    render(<PluginsPanel />, { wrapper: Wrapper });
    await screen.findByText("inject");
    fireEvent.click(screen.getByRole("button", { name: "New plugin" }));
    await waitFor(() => {
      expect(screen.getByText(/A plugin is a WASM module/i)).toBeInTheDocument();
    });
  });

  it("warns when the build lacks the plugin runtime", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ ...projection, compiled: false }),
      }),
    );
    render(<PluginsPanel />, { wrapper: Wrapper });
    expect(await screen.findByText(/does not include the WASM plugin runtime/i)).toBeInTheDocument();
  });

  it("shows the fetch egress allowlist on a fetch-enabled plugin", async () => {
    render(<PluginsPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("enrich")).toBeInTheDocument();
    expect(screen.getByText("api.example.com, auth.example.com")).toBeInTheDocument();
  });
});
