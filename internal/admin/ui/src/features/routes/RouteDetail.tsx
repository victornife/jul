/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Link } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import {
  type LocationProjection,
  type LocationWAF,
  type RouteProjection,
  type RouteTarget,
} from "@/api/client.ts";
import { ConfirmDialog } from "@/components/ConfirmDialog.tsx";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { LocationWAFEditor } from "@/features/security/LocationWAFEditor.tsx";
import { AuthEditor } from "@/features/routes/AuthEditor.tsx";
import { PredicatesEditor } from "@/features/routes/PredicatesEditor.tsx";
import { ResponseHeadersEditor } from "@/features/routes/ResponseHeadersEditor.tsx";
import { CORSEditor } from "@/features/routes/CORSEditor.tsx";
import {
  RouteActionEditor,
  RouteMatchEditor,
  RouteRenameEditor,
} from "@/features/routes/RouteEditors.tsx";
import { LocationRateLimitEditor } from "@/features/routes/LocationRateLimitEditor.tsx";
import { TranscodeQuickEdit } from "@/features/routes/TranscodeQuickEdit.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import type { PendingPatchDraft } from "@/lib/configDraftHandoff.ts";
import { isEditableAction } from "@/lib/routeEdit.ts";
import {
  buildRouteRemovalBatch,
  buildServerRemovalBatch,
  canonicalServerNames,
  formatServerIdentity,
  serverIdentityFromRoute,
} from "@/lib/routePatch.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import { describePatchBatchError, useRunPatchBatch } from "@/lib/useRunPatchBatch.ts";

function Row({ label, value }: { readonly label: string; readonly value: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[140px_1fr] gap-2 py-1.5">
      <span className="text-xs uppercase tracking-wider text-jul-muted">{label}</span>
      <span className="text-sm text-jul-text">{value}</span>
    </div>
  );
}

function Flag({ on, label }: { readonly on: boolean; readonly label: string }) {
  return (
    <span
      className={`inline-block rounded-full px-2 py-0.5 text-xs ${
        on ? "bg-jul-success/15 text-jul-success" : "bg-jul-border/40 text-jul-muted"
      }`}
    >
      {label}: {on ? "on" : "off"}
    </span>
  );
}

// routeOrderNote explains this location's declaration-order position among
// any other locations sharing its exact match type and path — the ambiguity
// predicates introduced (ADR 0018 §14). It returns null when this location's
// coordinates are unique, since match_ordinal is meaningless there. Computed
// purely from the already-fetched route projection; no new backend field.
function routeOrderNote(route: RouteProjection, loc: LocationProjection): string | null {
  const siblings = route.locations.filter((l) => l.type === loc.type && l.match === loc.match);
  if (siblings.length <= 1) return null;
  const position = (loc.match_ordinal ?? 0) + 1;
  return `${String(position)} of ${String(siblings.length)} routes sharing this match — evaluated in declaration order; the first whose predicates all match wins.`;
}

function describe(action: string): string {
  switch (action) {
    case "proxy":
      return "This route proxies traffic to an app. Jul receives the request, applies edge rules, and forwards it to the selected upstream.";
    case "grpc":
      return "This route proxies native gRPC traffic end-to-end over HTTP/2.";
    case "grpc_transcode":
      return "This route transcodes REST/JSON requests to gRPC and returns the reply as JSON.";
    case "static":
      return "This route serves static files from a directory on disk.";
    case "redirect":
      return "This route redirects matching requests to another URL.";
    case "deny":
      return "This route rejects matching requests with 403 Forbidden.";
    case "return":
      return "This route returns a fixed HTTP status code.";
    case "fastcgi":
      return "This route forwards requests to a FastCGI application.";
    case "uwsgi":
      return "This route forwards requests to a uWSGI application.";
    case "plugin":
      return "This route is served by a WASM plugin.";
    case "unknown":
      return "This route uses a custom or plugin-based action.";
    default:
      return "This route's action is not recognized.";
  }
}

