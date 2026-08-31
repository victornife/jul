/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

export type AccessLogFormat = "text" | "json";
export type AccessLogSink = "stdout" | "file" | "syslog";

export interface AccessLogDraft {
  enabled: boolean;
  sinks: AccessLogSink[];
  file: string;
  format: AccessLogFormat;
  rotateMaxMB: number;
  rotateKeep: number;
}

export function defaultAccessLogDraft(): AccessLogDraft {
  return {
    enabled: true,
    sinks: ["stdout"],
    file: "",
    format: "text",
    rotateMaxMB: 100,
    rotateKeep: 3,
  };
}

function tomlString(value: string): string {
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

export function accessLogWarnings(draft: AccessLogDraft): string[] {
  const warnings: string[] = [];
  if (draft.enabled && draft.sinks.length === 0) {
    warnings.push(
      "Access logging is enabled but no sink is selected; the server rejects this state. Select a sink or disable access logging.",
    );
  }
  if (draft.sinks.includes("file") && !draft.file.trim()) {
    warnings.push("The file sink requires a path, even while access logging is disabled.");
  }
  return warnings;
}

/** Generates the complete [observability.access_log] block. */
export function generateAccessLogToml(draft: AccessLogDraft): string {
  const lines = ["[observability.access_log]", `enabled = ${draft.enabled ? "true" : "false"}`];
  lines.push(`sinks = [${draft.sinks.map(tomlString).join(", ")}]`);
  if (draft.sinks.includes("file") && draft.file.trim()) {
    lines.push(`file = ${tomlString(draft.file.trim())}`);
  }
  lines.push(`format = ${tomlString(draft.format)}`);
  lines.push(`rotate_max_mb = ${String(Math.max(0, Math.trunc(draft.rotateMaxMB)))}`);
  lines.push(`rotate_keep = ${String(Math.max(0, Math.trunc(draft.rotateKeep)))}`);
  return lines.join("\n");
}
