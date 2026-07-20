/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import {
  patchConfigBatch,
  ConfigRejectedError,
  type ConfigPatch,
  type LocationActionPatch,
  type LocationAuthPatch,
  type RateLimitPatch,
  type RouteProjection,
  type RouteTarget,
} from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
import {
  authWarnings,
  AUTH_METHODS,
  emptyAuthDraft,
  type AuthDraft,
  type AuthMethod,
  type RouteAction,
  type RouteDraft,
} from "@/lib/routeToml.ts";

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

function Toggle({
  label,
  checked,
  onChange,
}: {
  readonly label: string;
  readonly checked: boolean;
  readonly onChange: (v: boolean) => void;
}) {
  return (
    <label className="flex items-center gap-2 text-sm text-jul-text">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => {
          onChange(e.target.checked);
        }}
        className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
      />
      {label}
    </label>
  );
}

const ACTIONS: { value: RouteAction; label: string }[] = [
  { value: "static", label: "Serve static files" },
  { value: "proxy", label: "Reverse proxy" },
  { value: "redirect", label: "Redirect" },
  { value: "deny", label: "Deny (403)" },
  { value: "return", label: "Return status" },
];

/**
 * AuthFields renders the method-specific inputs for the selected auth method.
 * Picking a concrete method (instead of a bare on/off toggle) is what lets the
 * editor emit auth TOML that actually enforces something — the old toggle
 * emitted "auth = {}", which the server treats as allow-all. Exported so the
 * per-location AuthEditor reuses the exact same inputs.
 */
export function AuthFields({
  auth,
  onChange,
}: {
  readonly auth: AuthDraft;
  readonly onChange: (next: AuthDraft) => void;
}) {
  function patch<K extends keyof AuthDraft>(key: K, value: AuthDraft[K]): void {
    onChange({ ...auth, [key]: value });
  }
  switch (auth.method) {
    case "none":
      return null;
    case "cidr":
      return (
        <div className="space-y-3">
          <TextField
            label="Allow CIDRs"
            hint="Comma- or space-separated, e.g. 10.0.0.0/8 192.168.1.0/24. Leave blank to allow all not denied."
            value={auth.allow}
            placeholder="10.0.0.0/8, 192.168.1.0/24"
            onChange={(v) => {
              patch("allow", v);
            }}
          />
          <TextField
            label="Deny CIDRs"
            hint="Evaluated first; a match is rejected with 403."
            value={auth.deny}
            placeholder="203.0.113.0/24"
            onChange={(v) => {
              patch("deny", v);
            }}
          />
        </div>
      );
    case "basic":
      return (
        <div className="space-y-3">
          <TextField
            label="htpasswd file"
            hint="Path on the server to a bcrypt htpasswd file."
            value={auth.basicFile}
            placeholder="/etc/jul/htpasswd"
            onChange={(v) => {
              patch("basicFile", v);
            }}
          />
          <TextField
            label="Realm (optional)"
            hint="Shown in the browser auth prompt. Defaults to “Restricted”."
            value={auth.basicRealm}
            placeholder="Restricted"
            onChange={(v) => {
              patch("basicRealm", v);
            }}
          />
        </div>
      );
    case "jwt":
      return (
        <div className="space-y-3">
          <TextField
            label="JWKS URL"
            hint="Must be https. The issuer's JSON Web Key Set endpoint."
            value={auth.jwtJwksUrl}
            placeholder="https://issuer.example/.well-known/jwks.json"
            onChange={(v) => {
              patch("jwtJwksUrl", v);
            }}
          />
          <TextField
            label="Issuer (optional)"
            hint="When set, must equal the token's iss claim."
            value={auth.jwtIssuer}
            placeholder="https://issuer.example/"
            onChange={(v) => {
              patch("jwtIssuer", v);
            }}
          />
          <TextField
            label="Audience (optional)"
            hint="When set, must be present in the token's aud claim."
            value={auth.jwtAudience}
            placeholder="api://jul"
            onChange={(v) => {
              patch("jwtAudience", v);
            }}
          />
        </div>
      );
    case "forward":
      return (
        <TextField
          label="Forward-auth URL"
          hint="http(s) endpoint that receives a subrequest and approves or rejects it."
          value={auth.forwardUrl}
          placeholder="http://127.0.0.1:4181/auth"
          onChange={(v) => {
            patch("forwardUrl", v);
          }}
        />
      );
  }
}