// generatedFragment renders only configuration fields that the projection can
// reproduce faithfully. Protocol-specific actions must never be displayed as a
// plain HTTP proxy: doing so would turn gRPC/FastCGI/uWSGI semantics into a
// misleading escape-hatch snippet even though no mutation has occurred.
function generatedFragment(route: RouteProjection, loc: LocationProjection): string {
  const quoted = (value: string): string => JSON.stringify(value);
  const lines: string[] = ["[[servers]]", `listen = ${quoted(route.listen)}`];
  if (route.server_names && route.server_names.length > 0) {
    lines.push(`server_names = [${route.server_names.map(quoted).join(", ")}]`);
  }
  lines.push("");
  lines.push("  [[servers.locations]]");
  lines.push(`  match = { type = ${quoted(loc.type)}, path = ${quoted(loc.match)} }`);
  switch (loc.action) {
    case "proxy":
      if (loc.target) lines.push(`  proxy_pass = ${quoted(loc.target)}`);
      break;
    case "grpc":
      if (loc.target) lines.push(`  proxy_pass = ${quoted(loc.target)}`);
      lines.push("  grpc = true");
      break;
    case "static":
      if (loc.target) lines.push(`  root = ${quoted(loc.target)}`);
      break;
    case "redirect":
      if (loc.target) lines.push(`  redirect = ${quoted(loc.target)}`);
      break;
    case "return":
      if (loc.target) lines.push(`  return = ${loc.target}`);
      break;
    case "deny":
      lines.push("  deny = true");
      break;
    case "fastcgi":
      if (loc.target) lines.push(`  fastcgi_pass = ${quoted(loc.target)}`);
      break;
    case "uwsgi":
      if (loc.target) lines.push(`  uwsgi_pass = ${quoted(loc.target)}`);
      break;
    case "grpc_transcode": {
      const tc = loc.transcode;
      lines.push("    [servers.locations.grpc_transcode]");
      if (loc.target) lines.push(`    target = ${quoted(loc.target)}`);
      if (tc?.descriptor_set) lines.push(`    descriptor_set = ${quoted(tc.descriptor_set)}`);
      if (tc?.use_reflection) lines.push("    use_reflection = true");
      if (tc?.tls) lines.push("    tls = true");
      if (tc?.preserve_proto_field_names) {
        lines.push("    preserve_proto_field_names = true");
      }
      if (tc?.streaming) lines.push("    streaming = true");
      if (tc?.stream_mode) lines.push(`    stream_mode = ${quoted(tc.stream_mode)}`);
      if (tc?.max_message_size) {
        lines.push(`    max_message_size = ${quoted(tc.max_message_size)}`);
      }
      break;
    }
    default:
      lines.push(
        `  # ${loc.action} action: inspect the raw configuration for protocol-specific fields`,
      );
  }
  if (loc.cache) lines.push("  cache = true");
  if (loc.rate_limit) {
    const rl = loc.rate_limit_detail;
    if (rl) {
      // Emit only the fields the projection actually carries. The detail's
      // rate/burst/key are optional, so interpolating them unconditionally
      // would render literal "undefined" into otherwise-valid TOML.
      const parts = ["enabled = true"];
      if (rl.rate !== undefined) parts.push(`rate = ${String(rl.rate)}`);
      if (rl.burst !== undefined) parts.push(`burst = ${String(rl.burst)}`);
      if (rl.key !== undefined) parts.push(`key = "${rl.key}"`);
      lines.push(`  rate_limit = { ${parts.join(", ")} }`);
    } else {
      lines.push("  rate_limit = { enabled = true }");
    }
  }
  // The route projection reports an auth rule's method and non-secret
  // identifiers (never credentials), so show the method rather than a literal
  // "auth = {}", which would read as a valid—but inert, allow-all—block and
  // misrepresent the effective policy.
  if (loc.auth)
    lines.push(
      `  # auth = { method = "${loc.auth_detail?.method ?? "…"}", … }  (edit it in place below)`,
    );
  return lines.join("\n");
}

