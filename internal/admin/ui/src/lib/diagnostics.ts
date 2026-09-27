/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { StatsSnapshot } from "@/api/client.ts";
import { formatBytes, formatCores } from "@/lib/formatBytes.ts";
import { STORAGE_CATEGORY_META, storageAttention, storageLabel } from "@/lib/storage.ts";

// Diagnostics guidance (#445): which existing, bounded diagnostic to reach for
// given what the resource readings show. Guidance never claims a cause — a
// reading has several possible explanations — and never collects anything.

export type Signal = "elevated" | "normal" | "unknown";
export type ProfileKind = "cpu" | "heap" | "goroutine";

export interface GuidanceItem {
  readonly id: "cpu" | "memory" | "goroutines" | "fds" | "storage";
  readonly title: string;
  readonly signal: Signal;
  readonly observation: string;
  readonly steps: readonly string[];
  readonly profile?: ProfileKind;
}

export interface GuidanceHistory {
  readonly rssBytes: readonly number[];
  readonly goroutines: readonly number[];
}

// UX hints only — not SLOs, alerts or paging conditions.
export const CPU_NEAR_LIMIT = 0.8;
export const FD_NEAR_LIMIT = 0.8;
export const GOROUTINE_HIGH = 10_000;

// rising compares the oldest and newest finite samples of the browser-local
// trend window (~2 minutes).
export function rising(series: readonly number[], factor: number, minDelta: number): boolean {
  const finite = series.filter((v) => Number.isFinite(v));
  if (finite.length < 2) return false;
  const first = finite[0] ?? 0;
  const last = finite[finite.length - 1] ?? 0;
  return last - first >= minDelta && last >= first * factor;
}

function cpuItem(s: StatsSnapshot): GuidanceItem {
  const steps = [
    "Compare with request and upstream load (Traffic and Capacity on this page, Operations → Logs) — CPU use usually follows load.",
    "Capture a 30-second CPU profile to see where time is spent.",
  ];
  if (s.cpuCores === undefined) {
    return {
      id: "cpu",
      title: "CPU",
      signal: "unknown",
      observation: "CPU use is not reported on this platform.",
      steps,
      profile: "cpu",
    };
  }
  const limit = s.goMaxProcs && s.goMaxProcs > 0 ? s.goMaxProcs : undefined;
  const near = limit !== undefined && s.cpuCores >= limit * CPU_NEAR_LIMIT;
  return {
    id: "cpu",
    title: "CPU",
    signal: limit === undefined ? "unknown" : near ? "elevated" : "normal",
    observation:
      limit === undefined
        ? `${formatCores(s.cpuCores)} used; the Go scheduler limit is not reported, so nearness cannot be judged.`
        : `${formatCores(s.cpuCores)} used of a Go scheduler limit of ${String(limit)} (GOMAXPROCS).`,
    steps,
    profile: "cpu",
  };
}

function memoryItem(s: StatsSnapshot, h: GuidanceHistory | undefined): GuidanceItem {
  if (s.rssBytes === undefined || s.goHeapAllocBytes === undefined) {
    return {
      id: "memory",
      title: "Memory",
      signal: "unknown",
      observation: "Resident memory or Go heap is not reported on this platform.",
      steps: ["Collect a support bundle and a heap profile if memory use looks high on the host."],
      profile: "heap",
    };
  }
  const growing = h !== undefined && rising(h.rssBytes, 1.25, 64 * 1024 * 1024);
  const heapShare = s.rssBytes > 0 ? s.goHeapAllocBytes / s.rssBytes : 1;
  const base = `Resident ${formatBytes(s.rssBytes)}, Go heap ${formatBytes(s.goHeapAllocBytes)}${growing ? "; resident memory rose over the last two minutes" : ""}.`;
  if (heapShare >= 0.5) {
    return {
      id: "memory",
      title: "Memory",
      signal: growing ? "elevated" : "normal",
      observation: `${base} Most resident memory is Go heap.`,
      steps: [
        "Capture a heap profile to see which allocations are live; compare two profiles taken minutes apart before concluding anything grows.",
        "Check cache occupancy under Capacity — a full memory tier is expected, bounded use.",
      ],
      profile: "heap",
    };
  }
  return {
    id: "memory",
    title: "Memory",
    signal: growing ? "elevated" : "normal",
    observation: `${base} Most resident memory is outside the Go heap, so a heap profile will not account for it — this does not indicate a Go heap leak.`,
    steps: [
      "Possible holders outside the heap: WASM plugin linear memory, goroutine stacks, memory-mapped files, and native or runtime state.",
      "A goroutine profile shows how many stacks are live; the Plugins panel shows which WASM modules are loaded.",
      "Collect a support bundle for broader context before changing limits.",
    ],
    profile: "goroutine",
  };
}

function goroutineItem(s: StatsSnapshot, h: GuidanceHistory | undefined): GuidanceItem {
  const steps = [
    "Capture a goroutine profile: it groups goroutines by stack, which shows what they are waiting on.",
    "Long-lived connections (WebSocket, SSE, gRPC streams, slow upstreams) each hold goroutines — check Capacity for connection and upstream pressure.",
  ];
  if (s.goroutines === undefined) {
    return {
      id: "goroutines",
      title: "Goroutines",
      signal: "unknown",
      observation: "Goroutine count is not reported.",
      steps,
      profile: "goroutine",
    };
  }
  const growing = h !== undefined && rising(h.goroutines, 1.5, 500);
  const high = s.goroutines >= GOROUTINE_HIGH;
  return {
    id: "goroutines",
    title: "Goroutines",
    signal: growing || high ? "elevated" : "normal",
    observation: `${s.goroutines.toLocaleString()} goroutines${growing ? ", rising over the last two minutes" : ""}. Growth under growing load is expected; growth at steady load is worth a profile.`,
    steps,
    profile: "goroutine",
  };
}