function targetHint(action: RouteAction): { label: string; placeholder: string } | null {
  switch (action) {
    case "static":
      return { label: "Root directory", placeholder: "/var/www/site" };
    case "proxy":
      return { label: "Upstream target", placeholder: "http://app or http://127.0.0.1:3000" };
    case "redirect":
      return { label: "Redirect URL", placeholder: "https://example.com/new" };
    case "return":
      return { label: "Status code", placeholder: "200" };
    case "deny":
      return null;
  }
}

// splitList mirrors the backend: split on commas/whitespace into trimmed,
// non-empty entries.
function splitList(s: string): string[] {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter((x) => x.length > 0);
}

// serverNames parses the comma-separated host names input into the array the
// patch ops expect. Empty input becomes an empty list (catch-all server block).
function serverNames(raw: string): string[] {
  return raw
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

// routeTarget returns the location coordinates for the draft.
function routeTarget(d: RouteDraft): RouteTarget {
  return {
    listen: d.listen.trim() || ":8080",
    server_names: serverNames(d.serverNames),
    match_type: d.matchType,
    path: d.path.trim() || "/",
  };
}

// actionPatch maps the draft action + target to the structured location action
// payload used by the location_add / location_set_action ops.
function actionPatch(d: RouteDraft): LocationActionPatch {
  const target = d.target.trim();
  switch (d.action) {
    case "static":
      return { kind: "static", target };
    case "proxy":
      return { kind: "proxy", target };
    case "redirect":
      return { kind: "redirect", target };
    case "return":
      return { kind: "return", status: Number(target) || 200 };
    case "deny":
      return { kind: "deny" };
  }
}

// authPatch maps the draft to the location_set_auth payload. Returns null when
// the operator explicitly chose "none".
function authPatch(d: AuthDraft): LocationAuthPatch | null {
  switch (d.method) {
    case "none":
      return null;
    case "basic": {
      const realm = d.basicRealm.trim();
      return {
        method: "basic",
        basic_file: d.basicFile.trim(),
        ...(realm ? { basic_realm: realm } : {}),
      };
    }
    case "jwt": {
      const issuer = d.jwtIssuer.trim();
      const audience = d.jwtAudience.trim();
      return {
        method: "jwt",
        jwt_jwks_url: d.jwtJwksUrl.trim(),
        ...(issuer ? { jwt_issuer: issuer } : {}),
        ...(audience ? { jwt_audience: audience } : {}),
      };
    }
    case "forward":
      return { method: "forward", forward_url: d.forwardUrl.trim() };
    case "cidr":
    default:
      return { method: "cidr", allow: splitList(d.allow), deny: splitList(d.deny) };
  }
}

// rateLimitPatch builds the default rate-limit payload used when the toggle is
// on in the route-creation form.
function rateLimitPatch(): RateLimitPatch {
  return { enabled: true, rate: 100, burst: 100, key: "ip" };
}

// serverExists checks whether a server block with the same listen + server_names
// already exists in the running config projection.
function serverExists(d: RouteDraft, existing: RouteProjection[] | undefined): boolean {
  const listen = d.listen.trim() || ":8080";
  const names = serverNames(d.serverNames);
  return (
    existing?.some((r) => {
      const sameListen = r.listen === listen;
      const existingNames = r.server_names ?? [];
      const sameNames =
        names.length === existingNames.length &&
        names.every((n, i) => n === existingNames[i]);
      return sameListen && sameNames;
    }) ?? false
  );
}

export interface RouteEditorProps {
  readonly initial?: Partial<RouteDraft>;
  readonly existingRoutes?: RouteProjection[];
  readonly serverHasTls?: boolean | undefined;
  readonly onReview?: () => void;
  readonly closeLabel?: string;
  readonly onClose: () => void;
}

/**
 * Guided route creation/editing (Milestone 2.2). It never writes directly: it
 * generates a candidate [[servers]] block, appends it to the running config,
 * and hands the draft to the Config editor where it flows through
 * Validate → Diff → Apply → Rollback.
 */
export function RouteEditor({
  initial,
  existingRoutes,
  serverHasTls,
  onReview,
  closeLabel,
  onClose,
}: RouteEditorProps) {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<RouteDraft>({
    listen: initial?.listen ?? ":8080",
    serverNames: initial?.serverNames ?? "",
    path: initial?.path ?? "/",
    matchType: initial?.matchType ?? "prefix",
    action: initial?.action ?? "proxy",
    target: initial?.target ?? "",
    auth: initial?.auth ?? emptyAuthDraft(),
    cache: initial?.cache ?? false,
    compression: initial?.compression ?? false,
    rateLimit: initial?.rateLimit ?? false,
  });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const th = targetHint(draft.action);
  const authWarn = authWarnings(draft.auth);

  function set<K extends keyof RouteDraft>(key: K, value: RouteDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: value }));
  }

  function buildPatches(): ConfigPatch[] {
    const target = routeTarget(draft);
    const ops: ConfigPatch[] = [];

    if (!serverExists(draft, existingRoutes)) {
      ops.push({
        op: "server_add",
        listen: target.listen,
        server_names: target.server_names,
      });
    }

    ops.push({
      op: "location_add",
      listen: target.listen,
      server_names: target.server_names,
      match_set: { type: draft.matchType, path: draft.path.trim() || "/" },
      action: actionPatch(draft),
    });

    const auth = authPatch(draft.auth);
    if (auth) {
      ops.push({ op: "location_set_auth", ...target, auth });
    }

    if (draft.cache) {
      ops.push({ op: "route_toggle_cache", ...target, enabled: true });
    }

    if (draft.rateLimit) {
      ops.push({ op: "route_set_rate_limit", ...target, rate_limit: rateLimitPatch() });
    }

    return ops;
  }

  async function openInEditor(): Promise<void> {
    setError(null);
    setBusy(true);
    try {
      const ops = buildPatches();
      const res = await patchConfigBatch(ops);
      setPendingDraft({
        kind: "patch",
        ops,
        baseVersion: res.base_version,
        previewDiff: res.diff,
        candidate: res.candidate,
      });
      if (onReview) {
        onReview();
      } else {
        void navigate("/config");
      }
    } catch (err) {
      setError(err instanceof ConfigRejectedError ? err.message : "The edit could not be applied.");
      setBusy(false);
    }
  }

  return (
    <Drawer
      title="New route"
      subtitle="Generate a route, then review and apply it safely in the editor."
      onClose={onClose}
      closeLabel={closeLabel}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          <button
            type="button"
            disabled={busy || authWarn.length > 0}
            onClick={() => {
              void openInEditor();
            }}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            {busy ? "Previewing…" : "Review in editor →"}
          </button>
        </div>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          A route tells Jul what to do when an incoming request matches a host and path. This editor
          builds the configuration for you; nothing is applied until you review the diff and confirm
          in the editor.
        </p>

        {serverHasTls ? (
          <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
            <p className="font-semibold text-jul-text">TLS is enabled on this server</p>
            <p>
              Routes inherit TLS from their parent server. You cannot turn TLS on or off per route
              — the listener terminates TLS before any path matching happens.
            </p>
            <p>
              Manage certificates and settings in the{" "}
              <Link
                to="/tls"
                className="inline-flex items-center gap-1 font-medium text-jul-accent underline hover:no-underline"
              >
                TLS &amp; Certificates
              </Link>{" "}
              panel.
            </p>
          </div>
        ) : (
          <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
            <p className="font-semibold text-jul-text">TLS is not enabled on this server</p>
            <p>
              Routes inherit TLS from their parent server. You cannot turn TLS on or off per route
              — the listener terminates TLS before any path matching happens.
            </p>
            <p>
              If you need HTTPS for this route, create or edit a TLS-enabled server in the{" "}
              <Link
                to="/tls"
                className="inline-flex items-center gap-1 font-medium text-jul-accent underline hover:no-underline"
              >
                TLS &amp; Certificates
              </Link>{" "}
              panel, then move the location into that server block.
            </p>
            <p>
              (You can still proxy to an <code>https://</code> upstream below for encrypted backend
              traffic regardless of this setting.)
            </p>
          </div>
        )}

        <TextField
          label="Listener"
          hint="The address this server block binds to."
          value={draft.listen}
          placeholder=":8080"
          onChange={(v) => {
            set("listen", v);
          }}
        />
        <TextField
          label="Host names (optional)"
          hint="Comma-separated. Leave blank to match any host."
          value={draft.serverNames}
          placeholder="example.com, www.example.com"
          onChange={(v) => {
            set("serverNames", v);
          }}
        />

        <div className="grid grid-cols-[1fr_2fr] gap-3">
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Match type</span>
            <select
              value={draft.matchType}
              onChange={(e) => {
                set("matchType", e.target.value as RouteDraft["matchType"]);
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="prefix">prefix</option>
              <option value="exact">exact</option>
              <option value="regex">regex</option>
            </select>
          </label>
          <TextField
            label="Path"
            value={draft.path}
            placeholder="/api/"
            onChange={(v) => {
              set("path", v);
            }}
          />
        </div>

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Action</span>
          <select
            value={draft.action}
            onChange={(e) => {
              set("action", e.target.value as RouteAction);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            {ACTIONS.map((a) => (
              <option key={a.value} value={a.value}>
                {a.label}
              </option>
            ))}
          </select>
        </label>

        {th && (
          <TextField
            label={th.label}
            value={draft.target}
            placeholder={th.placeholder}
            onChange={(v) => {
              set("target", v);
            }}
          />
        )}

        <div className="space-y-3 rounded-md border border-jul-border bg-jul-surface p-3">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Authentication
          </span>
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Method</span>
            <select
              value={draft.auth.method}
              onChange={(e) => {
                set("auth", { ...draft.auth, method: e.target.value as AuthMethod });
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              {AUTH_METHODS.map((m) => (
                <option key={m.value} value={m.value}>
                  {m.label}
                </option>
              ))}
            </select>
            <span className="text-xs text-jul-muted">
              {AUTH_METHODS.find((m) => m.value === draft.auth.method)?.hint}
            </span>
          </label>
          <AuthFields
            auth={draft.auth}
            onChange={(next) => {
              set("auth", next);
            }}
          />
          {authWarn.length > 0 && (
            <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2">
              {authWarn.map((wn, i) => (
                <p key={`aw-${String(i)}`} className="text-xs text-jul-warning">
                  {wn}
                </p>
              ))}
            </div>
          )}
        </div>

        <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Edge rules
          </span>
          <Toggle
            label="Cache responses"
            checked={draft.cache}
            onChange={(v) => {
              set("cache", v);
            }}
          />
          <Toggle
            label="Rate limit"
            checked={draft.rateLimit}
            onChange={(v) => {
              set("rateLimit", v);
            }}
          />
        </div>

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Patch operations
          </span>
          <ul className="space-y-1 rounded-md border border-jul-border bg-jul-surface p-3 font-mono text-xs leading-relaxed text-jul-text">
            {buildPatches().map((op, i) => {
              const loc =
                "match_type" in op
                  ? ` ${op.match_type} ${op.path}`
                  : "";
              const addr = "listen" in op ? ` ${op.listen}` : "";
              return (
                <li key={`patch-op-${String(i)}`}>
                  {op.op}
                  {addr}
                  {loc}
                </li>
              );
            })}
          </ul>
        </div>
      </div>
    </Drawer>
  );
}
