/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { usePermission } from "@/auth/usePermission.ts";

// PERMISSION_LABELS maps a permission string to a short, operator-facing verb
// phrase so the "why unavailable" note reads naturally. Unknown permissions
// fall back to the raw string.
const PERMISSION_LABELS: Readonly<Record<string, string>> = {
  "config:apply": "apply configuration changes",
  "config:write": "edit configuration",
  "config:raw": "read the raw configuration",
  "history:rollback": "roll back to a previous configuration",
  "plugins:upload": "upload plugin modules",
  "cache:purge": "purge the cache",
  "reload:trigger": "trigger a reload",
  "audit:export": "export the audit log",
  "admin:manage": "manage admin settings",
};

/** permissionLabel returns a human phrase for a permission string. */
function permissionLabel(permission: string): string {
  return PERMISSION_LABELS[permission] ?? permission;
}

/**
 * ForbiddenAction renders a compact, accessible explanation of why an action is
 * unavailable to the current identity (P3-03 §33). It renders nothing when the
 * identity is unknown (fail open) or already holds the permission, so callers
 * can drop it beside a gated control unconditionally:
 *
 *   <button disabled={!has("config:apply")}>Apply</button>
 *   <ForbiddenAction permission="config:apply" />
 */
export function ForbiddenAction({
  permission,
  className,
}: {
  readonly permission: string;
  readonly className?: string;
}) {
  const { has, ready, identity } = usePermission();
  if (!ready || has(permission)) return null;
  return (
    <p
      role="note"
      className={`mt-1 flex items-start gap-1 text-xs text-jul-muted ${className ?? ""}`}
    >
      <span aria-hidden>🔒</span>
      <span>
        Requires the <span className="font-mono">{permission}</span> permission to{" "}
        {permissionLabel(permission)}. Your role{" "}
        <span className="font-medium">{identity?.role ?? "current"}</span> does not grant it.
      </span>
    </p>
  );
}