// QuickEdits performs true in-place edits via the structured patch API. The
// server returns a secret-safe lifecycle assessment and diff, which the shared
// hook hands to ConfigPanel for final apply/stage review. Candidate TOML remains
// behind the separate config:raw boundary.
function QuickEdits({
  route,
  loc,
}: {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
}) {
  const [target, setTarget] = useState(loc.target ?? "");
  const { run: runPatch, error, busy } = useRunPatch();
  const { has } = usePermission();
  const canWrite = has("config:write");
  const [wafEditing, setWafEditing] = useState(false);
  const [authEditing, setAuthEditing] = useState(false);
  const [matchEditing, setMatchEditing] = useState(false);
  const [actionEditing, setActionEditing] = useState(false);
  const [rateLimitEditing, setRateLimitEditing] = useState(false);
  const [predicatesEditing, setPredicatesEditing] = useState(false);
  const [responseHeadersEditing, setResponseHeadersEditing] = useState(false);
  const [corsEditing, setCorsEditing] = useState(false);

  const canSetTarget = loc.action === "proxy";

  // routeTarget builds the structured location selector shared by the new
  // predicate/response-header/CORS editors. match_ordinal is only included
  // when the projection reports one, so exactOptionalPropertyTypes never sees
  // an explicit undefined where RouteTarget expects the key omitted entirely.
  const routeTarget: RouteTarget = {
    listen: route.listen,
    server_names: route.server_names ?? [],
    match_type: loc.type,
    path: loc.match,
    ...(loc.match_ordinal !== undefined ? { match_ordinal: loc.match_ordinal } : {}),
  };

  // wafTarget builds the LocationWAF the guided per-location editor expects from
  // the route coordinates plus the location's current override (or safe
  // detect-first defaults when the route still inherits the global policy). It
  // passes the advanced SecLang fields through verbatim so the editor seeds and
  // round-trips them instead of clobbering rules it never showed (Phase 4e).
  const wafTarget: LocationWAF = {
    listen: route.listen,
    server_names: route.server_names ?? [],
    match_type: loc.type,
    path: loc.match,
    enabled: loc.waf?.enabled ?? true,
    mode: loc.waf?.mode ?? "detect",
    crs_enabled: loc.waf?.crs_enabled ?? false,
    response_body_check: loc.waf?.response_body_check ?? false,
    ...(loc.waf?.block_status !== undefined ? { block_status: loc.waf.block_status } : {}),
    ...(loc.waf?.paranoia !== undefined ? { paranoia: loc.waf.paranoia } : {}),
    ...(loc.waf?.request_body_limit !== undefined
      ? { request_body_limit: loc.waf.request_body_limit }
      : {}),
    ...(loc.waf?.directives_files !== undefined
      ? { directives_files: loc.waf.directives_files }
      : {}),
    ...(loc.waf?.inline_rules !== undefined ? { inline_rules: loc.waf.inline_rules } : {}),
  };

  return (
    <div className="space-y-3 rounded-md border border-jul-border bg-jul-surface p-3">
      <div className="flex items-center justify-between">
        <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
          Quick edits (in place)
        </span>
        <span className="text-xs text-jul-muted">opens a diff to review &amp; apply</span>
      </div>

      {canSetTarget && (
        <div className="space-y-1">
          <span className="text-sm font-medium text-jul-text">Proxy target</span>
          <div className="flex gap-2">
            <input
              type="text"
              value={target}
              placeholder="http://app"
              onChange={(e) => {
                setTarget(e.target.value);
              }}
              className="flex-1 rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
            />
            <button
              type="button"
              disabled={busy || target.trim() === "" || target === loc.target || !canWrite}
              onClick={() => {
                runPatch({
                  op: "route_set_target",
                  listen: route.listen,
                  server_names: route.server_names ?? [],
                  match_type: loc.type,
                  path: loc.match,
                  target: target.trim(),
                });
              }}
              className="rounded-md bg-jul-accent px-3 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              Set target →
            </button>
          </div>
        </div>
      )}

      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          disabled={busy || !canWrite}
          onClick={() => {
            setMatchEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          Change match →
        </button>
        {isEditableAction(loc.action) && (
          <button
            type="button"
            disabled={busy || !canWrite}
            onClick={() => {
              setActionEditing(true);
            }}
            className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
          >
            Change action →
          </button>
        )}
        <button
          type="button"
          disabled={busy || !canWrite}
          onClick={() => {
            runPatch({
              op: "route_toggle_cache",
              listen: route.listen,
              server_names: route.server_names ?? [],
              match_type: loc.type,
              path: loc.match,
              enabled: !loc.cache,
            });
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.cache ? "Disable cache" : "Enable cache"} →
        </button>
        <button
          type="button"
          disabled={busy || !canWrite}
          onClick={() => {
            runPatch({
              op: "route_toggle_rate_limit",
              listen: route.listen,
              server_names: route.server_names ?? [],
              match_type: loc.type,
              path: loc.match,
              enabled: !loc.rate_limit,
            });
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.rate_limit ? "Disable rate limit" : "Enable rate limit (default)"} →
        </button>
        <button
          type="button"
          disabled={busy || !canWrite}
          onClick={() => {
            setRateLimitEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.rate_limit ? "Edit rate limit" : "Set rate limit"} →
        </button>
        <button
          type="button"
          disabled={busy || !canWrite || (!loc.require_client_cert && !route.tls?.client_auth)}
          title={
            !route.tls?.client_auth
              ? "Enable mutual TLS on this server first (TLS & Certificates → Mutual TLS)."
              : "Takes effect immediately on reload."
          }
          onClick={() => {
            runPatch({
              op: "location_toggle_require_client_cert",
              listen: route.listen,
              server_names: route.server_names ?? [],
              match_type: loc.type,
              path: loc.match,
              enabled: !loc.require_client_cert,
            });
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.require_client_cert ? "Don’t require client cert" : "Require client cert"} →
        </button>
        <button
          type="button"
          disabled={busy || !canWrite}
          onClick={() => {
            setWafEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.waf ? "Edit WAF override" : "Add WAF override"} →
        </button>
        <button
          type="button"
          disabled={busy || !canWrite}
          onClick={() => {
            setAuthEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.auth ? "Edit auth" : "Add auth"} →
        </button>
        <button
          type="button"
          disabled={busy || !canWrite}
          onClick={() => {
            setPredicatesEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.predicates ? "Edit predicates" : "Add predicates"} →
        </button>
        <button
          type="button"
          disabled={busy || !canWrite}
          onClick={() => {
            setResponseHeadersEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.response_headers ? "Edit response headers" : "Add response headers"} →
        </button>
        <button
          type="button"
          disabled={busy || !canWrite}
          onClick={() => {
            setCorsEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.cors ? "Edit CORS" : "Add CORS"} →
        </button>
      </div>

      {loc.action === "grpc_transcode" && <TranscodeQuickEdit route={route} loc={loc} />}

      {error && <p className="text-xs text-jul-danger">{error}</p>}
      <ForbiddenAction permission="config:write" />

      {wafEditing && (
        <LocationWAFEditor
          target={wafTarget}
          existing={Boolean(loc.waf)}
          onClose={() => {
            setWafEditing(false);
          }}
        />
      )}

      {authEditing && (
        <AuthEditor
          target={{
            listen: route.listen,
            server_names: route.server_names ?? [],
            match_type: loc.type,
            path: loc.match,
          }}
          seed={loc.auth_detail}
          existing={loc.auth}
          onClose={() => {
            setAuthEditing(false);
          }}
        />
      )}

      {predicatesEditing && (
        <PredicatesEditor
          target={routeTarget}
          seedMethods={loc.methods}
          seedHeaders={undefined}
          seedQuery={undefined}
          existing={Boolean(loc.predicates)}
          onClose={() => {
            setPredicatesEditing(false);
          }}
        />
      )}

      {responseHeadersEditing && (
        <ResponseHeadersEditor
          target={routeTarget}
          existing={Boolean(loc.response_headers)}
          onClose={() => {
            setResponseHeadersEditing(false);
          }}
        />
      )}

      {corsEditing && (
        <CORSEditor
          target={routeTarget}
          seed={loc.cors}
          existing={Boolean(loc.cors)}
          onClose={() => {
            setCorsEditing(false);
          }}
        />
      )}

      {matchEditing && (
        <RouteMatchEditor
          route={route}
          loc={loc}
          onClose={() => {
            setMatchEditing(false);
          }}
        />
      )}

      {actionEditing && (
        <RouteActionEditor
          route={route}
          loc={loc}
          onClose={() => {
            setActionEditing(false);
          }}
        />
      )}

      {rateLimitEditing && (
        <LocationRateLimitEditor
          route={route}
          loc={loc}
          onClose={() => {
            setRateLimitEditing(false);
          }}
        />
      )}
    </div>
  );
}

// ServerToggles flips the two server-scope protocol switches — HTTP/3 (QUIC) and
// cleartext HTTP/2 (h2c) — through the structured patch ops, reviewed as a diff
// like every other edit. They are mutually exclusive by transport: HTTP/3 needs
// TLS on the listener while h2c only applies to a plaintext one, so each button
// is disabled when the listener's TLS posture rules it out.
function ServerToggles({ route }: { readonly route: RouteProjection }) {
  const { run: runPatch, error, busy } = useRunPatch();
  const { has } = usePermission();
  const canWrite = has("config:write");
  const [renameEditing, setRenameEditing] = useState(false);
  const tlsOn = Boolean(route.tls?.enabled);

  return (
    <div className="space-y-2">
      <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
        Server protocols
      </span>
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          disabled={busy || !tlsOn || !canWrite}
          title={tlsOn ? undefined : "HTTP/3 requires TLS on this listener."}
          onClick={() => {
            runPatch({ op: "server_toggle_http3", listen: route.listen, enabled: !route.http3 });
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {route.http3 ? "Disable HTTP/3" : "Enable HTTP/3"} →
        </button>
        <button
          type="button"
          disabled={busy || tlsOn || !canWrite}
          title={
            tlsOn
              ? "TLS listeners negotiate HTTP/2 via ALPN; h2c is for plaintext listeners only."
              : undefined
          }
          onClick={() => {
            runPatch({ op: "server_toggle_h2c", listen: route.listen, enabled: !route.h2c });
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {route.h2c ? "Disable h2c" : "Enable h2c"} →
        </button>
      </div>
      <p className="text-xs text-jul-muted">
        {tlsOn
          ? "TLS listener: HTTP/3 (QUIC) is available; h2c does not apply (HTTP/2 is negotiated via ALPN)."
          : "Plaintext listener: h2c enables cleartext HTTP/2 for native gRPC; HTTP/3 needs TLS."}
      </p>
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          disabled={busy || !canWrite}
          onClick={() => {
            setRenameEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          Rename host names →
        </button>
      </div>
      {error && <p className="text-xs text-jul-danger">{error}</p>}
      <ForbiddenAction permission="config:write" />
      {renameEditing && (
        <RouteRenameEditor
          route={route}
          onClose={() => {
            setRenameEditing(false);
          }}
        />
      )}
    </div>
  );
}

type DeletionKind = "route" | "server";

interface DeletionPreview {
  readonly kind: DeletionKind;
  readonly draft: PendingPatchDraft;
}

function lifecycleOutcome(draft: PendingPatchDraft): string {
  const lifecycle = draft.lifecycle;
  if (lifecycle === undefined) return "Lifecycle classification is unavailable.";
  if (lifecycle.validation_rejected_paths.length > 0) {
    return "Lifecycle validation rejected this operation.";
  }
  if (lifecycle.can_apply_hot) return "Hot apply is available after review.";
  if (lifecycle.can_stage_restart) return "This deletion must be staged for restart.";
  return "The preview currently offers neither hot apply nor restart staging.";
}

function previewCanBeHandedOff(draft: PendingPatchDraft): boolean {
  return (
    draft.valid &&
    draft.lifecycle !== undefined &&
    draft.lifecycle.validation_rejected_paths.length === 0
  );
}

function DeletionConfirmation({
  preview,
  route,
  loc,
  onConfirm,
  onCancel,
}: {
  readonly preview: DeletionPreview;
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
  readonly onConfirm: () => void;
  readonly onCancel: () => void;
}) {
  const identity = serverIdentityFromRoute(route);
  const names = canonicalServerNames(identity.serverNames);
  const lifecycle = preview.draft.lifecycle;
  const serverDelete = preview.kind === "server";
  const shownRoutes = route.locations.slice(0, 8);
  const remainingRoutes = route.locations.length - shownRoutes.length;

  return (
    <ConfirmDialog
      title={serverDelete ? "Remove this exact server?" : "Remove this exact route?"}
      confirmLabel="Hand off deletion for apply review"
      danger
      confirmDisabled={!previewCanBeHandedOff(preview.draft)}
      onConfirm={onConfirm}
      onCancel={onCancel}
    >
      <div className="space-y-4">
        <p>
          This confirmation does not apply configuration directly. It hands the exact previewed
          operation and base version to the Configuration panel for final apply or restart staging.
        </p>

        <dl className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3 text-xs">
          <div className="grid grid-cols-[120px_1fr] gap-2">
            <dt className="font-semibold text-jul-muted">Listener</dt>
            <dd className="font-mono text-jul-text">{identity.listen}</dd>
          </div>
          <div className="grid grid-cols-[120px_1fr] gap-2">
            <dt className="font-semibold text-jul-muted">Server names</dt>
            <dd className="font-mono text-jul-text">
              {names.length > 0 ? names.join(", ") : "(any host)"}
            </dd>
          </div>
          {!serverDelete && (
            <div className="grid grid-cols-[120px_1fr] gap-2">
              <dt className="font-semibold text-jul-muted">Route match</dt>
              <dd className="font-mono text-jul-text">
                {loc.type} {loc.match}
              </dd>
            </div>
          )}
          {serverDelete && (
            <div className="grid grid-cols-[120px_1fr] gap-2">
              <dt className="font-semibold text-jul-muted">Contained routes</dt>
              <dd className="text-jul-text">{route.locations.length}</dd>
            </div>
          )}
          <div className="grid grid-cols-[120px_1fr] gap-2">
            <dt className="font-semibold text-jul-muted">Lifecycle</dt>
            <dd className="text-jul-text">{lifecycleOutcome(preview.draft)}</dd>
          </div>
        </dl>

        {serverDelete && route.locations.length > 0 && (
          <div>
            <p className="mb-1 text-xs font-semibold text-jul-muted">Contained route identities</p>
            <ul className="max-h-40 list-disc space-y-1 overflow-auto pl-5 font-mono text-xs text-jul-text">
              {shownRoutes.map((contained) => (
                <li key={`${contained.type}\u0000${contained.match}`}>
                  {contained.type} {contained.match}
                </li>
              ))}
              {remainingRoutes > 0 && <li>…and {remainingRoutes} more</li>}
            </ul>
          </div>
        )}

        <div>
          <p className="mb-1 text-xs font-semibold text-jul-muted">Exact operation</p>
          <pre className="max-h-48 overflow-auto rounded-md border border-jul-border bg-jul-bg p-3 font-mono text-xs text-jul-text">
            {JSON.stringify(preview.draft.ops, null, 2)}
          </pre>
        </div>

        {preview.draft.operationSummaries.length > 0 && (
          <ol className="list-decimal space-y-1 pl-5 text-xs text-jul-text">
            {preview.draft.operationSummaries.map((operation) => (
              <li key={`${String(operation.op_index)}-${operation.op}`}>
                <span className="font-mono">{operation.op}</span>: {operation.summary}
              </li>
            ))}
          </ol>
        )}

        {preview.draft.validationErrors.length > 0 && (
          <div className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3">
            <p className="text-xs font-semibold text-jul-danger">Validation issues</p>
            <ul className="mt-1 list-disc space-y-1 pl-5 text-xs text-jul-danger">
              {preview.draft.validationErrors.map((issue, index) => (
                <li key={`${issue.code}-${String(index)}`}>
                  {issue.path ? `${issue.path}: ` : ""}
                  {issue.summary}
                </li>
              ))}
            </ul>
          </div>
        )}

        {lifecycle !== undefined && (
          <div className="grid gap-1 text-xs text-jul-muted">
            <p>Hot paths: {lifecycle.hot_paths.length}</p>
            <p>Restart-required paths: {lifecycle.restart_required_paths.length}</p>
            <p>New-listener-only paths: {lifecycle.new_listener_only_paths.length}</p>
            <p>Pending subsystems: {lifecycle.pending_subsystems.join(", ") || "none"}</p>
          </div>
        )}

        <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
          {serverDelete ? (
            <>
              Removing this server removes only the exact server block and its{" "}
              {route.locations.length} contained {route.locations.length === 1 ? "route" : "routes"}
              . It does not delete referenced upstreams, applications, credentials, plugins, or
              unrelated resources.
            </>
          ) : (
            <>
              Removing this route does not cascade to upstreams, applications, credentials, plugins,
              sibling routes, or unrelated resources.
            </>
          )}
        </p>

        {!previewCanBeHandedOff(preview.draft) && (
          <p className="text-xs text-jul-danger">
            Handoff is disabled because the exact preview is invalid, lifecycle-rejected, or lacks
            an authoritative lifecycle classification.
          </p>
        )}
      </div>
    </ConfirmDialog>
  );
}

function RouteDangerZone({
  route,
  loc,
}: {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
}) {
  const batch = useRunPatchBatch();
  const { has } = usePermission();
  const canWrite = has("config:write");
  const [preview, setPreview] = useState<DeletionPreview | null>(null);
  const [previewing, setPreviewing] = useState<DeletionKind | null>(null);
  const identity = serverIdentityFromRoute(route);
  const error = describePatchBatchError(batch.error);

  async function previewDeletion(kind: DeletionKind): Promise<void> {
    if (!canWrite) return;
    setPreviewing(kind);
    const ops =
      kind === "route"
        ? buildRouteRemovalBatch(identity, { matchType: loc.type, path: loc.match })
        : buildServerRemovalBatch(identity);
    const draft = await batch.preview(ops);
    setPreviewing(null);
    if (draft !== null) setPreview({ kind, draft });
  }

  return (
    <div className="space-y-3 rounded-md border border-jul-danger/40 bg-jul-danger/5 p-3">
      <div>
        <span className="text-xs font-semibold uppercase tracking-wider text-jul-danger">
          Danger zone
        </span>
        <p className="mt-1 text-xs text-jul-muted">
          Deletion is always previewed against the exact server identity before a second explicit
          confirmation. Nothing is applied from this drawer.
        </p>
      </div>
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          aria-label={`Delete route ${loc.type} ${loc.match} from ${formatServerIdentity(identity)}`}
          disabled={batch.busy || !canWrite}
          onClick={() => {
            void previewDeletion("route");
          }}
          className="rounded-md border border-jul-danger/60 px-3 py-1.5 text-xs font-medium text-jul-danger hover:bg-jul-danger/10 disabled:opacity-40"
        >
          {previewing === "route" ? "Previewing route deletion…" : "Delete route…"}
        </button>
        <button
          type="button"
          aria-label={`Delete server ${formatServerIdentity(identity)} with ${String(route.locations.length)} contained routes`}
          disabled={batch.busy || !canWrite}
          onClick={() => {
            void previewDeletion("server");
          }}
          className="rounded-md border border-jul-danger/60 px-3 py-1.5 text-xs font-medium text-jul-danger hover:bg-jul-danger/10 disabled:opacity-40"
        >
          {previewing === "server" ? "Previewing server deletion…" : "Delete server…"}
        </button>
      </div>
      {error && <p className="text-xs text-jul-danger">{error}</p>}
      <ForbiddenAction permission="config:write" />

      {preview !== null && (
        <DeletionConfirmation
          preview={preview}
          route={route}
          loc={loc}
          onConfirm={() => {
            batch.handoff(preview.draft);
          }}
          onCancel={() => {
            setPreview(null);
          }}
        />
      )}
    </div>
  );
}

export interface RouteDetailProps {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
  readonly onClose: () => void;
  readonly onEdit: () => void;
}

/** Route detail drawer (Milestone 2.1): explains what a route does, shows its
 * effective config and any warnings, without the operator reading TOML. */
export function RouteDetail({ route, loc, onClose, onEdit }: RouteDetailProps) {
  const { has } = usePermission();
  const canWrite = has("config:write");
  const canReadRaw = has("config:raw");
  const orderNote = routeOrderNote(route, loc);
  const canClone =
    loc.action === "proxy" ||
    loc.action === "static" ||
    loc.action === "redirect" ||
    loc.action === "deny" ||
    loc.action === "return";

  const footer = canClone ? (
    <div className="ml-auto space-y-1 text-right">
      <button
        type="button"
        disabled={!canWrite}
        onClick={onEdit}
        className="rounded-md border border-jul-border px-4 py-1.5 text-sm text-jul-text hover:bg-jul-surface disabled:opacity-40"
      >
        Clone route →
      </button>
      <ForbiddenAction permission="config:write" className="justify-end" />
    </div>
  ) : loc.action === "grpc_transcode" ? (
    <div className="ml-auto space-y-1 text-right">
      {canWrite ? (
        <Link
          to={`/transcode?edit=1&listen=${encodeURIComponent(route.listen)}&server_names=${encodeURIComponent((route.server_names ?? []).join(","))}&match_type=${encodeURIComponent(loc.type)}&path=${encodeURIComponent(loc.match)}`}
          className="inline-block rounded-md border border-jul-border px-4 py-1.5 text-sm text-jul-text hover:bg-jul-surface"
        >
          Edit in Transcode designer →
        </Link>
      ) : (
        <button
          type="button"
          disabled
          className="rounded-md border border-jul-border px-4 py-1.5 text-sm text-jul-text opacity-40"
        >
          Edit in Transcode designer →
        </button>
      )}
      <p className="max-w-sm text-xs text-jul-muted">
        Structured route cloning does not faithfully reproduce a gRPC transcoding action.
      </p>
      <ForbiddenAction permission="config:write" className="justify-end" />
    </div>
  ) : (
    <div className="ml-auto space-y-1 text-right">
      {canReadRaw ? (
        <Link
          to="/config"
          className="inline-block rounded-md border border-jul-border px-4 py-1.5 text-sm text-jul-text hover:bg-jul-surface"
        >
          Open raw configuration →
        </Link>
      ) : (
        <button
          type="button"
          disabled
          className="rounded-md border border-jul-border px-4 py-1.5 text-sm text-jul-text opacity-40"
        >
          Open raw configuration →
        </button>
      )}
      <p className="max-w-sm text-xs text-jul-muted">
        The structured creator cannot faithfully reproduce the {loc.action} protocol/action, so it
        never substitutes a plain HTTP proxy.
      </p>
      <ForbiddenAction permission="config:raw" className="justify-end" />
    </div>
  );

  return (
    <Drawer
      title={`${loc.type} ${loc.match}`}
      subtitle={`${route.listen}${route.server_names && route.server_names.length > 0 ? " · " + route.server_names.join(", ") : ""}`}
      onClose={onClose}
      footer={footer}
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          {describe(loc.action)}
        </p>

        {loc.warnings && loc.warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
            <span className="text-xs font-semibold uppercase tracking-wider text-jul-warning">
              Warnings
            </span>
            {loc.warnings.map((wn, i) => (
              <p key={`w-${String(i)}`} className="text-xs text-jul-text">
                {wn}
              </p>
            ))}
          </div>
        )}

        {loc.action === "grpc_transcode" && (
          <p className="rounded-md border border-jul-accent/30 bg-jul-accent/10 p-3 text-xs text-jul-text">
            <span className="font-semibold">Tip:</span> You can design new gRPC transcoding routes
            and inspect descriptor sets in the{" "}
            <Link
              to="/transcode"
              className="font-medium text-jul-accent underline hover:no-underline"
            >
              Transcode
            </Link>{" "}
            panel, or{" "}
            <Link
              to={`/transcode?edit=1&listen=${encodeURIComponent(route.listen)}&server_names=${encodeURIComponent((route.server_names ?? []).join(","))}&match_type=${encodeURIComponent(loc.type)}&path=${encodeURIComponent(loc.match)}`}
              className="font-medium text-jul-accent underline hover:no-underline"
            >
              edit this route in the designer
            </Link>{" "}
            for a deeper configuration (re-upload descriptor, pick methods, etc.).
          </p>
        )}

        <div className="rounded-md border border-jul-border bg-jul-surface px-4 py-2">
          <Row label="Listener" value={<span className="font-mono">{route.listen}</span>} />
          <Row
            label="Host names"
            value={
              route.server_names && route.server_names.length > 0
                ? route.server_names.join(", ")
                : "any host"
            }
          />
          <Row label="Path match" value={<span className="font-mono">{loc.match}</span>} />
          <Row label="Match type" value={loc.type} />
          {loc.predicates && (
            <Row label="Predicates" value={<span className="font-mono">{loc.predicates}</span>} />
          )}
          {orderNote && <Row label="Route order" value={orderNote} />}
          <Row label="Action" value={loc.action} />
          {loc.target && (
            <Row label="Target" value={<span className="font-mono">{loc.target}</span>} />
          )}
          {loc.upstream && (
            <Row label="Upstream" value={<span className="font-mono">{loc.upstream}</span>} />
          )}
          <Row
            label="TLS"
            value={route.tls?.enabled ? `enabled${route.tls.acme ? " (ACME)" : ""}` : "off"}
          />
        </div>

        <div className="flex flex-wrap gap-2">
          <Flag on={loc.auth} label="auth" />
          <Flag on={loc.cache} label="cache" />
          <Flag on={loc.compression} label="compression" />
          <Flag on={loc.rate_limit} label="rate limit" />
          <Flag on={loc.secure} label="TLS" />
          <Flag on={loc.require_client_cert} label="client cert" />
          <Flag on={route.http3} label="HTTP/3" />
          <Flag on={route.h2c} label="h2c" />
          <Flag on={Boolean(loc.response_headers)} label="response headers" />
          <Flag on={Boolean(loc.cors?.enabled)} label="CORS" />
        </div>

        <QuickEdits route={route} loc={loc} />

        <ServerToggles route={route} />

        <RouteDangerZone route={route} loc={loc} />

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Generated TOML
          </span>
          <pre className="overflow-auto rounded-md border border-jul-border bg-jul-surface p-3 font-mono text-xs leading-relaxed text-jul-text">
            {generatedFragment(route, loc)}
          </pre>
        </div>
      </div>
    </Drawer>
  );
}
