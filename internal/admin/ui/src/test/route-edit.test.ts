/**
 * Unit tests for the pure in-place route-edit mapping (Phase 4f). They pin the
 * seed / change-detection / validation / toPatch logic for the three editors —
 * match (type + path), action switch, and server host-name rename — so each
 * drawer refuses saves the backend would reject and round-trips faithfully.
 */
import { describe, it, expect } from "vitest";

import {
  actionChanged,
  actionToPatch,
  actionWarnings,
  hostList,
  isEditableAction,
  matchChanged,
  matchToPatch,
  matchWarnings,
  normalizeMatchType,
  renameChanged,
  renameToNewNames,
  renameWarnings,
  seedAction,
  seedMatch,
  seedRename,
  type ActionDraft,
  type MatchDraft,
} from "@/lib/routeEdit.ts";
import type { LocationProjection } from "@/api/client.ts";

function loc(over: Partial<LocationProjection> = {}): LocationProjection {
  return {
    index: 0,
    match: "/api",
    type: "prefix",
    action: "proxy",
    auth: false,
    cache: false,
    compression: false,
    rate_limit: false,
    secure: false,
    require_client_cert: false,
    ...over,
  };
}

describe("routeEdit — match", () => {
  it("normalizes empty/unknown match types to prefix", () => {
    expect(normalizeMatchType("")).toBe("prefix");
    expect(normalizeMatchType("glob")).toBe("prefix");
    expect(normalizeMatchType("exact")).toBe("exact");
    expect(normalizeMatchType("regex")).toBe("regex");
  });

  it("seeds from the projection and detects a change", () => {
    const l = loc({ type: "exact", match: "/v1" });
    const d = seedMatch(l);
    expect(d).toEqual({ type: "exact", path: "/v1" });
    expect(matchChanged(d, l)).toBe(false);
    expect(matchChanged({ ...d, path: "/v2" }, l)).toBe(true);
  });

  it("flags a missing path and a non-compiling regex", () => {
    expect(matchWarnings({ type: "prefix", path: "  " })).toHaveLength(1);
    expect(matchWarnings({ type: "regex", path: "([" })).toHaveLength(1);
    expect(matchWarnings({ type: "prefix", path: "api" })).toHaveLength(1); // no leading /
    expect(matchWarnings({ type: "prefix", path: "/api" })).toHaveLength(0);
    expect(matchWarnings({ type: "regex", path: "^/api/" })).toHaveLength(0);
  });

  it("trims the path into the patch payload", () => {
    const d: MatchDraft = { type: "exact", path: "  /v2  " };
    expect(matchToPatch(d)).toEqual({ type: "exact", path: "/v2" });
  });
});

describe("routeEdit — action", () => {
  it("recognizes only the tag-free editable actions", () => {
    expect(isEditableAction("proxy")).toBe(true);
    expect(isEditableAction("deny")).toBe(true);
    expect(isEditableAction("grpc")).toBe(false);
    expect(isEditableAction("fastcgi")).toBe(false);
  });

  it("seeds from the projection and falls back to proxy for non-editable actions", () => {
    expect(seedAction(loc({ action: "static", target: "/var/www" }))).toEqual({
      kind: "static",
      target: "/var/www",
      status: "",
    });
    expect(seedAction(loc({ action: "grpc" })).kind).toBe("proxy");
  });

  it("detects a change of kind, target, or status", () => {
    const l = loc({ action: "proxy", target: "http://app" });
    expect(actionChanged(seedAction(l), l)).toBe(false);
    expect(actionChanged({ kind: "deny", target: "", status: "" }, l)).toBe(true);
    expect(actionChanged({ kind: "proxy", target: "http://other", status: "" }, l)).toBe(true);
  });

  it("validates per-kind required fields and status ranges", () => {
    expect(actionWarnings({ kind: "proxy", target: "", status: "" })).toHaveLength(1);
    expect(actionWarnings({ kind: "static", target: "", status: "" })).toHaveLength(1);
    expect(actionWarnings({ kind: "redirect", target: "", status: "" })).toHaveLength(1);
    expect(actionWarnings({ kind: "redirect", target: "https://x", status: "200" })).toHaveLength(1);
    expect(actionWarnings({ kind: "redirect", target: "https://x", status: "301" })).toHaveLength(0);
    expect(actionWarnings({ kind: "return", target: "", status: "" })).toHaveLength(1);
    expect(actionWarnings({ kind: "return", target: "", status: "700" })).toHaveLength(1);
    expect(actionWarnings({ kind: "return", target: "", status: "404" })).toHaveLength(0);
    expect(actionWarnings({ kind: "deny", target: "", status: "" })).toHaveLength(0);
  });

  it("builds the right patch payload per kind", () => {
    expect(actionToPatch({ kind: "proxy", target: " http://a ", status: "" })).toEqual({
      kind: "proxy",
      target: "http://a",
    });
    expect(actionToPatch({ kind: "redirect", target: "https://x", status: "301" })).toEqual({
      kind: "redirect",
      target: "https://x",
      status: 301,
    });
    expect(actionToPatch({ kind: "redirect", target: "https://x", status: "" })).toEqual({
      kind: "redirect",
      target: "https://x",
    });
    expect(actionToPatch({ kind: "return", target: "", status: "404" })).toEqual({
      kind: "return",
      status: 404,
    });
    expect(actionToPatch({ kind: "deny", target: "", status: "" })).toEqual({ kind: "deny" });
  });

  it("does not carry a status when the draft enables it but leaves it non-integer", () => {
    const d: ActionDraft = { kind: "redirect", target: "https://x", status: "abc" };
    expect(actionToPatch(d)).toEqual({ kind: "redirect", target: "https://x" });
  });
});

describe("routeEdit — rename", () => {
  it("splits host lists on newlines and commas, trimming blanks", () => {
    expect(hostList("a.example\n b.example , \n c.example ")).toEqual([
      "a.example",
      "b.example",
      "c.example",
    ]);
    expect(hostList("")).toEqual([]);
  });

  it("seeds from server_names and detects set changes ignoring order", () => {
    expect(seedRename(["a.example", "b.example"])).toEqual({ hosts: "a.example\nb.example" });
    expect(seedRename(undefined)).toEqual({ hosts: "" });
    expect(renameChanged({ hosts: "b.example\na.example" }, ["a.example", "b.example"])).toBe(false);
    expect(renameChanged({ hosts: "a.example" }, ["a.example", "b.example"])).toBe(true);
    expect(renameChanged({ hosts: "" }, [])).toBe(false);
  });

  it("warns about whitespace and duplicate host names", () => {
    expect(renameWarnings({ hosts: "a.example\na.example" })).toHaveLength(1);
    expect(renameWarnings({ hosts: "bad host" })).toHaveLength(1);
    expect(renameWarnings({ hosts: "a.example\nb.example" })).toHaveLength(0);
  });

  it("produces the new server_names list", () => {
    expect(renameToNewNames({ hosts: "a.example, b.example" })).toEqual([
      "a.example",
      "b.example",
    ]);
    expect(renameToNewNames({ hosts: "" })).toEqual([]);
  });
});
