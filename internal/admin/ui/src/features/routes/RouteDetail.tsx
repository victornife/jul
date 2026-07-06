/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import {
  patchConfig,
  ConfigRejectedError,
  type ConfigPatch,
  type LocationProjection,
  type LocationWAF,
  type RouteProjection,
} from "@/api/client.ts";
import { LocationWAFEditor } from "@/features/security/LocationWAFEditor.tsx";
import { AuthEditor } from "@/features/routes/AuthEditor.tsx";
import {
  RouteActionEditor,
  RouteMatchEditor,
  RouteRenameEditor,
} from "@/features/routes/RouteEditors.tsx";
import { LocationRateLimitEditor } from "@/features/routes/LocationRateLimitEditor.tsx";
import { TranscodeQuickEdit } from "@/features/routes/TranscodeQuickEdit.tsx";
import { isEditableAction } from "@/lib/routeEdit.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";

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

// generatedFragment renders the effective TOML for one location so the operator
// sees the effective config, not just raw config (Milestone 2.1 criterion).
function generatedFragment(route: RouteProjection, loc: LocationProjection): string {
  const lines: string[] = ["[[servers]]", `listen = "${route.listen}"`];
  if (route.server_names && route.server_names.length > 0) {
    lines.push(`server_names = [${route.server_names.map((n) => `"${n}"`).join(", ")}]`);
  }
  lines.push("");
  lines.push("  [[servers.locations]]");
  lines.push(`  match = { type = "${loc.type}", path = "${loc.match}" }`);
  if (loc.target) {
    const key =
      loc.action === "static"
        ? "root"
        : loc.action === "redirect"
          ? "redirect"
          : loc.action === "return"
            ? "return"
            : "proxy_pass";
    const val = loc.action === "return" ? loc.target : `"${loc.target}"`;
    lines.push(`  ${key} = ${val}`);
  }
  if (loc.action === "deny") lines.push("  deny = true");
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

// QuickEdits performs true in-place edits via the structured patch API (Wave B):
// the server applies the change to the parsed config and returns the candidate
// TOML, which we hand to the Config editor for diff review + apply. Unlike
// "Clone route" (which appends a draft block), these modify the existing
// route, so there are no duplicate blocks to prune.
function QuickEdits({
  route,
  loc,
}: {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
}) {
  const navigate = useNavigate();
  const [target, setTarget] = useState(loc.target ?? "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [wafEditing, setWafEditing] = useState(false);
  const [authEditing, setAuthEditing] = useState(false);
  const [matchEditing, setMatchEditing] = useState(false);
  const [actionEditing, setActionEditing] = useState(false);
  const [rateLimitEditing, setRateLimitEditing] = useState(false);

  async function runPatch(patch: ConfigPatch): Promise<void> {
    setError(null);
    setBusy(true);
    try {
      const res = await patchConfig(patch);
      setPendingDraft({
        kind: "patch",
        ops: [patch],
        baseVersion: res.base_version,
        previewDiff: res.diff,
        candidate: res.candidate,
      });
      void navigate("/config");
    } catch (err) {
      setError(err instanceof ConfigRejectedError ? err.message : "The edit could not be applied.");
    } finally {
      setBusy(false);
    }
  }

  const canSetTarget = loc.action === "proxy";

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
              disabled={busy || target.trim() === "" || target === loc.target}
              onClick={() => {
                void runPatch({
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
          disabled={busy}
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
            disabled={busy}
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
          disabled={busy}
          onClick={() => {
            void runPatch({
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
          disabled={busy}
          onClick={() => {
            void runPatch({
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
          disabled={busy}
          onClick={() => {
            setRateLimitEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.rate_limit ? "Edit rate limit" : "Set rate limit"} →
        </button>
        <button
          type="button"
          disabled={busy || (!loc.require_client_cert && !route.tls?.client_auth)}
          title={
            !route.tls?.client_auth
              ? "Enable mutual TLS on this server first (TLS & Certificates → Mutual TLS)."
              : "Takes effect immediately on reload."
          }
          onClick={() => {
            void runPatch({
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
          disabled={busy}
          onClick={() => {
            setWafEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.waf ? "Edit WAF override" : "Add WAF override"} →
        </button>
        <button
          type="button"
          disabled={busy}
          onClick={() => {
            setAuthEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {loc.auth ? "Edit auth" : "Add auth"} →
        </button>
      </div>

      {loc.action === "grpc_transcode" && (
        <TranscodeQuickEdit route={route} loc={loc} />
      )}

      {error && <p className="text-xs text-jul-danger">{error}</p>}

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
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [renameEditing, setRenameEditing] = useState(false);
  const tlsOn = Boolean(route.tls?.enabled);

  async function runPatch(patch: ConfigPatch): Promise<void> {
    setError(null);
    setBusy(true);
    try {
      const res = await patchConfig(patch);
      setPendingDraft({
        kind: "patch",
        ops: [patch],
        baseVersion: res.base_version,
        previewDiff: res.diff,
        candidate: res.candidate,
      });
      void navigate("/config");
    } catch (err) {
      setError(err instanceof ConfigRejectedError ? err.message : "The edit could not be applied.");
      setBusy(false);
    }
  }

  return (
    <div className="space-y-2">
      <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
        Server protocols
      </span>
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          disabled={busy || !tlsOn}
          title={tlsOn ? undefined : "HTTP/3 requires TLS on this listener."}
          onClick={() => {
            void runPatch({ op: "server_toggle_http3", listen: route.listen, enabled: !route.http3 });
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          {route.http3 ? "Disable HTTP/3" : "Enable HTTP/3"} →
        </button>
        <button
          type="button"
          disabled={busy || tlsOn}
          title={
            tlsOn
              ? "TLS listeners negotiate HTTP/2 via ALPN; h2c is for plaintext listeners only."
              : undefined
          }
          onClick={() => {
            void runPatch({ op: "server_toggle_h2c", listen: route.listen, enabled: !route.h2c });
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
          disabled={busy}
          onClick={() => {
            setRenameEditing(true);
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
        >
          Rename host names →
        </button>
      </div>
      {error && <p className="text-xs text-jul-danger">{error}</p>}
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

export interface RouteDetailProps {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
  readonly onClose: () => void;
  readonly onEdit: () => void;
}

/** Route detail drawer (Milestone 2.1): explains what a route does, shows its
 * effective config and any warnings, without the operator reading TOML. */
export function RouteDetail({ route, loc, onClose, onEdit }: RouteDetailProps) {
  return (
    <Drawer
      title={`${loc.type} ${loc.match}`}
      subtitle={`${route.listen}${route.server_names && route.server_names.length > 0 ? " · " + route.server_names.join(", ") : ""}`}
      onClose={onClose}
      footer={
        <button
          type="button"
          onClick={onEdit}
          className="ml-auto block rounded-md border border-jul-border px-4 py-1.5 text-sm text-jul-text hover:bg-jul-surface"
        >
          Clone route →
        </button>
      }
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
        </div>

        <QuickEdits route={route} loc={loc} />

        <ServerToggles route={route} />

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
