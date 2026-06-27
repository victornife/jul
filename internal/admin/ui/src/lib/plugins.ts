import type { PluginProjection, PluginDefPatch } from "@/api/client.ts";

// PluginDraft is the editable form state for a [plugins.NAME] declaration. It
// mirrors PluginDefPatch but keeps the capability lists and config map as the
// raw multi-line text the editor binds to, parsing them on save.
export interface PluginDraft {
  name: string;
  source: "path" | "inline";
  path: string;
  type: "middleware" | "handler";
  memoryLimit: string;
  timeout: string;
  kv: boolean;
  fetch: boolean;
  allowedHosts: string; // comma- or newline-separated
  config: string; // "key = value" lines
}

// emptyPluginDraft seeds the create form: a new plugin must reference a module
// path (inline is only offered when editing an existing inline plugin).
export function emptyPluginDraft(): PluginDraft {
  return {
    name: "",
    source: "path",
    path: "",
    type: "middleware",
    memoryLimit: "",
    timeout: "",
    kv: false,
    fetch: false,
    allowedHosts: "",
    config: "",
  };
}

// seedPluginDraft fills the edit form from a projected plugin. Inline plugins
// keep the "inline" source so saving preserves their embedded bytes (which the
// console never transmits).
export function seedPluginDraft(p: PluginProjection): PluginDraft {
  const cfg = p.config ?? {};
  const configLines = Object.keys(cfg)
    .sort()
    .map((k) => `${k} = ${cfg[k] ?? ""}`)
    .join("\n");
  return {
    name: p.name,
    source: p.source === "inline" ? "inline" : "path",
    path: p.path ?? "",
    type: p.type === "handler" ? "handler" : "middleware",
    memoryLimit: p.memory_limit ?? "",
    timeout: p.timeout ?? "",
    kv: p.kv,
    fetch: p.fetch,
    allowedHosts: (p.allowed_hosts ?? []).join(", "),
    config: configLines,
  };
}

// parseList splits a comma- or newline-separated field into trimmed, non-empty
// entries.
export function parseList(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

// parseConfigMap parses "key = value" lines into a record, ignoring blank lines.
// A line without "=" maps the trimmed key to an empty string.
export function parseConfigMap(raw: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of raw.split("\n")) {
    const trimmed = line.trim();
    if (trimmed === "") continue;
    const eq = trimmed.indexOf("=");
    if (eq < 0) {
      out[trimmed] = "";
      continue;
    }
    const key = trimmed.slice(0, eq).trim();
    if (key === "") continue;
    out[key] = trimmed.slice(eq + 1).trim();
  }
  return out;
}

// pluginDraftToPatch builds the plugin_set payload from the draft, omitting empty
// optional fields so the request stays minimal (exactOptionalPropertyTypes:
// absent rather than undefined). The validated apply re-parse enforces the rest.
export function pluginDraftToPatch(draft: PluginDraft): PluginDefPatch {
  const patch: PluginDefPatch = { source: draft.source, type: draft.type };
  if (draft.source === "path" && draft.path.trim() !== "") {
    patch.path = draft.path.trim();
  }
  if (draft.memoryLimit.trim() !== "") patch.memory_limit = draft.memoryLimit.trim();
  if (draft.timeout.trim() !== "") patch.timeout = draft.timeout.trim();
  if (draft.kv) patch.kv = true;
  if (draft.fetch) patch.fetch = true;
  const hosts = parseList(draft.allowedHosts);
  if (hosts.length > 0) patch.allowed_hosts = hosts;
  const cfg = parseConfigMap(draft.config);
  if (Object.keys(cfg).length > 0) patch.config = cfg;
  return patch;
}

// pluginDraftWarnings returns blocking validation messages shown before save,
// mirroring the backend's near-side checks so the operator gets feedback without
// a round-trip.
export function pluginDraftWarnings(draft: PluginDraft, isNew: boolean): string[] {
  const out: string[] = [];
  if (isNew && draft.name.trim() === "") {
    out.push("A plugin name is required.");
  }
  if (draft.source === "path" && draft.path.trim() === "") {
    out.push("A module path is required (the console does not upload WASM bytes).");
  }
  if (draft.fetch && parseList(draft.allowedHosts).length === 0) {
    out.push("Outbound fetch requires at least one allowed host.");
  }
  return out;
}
