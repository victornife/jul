/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import type { LocationProjection, RouteProjection } from "@/api/client.ts";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import {
  buildCachePolicyPatchOps,
  CACHE_POLICY_STARTERS,
  type CachePolicyFormState,
  type CachePolicyStarter,
  seedCachePolicyForm,
  validateCachePolicyForm,
} from "@/lib/cachePolicyDraft.ts";
import { routeTargetFromProjection } from "@/lib/routePatch.ts";
import { describePatchBatchError, useRunPatchBatch } from "@/lib/useRunPatchBatch.ts";

export interface LocationCachePolicyEditorProps {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
  readonly onClose: () => void;
}

export function LocationCachePolicyEditor({
  route,
  loc,
  onClose,
}: LocationCachePolicyEditorProps) {
  const batch = useRunPatchBatch();
  const { has } = usePermission();
  const canWrite = has("config:write");
  const [form, setForm] = useState<CachePolicyFormState>(() => seedCachePolicyForm(loc));

  const target = routeTargetFromProjection(route, loc);
  const validation = validateCachePolicyForm(form);
  const ops = buildCachePolicyPatchOps(target, loc, form, validation.patch);
  const batchError = describePatchBatchError(batch.error);
  const eff = loc.cache_effective;

  function applyStarter(starter: CachePolicyStarter): void {
    setForm({
      cacheEnabled: starter.cacheEnabled,
      defaultTTL: starter.defaultTTL,
      maxTTL: starter.maxTTL,
      staleWhileRevalidate: starter.staleWhileRevalidate,
      staleIfError: starter.staleIfError,
      bypassMode: starter.bypassMode,
      bypassCookiesInput: starter.bypassCookiesInput,
    });
  }

  async function save(): Promise<void> {
    if (!canWrite || validation.errors.length > 0 || ops.length === 0) return;
    await batch.run(ops, route.base_version);
  }

  async function resetToGlobalDefaults(): Promise<void> {
    if (!canWrite || loc.cache_policy === undefined) return;
    await batch.run(
      [{ op: "location_cache_policy_clear", ...target }],
      route.base_version,
    );
  }

  return (
    <Drawer
      title="Route cache policy"
      subtitle={`${loc.type} ${loc.match} on ${route.listen}`}
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            {loc.cache_policy !== undefined && (
              <button
                type="button"
                disabled={batch.busy || !canWrite}
                onClick={() => {
                  void resetToGlobalDefaults();
                }}
                className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-surface disabled:opacity-40"
              >
                Reset to global defaults →
              </button>
            )}
            {batchError && (
              <span role="alert" className="text-xs text-jul-danger">
                {batchError}
              </span>
            )}
            <button
              type="button"
              disabled={batch.busy || validation.errors.length > 0 || ops.length === 0 || !canWrite}
              onClick={() => {
                void save();
              }}
              className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              {batch.busy ? "Previewing…" : "Preview change →"}
            </button>
          </div>
          <ForbiddenAction permission="config:write" className="justify-end" />
        </div>
      }
    >
      <div className="space-y-4">
        <div className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted space-y-1.5">
          <p className="font-semibold text-jul-text">How route cache policy works</p>
          <p>
            Explicit origin exclusion (<span className="font-mono">Cache-Control: no-store</span>,{" "}
            <span className="font-mono">private</span>, <span className="font-mono">Set-Cookie</span>,{" "}
            <span className="font-mono">Vary: *</span>, <span className="font-mono">Vary: Cookie</span>,{" "}
            <span className="font-mono">Vary: Authorization</span>, or uncacheable{" "}
            <span className="font-mono">Authorization</span>) always wins.
          </p>
          <p>
            Leave a duration blank to inherit from global <span className="font-mono">[cache]</span>.
            Set an explicit <span className="font-mono">0s</span> to disable that fallback or stale
            window on this route (or to disable fresh storage when{" "}
            <span className="font-mono">max_ttl = &quot;0s&quot;</span>).
          </p>
        </div>

        {eff && (
          <div
            aria-label="Effective route cache posture"
            className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs space-y-1"
          >
            <p className="font-semibold text-jul-text">Current effective posture</p>
            <p className="text-jul-muted">
              Global cache: <span className="font-mono text-jul-text">{eff.global_enabled ? "enabled" : "disabled"}</span>{" "}
              · Route toggle: <span className="font-mono text-jul-text">{loc.cache ? "enabled" : "disabled"}</span>
            </p>
            <p className="text-jul-muted">
              Default TTL: <span className="font-mono text-jul-text">{eff.default_ttl}</span> ({eff.default_ttl_source})
              {" · "}Max TTL cap:{" "}
              <span className="font-mono text-jul-text">{eff.max_ttl ?? "none"}</span> ({eff.max_ttl_source})
            </p>
            <p className="text-jul-muted">
              SWR: <span className="font-mono text-jul-text">{eff.stale_while_revalidate}</span> ({eff.swr_source})
              {" · "}Stale-if-error: <span className="font-mono text-jul-text">{eff.stale_if_error}</span> ({eff.sie_source})
            </p>
            <p className="text-jul-muted">
              Cookie bypass: <span className="font-mono text-jul-text">{eff.bypass_mode}</span>
              {eff.bypass_cookies && eff.bypass_cookies.length > 0
                ? ` (${eff.bypass_cookies.join(", ")})`
                : ""}
            </p>
            <p className="text-jul-muted">
              Inherited global mechanics: fill lock{" "}
              <span className="font-mono text-jul-text">{eff.lock_enabled ? "on" : "off"}</span>, range{" "}
              <span className="font-mono text-jul-text">{eff.range_enabled ? "on" : "off"}</span>, disk tier{" "}
              <span className="font-mono text-jul-text">{eff.disk_enabled ? "on" : "off"}</span>
              {eff.max_entry_size ? `, max entry ${eff.max_entry_size}` : ""}.
            </p>
          </div>
        )}

        <div className="space-y-2">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Curated starters (populate form for review)
          </span>
          <div className="grid grid-cols-1 gap-2">
            {CACHE_POLICY_STARTERS.map((starter) => (
              <button
                key={starter.id}
                type="button"
                disabled={batch.busy || !canWrite}
                onClick={() => {
                  applyStarter(starter);
                }}
                className="rounded-md border border-jul-border bg-jul-bg p-2.5 text-left hover:border-jul-accent disabled:opacity-40"
              >
                <div className="text-xs font-semibold text-jul-text">{starter.label}</div>
                <div className="mt-0.5 text-xs text-jul-muted">{starter.summary}</div>
              </button>
            ))}
          </div>
        </div>

        <label className="flex items-center gap-2">
          <input
            type="checkbox"
            checked={form.cacheEnabled}
            onChange={(e) => {
              setForm((prev) => ({ ...prev, cacheEnabled: e.target.checked }));
            }}
            className="rounded border-jul-border"
          />
          <span className="text-sm text-jul-text">Enable caching on this route (cache = true)</span>
        </label>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Default TTL override</span>
            <input
              type="text"
              value={form.defaultTTL}
              aria-invalid={validation.errors.some((e) => e.includes("default_ttl"))}
              placeholder="inherit global (or 0s, 5m, 1h)"
              onChange={(e) => {
                setForm((prev) => ({ ...prev, defaultTTL: e.target.value }));
              }}
              className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
            />
            <span className="text-xs text-jul-muted">
              Fallback when origin omits freshness. Use 0s to require explicit origin headers.
            </span>
          </label>

          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Max TTL cap</span>
            <input
              type="text"
              value={form.maxTTL}
              aria-invalid={validation.errors.some((e) => e.includes("max_ttl"))}
              placeholder="no route cap (or 0s, 1m, 24h)"
              onChange={(e) => {
                setForm((prev) => ({ ...prev, maxTTL: e.target.value }));
              }}
              className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
            />
            <span className="text-xs text-jul-muted">
              Upper bound on fresh lifetime across s-maxage, max-age, Expires, and default_ttl.
            </span>
          </label>

          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Stale-while-revalidate</span>
            <input
              type="text"
              value={form.staleWhileRevalidate}
              aria-invalid={validation.errors.some((e) => e.includes("stale_while_revalidate"))}
              placeholder="inherit global (or 0s, 30s)"
              onChange={(e) => {
                setForm((prev) => ({ ...prev, staleWhileRevalidate: e.target.value }));
              }}
              className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
            />
            <span className="text-xs text-jul-muted">
              Fallback SWR window when origin omits stale-while-revalidate.
            </span>
          </label>

          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Stale-if-error</span>
            <input
              type="text"
              value={form.staleIfError}
              aria-invalid={validation.errors.some((e) => e.includes("stale_if_error"))}
              placeholder="inherit global (or 0s, 5m)"
              onChange={(e) => {
                setForm((prev) => ({ ...prev, staleIfError: e.target.value }));
              }}
              className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
            />
            <span className="text-xs text-jul-muted">
              Fallback error-tolerance window when origin omits stale-if-error.
            </span>
          </label>
        </div>

        <fieldset className="space-y-2 rounded-md border border-jul-border p-3">
          <legend className="px-1 text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Request cookie bypass (lookup &amp; storage)
          </legend>
          <label className="flex items-center gap-2 text-sm text-jul-text">
            <input
              type="radio"
              name="bypass-mode"
              checked={form.bypassMode === "none"}
              onChange={() => {
                setForm((prev) => ({ ...prev, bypassMode: "none" }));
              }}
            />
            <span>None (requests with cookies may use shared cache if origin allows)</span>
          </label>
          <label className="flex items-center gap-2 text-sm text-jul-text">
            <input
              type="radio"
              name="bypass-mode"
              checked={form.bypassMode === "any"}
              onChange={() => {
                setForm((prev) => ({ ...prev, bypassMode: "any" }));
              }}
            />
            <span>Bypass on any Cookie header (bypass_any_cookie = true)</span>
          </label>
          <label className="flex items-center gap-2 text-sm text-jul-text">
            <input
              type="radio"
              name="bypass-mode"
              checked={form.bypassMode === "named"}
              onChange={() => {
                setForm((prev) => ({ ...prev, bypassMode: "named" }));
              }}
            />
            <span>Bypass on specific cookie names (bypass_cookies)</span>
          </label>

          {form.bypassMode === "named" && (
            <label className="block space-y-1 pt-1">
              <span className="text-xs font-medium text-jul-text">
                Cookie names (comma-separated, matched case-insensitively)
              </span>
              <input
                type="text"
                value={form.bypassCookiesInput}
                aria-invalid={validation.errors.some(
                  (e) =>
                    e.includes("Cookie name") ||
                    e.includes("cookie name") ||
                    e.includes("bypass_cookies"),
                )}
                placeholder="session_id, __Host-auth, logged_in"
                onChange={(e) => {
                  setForm((prev) => ({ ...prev, bypassCookiesInput: e.target.value }));
                }}
                className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
              <span className="text-xs text-jul-muted">
                Up to 32 RFC 6265 cookie names. Cookie values are never inspected, logged, or cached.
              </span>
            </label>
          )}
        </fieldset>

        {validation.warnings.length > 0 && (
          <div
            role="status"
            aria-label="Cache policy warnings"
            className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3"
          >
            {validation.warnings.map((wn, i) => (
              <p key={`cp-warn-${String(i)}`} className="text-xs text-jul-warning">
                {wn}
              </p>
            ))}
          </div>
        )}

        {validation.errors.length > 0 && (
          <div
            role="alert"
            aria-label="Cache policy errors"
            className="space-y-1 rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3"
          >
            {validation.errors.map((errMsg, i) => (
              <p key={`cp-err-${String(i)}`} className="text-xs text-jul-danger">
                {errMsg}
              </p>
            ))}
          </div>
        )}
      </div>
    </Drawer>
  );
}
