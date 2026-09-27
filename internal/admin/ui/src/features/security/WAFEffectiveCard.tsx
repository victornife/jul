/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { SecurityProjection, WAFEffectivePolicy, WAFPolicySummary } from "@/api/client.ts";
import { formatBytes } from "@/lib/formatBytes.ts";
import { plural, ruleSources, servingDiffers } from "@/lib/wafEffective.ts";

function PolicySummary({ p }: { readonly p: WAFPolicySummary }) {
  return (
    <dl className="grid grid-cols-[10rem_1fr] gap-x-3 gap-y-1 text-xs">
      <dt className="text-jul-muted">Mode</dt>
      <dd>
        <span
          className={`rounded-full px-2 py-0.5 font-medium ${
            p.mode === "detect"
              ? "bg-jul-warning/15 text-jul-warning"
              : "bg-jul-success/15 text-jul-success"
          }`}
        >
          {p.mode === "detect" ? "detect only — not blocking" : "block"}
        </span>
        {p.mode !== "detect" && (
          <span className="ml-2 text-jul-muted">
            status {p.block_status}
            {p.crs_enabled ? " for non-CRS rules; CRS anomaly blocks use 403" : ""}
          </span>
        )}
      </dd>
      <dt className="text-jul-muted">Core Rule Set</dt>
      <dd>
        {p.crs_enabled
          ? `OWASP CRS ${p.crs_version || "(version not reported)"} · paranoia ${String(p.paranoia ?? 1)}${p.paranoia_default ? " (CRS default)" : ""}`
          : "not loaded"}
      </dd>
      <dt className="text-jul-muted">Request body</dt>
      <dd>
        {p.request_body_limit_bytes !== undefined
          ? `inspected up to ${formatBytes(p.request_body_limit_bytes)}`
          : "limit not reported"}
      </dd>
      <dt className="text-jul-muted">Response body</dt>
      <dd>
        {p.response_body_inspection
          ? "inspected (limit set by the engine and rule directives; not reported)"
          : "not inspected"}
      </dd>
      <dt className="text-jul-muted">Compiled rules</dt>
      <dd>{ruleSources(p)}</dd>
      <dt className="text-jul-muted">Rule sources</dt>
      <dd className="space-y-0.5">
        <div>
          {plural(p.rule_files_configured, "rule file")} configured
          {p.inline_rules ? " · inline rules" : ""}
        </div>
        {p.external_files > 0 ? (
          <div>
            {plural(p.external_files, "file")} read at compile · content{" "}
            <code className="font-mono" title={p.external_digest}>
              {p.external_digest?.slice(0, 19)}…
            </code>
          </div>
        ) : (
          <div className="text-jul-muted">no file read from disk</div>
        )}
      </dd>
    </dl>
  );
}

function routeLabel(o: NonNullable<WAFEffectivePolicy["overrides"]>[number]): string {
  const host = o.server_names && o.server_names.length > 0 ? ` ${o.server_names.join(",")}` : "";
  return `${o.listen}${host} ${o.match_type ?? "prefix"} ${o.path ?? "/"}`;
}

// WAFEffectiveCard shows the WAF policy the serving generation enforces
// (#440). It is read-only: configuration is edited above, and a candidate
// that fails to compile never changes what is shown here.
export function WAFEffectiveCard({
  data,
  effective,
}: {
  readonly data: SecurityProjection;
  readonly effective: WAFEffectivePolicy | undefined;
}) {
  if (!effective) return null;
  const e = effective;
  const compiledAt = e.compiled_at ? new Date(e.compiled_at).toLocaleString() : undefined;
  return (
    <section
      aria-labelledby="waf-effective-heading"
      className="rounded-lg border border-jul-border bg-jul-surface p-4"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id="waf-effective-heading" className="text-sm font-semibold">
          Serving WAF policy
        </h2>
        <span className="text-xs text-jul-muted">
          {e.generation > 0
            ? `generation ${String(e.generation)}${compiledAt ? ` · compiled ${compiledAt}` : ""}`
            : "no serving generation yet"}
        </span>
      </div>
      <p className="mt-1 text-xs text-jul-muted">
        What the running server enforces now, taken from the compiled engines. Configuration edits
        apply only after a successful reload; a rule set that fails to compile leaves this
        unchanged.
      </p>
      <div className="mt-3 space-y-3 text-xs">
        <div>
          {e.compiled ? (
            <span>
              Engine: Coraza{e.engine_version ? ` ${e.engine_version}` : ""} · embedded{" "}
              {e.embedded_crs_version
                ? `OWASP CRS ${e.embedded_crs_version}`
                : "CRS version not reported"}{" "}
              <span className="text-jul-muted">(pinned in this build; never updated online)</span>
            </span>
          ) : (
            <span className="text-jul-warning">
              This build has no WAF engine (the <code>waf</code> tag): no WAF policy is enforced.
            </span>
          )}
        </div>
        {e.generation > 0 && (
          <div>
            Routes: {e.inheriting_routes} inheriting the global policy · {e.override_routes} with
            their own policy · {e.unprotected_routes} not inspected
            {e.disabled_override_routes > 0
              ? ` (${String(e.disabled_override_routes)} turned off by an override)`
              : ""}
          </div>
        )}
        {servingDiffers(data, e) && (
          <p role="status" className="text-jul-warning">
            The saved global configuration differs from the serving policy — a reload is pending or
            the last one did not apply.
          </p>
        )}
        {e.global ? (
          <div className="space-y-1">
            <h3 className="font-semibold">Global policy</h3>
            <PolicySummary p={e.global} />
          </div>
        ) : (
          e.generation > 0 && (
            <div className="text-jul-muted">
              {e.global_enabled
                ? "Global policy enabled, but every route overrides it."
                : "Global policy disabled."}
            </div>
          )
        )}
        {(e.overrides ?? []).map((o, i) => (
          <div key={`waf-eff-${String(i)}`} className="space-y-1 border-t border-jul-border pt-2">
            <h3 className="font-mono font-semibold">{routeLabel(o)}</h3>
            {o.policy ? (
              <PolicySummary p={o.policy} />
            ) : (
              <div className="text-jul-muted">WAF turned off for this route by its override.</div>
            )}
          </div>
        ))}
      </div>
    </section>
  );
}
