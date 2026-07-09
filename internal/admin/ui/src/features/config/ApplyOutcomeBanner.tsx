/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { ApplyOutcome, ApplyOutcomeSeverity } from "@/lib/applyOutcome.ts";

/**
 * Renders a configuration-apply outcome (AUX-02) with a severity that matches
 * how live the change actually is: a full-live apply reads as success, an
 * in-flight reload as neutral-info, a partial subsystem failure as a warning
 * the operator must act on, and a restart-required apply as a blocking error
 * (nothing was saved). Consistent colour, wording, and ARIA role across the four
 * branches let an operator tell them apart at a glance instead of always seeing
 * a green "saved".
 */

const SEVERITY_STYLES: Record<ApplyOutcomeSeverity, { box: string; title: string }> = {
  success: {
    box: "border-jul-success/40 bg-jul-success/10",
    title: "text-jul-success",
  },
  info: {
    box: "border-jul-accent/40 bg-jul-accent/10",
    title: "text-jul-accent",
  },
  warning: {
    box: "border-jul-warning/40 bg-jul-warning/10",
    title: "text-jul-warning",
  },
  blocked: {
    box: "border-jul-danger/40 bg-jul-danger/10",
    title: "text-jul-danger",
  },
};

export function ApplyOutcomeBanner({
  outcome,
  capabilities,
}: {
  readonly outcome: ApplyOutcome;
  /** Optional capability tally shown for accepted (non-blocking) outcomes. */
  readonly capabilities?: { readonly active: number; readonly total: number };
}) {
  const styles = SEVERITY_STYLES[outcome.severity];
  // A warning or blocking outcome is an assertive, act-now signal; a success or
  // in-flight update is polite status. Match the ARIA role to the severity so
  // assistive tech announces degraded/blocked applies without interrupting a
  // routine success.
  const role = outcome.severity === "warning" || outcome.severity === "blocked" ? "alert" : "status";
  return (
    <div
      role={role}
      data-outcome={outcome.kind}
      className={`rounded-md border p-3 text-sm text-jul-text ${styles.box}`}
    >
      <p className={`font-medium ${styles.title}`}>{outcome.title}</p>
      <p className="mt-0.5 text-xs text-jul-muted">{outcome.message}</p>
      {outcome.failures.length > 0 && (
        <ul className="mt-2 space-y-1">
          {outcome.failures.map((f, i) => (
            <li key={`fail-${String(i)}`} className="text-xs text-jul-text">
              <span className="font-semibold text-jul-warning">{f.name}</span>
              {f.detail ? <span className="text-jul-muted"> — {f.detail}</span> : null}
            </li>
          ))}
        </ul>
      )}
      {!outcome.blocking && capabilities && (
        <p className="mt-1 text-xs text-jul-muted">
          {String(capabilities.active)} of {String(capabilities.total)} capabilities are active in
          the saved configuration.
        </p>
      )}
    </div>
  );
}
