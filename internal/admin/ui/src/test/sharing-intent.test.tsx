/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

vi.mock("@/features/config/CodeEditor.tsx", () => ({
  CodeEditor: ({
    value,
    onChange,
    readOnly,
  }: {
    value: string;
    onChange: (v: string) => void;
    readOnly?: boolean;
  }) => (
    <textarea
      aria-label="editor"
      value={value}
      readOnly={readOnly ?? false}
      onChange={(e) => {
        onChange(e.target.value);
      }}
    />
  ),
}));

import { RouteDetail } from "@/features/routes/RouteDetail.tsx";
import { RoutesPanel } from "@/features/routes/RoutesPanel.tsx";
import { DiffView } from "@/features/config/DiffView.tsx";
import { ConfigPanel } from "@/features/config/ConfigPanel.tsx";
import { PermissionContext } from "@/auth/usePermission.ts";
import { emptyAuthDraft, generateRouteToml } from "@/lib/routeToml.ts";
import {
  formatAudienceDeltaClassLabel,
  formatExternalEnforcerNote,
  formatLocationSharingSummary,
  formatSharingModeLabel,
  sharingAckEntries,
  sharingPlanRequiresAck,
} from "@/lib/sharingIntent.ts";
import { setPendingDraft, takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type {
  ConfigDiff,
  LocationProjection,
  PatchLifecycle,
  RouteProjection,
  SharingPlanReport,
} from "@/api/client.ts";

const realFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = realFetch;
  takePendingDraft();
  vi.restoreAllMocks();
});

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchInterval: false } },
  });
  return (
    <QueryClientProvider client={qc}>
      <PermissionContext.Provider
        value={{
          identity: {
            principal: "admin",
            role: "admin",
            token_id: "t1",
            permissions: ["status:read", "config:read", "config:write", "config:apply", "config:raw"],
            legacy: false,
          },
          isLoading: false,
          ready: true,
          has: () => true,
        }}
      >
        <MemoryRouter>{children}</MemoryRouter>
      </PermissionContext.Provider>
    </QueryClientProvider>
  );
}

const HOT_LIFECYCLE: PatchLifecycle = {
  changes: [],
  can_apply_hot: true,
  can_stage_restart: true,
  hot_paths: ["servers.*.locations.*.sharing.mode"],
  restart_required_paths: [],
  new_listener_only_paths: [],
  ignored_deprecated_paths: [],
  validation_rejected_paths: [],
  pending_subsystems: [],
};

describe("sharingIntent helpers and TOML round-trip", () => {
  it("formats provisional mode labels, SD-12 external enforcer notes, and delta class labels", () => {
    expect(formatSharingModeLabel("preview")).toBe("Local preview");
    expect(formatSharingModeLabel("private_network")).toBe("Private network");
    expect(formatSharingModeLabel("restricted")).toBe("Restricted");
    expect(formatSharingModeLabel("public")).toBe("Public");
    expect(formatSharingModeLabel("unknown")).toBe("Unknown (resolved when loaded)");
    expect(formatSharingModeLabel("not_declared")).toBe("Not declared");
    expect(formatSharingModeLabel(undefined)).toBe("Not declared");
    expect(formatSharingModeLabel("custom")).toBe("custom");

    expect(formatExternalEnforcerNote("")).toBe("");
    expect(formatExternalEnforcerNote("cloudflare-access:dash")).toBe(
      "cloudflare-access:dash (declared by the operator, not verified by Jul)",
    );

    expect(formatLocationSharingSummary(undefined)).toBe("Not declared");
    expect(
      formatLocationSharingSummary({
        mode: "restricted",
        audience_label: "family",
        external_enforcer: "cloudflare-access:dash",
      }),
    ).toBe(
      'Restricted · "family" · cloudflare-access:dash (declared by the operator, not verified by Jul)',
    );

    expect(formatAudienceDeltaClassLabel("widened")).toBe("Widened audience");
    expect(formatAudienceDeltaClassLabel("narrowed")).toBe("Narrowed audience");
    expect(formatAudienceDeltaClassLabel("uncertain")).toBe("Uncertain audience change");
    expect(formatAudienceDeltaClassLabel("metadata_only")).toBe("Declared intent metadata only");
    expect(formatAudienceDeltaClassLabel("unchanged")).toBe("Unchanged audience");
    expect(formatAudienceDeltaClassLabel("other")).toBe("other");
  });

  it("extracts acknowledgement requirements and entry lists from SharingPlanReport", () => {
    expect(sharingPlanRequiresAck(undefined)).toBe(false);
    expect(sharingAckEntries(undefined)).toEqual([]);

    const reportWithFallback: SharingPlanReport = {
      entries: [
        {
          ref: { kind: "route_id", route_id: "dash" },
          display_label: "app.example /dash (dash)",
          expect: "present",
          class: "widened",
          declared_mode_before: "restricted",
          declared_mode_after: "public",
          declared_mode_widened: true,
          requires_acknowledgement: true,
          triggers_retirement: false,
          provider_checklist_required: false,
        },
      ],
      requires_acknowledgement: true,
      file_backed_entry_count: 0,
      file_backed_entries_truncated: false,
    };
    expect(sharingPlanRequiresAck(reportWithFallback)).toBe(true);
    expect(sharingAckEntries(reportWithFallback)).toEqual(["app.example /dash (dash)"]);
  });

  it("preserves route_id and [servers.locations.sharing] in generateRouteToml", () => {
    const toml = generateRouteToml({
      listen: "127.0.0.1:8080",
      serverNames: "dash.example.com",
      path: "/dash",
      matchType: "prefix",
      action: "proxy",
      target: "http://dashboard",
      auth: emptyAuthDraft(),
      cache: false,
      rateLimit: false,
      routeId: "dashboard",
      sharing: {
        mode: "restricted",
        audienceLabel: "family",
        externalEnforcer: "cloudflare-access:dash-team",
      },
    });
    expect(toml).toContain('route_id = "dashboard"');
    expect(toml).toContain("[servers.locations.sharing]");
    expect(toml).toContain('mode = "restricted"');
    expect(toml).toContain('audience_label = "family"');
    expect(toml).toContain('external_enforcer = "cloudflare-access:dash-team"');
  });
});

