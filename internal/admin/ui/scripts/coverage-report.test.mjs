/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { coverageReport } from "./coverage-report.mjs";

function fixture(t, { file = true, measured = true, raw = true, covered = 8 } = {}) {
  const root = mkdtempSync(join(tmpdir(), "jul-coverage-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(join(root, "src/config"), { recursive: true });
  mkdirSync(join(root, "coverage"));
  const name = join(root, "src/config/Panel.tsx");
  if (file) writeFileSync(name, "export const panel = true;\n");
  const summary = Object.fromEntries(
    ["statements", "branches", "functions", "lines"].map((metric) => [
      metric,
      { total: 10, covered, pct: covered * 10 },
    ]),
  );
  const write = (path, value) => writeFileSync(join(root, path), JSON.stringify(value));
  write("coverage-policy.json", {
    global: { statements: 58 },
    paths: { "src/config/**": { statements: 80 } },
  });
  write("coverage/coverage-summary.json", {
    total: summary,
    ...(measured ? { [name]: summary } : {}),
  });
  write("coverage/coverage-final.json", raw ? { [name]: { s: {} } } : {});
  return root;
}

test("a measured target passes and retains aggregate counts and direct file measurement", (t) => {
  const report = coverageReport(fixture(t));
  assert.deepEqual(report.targets["src/config/**"].files, ["src/config/Panel.tsx"]);
  assert.equal(report.targets["src/config/**"].summary.statements.pct, 80);
  assert.equal(report.files["src/config/Panel.tsx"].statements.covered, 8);
});
test("an empty production match cannot pass as 100%", (t) => {
  assert.throws(() => coverageReport(fixture(t, { file: false })), /empty production match/);
});
test("a source omitted from coverage cannot silently improve an area", (t) => {
  assert.throws(
    () => coverageReport(fixture(t, { measured: false })),
    /missing src\/config\/Panel.tsx/,
  );
});
test("a missing raw per-file measurement also fails", (t) => {
  assert.throws(() => coverageReport(fixture(t, { raw: false })), /missing src\/config\/Panel.tsx/);
});
test("a below-floor target fails while the existing global floor still passes", (t) => {
  assert.throws(
    () => coverageReport(fixture(t, { covered: 7 })),
    /src\/config\/\*\*: statements 70 < 80/,
  );
});
test("an absent coverage report is an error", (t) => {
  const root = fixture(t);
  rmSync(join(root, "coverage/coverage-summary.json"));
  assert.throws(() => coverageReport(root), /ENOENT/);
});
