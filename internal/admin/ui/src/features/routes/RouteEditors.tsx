/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import type { LocationProjection, RouteProjection } from "@/api/client.ts";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import {
  actionChanged,
  actionToPatch,
  actionWarnings,
  matchChanged,
  matchToPatch,
  matchWarnings,
  renameChanged,
  renameToNewNames,
  renameWarnings,
  seedAction,
  seedMatch,
  seedRename,
  type ActionDraft,
  type EditableActionKind,
  type MatchDraft,
  type MatchType,
  type RenameDraft,
} from "@/lib/routeEdit.ts";

// RouteEditors are the in-place "rename / re-match / re-action" drawers added in
// Phase 4f. Like every console editor they never write directly: each previews
// a structured patch (location_set_match / location_set_action / route_rename),
// then hands the candidate diff to the Config editor for Validate → Diff →
// Apply. The pure draft <-> patch logic lives in lib/routeEdit.ts so it is unit
// tested independently of React.

function routeTarget(route: RouteProjection, loc: LocationProjection) {
  return {
    listen: route.listen,
    server_names: route.server_names ?? [],
    match_type: loc.type,
    path: loc.match,
  };
}

const MATCH_TYPES: readonly MatchType[] = ["prefix", "exact", "regex"];

// ── Match editor ─────────────────────────────────────────────────────────────

export interface RouteMatchEditorProps {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
  readonly onClose: () => void;
}

/** Edit a route's match (type + path) in place. Changing the match changes the
 * route's identity, so the diff lists the old route removed and the renamed
 * route added — the warning copy makes that explicit. */
export function RouteMatchEditor({ route, loc, onClose }: RouteMatchEditorProps) {
  const { run: runPatch, error, busy } = useRunPatch();
  const { has } = usePermission();
  const canWrite = has("config:write");
  const [draft, setDraft] = useState<MatchDraft>(() => seedMatch(loc));
  const warnings = matchWarnings(draft);
  const changed = matchChanged(draft, loc);

  function save(): void {
    runPatch({
      op: "location_set_match",
      ...routeTarget(route, loc),
      match_set: matchToPatch(draft),
    });
  }

  return (
    <Drawer
      title="Change route match"
      subtitle={`${loc.type} ${loc.match} on ${route.listen}`}
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex items-center justify-between gap-3">
            {error && <span className="text-xs text-jul-danger">{error}</span>}
            <button
              type="button"
              disabled={busy || !changed || warnings.length > 0 || !canWrite}
              onClick={save}
              className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              {busy ? "Previewing…" : "Preview change →"}
            </button>
          </div>
          <ForbiddenAction permission="config:write" className="justify-end" />
        </div>
      }
    >
      <div className="space-y-4">
        <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
          A route is identified by its match, so renaming it shows in the diff as the old route
          removed and the new one added — the location and all its settings move together.
        </p>
        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Match type</span>
          <select
            value={draft.type}
            onChange={(e) => {
              setDraft((d) => ({ ...d, type: e.target.value as MatchType }));
            }}
            className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            {MATCH_TYPES.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </label>
        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Path</span>
          <input
            type="text"
            value={draft.path}
            placeholder={draft.type === "regex" ? "^/api/v[0-9]+/" : "/api"}
            onChange={(e) => {
              setDraft((d) => ({ ...d, path: e.target.value }));
            }}
            className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
          />
        </label>
        {warnings.map((wn, i) => (
          <p key={`mw-${String(i)}`} className="text-xs text-jul-danger">
            {wn}
          </p>
        ))}
      </div>
    </Drawer>
  );
}

// ── Action editor ────────────────────────────────────────────────────────────

const ACTION_KINDS: readonly EditableActionKind[] = [
  "proxy",
  "static",
  "redirect",
  "return",
  "deny",
];

const ACTION_HINT: Record<EditableActionKind, string> = {
  proxy: "Forward matching requests to an app (an upstream reference or URL).",
  static: "Serve files from a directory on disk.",
  redirect: "Redirect matching requests to another URL (optionally with a 3xx status).",
  return: "Return a fixed HTTP status code.",
  deny: "Reject matching requests with 403 Forbidden.",
};

export interface RouteActionEditorProps {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
  readonly onClose: () => void;
}

/** Switch a route's action in place among the tag-free kinds represented by
 * the generic editor. Protocol-specific actions remain on dedicated/raw paths. */
