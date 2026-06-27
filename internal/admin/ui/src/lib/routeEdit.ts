import type {
  LocationActionPatch,
  LocationMatchPatch,
  LocationProjection,
} from "@/api/client.ts";

// routeEdit holds the pure draft <-> patch mapping and validation for the
// in-place route editors added in Phase 4f: changing a route's match (type +
// path), switching its action, and renaming a server block's host names. Each
// piece is React-free so the round-trip and validation are directly unit
// testable, mirroring wafOverride.ts. The warnings functions return BLOCKING
// issues (an empty array means the patch is safe to send) so the editor can
// refuse a save the backend would reject.

// ── Match (type + path) ──────────────────────────────────────────────────────

export type MatchType = "exact" | "prefix" | "regex";

export interface MatchDraft {
  type: MatchType;
  path: string;
}

// normalizeMatchType mirrors the backend's normMatchType: an empty or unknown
// type is treated as "prefix" (the default).
export function normalizeMatchType(t: string): MatchType {
  return t === "exact" || t === "regex" ? t : "prefix";
}

export function seedMatch(loc: LocationProjection): MatchDraft {
  return { type: normalizeMatchType(loc.type), path: loc.match };
}

export function matchChanged(d: MatchDraft, loc: LocationProjection): boolean {
  return d.type !== normalizeMatchType(loc.type) || d.path.trim() !== loc.match.trim();
}

export function matchWarnings(d: MatchDraft): string[] {
  const w: string[] = [];
  const path = d.path.trim();
  if (path === "") {
    w.push("The match path is required.");
    return w;
  }
  if (d.type === "regex") {
    try {
      new RegExp(path);
    } catch {
      w.push("The regex pattern does not compile.");
    }
  } else if (!path.startsWith("/")) {
    w.push("A prefix or exact match path should start with '/'.");
  }
  return w;
}

export function matchToPatch(d: MatchDraft): LocationMatchPatch {
  return { type: d.type, path: d.path.trim() };
}

// ── Action ───────────────────────────────────────────────────────────────────

export type EditableActionKind = "proxy" | "static" | "redirect" | "return" | "deny";

// EDITABLE_ACTIONS are the tag-free actions the console can switch between
// structurally. Richer actions (gRPC, transcode, FastCGI/uWSGI, handler plugin)
// are left to raw [config] editing, so the editor is offered only when the
// location's current action is one of these.
export const EDITABLE_ACTIONS: EditableActionKind[] = [
  "proxy",
  "static",
  "redirect",
  "return",
  "deny",
];

export function isEditableAction(action: string): action is EditableActionKind {
  return (EDITABLE_ACTIONS as string[]).includes(action);
}

export interface ActionDraft {
  kind: EditableActionKind;
  target: string; // proxy_pass / root / redirect URL
  status: string; // return status, or optional redirect code
}

export function seedAction(loc: LocationProjection): ActionDraft {
  const kind: EditableActionKind = isEditableAction(loc.action) ? loc.action : "proxy";
  return { kind, target: loc.target ?? "", status: "" };
}

export function actionChanged(d: ActionDraft, loc: LocationProjection): boolean {
  const seeded = seedAction(loc);
  return (
    d.kind !== seeded.kind ||
    d.target.trim() !== seeded.target.trim() ||
    d.status.trim() !== seeded.status.trim()
  );
}

export function actionWarnings(d: ActionDraft): string[] {
  const w: string[] = [];
  switch (d.kind) {
    case "proxy":
      if (d.target.trim() === "") {
        w.push("The proxy action needs a target (an upstream reference or URL).");
      }
      break;
    case "static":
      if (d.target.trim() === "") {
        w.push("The static action needs a root directory.");
      }
      break;
    case "redirect":
      if (d.target.trim() === "") {
        w.push("The redirect action needs a target URL.");
      }
      if (d.status.trim() !== "") {
        const s = Number(d.status);
        if (!Number.isInteger(s) || s < 300 || s > 399) {
          w.push("A redirect status must be in the 3xx range.");
        }
      }
      break;
    case "return":
      if (d.status.trim() === "") {
        w.push("The return action needs a status code.");
      } else {
        const s = Number(d.status);
        if (!Number.isInteger(s) || s < 100 || s > 599) {
          w.push("The return status must be a valid HTTP status (100–599).");
        }
      }
      break;
    case "deny":
      break;
  }
  return w;
}

export function actionToPatch(d: ActionDraft): LocationActionPatch {
  switch (d.kind) {
    case "proxy":
      return { kind: "proxy", target: d.target.trim() };
    case "static":
      return { kind: "static", target: d.target.trim() };
    case "redirect": {
      const s = Number(d.status);
      return {
        kind: "redirect",
        target: d.target.trim(),
        ...(d.status.trim() !== "" && Number.isInteger(s) ? { status: s } : {}),
      };
    }
    case "return":
      return { kind: "return", status: Number(d.status) };
    case "deny":
      return { kind: "deny" };
  }
}

// ── Server host-name rename ──────────────────────────────────────────────────

export interface RenameDraft {
  hosts: string; // one host per line or comma-separated; empty = catch-all
}

export function seedRename(serverNames: string[] | undefined): RenameDraft {
  return { hosts: (serverNames ?? []).join("\n") };
}

export function hostList(s: string): string[] {
  return s
    .split(/[\n,]/)
    .map((x) => x.trim())
    .filter((x) => x.length > 0);
}

export function renameChanged(d: RenameDraft, serverNames: string[] | undefined): boolean {
  return !sameSet(hostList(d.hosts), serverNames ?? []);
}

export function renameWarnings(d: RenameDraft): string[] {
  const w: string[] = [];
  const hosts = hostList(d.hosts);
  const seen = new Set<string>();
  for (const h of hosts) {
    if (/\s/.test(h)) {
      w.push(`Host name "${h}" contains whitespace.`);
    }
    if (seen.has(h)) {
      w.push(`Host name "${h}" is listed more than once.`);
    }
    seen.add(h);
  }
  return w;
}

export function renameToNewNames(d: RenameDraft): string[] {
  return hostList(d.hosts);
}

// sameSet reports order-independent multiset equality of two host-name lists.
function sameSet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  const counts = new Map<string, number>();
  for (const s of a) counts.set(s, (counts.get(s) ?? 0) + 1);
  for (const s of b) {
    const n = counts.get(s);
    if (n === undefined || n === 0) return false;
    counts.set(s, n - 1);
  }
  return true;
}
