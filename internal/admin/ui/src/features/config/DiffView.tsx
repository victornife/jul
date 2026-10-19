/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { ConfigDiff, SharingPlanReport } from "@/api/client.ts";
import {
  formatAudienceDeltaClassLabel,
  formatSharingModeLabel,
} from "@/lib/sharingIntent.ts";

function Section({
  title,
  entries,
  tone,
}: {
  readonly title: string;
  readonly entries: NonNullable<ConfigDiff["additions"]>;
  readonly tone: "add" | "remove" | "modify";
}) {
  if (entries.length === 0) return null;
  const toneClass =
    tone === "add"
      ? "text-jul-success"
      : tone === "remove"
        ? "text-jul-danger"
        : "text-jul-warning";
  const sign = tone === "add" ? "+" : tone === "remove" ? "−" : "~";
  return (
    <div className="space-y-1">
      <h4 className={`text-xs font-semibold uppercase tracking-wider ${toneClass}`}>
        {title} ({entries.length})
      </h4>
      <ul className="space-y-1">
        {entries.map((e, i) => (
          <li
            key={`${e.kind}-${e.name ?? String(i)}`}
            className="flex gap-2 font-mono text-xs text-jul-text"
          >
            <span className={`shrink-0 ${toneClass}`}>{sign}</span>
            <span className="text-jul-muted">{e.kind}</span>
            {e.name && <span>{e.name}</span>}
            {e.detail && <span className="text-jul-muted">— {e.detail}</span>}
            {(e.before ?? e.after) && (
              <span className="text-jul-muted">
                {e.before ?? "∅"} → {e.after ?? "∅"}
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** Renders a structured config diff (summary, warnings, and grouped changes). */
export function DiffView({ diff }: { readonly diff: ConfigDiff }) {
  const additions = diff.additions ?? [];
  const removals = diff.removals ?? [];
  const modifications = diff.modifications ?? [];
  const warnings = diff.warnings ?? [];
  const empty =
    additions.length === 0 && removals.length === 0 && modifications.length === 0;

  return (
    <div className="space-y-3 rounded-md border border-jul-border bg-jul-bg p-4">
      <p className="text-sm text-jul-text">{diff.summary}</p>
      {diff.affected && diff.affected.length > 0 && (
        <p className="text-xs text-jul-muted">
          Affected: {diff.affected.join(", ")}
        </p>
      )}
      {warnings.length > 0 && (
        <ul className="space-y-1">
          {warnings.map((w, i) => (
            <li key={`warn-${String(i)}`} className="text-xs text-jul-warning">
              ⚠ {w}
            </li>
          ))}
        </ul>
      )}
      {empty ? (
        <p className="text-xs text-jul-muted">No structural changes detected.</p>
      ) : (
        <div className="space-y-3">
          <Section title="Added" entries={additions} tone="add" />
          <Section title="Removed" entries={removals} tone="remove" />
          <Section title="Modified" entries={modifications} tone="modify" />
        </div>
      )}
      {diff.sharing && <SharingPlanSummary report={diff.sharing} />}
    </div>
  );
}

function SharingPlanSummary({ report }: { readonly report: SharingPlanReport }) {
  const entries = report.entries;
  const findings = report.intent_findings ?? [];
  const fileEntries = report.file_backed_entries ?? [];
  const hasContent =
    entries.length > 0 ||
    findings.length > 0 ||
    Boolean(report.file_backed_inputs_notice) ||
    Boolean(report.provider_checklist_notice) ||
    Boolean(report.postcondition);

  if (!hasContent) return null;

  return (
    <section
      aria-label="Sharing and audience assessment"
      className="space-y-2 border-t border-jul-border pt-3"
    >
      <h4 className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
        Sharing &amp; audience assessment
      </h4>

      {entries.length > 0 && (
        <ul className="space-y-1.5 text-xs">
          {entries.map((entry, idx) => {
            const tone =
              entry.class === "widened" || entry.class === "uncertain" || entry.declared_mode_widened
                ? "text-jul-warning"
                : entry.class === "narrowed"
                  ? "text-jul-accent"
                  : "text-jul-muted";
            return (
              <li
                key={`share-entry-${entry.display_label}-${String(idx)}`}
                className="rounded border border-jul-border/60 bg-jul-surface/60 px-2.5 py-1.5"
              >
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <span className="font-mono font-medium text-jul-text">{entry.display_label}</span>
                  <span className={`font-medium ${tone}`}>
                    {formatAudienceDeltaClassLabel(entry.class)}
                  </span>
                </div>
                {entry.declared_mode_before !== entry.declared_mode_after && (
                  <p className="mt-0.5 text-jul-muted">
                    Declared intent: {formatSharingModeLabel(entry.declared_mode_before)} →{" "}
                    {formatSharingModeLabel(entry.declared_mode_after)}
                  </p>
                )}
                {entry.reasons && entry.reasons.length > 0 && (
                  <p className="mt-0.5 text-jul-muted">{entry.reasons.join("; ")}</p>
                )}
              </li>
            );
          })}
        </ul>
      )}

      {findings.length > 0 && (
        <div className="space-y-1 rounded border border-jul-warning/40 bg-jul-warning/10 p-2.5 text-xs">
          <p className="font-semibold text-jul-warning">Sharing intent findings</p>
          <ul className="list-disc space-y-1 pl-4 text-jul-text">
            {findings.map((f, i) => (
              <li key={`${f.code}-${String(i)}`}>
                <span className="font-mono text-jul-warning">{f.code}</span>: {f.message}
              </li>
            ))}
          </ul>
        </div>
      )}

      {report.file_backed_inputs_notice && (
        <div className="rounded border border-jul-border bg-jul-surface p-2.5 text-xs text-jul-muted">
          <p>{report.file_backed_inputs_notice}</p>
          {fileEntries.length > 0 && (
            <p className="mt-1">
              Entries with file-backed inputs ({report.file_backed_entry_count}):{" "}
              <span className="font-mono text-jul-text">
                {fileEntries.map((e) => e.display_label).join(", ")}
              </span>
              {report.file_backed_entries_truncated ? " (and more)" : ""}
            </p>
          )}
        </div>
      )}

      {report.provider_checklist_notice && (
        <p className="text-xs text-jul-warning">{report.provider_checklist_notice}</p>
      )}

      {report.postcondition && (
        <p className="text-xs text-jul-muted">
          Postcondition:{" "}
          {report.postcondition.status === "available" ? (
            <span className="font-mono text-jul-text">
              available ({report.postcondition.key_id})
            </span>
          ) : (
            <span>
              unavailable ({report.postcondition.unavailable_reason ?? "restricted"})
            </span>
          )}
        </p>
      )}
    </section>
  );
}