describe("RouteDetail and RoutesPanel read-only declared intent display", () => {
  it("renders declared sharing intent with SD-12 wording and preserves [servers.locations.sharing] in generated TOML", async () => {
    const loc: LocationProjection = {
      index: 0,
      route_id: "dashboard",
      match: "/",
      type: "prefix",
      action: "proxy",
      target: "http://dashboard",
      auth: false,
      cache: false,
      compression: false,
      rate_limit: false,
      secure: false,
      require_client_cert: false,
      sharing: {
        mode: "restricted",
        audience_label: "family",
        external_enforcer: "cloudflare-access:dash-team",
      },
    };
    const route: RouteProjection = {
      base_version: "v1",
      listen: "127.0.0.1:8080",
      server_names: ["dash.example.com"],
      http3: false,
      h2c: false,
      locations: [loc],
    };

    render(<RouteDetail route={route} loc={loc} onClose={vi.fn()} onEdit={vi.fn()} />, {
      wrapper: Wrapper,
    });

    expect(screen.getByText("Declared intent")).toBeInTheDocument();
    expect(screen.getByText("Restricted")).toBeInTheDocument();
    expect(screen.getByText(/Audience: family/)).toBeInTheDocument();
    expect(
      screen.getByText(/cloudflare-access:dash-team \(declared by the operator, not verified by Jul\)/),
    ).toBeInTheDocument();
    expect(screen.getByText(/\[servers\.locations\.sharing\]/)).toBeInTheDocument();

    globalThis.fetch = vi.fn().mockImplementation((input: string | URL) => {
      const url = typeof input === "string" ? input : input.href;
      if (url.endsWith("/api/routes")) {
        return Promise.resolve(
          new Response(JSON.stringify([route]), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return Promise.resolve(
        new Response(JSON.stringify({ base_version: "v1" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    });

    render(<RoutesPanel />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByTitle("Declared sharing intent: Restricted")).toBeInTheDocument();
    });
  });
});

describe("DiffView and ConfigPanel widening acknowledgement gate", () => {
  const wideningReport: SharingPlanReport = {
    entries: [
      {
        ref: { kind: "route_id", route_id: "dashboard" },
        display_label: "dash.example.com / (dashboard)",
        expect: "present",
        class: "widened",
        declared_mode_before: "restricted",
        declared_mode_after: "public",
        declared_mode_widened: true,
        requires_acknowledgement: true,
        triggers_retirement: false,
        reasons: ["all credential checks removed"],
        intent_findings: [
          {
            code: "intent_restricted_unenforced",
            severity: "warning",
            message: "location declares restricted with no Jul check",
            created: true,
          },
        ],
        provider_checklist_required: true,
      },
    ],
    requires_acknowledgement: true,
    acknowledgement_entries: ["dash.example.com / (dashboard)"],
    intent_findings: [
      {
        code: "intent_restricted_unenforced",
        severity: "warning",
        message: "location declares restricted with no Jul check",
        created: true,
      },
    ],
    file_backed_inputs_notice:
      "Publishing reads file-backed inputs again; entries whose loaded file contents changed at publish will have their open exchanges ended.",
    file_backed_entries: [
      {
        ref: { kind: "route_id", route_id: "dashboard" },
        display_label: "dash.example.com / (dashboard)",
        input_kinds: ["htpasswd"],
      },
    ],
    file_backed_entry_count: 1,
    file_backed_entries_truncated: false,
    provider_checklist_notice:
      "Review external provider policy, DNS, and connectors before applying: Jul cannot verify external audience changes.",
    postcondition: {
      status: "available",
      postcondition_version: 1,
      key_id: "pk1-testkey",
      base_config_digest: "hmac-sha256:11",
      candidate_config_digest: "hmac-sha256:22",
      repeatable: true,
      entries: [],
    },
  };

  it("renders SharingPlanSummary in DiffView", () => {
    const diff: ConfigDiff = {
      summary: "This change affects 1 item.",
      modifications: [
        {
          kind: "sharing",
          name: "dash.example.com /",
          before: "restricted",
          after: "public",
          detail: "Change declared sharing mode",
        },
      ],
      sharing: wideningReport,
    };

    render(<DiffView diff={diff} />, { wrapper: Wrapper });
    expect(screen.getByLabelText("Sharing and audience assessment")).toBeInTheDocument();
    expect(screen.getByText("Widened audience")).toBeInTheDocument();
    expect(screen.getByText(/Declared intent: Restricted → Public/)).toBeInTheDocument();
    expect(screen.getByText("intent_restricted_unenforced")).toBeInTheDocument();
    expect(screen.getByText(/Publishing reads file-backed inputs again/)).toBeInTheDocument();
    expect(screen.getByText(/available \(pk1-testkey\)/)).toBeInTheDocument();
  });

  it("disables Apply confirmation until the operator acknowledges widened/uncertain entries", async () => {
    let applyCalled = false;
    setPendingDraft({
      kind: "patch",
      ops: [
        {
          op: "location_set_sharing",
          route_id: "dashboard",
          sharing: { mode: "public" },
        },
      ],
      summary: "route sharing intent set to public",
      operationSummaries: [
        {
          op_index: 0,
          op: "location_set_sharing",
          summary: "route sharing intent set to public",
          resource_id: "dashboard",
        },
      ],
      previewDiff: {
        summary: "1 modification",
        modifications: [{ kind: "sharing", name: "dashboard", before: "restricted", after: "public" }],
        sharing: wideningReport,
      },
      baseVersion: "v1",
      valid: true,
      validationErrors: [],
      lifecycle: HOT_LIFECYCLE,
      recommendedAction: "hot",
      pendingRestart: { state: "none", subsystems: [] },
      candidateState: "not_requested",
      requiresFreshPreview: false,
    });

    globalThis.fetch = vi.fn().mockImplementation((input: string | URL) => {
      const url = typeof input === "string" ? input : input.href;
      if (url.endsWith("/api/config")) {
        return Promise.resolve(
          new Response(JSON.stringify({ raw: "# base", base_version: "v1" }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.endsWith("/api/config/pending-restart")) {
        return Promise.resolve(
          new Response(JSON.stringify({ pending: false }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url.endsWith("/api/config/patch/candidate")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              ok: true,
              summary: "route sharing intent set to public",
              operation_summaries: [],
              base_version: "v1",
              valid: true,
              diff: { summary: "1 modification", sharing: wideningReport },
              validation_errors: [],
              lifecycle: HOT_LIFECYCLE,
              candidate: "# candidate",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (url.endsWith("/api/config/patch/apply")) {
        applyCalled = true;
        return Promise.resolve(
          new Response(
            JSON.stringify({
              ok: true,
              version: "v2",
              persisted_version: "v2",
              serving_version: "v2",
              mode: "hot",
              reload: { ok: true, outcome: "applied_live" },
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    });

    render(<ConfigPanel />, { wrapper: Wrapper });

    const openApplyBtn = await screen.findByRole("button", { name: "Apply live" });
    await waitFor(() => {
      expect(openApplyBtn).not.toBeDisabled();
    });
    fireEvent.click(openApplyBtn);

    const ackGroup = await screen.findByRole("group", {
      name: /Audience widening or uncertain change acknowledgement required/i,
    });
    expect(ackGroup).toBeInTheDocument();

    const checkbox = screen.getByRole("checkbox", {
      name: /I acknowledge the audience change for dash\.example\.com \/ \(dashboard\)/i,
    });
    expect(checkbox).not.toBeChecked();

    // Find the dialog's confirm button.
    const applyButtons = screen.getAllByRole("button", { name: "Apply live" });
    const confirmBtn = applyButtons[applyButtons.length - 1];
    if (!confirmBtn) throw new Error("missing confirm button");
    expect(confirmBtn).toBeDisabled();

    fireEvent.click(checkbox);
    expect(checkbox).toBeChecked();
    expect(confirmBtn).not.toBeDisabled();

    fireEvent.click(confirmBtn);
    await waitFor(() => {
      expect(applyCalled).toBe(true);
    });
  });
});
