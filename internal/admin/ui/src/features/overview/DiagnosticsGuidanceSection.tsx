/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Link } from "react-router-dom";
import type { StatsSnapshot } from "@/api/client.ts";
import { usePermission } from "@/auth/usePermission.ts";
import {
  buildGuidance,
  profileAccess,
  profileCommand,
  PROFILE_COST,
  type GuidanceHistory,
  type GuidanceItem,
  type ProfileAccess,
  type ProfileKind,
  type Signal,
} from "@/lib/diagnostics.ts";

const SIGNAL_TEXT: Record<Signal, string> = {
  elevated: "Elevated now",
  normal: "Within hints",
  unknown: "Not judged",
};

const SIGNAL_CLASS: Record<Signal, string> = {
  elevated: "bg-jul-warning/20 text-jul-warning",
  normal: "bg-jul-border text-jul-muted",
  unknown: "bg-jul-border text-jul-muted",
};

const PROFILE_LABEL: Record<ProfileKind, string> = {
  cpu: "CPU profile",
  heap: "heap profile",
  goroutine: "goroutine profile",
};

function ProfileAction({
  kind,
  access,
}: {
  readonly kind: ProfileKind;
  readonly access: ProfileAccess;
}) {
  const [copied, setCopied] = useState(false);
  if (access === "disabled") {
    return (
      <p className="text-xs text-jul-muted">
        Profiling is turned off on this server (<code>[admin] pprof = false</code>).
      </p>
    );
  }
  if (access === "denied") {
    return (
      <p className="text-xs text-jul-muted">
        Capturing a {PROFILE_LABEL[kind]} requires the <code>admin:manage</code> permission, which
        your role does not have.
      </p>
    );
  }
  if (access === "unknown") {
    return (
      <p className="text-xs text-jul-muted">
        Profiling availability is not known yet, so no command is shown.
      </p>
    );
  }
  const command = profileCommand(kind, window.location.origin);
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-medium">
          Capture a {PROFILE_LABEL[kind]} (run it yourself)
        </span>
        <button
          type="button"
          className="rounded-md border border-jul-border px-2 py-0.5 text-xs hover:bg-jul-bg"
          aria-label={`Copy the ${PROFILE_LABEL[kind]} command`}
          onClick={() => {
            navigator.clipboard.writeText(command).then(
              () => {
                setCopied(true);
              },
              () => {
                // Clipboard refused; the command stays selectable below.
              },
            );
          }}
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <pre className="overflow-x-auto rounded bg-jul-bg p-2 font-mono text-[11px]">{command}</pre>
      <p className="text-xs text-jul-muted">
        {PROFILE_COST[kind]} Set <code>JUL_ADMIN_TOKEN</code> to a credential with{" "}
        <code>admin:manage</code>; the Console never inserts one. Profiles can contain sensitive
        process and request data — review before sharing.
      </p>
    </div>
  );
}

function GuidanceCard({
  item,
  access,
}: {
  readonly item: GuidanceItem;
  readonly access: ProfileAccess;
}) {
  return (
    <li
      className={`rounded-lg border p-4 ${
        item.signal === "elevated"
          ? "border-jul-warning/50 bg-jul-warning/5"
          : "border-jul-border bg-jul-surface"
      }`}
      aria-label={`${item.title} guidance`}
    >
      <div className="flex items-start justify-between gap-2">
        <h3 className="text-sm font-semibold">{item.title}</h3>
        <span
          className={`rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${SIGNAL_CLASS[item.signal]}`}
        >
          {SIGNAL_TEXT[item.signal]}
        </span>
      </div>
      <p className="mt-1 text-xs text-jul-text">{item.observation}</p>
      <ul className="mt-2 list-disc space-y-0.5 pl-4 text-xs text-jul-muted">
        {item.steps.map((s) => (
          <li key={s}>{s}</li>
        ))}
      </ul>
      {item.profile && (
        <div className="mt-3">
          <ProfileAction kind={item.profile} access={access} />
        </div>
      )}
    </li>
  );
}

// DiagnosticsGuidanceSection connects the resource and storage readings above
// to Jul's existing diagnostics (#445): gated pprof, jul doctor, support
// bundles and Operations. It collects nothing and diagnoses nothing.
export function DiagnosticsGuidanceSection({
  stats,
  history,
  pprofEnabled,
}: {
  readonly stats: StatsSnapshot;
  readonly history?: GuidanceHistory;
  readonly pprofEnabled: boolean | undefined;
}) {
  const perms = usePermission();
  const access = profileAccess(pprofEnabled, perms.ready, perms.has("admin:manage"));
  const items = buildGuidance(stats, history);
  return (
    <section className="space-y-4" aria-labelledby="diagnostics-guidance-heading">
      <div className="space-y-1">
        <h2 id="diagnostics-guidance-heading" className="text-sm font-semibold text-jul-muted">
          Diagnostics guidance
        </h2>
        <p className="max-w-3xl text-xs text-jul-muted">
          What to collect next, based on the readings above. This is guidance, not a diagnosis: a
          reading can have several causes, and nothing here is collected automatically.
        </p>
      </div>
      <ul className="grid gap-4 lg:grid-cols-2">
        {items.map((item) => (
          <GuidanceCard key={item.id} item={item} access={access} />
        ))}
      </ul>
      <div className="rounded-lg border border-jul-border bg-jul-surface p-4 text-xs">
        <h3 className="text-sm font-semibold">For any problem</h3>
        <ul className="mt-2 list-disc space-y-1 pl-4 text-jul-muted">
          <li>
            On the host, <code>jul doctor --config &lt;file&gt;</code> checks configuration and
            deployment prerequisites (read-only).
          </li>
          <li>
            <code>jul support-bundle --config &lt;file&gt;</code> writes a bounded, redacted archive
            for offline review; add <code>--include-logs</code> for a bounded access-log tail.
            Review it before sharing.
          </li>
          <li>
            <Link to="/operations" className="text-jul-accent underline">
              Operations
            </Link>{" "}
            shows recent events, logs and the timeline.
          </li>
        </ul>
      </div>
    </section>
  );
}
