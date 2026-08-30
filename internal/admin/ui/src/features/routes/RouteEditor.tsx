/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Link } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import {
  type ConfigPatch,
  type LocationAuthPatch,
  type RateLimitPatch,
  type RouteProjection,
} from "@/api/client.ts";
import { usePermission } from "@/auth/usePermission.ts";
import {
  authWarnings,
  AUTH_METHODS,
  emptyAuthDraft,
  type AuthDraft,
  type AuthMethod,
  type RouteAction,
  type RouteDraft,
} from "@/lib/routeToml.ts";
import {
  buildExistingServerRouteBatch,
  buildNewServerRouteBatch,
  findExactServer,
  formatServerIdentity,
  parsePluginNamesInput,
  parseServerNamesInput,
  RoutePatchValidationError,
  serverIdentityFromRoute,
  serverIdentityKey,
  storeRouteIdentity,
  type RouteCreateSpec,
  type ServerIdentity,
  type StoredRouteSelection,
  type StructuredRouteAction,
} from "@/lib/routePatch.ts";
import { describePatchBatchError, useRunPatchBatch } from "@/lib/useRunPatchBatch.ts";

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
  readonly onChange: (value: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <input
        type="text"
        value={value}
        placeholder={placeholder}
        onChange={(event) => {
          onChange(event.target.value);
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
  readonly onChange: (value: boolean) => void;
}) {
  return (
    <label className="flex items-center gap-2 text-sm text-jul-text">
      <input
        type="checkbox"
        checked={checked}
        onChange={(event) => {
          onChange(event.target.checked);
        }}
        className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
      />
      {label}
    </label>
  );
}

export type StructuredRouteEditorAction = Exclude<RouteAction, "grpc">;

export type RouteEditorInitial = Omit<Partial<RouteDraft>, "action"> & {
  readonly action?: StructuredRouteEditorAction | undefined;
  readonly plugins?: string | undefined;
};

const ACTIONS: ReadonlyArray<{
  readonly value: StructuredRouteEditorAction;
  readonly label: string;
}> = [
  { value: "static", label: "Serve static files" },
  { value: "proxy", label: "HTTP reverse proxy" },
  { value: "redirect", label: "Redirect" },
  { value: "deny", label: "Deny (403)" },
  { value: "return", label: "Fixed response status" },
];

/** Shared auth inputs used by route creation and the per-location auth editor. */
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
            hint="Comma- or space-separated. Leave blank to allow all addresses not explicitly denied."
            value={auth.allow}
            placeholder="10.0.0.0/8, 192.168.1.0/24"
            onChange={(value) => {
              patch("allow", value);
            }}
          />
          <TextField
            label="Deny CIDRs"
            hint="Evaluated first; a match is rejected with 403."
            value={auth.deny}
            placeholder="203.0.113.0/24"
            onChange={(value) => {
              patch("deny", value);
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
            onChange={(value) => {
              patch("basicFile", value);
            }}
          />
          <TextField
            label="Realm (optional)"
            hint="Shown in the browser auth prompt. Defaults to “Restricted”."
            value={auth.basicRealm}
            placeholder="Restricted"
            onChange={(value) => {
              patch("basicRealm", value);
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
            onChange={(value) => {
              patch("jwtJwksUrl", value);
            }}
          />
          <TextField
            label="Issuer (optional)"
            hint="When set, must equal the token's iss claim."
            value={auth.jwtIssuer}
            placeholder="https://issuer.example/"
            onChange={(value) => {
              patch("jwtIssuer", value);
            }}
          />
          <TextField
            label="Audience (optional)"
            hint="When set, must be present in the token's aud claim."
            value={auth.jwtAudience}
            placeholder="api://jul"
            onChange={(value) => {
              patch("jwtAudience", value);
            }}
          />
        </div>
      );
    case "forward":
      return (
        <TextField
          label="Forward-auth URL"
          hint="HTTP(S) endpoint that receives a subrequest and approves or rejects it."
          value={auth.forwardUrl}
          placeholder="http://127.0.0.1:4181/auth"
          onChange={(value) => {
            patch("forwardUrl", value);
          }}
        />
      );
  }
}

interface RouteCreateDraft {
  listen: string;
  serverNames: string;
  path: string;
  matchType: "prefix" | "exact" | "regex";
  action: StructuredRouteEditorAction;
  target: string;
  auth: AuthDraft;
  cache: boolean;
  rateLimit: boolean;
  plugins: string;
}

type RouteCreationMode = "existing" | "new";

function splitList(value: string): string[] {
  return value
    .split(/[\s,]+/)
    .map((entry) => entry.trim())
    .filter((entry) => entry.length > 0);
}

function actionPatch(draft: RouteCreateDraft): StructuredRouteAction {
  const target = draft.target.trim();
  switch (draft.action) {
    case "static":
      return { kind: "static", target };
    case "proxy":
      return { kind: "proxy", target };
    case "redirect":
      return { kind: "redirect", target };
    case "return":
      return { kind: "return", status: Number(target) };
    case "deny":
      return { kind: "deny" };
  }
}

function authPatch(draft: AuthDraft): LocationAuthPatch | null {
  switch (draft.method) {
    case "none":
      return null;
    case "basic": {
      const realm = draft.basicRealm.trim();
      return {
        method: "basic",
        basic_file: draft.basicFile.trim(),
        ...(realm ? { basic_realm: realm } : {}),
      };
    }
    case "jwt": {
      const issuer = draft.jwtIssuer.trim();
      const audience = draft.jwtAudience.trim();
      return {
        method: "jwt",
        jwt_jwks_url: draft.jwtJwksUrl.trim(),
        ...(issuer ? { jwt_issuer: issuer } : {}),
        ...(audience ? { jwt_audience: audience } : {}),
      };
    }
    case "forward":
      return { method: "forward", forward_url: draft.forwardUrl.trim() };
    case "cidr":
      return { method: "cidr", allow: splitList(draft.allow), deny: splitList(draft.deny) };
  }
}

function defaultRateLimit(): RateLimitPatch {
  return { enabled: true, rate: 100, burst: 100, key: "ip" };
}

function targetHint(
  action: StructuredRouteEditorAction,
): { readonly label: string; readonly placeholder: string } | null {
  switch (action) {
    case "static":
      return { label: "Root directory", placeholder: "/var/www/site" };
    case "proxy":
      return { label: "HTTP upstream target", placeholder: "http://app or http://127.0.0.1:3000" };
    case "redirect":
      return { label: "Redirect URL", placeholder: "https://example.com/new" };
    case "return":
      return { label: "HTTP status code", placeholder: "204" };
    case "deny":
      return null;
  }
}

function initialServerIdentity(initial: RouteEditorInitial | undefined): ServerIdentity | null {
  if (initial?.listen === undefined && initial?.serverNames === undefined) return null;
  const listen = (initial.listen ?? "").trim();
  const names = parseServerNamesInput(initial.serverNames ?? "");
  if (listen === "" || names.errors.length > 0) return null;
  return { listen, serverNames: names.names };
}

function routeSpec(draft: RouteCreateDraft, server: ServerIdentity): RouteCreateSpec {
  const plugins = parsePluginNamesInput(draft.plugins);
  if (plugins.errors.length > 0) throw new RoutePatchValidationError(plugins.errors);
  return {
    server,
    matchType: draft.matchType,
    path: draft.path,
    action: actionPatch(draft),
    auth: authPatch(draft.auth),
    cache: draft.cache,
    rateLimit: draft.rateLimit ? defaultRateLimit() : null,
    plugins: plugins.names,
  };
}

export interface RouteEditorProps {
  readonly initial?: RouteEditorInitial | undefined;
  readonly existingRoutes?: RouteProjection[] | undefined;
  /** Called immediately before the completed preview handoff navigates to ConfigPanel. */
  readonly onReview?: ((selection: StoredRouteSelection) => void) | undefined;
  readonly closeLabel?: string | undefined;
  readonly onClose: () => void;
}

/**
 * Guided route creation through one deterministic typed patch batch. The
 * operator explicitly chooses whether the location belongs to an exact existing
 * server identity or to a new server; the editor never infers or switches modes
 * from identity collisions.
 */
export function RouteEditor({
  initial,
  existingRoutes = [],
  onReview,
  closeLabel,
  onClose,
}: RouteEditorProps) {
  const initialIdentity = initialServerIdentity(initial);
  const initialExactRoute =
    initialIdentity === null ? null : findExactServer(existingRoutes, initialIdentity);
  const initialHasServer = initial?.listen !== undefined || initial?.serverNames !== undefined;
  const firstExisting = initialExactRoute ?? existingRoutes[0] ?? null;
  const [mode, setMode] = useState<RouteCreationMode>(() => {
    if (initialExactRoute !== null) return "existing";
    if (initialHasServer) return "new";
    return existingRoutes.length > 0 ? "existing" : "new";
  });
  const [selectedServerKey, setSelectedServerKey] = useState(
    firstExisting === null ? "" : serverIdentityKey(serverIdentityFromRoute(firstExisting)),
  );
  const [draft, setDraft] = useState<RouteCreateDraft>({
    listen: initial?.listen ?? ":8080",
    serverNames: initial?.serverNames ?? "",
    path: initial?.path ?? "/",
    matchType: initial?.matchType ?? "prefix",
    action: initial?.action ?? "proxy",
    target: initial?.target ?? "",
    auth: initial?.auth ?? emptyAuthDraft(),
    cache: initial?.cache ?? false,
    rateLimit: initial?.rateLimit ?? false,
    plugins: initial?.plugins ?? "",
  });
  const [localError, setLocalError] = useState<string | null>(null);
  const batch = useRunPatchBatch();
  const { has } = usePermission();
  const canWrite = has("config:write");

  const selectedRoute =
    existingRoutes.find(
      (route) => serverIdentityKey(serverIdentityFromRoute(route)) === selectedServerKey,
    ) ?? null;
  const selectedIdentity = selectedRoute === null ? null : serverIdentityFromRoute(selectedRoute);
  const target = targetHint(draft.action);
  const authIssues = authWarnings(draft.auth);

  function set<K extends keyof RouteCreateDraft>(key: K, value: RouteCreateDraft[K]): void {
    setDraft((current) => ({ ...current, [key]: value }));
    setLocalError(null);
    batch.clearError();
  }

  function buildPlan(): {
    readonly ops: ConfigPatch[];
    readonly selection: StoredRouteSelection;
  } {
    let server: ServerIdentity;
    let ops: ConfigPatch[];
    if (mode === "existing") {
      if (selectedIdentity === null) {
        throw new RoutePatchValidationError(["Select an exact existing server identity."]);
      }
      server = selectedIdentity;
      ops = buildExistingServerRouteBatch(routeSpec(draft, server), existingRoutes);
    } else {
      const parsedNames = parseServerNamesInput(draft.serverNames);
      if (parsedNames.errors.length > 0) throw new RoutePatchValidationError(parsedNames.errors);
      server = {
        listen: draft.listen.trim(),
        serverNames: parsedNames.names,
      };
      ops = buildNewServerRouteBatch(routeSpec(draft, server), existingRoutes);
    }
    return {
      ops,
      selection: storeRouteIdentity(server, {
        matchType: draft.matchType,
        path: draft.path,
      }),
    };
  }

  let previewOps: ConfigPatch[] = [];
  let buildIssues: string[] = [];
  try {
    previewOps = buildPlan().ops;
  } catch (error) {
    buildIssues =
      error instanceof RoutePatchValidationError
        ? [...error.issues]
        : ["The structured route batch could not be built."];
  }
  const formIssues = [...authIssues, ...buildIssues];
  const previewError = localError ?? describePatchBatchError(batch.error);

  async function openInEditor(): Promise<void> {
    setLocalError(null);
    batch.clearError();
    if (!canWrite) {
      setLocalError("The config:write permission is required to preview route changes.");
      return;
    }

    let plan: ReturnType<typeof buildPlan>;
    try {
      plan = buildPlan();
    } catch (error) {
      setLocalError(
        error instanceof RoutePatchValidationError
          ? error.message
          : "The structured route batch could not be built.",
      );
      return;
    }

    const assessment = await batch.preview(plan.ops);
    if (assessment === null) return;
    onReview?.(plan.selection);
    batch.handoff(assessment);
  }

  const serverHasTls = mode === "existing" && Boolean(selectedRoute?.tls?.enabled);

  return (
    <Drawer
      title="Create route"
      subtitle="Build one ordered structured patch batch, then review its authoritative lifecycle and diff."
      onClose={onClose}
      closeLabel={closeLabel}
      footer={
        <div className="w-full space-y-2">
          {previewError && <p className="text-xs text-jul-danger">{previewError}</p>}
          <div className="flex items-center justify-end gap-3">
            <button
              type="button"
              disabled={batch.busy || formIssues.length > 0 || !canWrite}
              onClick={() => {
                void openInEditor();
              }}
              className="rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              {batch.busy ? "Previewing…" : "Review lifecycle and diff →"}
            </button>
          </div>
          <ForbiddenAction permission="config:write" className="justify-end" />
        </div>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          Nothing is persisted here. Jul previews the exact operations in order, classifies their
          lifecycle server-side, and hands the secret-safe assessment to the Configuration panel.
        </p>

        <fieldset className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
          <legend className="px-1 text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Server mode
          </legend>
          <label className="flex items-start gap-2 text-sm text-jul-text">
            <input
              type="radio"
              name="route-server-mode"
              value="existing"
              checked={mode === "existing"}
              disabled={existingRoutes.length === 0}
              onChange={() => {
                setMode("existing");
                setLocalError(null);
                batch.clearError();
              }}
              className="mt-0.5 accent-jul-accent"
            />
            <span>
              <span className="font-medium">Add to an existing server</span>
              <span className="block text-xs text-jul-muted">
                Select the complete listen + case-sensitive server-name identity. No server_add is
                emitted.
              </span>
            </span>
          </label>
          <label className="flex items-start gap-2 text-sm text-jul-text">
            <input
              type="radio"
              name="route-server-mode"
              value="new"
              checked={mode === "new"}
              onChange={() => {
                setMode("new");
                setLocalError(null);
                batch.clearError();
              }}
              className="mt-0.5 accent-jul-accent"
            />
            <span>
              <span className="font-medium">Create a new server</span>
              <span className="block text-xs text-jul-muted">
                Emits server_add first, then location_add. An exact identity collision is rejected.
              </span>
            </span>
          </label>
        </fieldset>

        {mode === "existing" ? (
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Exact existing server</span>
            <select
              aria-label="Exact existing server"
              value={selectedServerKey}
              onChange={(event) => {
                setSelectedServerKey(event.target.value);
                setLocalError(null);
                batch.clearError();
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              {existingRoutes.map((route) => {
                const identity = serverIdentityFromRoute(route);
                const key = serverIdentityKey(identity);
                return (
                  <option key={key} value={key}>
                    {formatServerIdentity(identity)}
                  </option>
                );
              })}
            </select>
            <span className="text-xs text-jul-muted">
              Server-name order is canonicalized only for display; identity comparison remains
              case-sensitive and set-based.
            </span>
          </label>
        ) : (
          <div className="space-y-3">
            <TextField
              label="New listener"
              hint="The exact address the new server block binds to."
              value={draft.listen}
              placeholder=":8080"
              onChange={(value) => {
                set("listen", value);
              }}
            />
            <TextField
              label="New server names (optional)"
              hint="Comma-separated, case-sensitive set. Blank entries and duplicates are rejected. Leave the whole field blank for any host."
              value={draft.serverNames}
              placeholder="example.com, www.example.com"
              onChange={(value) => {
                set("serverNames", value);
              }}
            />
          </div>
        )}

        {serverHasTls ? (
          <div className="space-y-1 rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
            <p className="font-semibold text-jul-text">TLS is enabled on the selected server</p>
            <p>Locations inherit TLS from their exact parent server identity.</p>
          </div>
        ) : (
          <div className="space-y-1 rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
            <p className="font-semibold text-jul-text">
              {mode === "new" ? "The new server starts without TLS" : "TLS is off on this server"}
            </p>
            <p>
              Configure listener certificates separately in the{" "}
              <Link to="/tls" className="font-medium text-jul-accent underline hover:no-underline">
                TLS &amp; Certificates
              </Link>{" "}
              panel. The route may still proxy to an https:// backend.
            </p>
          </div>
        )}

        <div className="grid grid-cols-[1fr_2fr] gap-3">
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Match type</span>
            <select
              value={draft.matchType}
              onChange={(event) => {
                set("matchType", event.target.value as RouteCreateDraft["matchType"]);
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
            onChange={(value) => {
              set("path", value);
            }}
          />
        </div>

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Structured action</span>
          <select
            value={draft.action}
            onChange={(event) => {
              set("action", event.target.value as StructuredRouteEditorAction);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            {ACTIONS.map((action) => (
              <option key={action.value} value={action.value}>
                {action.label}
              </option>
            ))}
          </select>
          <span className="text-xs text-jul-muted">
            Native gRPC, transcoding, FastCGI, uWSGI, and handler-plugin actions require their
            protocol-specific workflow or the raw editor so listener and protocol settings are not
            silently changed.
          </span>
        </label>

        {target && (
          <TextField
            label={target.label}
            value={draft.target}
            placeholder={target.placeholder}
            onChange={(value) => {
              set("target", value);
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
              onChange={(event) => {
                set("auth", { ...draft.auth, method: event.target.value as AuthMethod });
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              {AUTH_METHODS.map((method) => (
                <option key={method.value} value={method.value}>
                  {method.label}
                </option>
              ))}
            </select>
            <span className="text-xs text-jul-muted">
              {AUTH_METHODS.find((method) => method.value === draft.auth.method)?.hint}
            </span>
          </label>
          <AuthFields
            auth={draft.auth}
            onChange={(next) => {
              set("auth", next);
            }}
          />
        </div>

        <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Route modifiers
          </span>
          <Toggle
            label="Cache responses"
            checked={draft.cache}
            onChange={(value) => {
              set("cache", value);
            }}
          />
          <Toggle
            label="Rate limit (100 requests, burst 100, key IP)"
            checked={draft.rateLimit}
            onChange={(value) => {
              set("rateLimit", value);
            }}
          />
          <TextField
            label="Middleware plugins (optional)"
            hint="Comma- or newline-separated, in middleware execution order. Duplicates and blank entries are rejected."
            value={draft.plugins}
            placeholder="request-id, security-headers"
            onChange={(value) => {
              set("plugins", value);
            }}
          />
        </div>

        {formIssues.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
            <p className="text-xs font-semibold uppercase tracking-wider text-jul-warning">
              Resolve before preview
            </p>
            {formIssues.map((issue, index) => (
              <p key={`${issue}-${String(index)}`} className="text-xs text-jul-text">
                {issue}
              </p>
            ))}
          </div>
        )}

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Exact ordered patch batch
          </span>
          {previewOps.length > 0 ? (
            <ol className="space-y-1 rounded-md border border-jul-border bg-jul-surface p-3 pl-8 font-mono text-xs leading-relaxed text-jul-text">
              {previewOps.map((operation, index) => (
                <li key={`${String(index)}-${operation.op}`} className="list-decimal">
                  {operation.op}
                </li>
              ))}
            </ol>
          ) : (
            <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
              The batch will appear after the identity and route fields are valid.
            </p>
          )}
        </div>
      </div>
    </Drawer>
  );
}
