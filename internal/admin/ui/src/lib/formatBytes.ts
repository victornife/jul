/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * formatBytes renders a byte count using binary (1024-based) units, matching
 * the repository's normal unit convention (KiB/MiB/GiB, not decimal KB/MB/GB).
 */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = n;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = unit === 0 ? 0 : 1;
  return `${value.toFixed(digits)} ${units[unit] ?? "B"}`;
}

/** formatBytesPerSec is formatBytes with a "/s" bandwidth suffix. */
export function formatBytesPerSec(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "—";
  return `${formatBytes(n)}/s`;
}

/**
 * formatCores renders a CPU-cores-used value (#431 §10): explicitly "N
 * cores", never a percentage with an ambiguous denominator.
 */
export function formatCores(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "—";
  return `${n.toFixed(2)} cores`;
}
