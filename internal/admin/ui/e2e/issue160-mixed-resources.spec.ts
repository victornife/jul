/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { expect, test } from "@playwright/test";
import { readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { RawConfigSchema } from "../src/api/client.ts";

function rewriteSection(
  raw: string,
  section: string,
  replacements: Record<string, string>,
): string {
  const lines = raw.split("\n");
  const start = lines.findIndex((line) => line.trim() === `[${section}]`);
  if (start < 0) throw new Error(`fixture has no [${section}] section`);
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i += 1) {
    if (/^\s*\[/.test(lines[i] ?? "")) {
      end = i;
      break;
    }
  }
  const keys = new Set(Object.keys(replacements));
  const body = lines
    .slice(start + 1, end)
    .filter((line) => !keys.has((line.split("=", 1)[0] ?? "").trim()));
  for (const [key, value] of Object.entries(replacements)) {
    body.push(`${key} = ${value}`);
  }
  return [...lines.slice(0, start + 1), ...body, ...lines.slice(end)].join("\n");
}

async function currentRaw(request: import("@playwright/test").APIRequestContext) {
  const response = await request.get("/api/config");
  expect(response.status()).toBe(200);
  return RawConfigSchema.parse(await response.json());
}

async function rawApply(
  request: import("@playwright/test").APIRequestContext,
  raw: string,
  baseVersion: string,
) {
  const url = baseVersion
    ? `/api/config/apply?base_version=${encodeURIComponent(baseVersion)}`
    : "/api/config/apply";
  return request.post(url, {
    headers: { "Content-Type": "application/toml" },
    data: raw,
  });
}

test("HR-07C audit sink and access-log sink hot-apply together without partial publication", async ({
  request,
}) => {
  const initial = await currentRaw(request);
  const originalRaw = initial.raw ?? "";
  const nonce = `${process.pid}-${Date.now()}`;
  const auditPath = join(tmpdir(), `jul-hr07c-mixed-audit-${nonce}.jsonl`);
  const accessPath = join(tmpdir(), `jul-hr07c-mixed-access-${nonce}.log`);
  await Promise.all([rm(auditPath, { force: true }), rm(accessPath, { force: true })]);
  let changed = false;
  try {
    let candidate = rewriteSection(originalRaw, "admin", {
      audit_log_file: JSON.stringify(auditPath),
      audit_log_rotate_max_mb: "11",
      audit_log_rotate_keep: "4",
    });
    candidate = rewriteSection(candidate, "observability.access_log", {
      sinks: "['file']",
      file: JSON.stringify(accessPath),
      format: "'text'",
      rotate_max_mb: "100",
      rotate_keep: "7",
    });

    const applied = await rawApply(request, candidate, initial.base_version ?? "");
    expect(applied.status()).toBe(200);
    changed = true;

    const traffic = await request.get("http://127.0.0.1:9292/", {
      headers: { Authorization: "" },
    });
    expect(traffic.status()).toBe(200);

    await expect
      .poll(async () => {
        try {
          return (await readFile(accessPath, "utf8")).includes("method=GET");
        } catch {
          return false;
        }
      })
      .toBe(true);

    await expect
      .poll(async () => {
        try {
          return (await readFile(auditPath, "utf8"))
            .split("\n")
            .filter(Boolean)
            .map((line) => JSON.parse(line) as { operation?: string; result?: string })
            .some((event) => event.operation === "config.apply" && event.result === "success");
        } catch {
          return false;
        }
      })
      .toBe(true);

    const live = await currentRaw(request);
    expect(live.raw ?? "").toContain(`audit_log_file = ${JSON.stringify(auditPath)}`);
    expect(live.raw ?? "").toContain(`file = ${JSON.stringify(accessPath)}`);
  } finally {
    if (changed) {
      const current = await currentRaw(request);
      const restored = await rawApply(request, originalRaw, current.base_version ?? "");
      expect(restored.status()).toBe(200);
    }
    await Promise.all([rm(auditPath, { force: true }), rm(accessPath, { force: true })]);
  }
});
