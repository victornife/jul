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

export interface LocationRateLimitEditorProps {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
  readonly onClose: () => void;
}

/** Edit a per-location rate limit in place. The editor seeds from the existing
 * rate_limit_detail when present, or uses safe defaults (100 req/s, burst 100,
 * key ip) when creating a new override. */
export function LocationRateLimitEditor({
  route,
  loc,
  onClose,
}: LocationRateLimitEditorProps) {
  const { run: runPatch, error, busy } = useRunPatch();
  const { has } = usePermission();
  const canWrite = has("config:write");

  const seed = loc.rate_limit_detail;
  const [enabled, setEnabled] = useState(seed?.enabled ?? false);
  const [rateRaw, setRateRaw] = useState(
    seed?.rate ? String(seed.rate) : "100",
  );
  const [burstRaw, setBurstRaw] = useState(
    seed?.burst ? String(seed.burst) : "100",
  );
  const [key, setKey] = useState(seed?.key ?? "ip");

  const rate = Number.parseInt(rateRaw, 10);
  const burst = Number.parseInt(burstRaw, 10);

  const warnings: string[] = [];
  if (Number.isNaN(rate) || rate <= 0) {
    warnings.push("Rate must be a positive number.");
  }
  if (Number.isNaN(burst) || burst <= 0) {
    warnings.push("Burst must be a positive number.");
  } else if (!Number.isNaN(rate) && burst < rate) {
    warnings.push("Burst must be greater than or equal to rate.");
  }
  if (!key.match(/^(ip|header:[^\s]+|jwt:[^\s]+)$/)) {
    warnings.push('Key must be "ip", "header:<Name>" or "jwt:<claim>".');
  }

  function save(): void {
    runPatch({
      op: "route_set_rate_limit",
      listen: route.listen,
      server_names: route.server_names ?? [],
      match_type: loc.type,
      path: loc.match,
      rate_limit: {
        enabled,
        rate,
        burst,
        key,
      },
    });
  }

  return (
    <Drawer
      title="Rate limit"
      subtitle={`${loc.type} ${loc.match} on ${route.listen}`}
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex items-center justify-between gap-3">
            {error && <span className="text-xs text-jul-danger">{error}</span>}
            <button
              type="button"
              disabled={
                busy || warnings.length > 0 || (!enabled && !seed?.enabled) || !canWrite
              }
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
        <label className="flex items-center gap-2">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => {
              setEnabled(e.target.checked);
            }}
            className="rounded border-jul-border"
          />
          <span className="text-sm text-jul-text">Enable rate limiting</span>
        </label>

        {enabled && (
          <>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">
                Rate (requests / second)
              </span>
              <input
                type="number"
                min={1}
                value={rateRaw}
                onChange={(e) => {
                  setRateRaw(e.target.value);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
            </label>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">
                Burst (max burst above rate)
              </span>
              <input
                type="number"
                min={1}
                value={burstRaw}
                onChange={(e) => {
                  setBurstRaw(e.target.value);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
            </label>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Key</span>
              <input
                type="text"
                value={key}
                placeholder="ip"
                onChange={(e) => {
                  setKey(e.target.value);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
              <span className="text-xs text-jul-muted">
                ip | header:&lt;Name&gt; | jwt:&lt;claim&gt;
              </span>
            </label>
          </>
        )}

        {warnings.map((wn, i) => (
          <p key={`rl-${String(i)}`} className="text-xs text-jul-danger">
            {wn}
          </p>
        ))}
      </div>
    </Drawer>
  );
}
