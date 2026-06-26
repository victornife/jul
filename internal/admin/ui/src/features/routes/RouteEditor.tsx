import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import { fetchRawConfig } from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
import {
  appendFragment,
  authWarnings,
  emptyAuthDraft,
  generateRouteToml,
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

const AUTH_METHODS: { value: AuthMethod; label: string; hint: string }[] = [
  { value: "none", label: "No authentication", hint: "Anyone who matches the route is allowed." },
  {
    value: "cidr",
    label: "IP allow / deny (CIDR)",
    hint: "Permit or reject clients by IP range. Deny wins over allow.",
  },
  {
    value: "basic",
    label: "HTTP Basic (htpasswd)",
    hint: "Username/password checked against a bcrypt htpasswd file.",
  },
  {
    value: "jwt",
    label: "JWT bearer (JWKS)",
    hint: "Validate bearer tokens against an issuer's JWKS endpoint.",
  },
  {
    value: "forward",
    label: "Forward-auth (external)",
    hint: "Delegate the decision to an external HTTP endpoint.",
  },
];

/**
 * AuthFields renders the method-specific inputs for the selected auth method.
 * Picking a concrete method (instead of a bare on/off toggle) is what lets the
 * editor emit auth TOML that actually enforces something — the old toggle
 * emitted "auth = {}", which the server treats as allow-all.
 */
function AuthFields({
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

export interface RouteEditorProps {
  readonly initial?: Partial<RouteDraft>;
  readonly onClose: () => void;
}

/**
 * Guided route creation/editing (Milestone 2.2). It never writes directly: it
 * generates a candidate [[servers]] block, appends it to the running config,
 * and hands the draft to the Config editor where it flows through
 * Validate → Diff → Apply → Rollback.
 */
export function RouteEditor({ initial, onClose }: RouteEditorProps) {
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

  const fragment = generateRouteToml(draft);
  const th = targetHint(draft.action);
  const authWarn = authWarnings(draft.auth);

  function set<K extends keyof RouteDraft>(key: K, value: RouteDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: value }));
  }

  async function openInEditor(): Promise<void> {
    setError(null);
    try {
      const raw = await fetchRawConfig();
      setPendingDraft({ kind: "toml", toml: appendFragment(raw.raw ?? "", fragment) });
      void navigate("/config");
    } catch {
      setError("Could not load the current configuration to merge this route.");
    }
  }

  return (
    <Drawer
      title="New route"
      subtitle="Generate a route, then review and apply it safely in the editor."
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          <button
            type="button"
            onClick={() => {
              void openInEditor();
            }}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110"
          >
            Review in editor →
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
            Generated TOML
          </span>
          <pre className="overflow-auto rounded-md border border-jul-border bg-jul-surface p-3 font-mono text-xs leading-relaxed text-jul-text">
            {fragment}
          </pre>
        </div>
      </div>
    </Drawer>
  );
}