function fdItem(s: StatsSnapshot): GuidanceItem {
  const steps = [
    "Compare with Listener Connections and upstream pressure under Capacity: each client and upstream connection holds a descriptor.",
    "If the limit is the constraint, raise it for the service (LimitNOFILE) — see the deployment guide's resource limits.",
    "Run jul doctor on the host to check the deployment's prerequisites.",
  ];
  if (s.openFDs === undefined) {
    return {
      id: "fds",
      title: "File descriptors",
      signal: "unknown",
      observation: "Open descriptors are not reported on this platform.",
      steps,
    };
  }
  if (s.maxFDs === undefined || s.maxFDs <= 0) {
    return {
      id: "fds",
      title: "File descriptors",
      signal: "unknown",
      observation: `${s.openFDs.toLocaleString()} open; the per-process limit is not reported, so nearness cannot be judged.`,
      steps,
    };
  }
  const near = s.openFDs / s.maxFDs >= FD_NEAR_LIMIT;
  return {
    id: "fds",
    title: "File descriptors",
    signal: near ? "elevated" : "normal",
    observation: `${s.openFDs.toLocaleString()} open of a limit of ${s.maxFDs.toLocaleString()}.`,
    steps,
  };
}

function storageItem(s: StatsSnapshot): GuidanceItem | undefined {
  const storage = s.storage;
  if (!storage || storage.length === 0) return undefined;
  const attention = storage.filter(storageAttention);
  const failing = attention.filter((h) => h.writesFailing);
  const keys = [
    ...new Set(attention.map((h) => STORAGE_CATEGORY_META[h.category]?.configKey).filter(Boolean)),
  ];
  const steps = [
    ...(keys.length > 0
      ? [
          `Free space on that filesystem or move the location (${keys.join(", ")}). Jul never deletes logs or cache entries to recover space.`,
        ]
      : []),
    "A fast-filling access log is bounded by observability.access_log.rotate_max_mb × rotate_keep; the disk cache by cache.disk_max_size.",
    "Collect a support bundle (optionally with --include-logs) before cleaning up, if you need the record.",
  ];
  if (attention.length === 0) {
    return {
      id: "storage",
      title: "Storage",
      signal: storage.every((h) => h.state === "ok") ? "normal" : "unknown",
      observation: "No Jul-owned storage category is low on headroom or failing writes.",
      steps: steps.slice(-2),
    };
  }
  return {
    id: "storage",
    title: "Storage",
    signal: "elevated",
    observation:
      `${attention.map((h) => `${storageLabel(h.category)} (${h.writesFailing ? "writes failing" : `${h.state} headroom`})`).join(", ")}.` +
      (failing.length > 0
        ? " Failing writes mean data is already being lost."
        : " Writes still succeed; low headroom is an early hint, not a failure."),
    steps,
  };
}

const ORDER: Record<Signal, number> = { elevated: 0, unknown: 1, normal: 2 };

// buildGuidance returns one item per resource, elevated readings first.
export function buildGuidance(s: StatsSnapshot, h?: GuidanceHistory): GuidanceItem[] {
  const items = [cpuItem(s), memoryItem(s, h), goroutineItem(s, h), fdItem(s)];
  const st = storageItem(s);
  if (st) items.push(st);
  return items
    .map((item, i) => ({ item, i }))
    .sort((a, b) => ORDER[a.item.signal] - ORDER[b.item.signal] || a.i - b.i)
    .map(({ item }) => item);
}

export type ProfileAccess = "available" | "disabled" | "denied" | "unknown";

// profileAccess mirrors the server's gates for display only: the server still
// authorizes every profile request (admin.pprof, admin:manage, secure
// transport). Unknown capability or identity never shows a command.
export function profileAccess(
  pprofEnabled: boolean | undefined,
  identityReady: boolean,
  canManage: boolean,
): ProfileAccess {
  if (pprofEnabled === undefined) return "unknown";
  if (!pprofEnabled) return "disabled";
  if (!identityReady) return "unknown";
  return canManage ? "available" : "denied";
}

const PROFILE_PATH: Record<ProfileKind, string> = {
  cpu: "/debug/pprof/profile?seconds=30",
  heap: "/debug/pprof/heap",
  goroutine: "/debug/pprof/goroutine",
};

// profileCommand is the existing gated pprof endpoint as a shell command. The
// token is a placeholder the operator supplies; the Console never embeds its
// own credential.
export function profileCommand(kind: ProfileKind, origin: string): string {
  const file = `jul-${kind}.pprof`;
  return [
    `curl -fsS -H "Authorization: Bearer $JUL_ADMIN_TOKEN" -o ${file} \\`,
    `  "${origin}${PROFILE_PATH[kind]}"`,
    `go tool pprof -top ${file}`,
  ].join("\n");
}

export const PROFILE_COST: Record<ProfileKind, string> = {
  cpu: "Samples for 30 seconds with low overhead while it runs.",
  heap: "A brief snapshot of sampled allocations as of the last garbage collection; little overhead.",
  goroutine: "A brief snapshot; its cost grows with the number of goroutines.",
};