export function RouteActionEditor({ route, loc, onClose }: RouteActionEditorProps) {
  const { run: runPatch, error, busy } = useRunPatch();
  const { has } = usePermission();
  const canWrite = has("config:write");
  const [draft, setDraft] = useState<ActionDraft>(() => seedAction(loc));
  const warnings = actionWarnings(draft);
  const changed = actionChanged(draft, loc);
  const needsTarget =
    draft.kind === "proxy" || draft.kind === "static" || draft.kind === "redirect";
  const needsStatus = draft.kind === "redirect" || draft.kind === "return";

  function save(): void {
    runPatch({
      op: "location_set_action",
      ...routeTarget(route, loc),
      action: actionToPatch(draft),
    });
  }

  return (
    <Drawer
      title="Change route action"
      subtitle={`${loc.type} ${loc.match} on ${route.listen}`}
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex items-center justify-between gap-3">
            {error && <span className="text-xs text-jul-danger">{error}</span>}
            <button
              type="button"
              disabled={busy || !changed || warnings.length > 0 || !canWrite}
              onClick={save}
              className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              {busy ? "Previewing…" : "Preview change →"}
            </button>
          </div>
          <ForbiddenAction permission="config:write" className="justify-end" />
        </div>
      }
    >
      <div className="space-y-4">
        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Action</span>
          <select
            value={draft.kind}
            onChange={(e) => {
              setDraft((d) => ({ ...d, kind: e.target.value as EditableActionKind }));
            }}
            className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            {ACTION_KINDS.map((k) => (
              <option key={k} value={k}>
                {k}
              </option>
            ))}
          </select>
          <span className="text-xs text-jul-muted">{ACTION_HINT[draft.kind]}</span>
        </label>
        {needsTarget && (
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">
              {draft.kind === "static"
                ? "Root directory"
                : draft.kind === "redirect"
                  ? "Redirect URL"
                  : "Target"}
            </span>
            <input
              type="text"
              value={draft.target}
              placeholder={draft.kind === "static" ? "/var/www" : "http://app"}
              onChange={(e) => {
                setDraft((d) => ({ ...d, target: e.target.value }));
              }}
              className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
            />
          </label>
        )}
        {needsStatus && (
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">
              {draft.kind === "redirect" ? "Redirect status (optional, 3xx)" : "Status code"}
            </span>
            <input
              type="text"
              inputMode="numeric"
              value={draft.status}
              placeholder={draft.kind === "redirect" ? "301" : "404"}
              onChange={(e) => {
                setDraft((d) => ({ ...d, status: e.target.value }));
              }}
              className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
            />
          </label>
        )}
        {warnings.map((wn, i) => (
          <p key={`aw-${String(i)}`} className="text-xs text-jul-danger">
            {wn}
          </p>
        ))}
      </div>
    </Drawer>
  );
}

// ── Server host-name rename editor ───────────────────────────────────────────

export interface RouteRenameEditorProps {
  readonly route: RouteProjection;
  readonly onClose: () => void;
}

/** Rename a server block's host names (server_names) in place. This changes the
 * virtual host's identity, so the diff may re-create the block when the first
 * host name changes — the warning copy makes that explicit. */
export function RouteRenameEditor({ route, onClose }: RouteRenameEditorProps) {
  const { run: runPatch, error, busy } = useRunPatch();
  const { has } = usePermission();
  const canWrite = has("config:write");
  const [draft, setDraft] = useState<RenameDraft>(() => seedRename(route.server_names));
  const warnings = renameWarnings(draft);
  const changed = renameChanged(draft, route.server_names);

  function save(): void {
    runPatch({
      op: "route_rename",
      listen: route.listen,
      server_names: route.server_names ?? [],
      new_server_names: renameToNewNames(draft),
    });
  }

  return (
    <Drawer
      title="Rename host names"
      subtitle={`Server on ${route.listen}`}
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex items-center justify-between gap-3">
            {error && <span className="text-xs text-jul-danger">{error}</span>}
            <button
              type="button"
              disabled={busy || !changed || warnings.length > 0 || !canWrite}
              onClick={save}
              className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              {busy ? "Previewing…" : "Preview change →"}
            </button>
          </div>
          <ForbiddenAction permission="config:write" className="justify-end" />
        </div>
      }
    >
      <div className="space-y-4">
        <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
          Host names decide which requests (by Host header / TLS SNI) this virtual host serves.
          Changing the first host name re-keys the block, so the diff may show it removed and
          re-added with all its routes — that is expected.
        </p>
        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Host names</span>
          <textarea
            value={draft.hosts}
            placeholder={"app.example.com\nwww.example.com"}
            rows={4}
            onChange={(e) => {
              setDraft({ hosts: e.target.value });
            }}
            className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
          />
          <span className="text-xs text-jul-muted">
            One host per line (or comma-separated). Leave empty for the catch-all (any host).
          </span>
        </label>
        {warnings.map((wn, i) => (
          <p key={`rw-${String(i)}`} className="text-xs text-jul-danger">
            {wn}
          </p>
        ))}
      </div>
    </Drawer>
  );
}
