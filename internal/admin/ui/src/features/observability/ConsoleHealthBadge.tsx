/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { fetchConsoleHealth } from "@/api/client.ts";

function tone(status: string): string {
  switch (status) {
    case "ok":
      return "bg-jul-success/15 text-jul-success";
    case "degraded":
      return "bg-jul-warning/15 text-jul-warning";
    case "…":
      return "bg-jul-muted/15 text-jul-muted";
    default:
      return "bg-jul-danger/15 text-jul-danger";
  }
}

export interface ConsoleHealthBadgeProps {
  readonly compact?: boolean;
}

/**
 * ConsoleHealthBadge is the subtle footer indicator required by Milestone 5.7.
 * It polls the Console's own health endpoint and links to the Operations view
 * for detail. It stays quiet (a small dot + word) so it never competes with the
 * primary content.
 */
export function ConsoleHealthBadge({ compact = false }: ConsoleHealthBadgeProps) {
  const { data, isError } = useQuery({
    queryKey: ["console-health-badge"],
    queryFn: fetchConsoleHealth,
    refetchInterval: 30_000,
  });

  const status = isError ? "error" : (data?.status ?? "…");
  const p95 = data?.latency_p95;

  if (compact) {
    return (
      <Link
        to="/operations"
        title={`Console ${status}${p95 !== undefined && p95 > 0 ? ` — ${p95.toFixed(0)}ms` : ""}`}
        className={`flex items-center justify-center rounded-full p-1 text-xs transition-colors ${tone(status)}`}
        aria-label={`Console ${status}`}
      >
        <span className="h-2 w-2 rounded-full bg-current" aria-hidden="true" />
      </Link>
    );
  }

  return (
    <Link
      to="/operations"
      title="Console health — open Operations"
      className={`flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ${tone(status)}`}
    >
      <span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true" />
      <span>console {status}</span>
      {p95 !== undefined && p95 > 0 && (
        <span className="opacity-70">{`${p95.toFixed(0)}ms`}</span>
      )}
    </Link>
  );
}
