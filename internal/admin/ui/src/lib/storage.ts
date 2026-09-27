/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { StorageHeadroom } from "@/api/client.ts";

// Jul-owned storage categories (#437): display name, the configuration key
// that places it, and the Console panel where it is configured. The key is
// shown so an operator knows what to change — the path itself is never sent.
export const STORAGE_CATEGORY_META: Record<
  string,
  {
    readonly label: string;
    readonly configKey: string;
    readonly route: string;
    readonly where: string;
  }
> = {
  cache: { label: "Disk cache", configKey: "cache.disk_path", route: "/traffic", where: "Traffic" },
  access_log: {
    label: "Access log",
    configKey: "observability.access_log.file",
    route: "/traffic",
    where: "Traffic",
  },
  audit_log: {
    label: "Audit log",
    configKey: "admin.audit_log_file",
    route: "/plugins",
    where: "Plugins → admin runtime settings",
  },
  config: { label: "Configuration file", configKey: "--config", route: "/config", where: "Config" },
  config_history: {
    label: "Configuration history",
    configKey: "admin.history_dir",
    route: "/history",
    where: "History",
  },
  plugin_upload: {
    label: "Plugin uploads",
    configKey: "admin.plugin_upload_dir",
    route: "/plugins",
    where: "Plugins → admin runtime settings",
  },
  acme: {
    label: "ACME certificates",
    configKey: "servers.tls.acme.cache_dir",
    route: "/tls",
    where: "TLS",
  },
};

export function storageLabel(category: string): string {
  return STORAGE_CATEGORY_META[category]?.label ?? category;
}

export const STORAGE_REASON_TEXT: Record<string, string> = {
  unsupported_platform: "this platform cannot report filesystem capacity",
  path_unresolved: "the configured location could not be resolved (dangling link)",
  permission_denied: "Jul cannot examine the configured location (permission denied)",
  stat_failed: "the filesystem capacity call failed",
};

// storageAttention reports whether a category needs operator attention: low
// or critical advisory headroom, or the owning subsystem's writes failing.
export function storageAttention(h: StorageHeadroom): boolean {
  return h.state === "low" || h.state === "critical" || h.writesFailing === true;
}

// storageSummary is the Overview summary-band reading: the worst condition
// across categories, with the affected categories named in the tooltip.
export function storageSummary(storage: StorageHeadroom[]): {
  value: string;
  tone: "ok" | "warn" | "down" | "idle";
  tooltip: string;
} {
  const names = (pred: (h: StorageHeadroom) => boolean) =>
    storage.filter(pred).map((h) => storageLabel(h.category));
  const failing = names((h) => h.writesFailing === true);
  if (failing.length > 0)
    return {
      value: "writes failing",
      tone: "down",
      tooltip: `Writes failing: ${failing.join(", ")}`,
    };
  const critical = names((h) => h.state === "critical");
  if (critical.length > 0)
    return {
      value: "critical",
      tone: "down",
      tooltip: `Critical headroom: ${critical.join(", ")}`,
    };
  const low = names((h) => h.state === "low");
  if (low.length > 0)
    return { value: "low", tone: "warn", tooltip: `Low headroom: ${low.join(", ")}` };
  const unknown = names((h) => h.state !== "ok");
  if (unknown.length === storage.length)
    return {
      value: "unknown",
      tone: "idle",
      tooltip: "Capacity unavailable for every storage category",
    };
  return {
    value: "ok",
    tone: "ok",
    tooltip:
      unknown.length > 0
        ? `Headroom ok; unavailable for ${unknown.join(", ")}`
        : "Headroom ok for every Jul-owned storage category",
  };
}
