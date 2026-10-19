/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { LocationSharingState, SharingPlanReport } from "@/api/client.ts";

/**
 * Provisional sharing-mode labels from docs/specs/application-sharing.md §2.
 */
export function formatSharingModeLabel(mode: string | undefined): string {
  switch (mode) {
    case "preview":
      return "Local preview";
    case "private_network":
      return "Private network";
    case "restricted":
      return "Restricted";
    case "public":
      return "Public";
    case "unknown":
      return "Unknown (resolved when loaded)";
    case "not_declared":
    case undefined:
    case "":
      return "Not declared";
    default:
      return mode;
  }
}

/**
 * Formats an operator-declared external_enforcer label with the mandatory SD-12
 * qualification ("declared by the operator, not verified by Jul").
 */
export function formatExternalEnforcerNote(enforcer: string | undefined): string {
  const trimmed = (enforcer ?? "").trim();
  if (!trimmed) return "";
  return `${trimmed} (declared by the operator, not verified by Jul)`;
}

/**
 * Summarizes a location's declared sharing state for compact read-only display.
 */
export function formatLocationSharingSummary(sharing: LocationSharingState | undefined): string {
  if (!sharing) return "Not declared";
  const parts = [formatSharingModeLabel(sharing.mode)];
  if (sharing.audience_label) {
    parts.push(`"${sharing.audience_label}"`);
  }
  if (sharing.external_enforcer) {
    parts.push(formatExternalEnforcerNote(sharing.external_enforcer));
  }
  return parts.join(" · ");
}

/**
 * Human-readable label for a §9.5 audience-delta class.
 */
export function formatAudienceDeltaClassLabel(cls: string): string {
  switch (cls) {
    case "widened":
      return "Widened audience";
    case "narrowed":
      return "Narrowed audience";
    case "uncertain":
      return "Uncertain audience change";
    case "metadata_only":
      return "Declared intent metadata only";
    case "unchanged":
      return "Unchanged audience";
    default:
      return cls;
  }
}

export function sharingPlanRequiresAck(report: SharingPlanReport | undefined): boolean {
  return Boolean(report?.requires_acknowledgement);
}

export function sharingAckEntries(report: SharingPlanReport | undefined): string[] {
  if (!report || !report.requires_acknowledgement) return [];
  if (report.acknowledgement_entries && report.acknowledgement_entries.length > 0) {
    return report.acknowledgement_entries;
  }
  return report.entries
    .filter((e) => e.requires_acknowledgement)
    .map((e) => e.display_label);
}
