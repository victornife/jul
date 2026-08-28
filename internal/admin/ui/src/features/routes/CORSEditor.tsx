/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import { type LocationCORSState, type RouteTarget } from "@/api/client.ts";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import { corsDraftToPatch, corsWarnings, seedCORSDraft, type CORSDraft } from "@/lib/corsDraft.ts";

export interface CORSEditorProps {
  /** The location whose CORS policy is being edited. */
  readonly target: RouteTarget;
  /** The location's current policy, from the route projection, used to seed the form. */
  readonly seed?: LocationCORSState | undefined;
  /** Whether the location already has a CORS policy. Defaults to whether a seed was supplied. */
  readonly existing?: boolean;
  readonly onClose: () => void;
}

/**
 * Guided editor for a single location's CORS policy (ADR 0018 §9, #147). It
 * routes the change through location_cors_set / location_cors_clear, replacing
 * the location's whole policy wholesale — the same convention every other
 * per-location editor here uses. allowed_methods/allowed_headers/exposed_headers
 * govern preflight approval only, never ordinary requests (the route's own
 * method predicates do that). Like every console editor it never writes
 * directly: it previews the patch, then hands the diff to the Config editor for
 * Validate → Diff → Apply.
 */
export function CORSEditor({ target, seed, existing = Boolean(seed), onClose }: CORSEditorProps) {
  const [draft, setDraft] = useState<CORSDraft>(() => seedCORSDraft(seed));
  const { run: runPatch, error, busy } = useRunPatch();
  const { has } = usePermission();
  const canWrite = has("config:write");

  const where = `${target.listen}${target.path ? ` ${target.path}` : ""}`;
  const warnings = corsWarnings(draft);

  function set<K extends keyof CORSDraft>(key: K, val: CORSDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: val }));
  }

  function save(): void {
    runPatch({ op: "location_cors_set", ...target, cors_set: corsDraftToPatch(draft) });
  }

  function clearPolicy(): void {
    runPatch({ op: "location_cors_clear", ...target });
  }

  return (
    <Drawer
      title={existing ? "Edit CORS policy" : "Add CORS policy"}
      subtitle={`CORS for ${where}`}
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex items-center justify-between gap-3">
            {error && (
              <span role="alert" className="text-xs text-jul-danger">
                {error}
              </span>
            )}
            {existing && (
              <button
                type="button"
                disabled={busy || !canWrite}
                onClick={clearPolicy}
                className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-danger hover:bg-jul-bg disabled:opacity-40"
              >
                Clear policy
              </button>
            )}
            <button
              type="button"
              disabled={busy || warnings.length > 0 || !canWrite}
              onClick={save}
              className="rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              {busy ? "Previewing…" : "Review lifecycle and diff →"}
            </button>
          </div>
          <ForbiddenAction permission="config:write" className="justify-end" />
        </div>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          This <strong>replaces</strong> the whole CORS policy for <span className="font-mono">{where}</span>{" "}
          wholesale — it is not merged. Allowed methods/headers/exposed headers govern preflight
          approval only, never ordinary requests.
        </p>

        <label className="flex items-center gap-2 text-sm text-jul-text">
          <input
            type="checkbox"
            checked={draft.enabled}
            onChange={(e) => {
              set("enabled", e.target.checked);
            }}
            className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
          />
          Enable CORS for this location
        </label>

        <TextField
          label="Allowed origins"
          hint='Comma/space separated exact origins, e.g. "https://app.example.test". "*" alone allows every origin (forbids credentials).'
          value={draft.allowedOrigins}
          placeholder="https://app.example.test, https://admin.example.test"
          onChange={(v) => {
            set("allowedOrigins", v);
          }}
        />
        <TextField
          label="Allowed methods"
          hint="Preflight approval only. Blank defaults to GET, HEAD, POST."
          value={draft.allowedMethods}
          placeholder="GET, POST, PUT"
          onChange={(v) => {
            set("allowedMethods", v);
          }}
        />
        <TextField
          label="Allowed headers"
          hint="Preflight approval only. Every Access-Control-Request-Headers token a browser sends must be listed here explicitly — there is no implicit safelist exemption."
          value={draft.allowedHeaders}
          placeholder="Content-Type, Authorization"
          onChange={(v) => {
            set("allowedHeaders", v);
          }}
        />
        <TextField
          label="Exposed headers"
          hint="Sent as Access-Control-Expose-Headers on a granted response, so browser script can read them."
          value={draft.exposedHeaders}
          placeholder="X-Request-Id"
          onChange={(v) => {
            set("exposedHeaders", v);
          }}
        />

        <label className="flex items-center gap-2 text-sm text-jul-text">
          <input
            type="checkbox"
            checked={draft.allowCredentials}
            onChange={(e) => {
              set("allowCredentials", e.target.checked);
            }}
            className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
          />
          Allow credentials (cookies, Authorization) cross-origin
        </label>

        <TextField
          label="Max age"
          hint='Access-Control-Max-Age, up to 24h, e.g. "10m". Blank omits the header.'
          value={draft.maxAge}
          placeholder="10m"
          onChange={(v) => {
            set("maxAge", v);
          }}
        />

        {warnings.length > 0 && (
          <div
            role="alert"
            className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3"
          >
            {warnings.map((w, i) => (
              <p key={`corsw-${String(i)}`} className="text-xs text-jul-text">
                {w}
              </p>
            ))}
          </div>
        )}
      </div>
    </Drawer>
  );
}

function TextField({
  label,
  hint,
  value,
  placeholder,
  onChange,
}: {
  readonly label: string;
  readonly hint?: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly onChange: (v: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <input
        type="text"
        value={value}
        placeholder={placeholder}
        onChange={(e) => {
          onChange(e.target.value);
        }}
        className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
      />
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}
